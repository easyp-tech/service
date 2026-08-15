package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/ratelimit"
	"github.com/hellofresh/health-go/v5"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	adapter_audit "github.com/easyp-tech/service/internal/adapters/audit"
	adapter_metrics "github.com/easyp-tech/service/internal/adapters/metrics"
	"github.com/easyp-tech/service/internal/adapters/registry"
	"github.com/easyp-tech/service/internal/adapters/storage"
	"github.com/easyp-tech/service/internal/api"
	"github.com/easyp-tech/service/internal/auth"
	"github.com/easyp-tech/service/internal/config"
	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/database"
	"github.com/easyp-tech/service/internal/database/connectors"
	"github.com/easyp-tech/service/internal/database/goosemigrate"
	"github.com/easyp-tech/service/internal/grpchelper"
	"github.com/easyp-tech/service/internal/license"
	"github.com/easyp-tech/service/internal/monitor"
	"github.com/easyp-tech/service/internal/ratelimiter"
	"github.com/easyp-tech/service/internal/safe"
	"github.com/easyp-tech/service/internal/serve"
	"github.com/easyp-tech/service/internal/telemetry"
)

const (
	// serviceNamespace is the fixed namespace for Prometheus metrics.
	serviceNamespace         = "easyp"
	exitCode                 = 2
	componentShutdownTimeout = 5 * time.Second
	// healthReadHeaderTimeout bounds how long a probe may take to send its
	// headers. Probes are local and instant; anything slower is not a probe.
	healthReadHeaderTimeout = 5 * time.Second
	// auditFlushHeadroom keeps the worker's final write strictly inside the
	// shutdown budget, so a slow write is reported rather than cut off.
	auditFlushHeadroom = 500 * time.Millisecond
)

// grpcMetrics implements grpchelper.Metrics for the recovery interceptor.
type grpcMetrics struct {
	panics prometheus.Counter
}

func (m *grpcMetrics) PanicsTotal() prometheus.Counter { //nolint:ireturn // interface requires prometheus.Counter
	return m.panics
}

// runServiceStart initializes and runs the complete service.
func runServiceStart(ctx context.Context, cfgPath string, logLevelStr string) error {
	slogLvl, levelErr := parseLogLevel(logLevelStr)

	log := buildLogger(slogLvl)

	// Reported rather than swallowed: an unreadable level and a deliberate one
	// produce the same running service otherwise, and the operator who mistyped
	// it has no way to tell which they got.
	if levelErr != nil {
		log.Warn("unrecognised log level, defaulting to info",
			"requested", logLevelStr, "error", levelErr)
	}

	ctx = monitor.WithContext(ctx, log.With(
		slog.String("version", version),
		slog.String("app", serviceNamespace),
	))

	cfg, err := loadConfig(ctx, cfgPath, log)
	if err != nil {
		return err
	}

	// Armed here rather than in main: the budget has to come from the config,
	// and the config is only known once the command runs. The other
	// subcommands are short-lived and stop on context cancellation, so they
	// need no watchdog.
	go forceShutdown(ctx, cfg.Server.ForceShutdownAfter)

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(collectors.NewGoCollector())

	err = run(ctx, cfg, reg)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	return nil
}

// loadConfig builds the configuration from wherever it comes from.
//
// Both branches end at the same place on purpose: settings resolve identically
// whether the service was pointed at a file or handed an environment, and the
// file path uses the very call "plugins push" uses, so the two cannot disagree
// about which store or which database they are talking to.
func loadConfig(ctx context.Context, cfgPath string, log *slog.Logger) (config.Config, error) {
	cfg := config.Config{}

	if cfgPath == "" {
		err := config.ApplyEnv(ctx, &cfg)
		if err != nil {
			return cfg, fmt.Errorf("config.ApplyEnv: %w", err)
		}

		err = cfg.Validate()
		if err != nil {
			return cfg, fmt.Errorf("config validation: %w", err)
		}

		return cfg, nil
	}

	loaded, warnings, err := config.LoadAndValidate(ctx, cfgPath)

	// Reported before the error is returned: an unknown field is the likeliest
	// explanation for a validation failure right after someone edited the file,
	// and it is the one clue that would otherwise be thrown away.
	for _, warning := range warnings {
		log.Warn("config file contains fields this build does not recognise; they were ignored",
			"detail", warning)
	}

	if err != nil {
		return cfg, fmt.Errorf("config.LoadAndValidate: %w", err)
	}

	return *loaded, nil
}

func run(ctx context.Context, cfg config.Config, reg *prometheus.Registry) error {
	namespace := serviceNamespace
	log := monitor.FromContext(ctx)

	cleanupObservability := initObservability(ctx, cfg, namespace, &log)
	defer cleanupObservability(ctx)
	// Update context with the new telemetry-enabled logger
	ctx = monitor.WithContext(ctx, log)

	// Before initInfrastructure, because that runs the migrations and the
	// startup probe is already counting by then. Readiness stays negative until
	// the database is actually usable; only liveness answers early, which is the
	// distinction that keeps a slow migration from looking like a hung process
	// and getting the pod killed and restarted into the same migration.
	readiness := new(atomic.Pointer[health.Health])

	waitHealth, err := startHealthServer(ctx, log, cfg, readiness)
	if err != nil {
		return err
	}

	repo, cleanupInfra, err := initInfrastructure(ctx, cfg, reg, namespace)
	if err != nil {
		return err
	}
	defer cleanupInfra()

	auditWorker, partitions, cleanupAudit := initAudit(ctx, cfg, repo, reg, namespace)
	defer cleanupAudit()

	grpcCreds, err := grpchelper.BuildServerCreds(cfg.Server.TLS, log)
	if err != nil {
		return fmt.Errorf("grpchelper.BuildServerCreds: %w", err)
	}

	licenseClient, err := buildLicenseClient(cfg.License, log)
	if err != nil {
		return fmt.Errorf("buildLicenseClient: %w", err)
	}

	_, pool, _, grpcServer, apiSrv := initApp(ctx, cfg, repo, reg, namespace, auditWorker, grpcCreds, licenseClient)

	defer func() {
		lost := pool.Shutdown(cfg.WorkerPool.ShutdownTimeout)
		if lost > 0 {
			log.Warn("generation jobs lost on shutdown", "count", lost)
		}
	}()

	healthCheck, err := initHealthCheck(repo, namespace)
	if err != nil {
		return fmt.Errorf("initHealthCheck: %w", err)
	}

	// From here readiness reports the database rather than "starting".
	readiness.Store(healthCheck)

	serveErr := serveApp(ctx, log, cfg, reg, grpcServer, apiSrv.MCPHandler(), partitions.Run)
	if serveErr != nil {
		serveErr = fmt.Errorf("serveApp: %w", serveErr)
	}

	// The health server outlives the others on purpose: while the rest drain,
	// an unready answer is what takes this pod out of load balancing. A closed
	// port would too, but it reads as a crash in every dashboard.
	return errors.Join(serveErr, waitHealth())
}

// startHealthServer brings the health endpoints up before anything slow enough
// to matter, and returns a function that waits for the server to stop.
//
// The listener is bound synchronously so that a port already in use fails the
// startup instead of being logged into the void while the process runs on.
func startHealthServer(
	ctx context.Context,
	log *slog.Logger,
	cfg config.Config,
	readiness *atomic.Pointer[health.Health],
) (func() error, error) {
	healthPort, err := strconv.ParseUint(cfg.Server.Port.Health, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid health port: %w", err)
	}

	mux := http.NewServeMux()

	// "/live" reports liveness and deliberately checks nothing. Pointing a
	// liveness probe at the readiness handler would restart every pod at once
	// during a database blip — turning a recoverable outage into a rolling
	// crash loop.
	mux.HandleFunc("/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// "/" reports readiness: once wired up it checks postgres, so a pod drops
	// out of load balancing while the database is unreachable. Until then there
	// is nothing truthful to report but "not yet".
	mux.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
		checker := readiness.Load()
		if checker == nil {
			http.Error(writer, "starting", http.StatusServiceUnavailable)

			return
		}

		checker.Handler().ServeHTTP(writer, req)
	})

	addr := net.JoinHostPort(cfg.Server.Host, strconv.FormatUint(healthPort, 10))

	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen health: %w", err)
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: healthReadHeaderTimeout} //nolint:exhaustruct

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(listener) }()

	log.Info("started health server", "host", cfg.Server.Host, "port", healthPort)

	return func() error {
		<-ctx.Done()

		// Detached: the shutdown is triggered by this context being cancelled,
		// so passing it back in would abandon in-flight probes instead of
		// letting them finish.
		shutdownErr := srv.Shutdown(context.WithoutCancel(ctx))

		serveErr := <-errc
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}

		log.Info("shutdown health server")

		return errors.Join(serveErr, shutdownErr)
	}, nil
}

func initObservability(ctx context.Context, cfg config.Config, namespace string, log **slog.Logger) func(context.Context) {
	// Initialize telemetry
	telCfg := telemetry.Config{
		OTLPEndpoint:      cfg.Telemetry.OTLPEndpoint,
		ServiceName:       namespace,
		PyroscopeEndpoint: cfg.Telemetry.PyroscopeEndpoint,
		ServiceVersion:    version,
		ServiceTier:       cfg.Telemetry.ServiceTier,
	}
	baseHandler := (*log).Handler()
	shutdownTelemetry, telLog, err := telemetry.Init(ctx, telCfg, baseHandler)
	if err != nil {
		(*log).Error("telemetry.Init", "error", err)

		return func(context.Context) {}
	}
	*log = telLog

	return func(ctx context.Context) {
		shutdownCtx, cancel := context.WithTimeout(ctx, componentShutdownTimeout)
		defer cancel()
		err := shutdownTelemetry(shutdownCtx)
		if err != nil {
			(*log).Error("failed to shutdown tracer provider", "error", err)
		}
	}
}

func initInfrastructure(
	ctx context.Context,
	cfg config.Config,
	reg *prometheus.Registry,
	namespace string,
) (*registry.Registry, func(), error) {
	log := monitor.FromContext(ctx)

	err := goosemigrate.Up(ctx, cfg.DB.Postgres)
	if err != nil {
		return nil, nil, fmt.Errorf("goosemigrate.Up: %w", err)
	}

	dalMetrics := database.NewMetrics(reg, namespace, "repo", new(core.Registry))
	sqlCfg := database.SQLConfig{Metrics: dalMetrics}
	db, err := database.NewSQL(ctx, cfg.DB.Driver, sqlCfg, &connectors.Raw{Query: cfg.DB.Postgres})
	if err != nil {
		return nil, nil, fmt.Errorf("database.NewSQL: %w", err)
	}

	bStorage, err := initBinaryStorage(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	cacheOpts := pluginCacheOptions(cfg, reg)

	repo, err := registry.New(ctx, db, cfg.Registry.PluginsDir, cfg.Registry.MaxOutputSize, bStorage, cacheOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("registry.New: %w", err)
	}

	dbCollector := adapter_metrics.NewDBCollector(repo.DB().UnderlyingDB(), namespace)
	reg.MustRegister(dbCollector)

	businessCollector := adapter_metrics.NewBusinessMetricsCollector(repo.DB().UnderlyingDB(), namespace, log)
	reg.MustRegister(businessCollector)

	cleanupRepo := func() {
		err := repo.Close()
		if err != nil {
			log.Error("close database connection", "error", err)
		}
	}

	return repo, cleanupRepo, nil
}

// initAudit starts the audit writer and the partition maintainer. The returned
// cleanup drains whatever the writer still holds; the maintainer is driven by
// the caller through Maintainer.Run.
func initAudit(
	ctx context.Context,
	cfg config.Config,
	repo *registry.Registry,
	reg *prometheus.Registry,
	namespace string,
) (*adapter_audit.Worker, *adapter_audit.Maintainer, func()) {
	log := monitor.FromContext(ctx)

	auditStore := adapter_audit.New(repo.DB(), log)
	worker := adapter_audit.NewWorker(auditStore, adapter_audit.Config{
		BufferSize:     cfg.Audit.BufferSize,
		BatchSize:      cfg.Audit.BatchSize,
		FlushInterval:  cfg.Audit.FlushInterval,
		MaxSaveRetries: cfg.Audit.MaxSaveRetries,
		EnqueueTimeout: cfg.Audit.EnqueueTimeout,
		FlushTimeout:   cfg.Audit.FlushTimeout,
		// Leave the worker room to finish its last write before Shutdown stops
		// waiting for it.
		ShutdownFlushTimeout: componentShutdownTimeout - auditFlushHeadroom,
	}, log, reg, namespace)

	// Since audit worker runs in background, we launch it here.
	go worker.Run(ctx)

	partitions := adapter_audit.NewMaintainer(repo.DB(), adapter_audit.PartitionConfig{
		RetentionMonths:  cfg.Audit.RetentionMonths,
		PreCreateMonths:  cfg.Audit.PreCreateMonths,
		Interval:         cfg.Audit.PartitionCheckInterval,
		OperationTimeout: cfg.Audit.PartitionOpTimeout,
	}, log, reg, namespace)

	cleanup := func() {
		lost := worker.Shutdown(componentShutdownTimeout)
		if lost > 0 {
			log.Warn("audit events lost on shutdown", "count", lost)
		}
	}

	return worker, partitions, cleanup
}

// cappedWorkers applies the licence's worker ceiling to the configured number.
//
// A ceiling, not a substitution. This used to assign the licence limit outright,
// which made it a floor as well: a community deployment asking for two workers
// got four, because the tier permitted four. Nothing said so — the configuration
// on disk read `workers: 2` and the pool ran four — and the only way to find out
// was to count goroutines.
//
// A licence that imposes no limit of its own reports core.LicenseUnlimited (-1),
// so anything non-positive leaves the configured value alone.
func cappedWorkers(configured, licenseLimit int, log *slog.Logger) int {
	if licenseLimit <= 0 || licenseLimit >= configured {
		return configured
	}

	log.Info("worker_pool.workers lowered to the licence tier's limit",
		"configured", configured, "licence_limit", licenseLimit)

	return licenseLimit
}

func initApp(
	ctx context.Context,
	cfg config.Config,
	repo *registry.Registry,
	reg *prometheus.Registry,
	namespace string,
	auditSink core.AuditSink,
	grpcCreds credentials.TransportCredentials,
	licenseClient core.LicenseClient,
) (*core.Core, *core.WorkerPool, *license.FeatureGate, *grpc.Server, *api.API) {
	log := monitor.FromContext(ctx)

	lm, err := license.NewManager(ctx, licenseClient, license.Config{
		CacheTTL: cfg.License.CacheTTL,
	}, log, reg, namespace)
	if err != nil {
		log.Warn("license initialization error, continuing in community mode", "error", err)
	}
	lm.StartRefreshWatcher(ctx)

	gate := license.NewFeatureGate(lm)

	rl, cl := buildLimiters(ctx, cfg, gate, log, reg, namespace)

	tracedRegistry := telemetry.NewTracingRegistry(repo)

	wpWorkers := cappedWorkers(cfg.WorkerPool.Workers, gate.MaxWorkers(), log)

	metricsAdapter := adapter_metrics.New(reg, namespace)
	pool := core.NewWorkerPool(tracedRegistry, core.WorkerPoolConfig{
		Workers:   wpWorkers,
		QueueSize: cfg.WorkerPool.QueueSize,
		// Not capped by the licence: MaxWorkers bounds plugin lookups, and this
		// bounds plugin processes. Tying the paid limit to it is a pricing
		// decision, not a plumbing one.
		MaxConcurrentGenerations: cfg.WorkerPool.MaxConcurrentGenerations,
		GenerationTimeout:        cfg.WorkerPool.GenerationTimeout,
		MaxRetries:               cfg.WorkerPool.MaxRetries,
		ShutdownTimeout:          cfg.WorkerPool.ShutdownTimeout,
	}, log, metricsAdapter, reg, namespace)
	pool.Start(ctx)

	module := core.New(metricsAdapter, pool, gate, auditSink, log)
	tracedCore := telemetry.NewTracingCore(module)

	authenticator := auth.NewStaticTokenAuthenticator(cfg.Auth.WriteTokens)
	if authenticator.Empty() {
		log.Warn("no write tokens configured: CreatePlugin, UpdatePlugin and DeletePlugin will reject every call",
			"hint", "generate one with `easyp-svc auth new-token` and add it to auth.write_tokens")
	}

	// contextcheck traces into ConcurrencyLimiter.StreamServerInterceptor and
	// asks for the context to reach the handler. A stream interceptor can only
	// do that by substituting a wrapped ServerStream, which is worth doing when
	// a new context is derived (as the auth interceptor does) and is empty
	// ceremony when, as here, the stream's own context is merely read.
	grpcSrv, apiSrv := buildGRPCServer( //nolint:contextcheck // limiter reads ss.Context(), derives nothing
		log, reg, gate, rl, cl, tracedCore, grpcCreds, authenticator, cfg.Server)

	return module, pool, gate, grpcSrv, apiSrv
}

func buildGRPCServer(
	log *slog.Logger,
	reg *prometheus.Registry,
	gate *license.FeatureGate,
	rl *ratelimiter.RateLimiter,
	cl *ratelimiter.ConcurrencyLimiter,
	tracedCore *telemetry.TracingCore,
	creds credentials.TransportCredentials,
	authenticator auth.Authenticator,
	srvCfg config.Server,
) (*grpc.Server, *api.API) {
	serverMetrics := grpchelper.NewServerMetrics(reg, "easyp", "api")

	// The same counter the background barriers report into, not one of its own.
	// Registering a second easyp_panics_total is not merely untidy — Prometheus
	// rejects a duplicate name carrying a different help string, and the process
	// dies at startup. Sharing it also means the existing alert covers panics
	// wherever they happen, rather than only the ones a handler saw.
	panicsCounter := safe.NewGuard(reg, serviceNamespace).Counter()

	unaryExtra, streamExtra := buildExtraInterceptors(log, reg, gate, rl, cl, authenticator)

	// Validate has already rejected an unparseable CIDR, so this cannot fail on
	// a configuration that got this far.
	trustedProxies, err := srvCfg.TrustedProxyPrefixes()
	if err != nil {
		log.Error("trusted proxies could not be parsed, treating the peer as the caller", "error", err)
	}

	grpcSrv, healthSrv := grpchelper.NewServer(
		&grpcMetrics{panics: panicsCounter},
		log,
		serverMetrics,
		api.ErrorToStatus,
		creds,
		unaryExtra,
		streamExtra,
		grpchelper.ServerOptions{
			TrustedProxies:       trustedProxies,
			MaxRecvMsgSize:       srvCfg.MaxRecvMsgSize,
			MaxSendMsgSize:       srvCfg.MaxSendMsgSize,
			MaxConcurrentStreams: srvCfg.MaxConcurrentStreams,
		},
	)
	serverMetrics.InitializeMetrics(grpcSrv)

	apiSrv := api.New(grpcSrv, healthSrv, tracedCore, log)

	return grpcSrv, apiSrv
}

// buildLimiters assembles the two load-shedding limiters. They answer different
// questions — how fast a client calls, and how much it holds at once — and
// neither substitutes for the other.
func buildLimiters(
	ctx context.Context,
	cfg config.Config,
	gate *license.FeatureGate,
	log *slog.Logger,
	reg *prometheus.Registry,
	namespace string,
) (*ratelimiter.RateLimiter, *ratelimiter.ConcurrencyLimiter) {
	rl := ratelimiter.New(ratelimiter.Config{
		RequestsPerSecond: cfg.RateLimit.RequestsPerSecond,
		Burst:             cfg.RateLimit.Burst,
		CleanupInterval:   cfg.RateLimit.CleanupInterval,
	}, gate, nil, log, reg, namespace)
	rl.StartCleanup(ctx)

	cl := ratelimiter.NewConcurrencyLimiter(cfg.RateLimit.MaxConcurrentPerIP, gate, nil, log, reg, namespace)

	return rl, cl
}

// buildExtraInterceptors assembles the chain that runs after grpchelper's
// built-in one. The order is deliberate: load shedding comes first — rate,
// then concurrency — so neither authentication nor the licence check spends
// anything on a request that is about to be refused; the licence check runs
// last so it already sees an authenticated caller.
func buildExtraInterceptors(
	log *slog.Logger,
	reg *prometheus.Registry,
	gate *license.FeatureGate,
	rl *ratelimiter.RateLimiter,
	cl *ratelimiter.ConcurrencyLimiter,
	authenticator auth.Authenticator,
) ([]grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor) {
	authInterceptor := api.NewAuthInterceptor(authenticator, log, reg, serviceNamespace)
	licenseInterceptor := api.NewLicenseInterceptor(gate, log)

	return []grpc.UnaryServerInterceptor{
			ratelimit.UnaryServerInterceptor(rl),
			cl.UnaryServerInterceptor(),
			authInterceptor.UnaryServerInterceptor(),
			licenseInterceptor.UnaryServerInterceptor(),
		}, []grpc.StreamServerInterceptor{
			ratelimit.StreamServerInterceptor(rl),
			cl.StreamServerInterceptor(),
			authInterceptor.StreamServerInterceptor(),
			licenseInterceptor.StreamServerInterceptor(),
		}
}

func serveApp(
	ctx context.Context,
	log *slog.Logger,
	cfg config.Config,
	reg *prometheus.Registry,
	grpcServer *grpc.Server,
	mcpHandler http.Handler,
	jobs ...func(context.Context) error,
) error {
	mcpMux := http.NewServeMux()
	mcpMux.Handle("/mcp", mcpHandler)

	grpcPort, err := strconv.ParseUint(cfg.Server.Port.GRPC, 10, 16)
	if err != nil {
		return fmt.Errorf("invalid grpc port: %w", err)
	}

	metricPort, err := strconv.ParseUint(cfg.Server.Port.Metric, 10, 16)
	if err != nil {
		return fmt.Errorf("invalid metric port: %w", err)
	}

	mcpPort, err := strconv.ParseUint(cfg.Server.Port.MCP, 10, 16)
	if err != nil {
		return fmt.Errorf("invalid mcp port: %w", err)
	}

	// Health is not among them: it is started before the migrations, back in
	// run, and outlives these.
	const builtinServices = 3

	services := make([]func(context.Context) error, 0, builtinServices+len(jobs))
	services = append(services,
		serve.Metrics(log.With("module", "metric"), cfg.Server.Host, uint16(metricPort), reg),
		serve.GRPC(log.With("module", "gRPC"), cfg.Server.Host, uint16(grpcPort), grpcServer),
		serve.HTTP(log.With("module", "mcp"), cfg.Server.Host, uint16(mcpPort), mcpMux),
	)

	// Background jobs are supervised alongside the servers so they are
	// guaranteed to have stopped before run() closes the DB pool.
	services = append(services, jobs...)

	err = serve.Start(
		ctx,
		services...,
	)
	if err != nil {
		return fmt.Errorf("serve.Start: %w", err)
	}

	return nil
}

// initBinaryStorage opens object storage for plugin archives, or returns nil
// when none is configured — in which case the files in plugins_dir are the only
// copy of an artifact and are never evicted.
//
//nolint:ireturn // core.BinaryStorage is the port; nil is a meaningful value for it
func initBinaryStorage(ctx context.Context, cfg config.Config) (core.BinaryStorage, error) {
	if !cfg.Registry.S3.Enabled() {
		return nil, nil //nolint:nilnil // no storage configured is not an error
	}

	s3Store, err := storage.NewS3Storage(ctx, storage.S3Options{
		Endpoint:        cfg.Registry.S3.Endpoint,
		Bucket:          cfg.Registry.S3.Bucket,
		Region:          cfg.Registry.S3.Region,
		Prefix:          cfg.Registry.S3.Prefix,
		AccessKeyID:     strings.TrimSpace(cfg.Registry.S3.AccessKeyID),
		SecretAccessKey: strings.TrimSpace(cfg.Registry.S3.SecretAccessKey),
		ForcePathStyle:  cfg.Registry.S3.ForcePathStyle,
	})
	if err != nil {
		return nil, fmt.Errorf("storage.NewS3Storage: %w", err)
	}

	monitor.FromContext(ctx).Info("S3 plugin storage enabled",
		"bucket", cfg.Registry.S3.Bucket,
		"endpoint", cfg.Registry.S3.Endpoint,
	)

	return s3Store, nil
}

// pluginCacheOptions derives the eviction settings from the pool timings.
//
// A generation cannot outlive GenerationTimeout, so a plugin untouched for
// twice that long is certainly not executing. That is what lets eviction skip
// reference counting and still never pull a binary out from under a running
// process.
func pluginCacheOptions(cfg config.Config, reg *prometheus.Registry) registry.CacheOptions {
	const generationsOfSlack = 2

	return registry.CacheOptions{
		MaxBytes:  cfg.Registry.CacheMaxBytes,
		MinAge:    generationsOfSlack * cfg.WorkerPool.GenerationTimeout,
		Registry:  reg,
		Namespace: serviceNamespace,
	}
}

func initHealthCheck(repo *registry.Registry, namespace string) (*health.Health, error) {
	const healthTimeout = 5 * time.Second

	healthCheck, err := health.New(
		health.WithComponent(
			health.Component{
				Name:    namespace,
				Version: "dev",
			},
		),
		health.WithChecks(
			health.Config{
				Name:    "postgres",
				Timeout: healthTimeout,
				Check:   repo.Health,
			},
		),
	)
	if err != nil {
		return nil, fmt.Errorf("health.New: %w", err)
	}

	return healthCheck, nil
}

// parseLogLevel resolves the --log_level flag, returning the level to use and
// whether the value was understood.
//
// A value that does not parse falls back to Info, not Debug. Debug is the
// loudest setting there is, so treating a typo as a request for it turns a
// harmless mistake into a production incident: full request tracing, on a
// service whose requests are other people's source code. The quiet direction is
// the safe one to guess in.
func parseLogLevel(s string) (slog.Level, error) {
	var lvl slog.Level

	err := lvl.UnmarshalText([]byte(s))
	if err != nil {
		return slog.LevelInfo, fmt.Errorf("slog.Level.UnmarshalText: %w", err)
	}

	return lvl, nil
}

func buildLogger(level slog.Level) *slog.Logger {
	return slog.New(
		slog.NewJSONHandler(
			os.Stdout,
			&slog.HandlerOptions{
				AddSource: true,
				Level:     level,
			},
		),
	)
}

// forceShutdown exits the process if graceful shutdown has not finished within
// delay of the termination signal. The delay must outlast an in-flight
// generation, otherwise the watchdog itself is what severs the work.
func forceShutdown(ctx context.Context, shutdownDelay time.Duration) {
	log := monitor.FromContext(ctx)
	<-ctx.Done()
	<-time.After(shutdownDelay)
	log.Error("failed to graceful shutdown")
	os.Exit(exitCode)
}
