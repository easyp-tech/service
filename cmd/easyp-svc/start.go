package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/ratelimit"
	"github.com/hellofresh/health-go/v5"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/sethvargo/go-envconfig"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"gopkg.in/yaml.v3"

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
	"github.com/easyp-tech/service/internal/flags"
	"github.com/easyp-tech/service/internal/grpchelper"
	"github.com/easyp-tech/service/internal/license"
	"github.com/easyp-tech/service/internal/monitor"
	"github.com/easyp-tech/service/internal/ratelimiter"
	"github.com/easyp-tech/service/internal/serve"
	"github.com/easyp-tech/service/internal/telemetry"
)

const (
	// serviceNamespace is the fixed namespace for Prometheus metrics.
	serviceNamespace         = "easyp"
	exitCode                 = 2
	configFileSize           = 1024 * 1024
	componentShutdownTimeout = 5 * time.Second
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
	// Parse log level
	var slogLvl slog.Level
	err := slogLvl.UnmarshalText([]byte(logLevelStr))
	if err != nil {
		slogLvl = slog.LevelDebug // fallback
	}

	log := buildLogger(slogLvl)

	ctx = monitor.WithContext(ctx, log.With(
		slog.String("version", "dev"),
		slog.String("app", serviceNamespace),
	))

	cfg := config.Config{}

	if cfgPath != "" {
		// Use flags.File logic manually
		configFile := &flags.File{DefaultPath: "", MaxSize: configFileSize}
		err = configFile.Set(cfgPath)
		if err != nil {
			return fmt.Errorf("read config file: %w", err)
		}

		err = yaml.NewDecoder(configFile).Decode(&cfg)
		if err != nil {
			return fmt.Errorf("yaml.NewDecoder.Decode: %w", err)
		}
	} else {
		err = envconfig.Process(ctx, &cfg)
		if err != nil {
			return fmt.Errorf("envconfig.Process: %w", err)
		}
	}

	err = cfg.Validate()
	if err != nil {
		return fmt.Errorf("config validation: %w", err)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(collectors.NewGoCollector())

	err = run(ctx, cfg, reg)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	return nil
}

func run(ctx context.Context, cfg config.Config, reg *prometheus.Registry) error {
	namespace := serviceNamespace
	log := monitor.FromContext(ctx)

	cleanupObservability := initObservability(ctx, cfg, namespace, &log)
	defer cleanupObservability(ctx)
	// Update context with the new telemetry-enabled logger
	ctx = monitor.WithContext(ctx, log)

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

	licenseCreds, err := resolveLicense(cfg.License)
	if err != nil {
		return fmt.Errorf("resolveLicense: %w", err)
	}

	_, pool, _, grpcServer, apiSrv := initApp(ctx, cfg, repo, reg, namespace, auditWorker, grpcCreds, licenseCreds)

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

	err = serveApp(ctx, log, cfg, reg, grpcServer, apiSrv.MCPHandler(), healthCheck, partitions.Run)
	if err != nil {
		return fmt.Errorf("serveApp: %w", err)
	}

	return nil
}

func initObservability(ctx context.Context, cfg config.Config, namespace string, log **slog.Logger) func(context.Context) {
	// Initialize telemetry
	telCfg := telemetry.Config{
		OTLPEndpoint:      cfg.Telemetry.OTLPEndpoint,
		ServiceName:       namespace,
		PyroscopeEndpoint: cfg.Telemetry.PyroscopeEndpoint,
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

	var bStorage core.BinaryStorage
	if cfg.Registry.S3.Enabled() {
		s3Store, s3Err := storage.NewS3Storage(ctx, storage.S3Options{
			Endpoint:        cfg.Registry.S3.Endpoint,
			Bucket:          cfg.Registry.S3.Bucket,
			Region:          cfg.Registry.S3.Region,
			Prefix:          cfg.Registry.S3.Prefix,
			AccessKeyID:     cfg.Registry.S3.AccessKeyID,
			SecretAccessKey: cfg.Registry.S3.SecretAccessKey,
			ForcePathStyle:  cfg.Registry.S3.ForcePathStyle,
		})
		if s3Err != nil {
			return nil, nil, fmt.Errorf("storage.NewS3Storage: %w", s3Err)
		}
		bStorage = s3Store
		log.Info("S3 plugin storage enabled",
			"bucket", cfg.Registry.S3.Bucket,
			"endpoint", cfg.Registry.S3.Endpoint,
		)
	}

	repo, err := registry.New(ctx, db, cfg.Registry.PluginsDir, cfg.Registry.MaxOutputSize, bStorage)
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

func initApp(
	ctx context.Context,
	cfg config.Config,
	repo *registry.Registry,
	reg *prometheus.Registry,
	namespace string,
	auditSink core.AuditSink,
	grpcCreds credentials.TransportCredentials,
	licenseCreds licenseCredentials,
) (*core.Core, *core.WorkerPool, *license.FeatureGate, *grpc.Server, *api.API) {
	log := monitor.FromContext(ctx)

	licenseClient := license.NewMockLicenseClient(licenseCreds.token, licenseCreds.publicKey, log)
	lm, err := license.NewManager(ctx, licenseClient, license.Config{
		CacheTTL: cfg.License.CacheTTL,
	}, log, reg, namespace)
	if err != nil {
		log.Warn("license initialization error, continuing in community mode", "error", err)
	}
	lm.StartRefreshWatcher(ctx)

	gate := license.NewFeatureGate(lm)

	rl := ratelimiter.New(ratelimiter.Config{
		RequestsPerSecond: cfg.RateLimit.RequestsPerSecond,
		Burst:             cfg.RateLimit.Burst,
		CleanupInterval:   cfg.RateLimit.CleanupInterval,
	}, gate, nil, log, reg)
	rl.StartCleanup(ctx)

	tracedRegistry := telemetry.NewTracingRegistry(repo)

	wpWorkers := cfg.WorkerPool.Workers
	if licenseWorkers := gate.MaxWorkers(); licenseWorkers > 0 {
		wpWorkers = licenseWorkers
	}

	metricsAdapter := adapter_metrics.New(reg, namespace)
	pool := core.NewWorkerPool(tracedRegistry, core.WorkerPoolConfig{
		Workers:           wpWorkers,
		QueueSize:         cfg.WorkerPool.QueueSize,
		GenerationTimeout: cfg.WorkerPool.GenerationTimeout,
		MaxRetries:        cfg.WorkerPool.MaxRetries,
		ShutdownTimeout:   cfg.WorkerPool.ShutdownTimeout,
	}, log, metricsAdapter, reg, namespace)
	pool.Start(ctx)

	module := core.New(metricsAdapter, pool, gate, auditSink, log)
	tracedCore := telemetry.NewTracingCore(module)

	authenticator := auth.NewStaticTokenAuthenticator(cfg.Auth.WriteTokens)
	if authenticator.Empty() {
		log.Warn("no write tokens configured: CreatePlugin, UpdatePlugin and DeletePlugin will reject every call",
			"hint", "generate one with `easyp-svc auth new-token` and add it to auth.write_tokens")
	}

	grpcSrv, apiSrv := buildGRPCServer(log, reg, gate, rl, tracedCore, grpcCreds, authenticator)

	return module, pool, gate, grpcSrv, apiSrv
}

func buildGRPCServer(
	log *slog.Logger,
	reg *prometheus.Registry,
	gate *license.FeatureGate,
	rl *ratelimiter.RateLimiter,
	tracedCore *telemetry.TracingCore,
	creds credentials.TransportCredentials,
	authenticator auth.Authenticator,
) (*grpc.Server, *api.API) {
	serverMetrics := grpchelper.NewServerMetrics(reg, "easyp", "api")

	panicsCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "easyp",
		Name:      "panics_total",
		Help:      "Total number of panics recovered in gRPC handlers.",
	})
	reg.MustRegister(panicsCounter)

	unaryExtra, streamExtra := buildExtraInterceptors(log, reg, gate, rl, authenticator)

	grpcSrv, healthSrv := grpchelper.NewServer(
		&grpcMetrics{panics: panicsCounter},
		log,
		serverMetrics,
		api.ErrorToStatus,
		creds,
		unaryExtra,
		streamExtra,
	)
	serverMetrics.InitializeMetrics(grpcSrv)

	apiSrv := api.New(grpcSrv, healthSrv, tracedCore, log)

	return grpcSrv, apiSrv
}

// buildExtraInterceptors assembles the chain that runs after grpchelper's
// built-in one. The order is deliberate: rate limiting sheds load before
// authentication spends anything on it, and the licence check runs last so it
// already sees an authenticated caller.
func buildExtraInterceptors(
	log *slog.Logger,
	reg *prometheus.Registry,
	gate *license.FeatureGate,
	rl *ratelimiter.RateLimiter,
	authenticator auth.Authenticator,
) ([]grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor) {
	authInterceptor := api.NewAuthInterceptor(authenticator, log, reg, serviceNamespace)
	licenseInterceptor := api.NewLicenseInterceptor(gate, log)

	return []grpc.UnaryServerInterceptor{
			ratelimit.UnaryServerInterceptor(rl),
			authInterceptor.UnaryServerInterceptor(),
			licenseInterceptor.UnaryServerInterceptor(),
		}, []grpc.StreamServerInterceptor{
			ratelimit.StreamServerInterceptor(rl),
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
	healthCheck *health.Health,
	jobs ...func(context.Context) error,
) error {
	mcpMux := http.NewServeMux()
	mcpMux.Handle("/mcp", mcpHandler)

	healthMux := http.NewServeMux()
	healthMux.Handle("/", healthCheck.Handler())

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

	healthPort, err := strconv.ParseUint(cfg.Server.Port.Health, 10, 16)
	if err != nil {
		return fmt.Errorf("invalid health port: %w", err)
	}

	const builtinServices = 4

	services := make([]func(context.Context) error, 0, builtinServices+len(jobs))
	services = append(services,
		serve.Metrics(log.With("module", "metric"), cfg.Server.Host, uint16(metricPort), reg),
		serve.GRPC(log.With("module", "gRPC"), cfg.Server.Host, uint16(grpcPort), grpcServer),
		serve.HTTP(log.With("module", "mcp"), cfg.Server.Host, uint16(mcpPort), mcpMux),
		serve.HTTP(log.With("module", "health"), cfg.Server.Host, uint16(healthPort), healthMux),
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

func forceShutdown(ctx context.Context) {
	log := monitor.FromContext(ctx)
	const shutdownDelay = 15 * time.Second
	<-ctx.Done()
	<-time.After(shutdownDelay)
	log.Error("failed to graceful shutdown")
	os.Exit(exitCode)
}
