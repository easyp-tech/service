// Package config provides shared configuration types and validation
// for both the EasyP server and the epctl CLI utility.
package config

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sethvargo/go-envconfig"
	"gopkg.in/yaml.v3"
)

// Config is the root configuration for the EasyP service.
type Config struct {
	Server     Server           `env:", prefix=SERVER_"      yaml:"server"`
	DB         DBConfig         `env:", prefix=DB_"          yaml:"db"`
	Registry   RegistryConfig   `env:", prefix=REGISTRY_"    yaml:"registry"`
	Telemetry  TelemetryConfig  `env:", prefix=TELEMETRY_"   yaml:"telemetry"`
	WorkerPool WorkerPoolConfig `env:", prefix=WORKER_POOL_" yaml:"worker_pool"`
	License    LicenseConfig    `env:", prefix=LICENSE_"     yaml:"license"`
	RateLimit  RateLimitConfig  `env:", prefix=RATE_LIMIT_"  yaml:"rate_limit"`
	Audit      AuditConfig      `env:", prefix=AUDIT_"       yaml:"audit"`
	Auth       AuthConfig       `env:", prefix=AUTH_"        yaml:"auth"`
	MCP        MCPConfig        `env:", prefix=MCP_"         yaml:"mcp"`
	Log        LogConfig        `env:", prefix=LOG_"         yaml:"log"`
}

// MCPConfig configures the MCP endpoint — the HTTP surface AI tooling uses to
// read the plugin catalog and the easyp.yaml schema.
//
// Off by default because the endpoint sits outside the gRPC interceptor chain:
// no TLS, no rate limit, no audit. It exposes nothing the anonymous gRPC reads
// do not, so turning it on inside a trusted network or behind an ingress that
// terminates TLS is a one-line, low-stakes decision — but it should be a
// decision, not a port that was open before anyone asked.
type MCPConfig struct {
	// Enabled starts the MCP HTTP listener on server.port.mcp.
	Enabled bool `env:"ENABLED,default=false" yaml:"enabled"`
}

// Server holds HTTP/gRPC server settings.
type Server struct {
	Host string    `env:"HOST, default=0.0.0.0" yaml:"host"`
	Port Ports     `env:", prefix=PORT_"        yaml:"port"`
	TLS  TLSConfig `env:", prefix=TLS_"         yaml:"tls"`

	// ForceShutdownAfter is how long the process waits after a termination
	// signal before exiting outright. It has to outlast a generation the
	// service has already accepted, or every rolling deploy severs work in
	// progress; Validate enforces that against worker_pool.generation_timeout.
	ForceShutdownAfter time.Duration `env:"FORCE_SHUTDOWN_AFTER,default=150s" yaml:"force_shutdown_after"`

	// TrustedProxies lists the CIDRs whose X-Forwarded-For and X-Real-IP
	// headers may be believed. Anything else is taken at its connecting
	// address, so a header from an untrusted source cannot forge a caller.
	//
	// Empty means the TCP peer is the client. That is right for a listener
	// clients reach directly and wrong behind a proxy, where every caller
	// arrives from the proxy's address: rate limits and the per-caller
	// concurrency limit collapse into a single bucket shared by the world, and
	// the audit log records the proxy instead of who acted. Set this to the
	// range your ingress runs in.
	TrustedProxies []string `env:"TRUSTED_PROXIES" yaml:"trusted_proxies"`

	// MaxRecvMsgSize and MaxSendMsgSize bound one gRPC message in each
	// direction. They are set explicitly because gRPC's own defaults do not fit
	// this service: 4 MiB received, which a request carrying a large proto tree
	// exceeds, and effectively unlimited sent, which lets a plugin's output
	// leave without anything having agreed to its size.
	//
	// MaxSendMsgSize must be at least registry.max_output_size, otherwise the
	// output a plugin is permitted to produce cannot be delivered; Validate
	// enforces it.
	MaxRecvMsgSize int `env:"MAX_RECV_MSG_SIZE,default=67108864" yaml:"max_recv_msg_size"`
	MaxSendMsgSize int `env:"MAX_SEND_MSG_SIZE,default=67108864" yaml:"max_send_msg_size"`

	// MaxConcurrentStreams bounds concurrent streams on one connection. gRPC
	// defaults to math.MaxUint32, so a single caller could open streams until
	// the process ran out of memory — each costs goroutines and buffers before
	// any limiter in the chain sees the request.
	MaxConcurrentStreams uint32 `env:"MAX_CONCURRENT_STREAMS,default=256" yaml:"max_concurrent_streams"`
}

// TrustedProxyPrefixes parses TrustedProxies. Validate has already rejected
// anything unparseable, so a bad entry here cannot reach a running server.
func (s Server) TrustedProxyPrefixes() ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(s.TrustedProxies))

	for _, raw := range s.TrustedProxies {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}

		prefix, err := netip.ParsePrefix(trimmed)
		if err != nil {
			return nil, fmt.Errorf("server.trusted_proxies: %q is not a CIDR: %w", trimmed, err)
		}

		prefixes = append(prefixes, prefix)
	}

	return prefixes, nil
}

// TLSConfig configures transport security for the gRPC server.
// An empty CertFile disables TLS entirely, which is only appropriate for local
// development: in that mode the service speaks plaintext and anything on the
// network can read and forge requests.
type TLSConfig struct {
	CertFile string `env:"CERT_FILE" yaml:"cert_file"`
	KeyFile  string `env:"KEY_FILE"  yaml:"key_file"`
	// ClientCAFile turns the listener into mutual TLS: clients must present a
	// certificate signed by this CA. Empty means server-side TLS only.
	ClientCAFile string `env:"CLIENT_CA_FILE" yaml:"client_ca_file"`
}

// Enabled reports whether the gRPC listener should serve TLS.
func (c TLSConfig) Enabled() bool {
	return c.CertFile != "" && c.KeyFile != ""
}

// MutualTLS reports whether clients must present a verified certificate.
func (c TLSConfig) MutualTLS() bool {
	return c.Enabled() && c.ClientCAFile != ""
}

// Ports defines listening ports for each protocol.
type Ports struct {
	GRPC   string `env:"GRPC, default=23410"   yaml:"grpc"`
	Metric string `env:"METRIC, default=23411" yaml:"metric"`
	Health string `env:"HEALTH, default=23412" yaml:"health"`
	MCP    string `env:"MCP, default=23413"    yaml:"mcp"`
}

// LogConfig holds the logging settings.
//
// The level was a command-line flag and nothing else: no YAML key, no variable,
// and absent from `config print`, which claims to show the configuration the
// service will run with. It is also the setting an operator reaches for most
// often, and the one they most often want to change without editing a committed
// docker-compose.yml.
//
// The default is info. The flag's default was debug, which on this service means
// full request tracing over other people's source code — the loudest possible
// setting, chosen by anyone who said nothing.
type LogConfig struct {
	Level string `env:"LEVEL,default=info" yaml:"level"`
}

// DBConfig holds database connection settings.
//
// The DSN carries the database password, so it is marked secret: `config print`
// redacts it, and anything else that reports the configuration back to a human
// has one place to ask rather than a list to remember.
type DBConfig struct {
	Postgres string `env:"POSTGRES_DSN" secret:"true" yaml:"postgres"`
}

// RegistryConfig configures plugin execution.
type RegistryConfig struct {
	PluginsDir    string `env:"PLUGINS_DIR, default=/plugins"     yaml:"plugins_dir"`
	MaxOutputSize int64  `env:"MAX_OUTPUT_SIZE, default=67108864" yaml:"max_output_size"`

	// CacheMaxBytes bounds the unpacked plugins under PluginsDir. Archives are
	// downloaded on demand and never removed on their own, so without a limit
	// the directory grows towards the size of every plugin ever requested and
	// the volume fills silently. 0 disables eviction.
	//
	// Only meaningful with object storage configured: without it the files on
	// disk are the sole copy and are never evicted.
	CacheMaxBytes int64 `env:"CACHE_MAX_BYTES, default=21474836480" yaml:"cache_max_bytes"`

	S3 S3Config `env:", prefix=S3_" yaml:"s3"`
}

// S3Config configures S3-compatible storage for plugin binaries.
// When Bucket is empty, S3 storage is disabled and only local plugins_dir is used.
// Credentials may also be supplied via REGISTRY_S3_ACCESS_KEY_ID and
// REGISTRY_S3_SECRET_ACCESS_KEY environment variables instead of YAML; when
// both are absent, the default AWS credential chain is used.
//
// Only the secret half of the credential is marked as such. The key id names
// the caller and is worth seeing in a printed configuration — redacting it would
// hide which key a deployment is using while protecting nothing.
type S3Config struct {
	Endpoint        string `env:"ENDPOINT"                 yaml:"endpoint"`
	Bucket          string `env:"BUCKET"                   yaml:"bucket"`
	Region          string `env:"REGION,default=us-east-1" yaml:"region"`
	Prefix          string `env:"PREFIX"                   yaml:"prefix"`
	AccessKeyID     string `env:"ACCESS_KEY_ID"            yaml:"access_key_id"`
	SecretAccessKey string `env:"SECRET_ACCESS_KEY"        secret:"true"           yaml:"secret_access_key"`
	ForcePathStyle  bool   `env:"FORCE_PATH_STYLE"         yaml:"force_path_style"`
}

// Enabled returns true if S3 storage is configured.
func (c S3Config) Enabled() bool {
	return c.Bucket != ""
}

// TelemetryConfig configures observability endpoints.
//
// Neither carries a default, and that is the setting: empty means no collector,
// and Init then builds no exporter at all. A default would make "off"
// inexpressible — an empty value in a config file is the zero value, so a
// default would immediately fill it back in — and the fallback it filled in
// would name a collector that is not there on any deployment that did not ask
// for one. The exporters connect lazily, so that costs an endless retry rather
// than an error, which is the quietest possible way to waste a deployment's
// time.
//
// Note the full variable names: the section prefix is part of them, so these are
// TELEMETRY_OTLP_ENDPOINT, TELEMETRY_PYROSCOPE_ENDPOINT and
// TELEMETRY_SERVICE_TIER.
//
// The endpoint was called TELEMETRY_OTEL_EXPORTER_OTLP_ENDPOINT until v0.13.0, a
// name that looked like the OpenTelemetry SDK variable without being it, and
// that the repository carried four separate comments to warn about. It is now
// named after its own key, and the real OTEL_EXPORTER_OTLP_ENDPOINT is read as
// an alternative — see envAliases — so an operator who knows OTel gets it right
// on the first try instead of getting a service with no traces and no error.
type TelemetryConfig struct {
	OTLPEndpoint      string `env:"OTLP_ENDPOINT"      yaml:"otlp_endpoint"`
	PyroscopeEndpoint string `env:"PYROSCOPE_ENDPOINT" yaml:"pyroscope_endpoint"`

	// ServiceTier tags traces and profiles with the licence tier this
	// deployment serves, for stacks that run community and enterprise side by
	// side. Empty on a deployment that runs only one, where the tag would
	// distinguish nothing.
	//
	// Declared here rather than read from the licence because the value has to
	// exist before the licence is fetched, and because it must agree with the
	// tier label Alloy derives from a container label. Two mechanisms deciding
	// the same thing is how they end up disagreeing.
	ServiceTier string `env:"SERVICE_TIER" yaml:"service_tier"`
}

// WorkerPoolConfig configures bounded concurrency for plugin execution.
//
// Workers and MaxConcurrentGenerations bound different things and are
// deliberately separate. A worker is busy only while a plugin is being located:
// a database lookup and, on a cache miss, a download and unpack from object
// storage — network-bound work. Running the plugin binary happens afterwards,
// outside the pool, and is CPU- and memory-bound. One number for both would
// either starve downloads or over-admit executions.
type WorkerPoolConfig struct {
	Workers   int `env:"WORKERS,default=4"     yaml:"workers"`
	QueueSize int `env:"QUEUE_SIZE,default=16" yaml:"queue_size"`

	// MaxConcurrentGenerations caps how many plugin processes may run at once.
	// Requests beyond it queue up to QueueSize deep and are then rejected with
	// ErrServerOverloaded rather than piling more processes onto the host.
	MaxConcurrentGenerations int `env:"MAX_CONCURRENT_GENERATIONS,default=16" yaml:"max_concurrent_generations"`

	GenerationTimeout time.Duration `env:"GENERATION_TIMEOUT,default=120s" yaml:"generation_timeout"`
	MaxRetries        int           `env:"MAX_RETRIES,default=2"           yaml:"max_retries"`
	ShutdownTimeout   time.Duration `env:"SHUTDOWN_TIMEOUT,default=30s"    yaml:"shutdown_timeout"`
}

// LicenseConfig points at the licence token and the key it is verified against,
// and configures how often it is re-validated. Without both a token and a
// public key the service runs in community mode.
//
// The env names below are relative to the section prefix, so they resolve to
// LICENSE_KEY, LICENSE_FILE, LICENSE_PUBLIC_KEY and LICENSE_CACHE_TTL.
type LicenseConfig struct {
	// Key is an inline PASETO token. Takes priority over File. It is a
	// credential — anyone holding it can run an enterprise tier — so it is
	// marked secret and never printed.
	Key string `env:"KEY" secret:"true" yaml:"key"`
	// File is a path to a file holding the token.
	File string `env:"FILE" yaml:"file"`

	// PublicKeys maps key id to hex-encoded Ed25519 public key. The key id in
	// the token footer selects one of these, which is what lets a signing key be
	// rotated without every deployment having to change key on the same day.
	//
	// Note that this makes the trust anchor configuration rather than a property
	// of the build: whoever can edit this file — or set LICENSE_PUBLIC_KEYS —
	// decides which authority may issue licences.
	//
	// The key id "*" is reserved and means "verify any token this map does not
	// otherwise cover", including one whose footer carries no usable key id.
	// That was a second setting, license.public_key, until v0.13.0: one trust
	// anchor described by two fields, spelled three ways across the YAML, the
	// environment and the chart's values, and already diverging between them.
	//
	// Through the environment: LICENSE_PUBLIC_KEYS="<kid>:<hex>,<kid>:<hex>".
	PublicKeys map[string]string `env:"PUBLIC_KEYS" yaml:"public_keys"`

	// CacheTTL is how often the token is re-validated. The default is stated
	// here rather than only in license.NewManager, which substitutes the same
	// five minutes for a non-positive value: a fallback that lives past the
	// config package is one `config print` cannot see, so the configuration
	// would report zero while the service ran on five minutes.
	CacheTTL time.Duration `env:"CACHE_TTL,default=5m" yaml:"cache_ttl"`
}

// hexEd25519KeyLength is the length of a hex-encoded Ed25519 public key.
const hexEd25519KeyLength = 64

// Validate reports configuration that could only be a mistake: a key that
// cannot be a key, or a key id that would not survive being written to an
// environment variable.
func (c LicenseConfig) Validate() error {
	for kid, hexKey := range c.PublicKeys {
		if kid == "" {
			return errors.New("license.public_keys: key id must not be empty")
		}

		// The separators of the environment encoding, "<kid>:<hex>,<kid>:<hex>":
		// a key id containing either would be unreadable from that layer while
		// working from the file.
		if strings.ContainsAny(kid, ",:") {
			return fmt.Errorf("license.public_keys: key id %q must not contain ',' or ':'", kid)
		}

		err := validateHexKey(fmt.Sprintf("license.public_keys[%s]", kid), hexKey)
		if err != nil {
			return err
		}
	}

	return nil
}

func validateHexKey(name, hexKey string) error {
	trimmed := strings.TrimSpace(hexKey)

	if len(trimmed) != hexEd25519KeyLength {
		return fmt.Errorf("%s: expected %d hex characters, got %d", name, hexEd25519KeyLength, len(trimmed))
	}

	_, decodeErr := hex.DecodeString(trimmed)
	if decodeErr != nil {
		return fmt.Errorf("%s: not valid hex: %w", name, decodeErr)
	}

	return nil
}

// RateLimitConfig configures per-IP rate limiting.
type RateLimitConfig struct {
	RequestsPerSecond float64       `env:"REQUESTS_PER_SECOND,default=10.0" yaml:"requests_per_second"`
	Burst             int           `env:"BURST,default=20"                 yaml:"burst"`
	CleanupInterval   time.Duration `env:"CLEANUP_INTERVAL,default=10m"     yaml:"cleanup_interval"`

	// MaxConcurrentPerIP bounds requests one client may have in flight at once.
	// Rate alone does not: a caller staying under the limit can still hold every
	// generation slot with long requests. Zero disables the check.
	MaxConcurrentPerIP int `env:"MAX_CONCURRENT_PER_IP,default=2" yaml:"max_concurrent_per_ip"`
}

// AuditConfig configures the audit log writer. Audit is an Enterprise feature:
// without it nothing is written regardless of these settings.
type AuditConfig struct {
	BufferSize     int           `env:"BUFFER_SIZE,default=1000"   yaml:"buffer_size"`
	BatchSize      int           `env:"BATCH_SIZE,default=100"     yaml:"batch_size"`
	FlushInterval  time.Duration `env:"FLUSH_INTERVAL,default=1s"  yaml:"flush_interval"`
	MaxSaveRetries int           `env:"MAX_SAVE_RETRIES,default=3" yaml:"max_save_retries"`

	// EnqueueTimeout bounds how long an operation waits for room in the audit
	// queue. It is the one place audit can slow a request down, and only when
	// the queue is genuinely backed up: a healthy writer drains
	// BatchSize/FlushInterval entries a second.
	EnqueueTimeout time.Duration `env:"ENQUEUE_TIMEOUT,default=1s" yaml:"enqueue_timeout"`
	// FlushTimeout bounds one write to storage, retries included. A batch that
	// misses it is lost and counted rather than holding the writer up behind a
	// database that has stopped answering.
	FlushTimeout time.Duration `env:"FLUSH_TIMEOUT,default=5s" yaml:"flush_timeout"`

	// RetentionMonths is how many months of audit history to keep. 0 keeps everything.
	RetentionMonths        int           `env:"RETENTION_MONTHS,default=12"         yaml:"retention_months"`
	PreCreateMonths        int           `env:"PRE_CREATE_MONTHS,default=3"         yaml:"pre_create_months"`
	PartitionCheckInterval time.Duration `env:"PARTITION_CHECK_INTERVAL,default=6h" yaml:"partition_check_interval"`
	PartitionOpTimeout     time.Duration `env:"PARTITION_OP_TIMEOUT,default=30s"    yaml:"partition_op_timeout"`
}

// RetentionEnabled reports whether expired audit partitions should be dropped.
// Zero means keep everything.
func (c AuditConfig) RetentionEnabled() bool {
	return c.RetentionMonths > 0
}

// Validate performs structural validation of the configuration.
// Called by the server at startup and by LoadAndValidate.

func (c *Config) Validate() error {
	if c.Server.Port.GRPC == "" {
		return errors.New("server.port.grpc is required")
	}

	// The remaining ports are parsed long after startup begins — metric and mcp
	// only once the listeners are built — so an empty one would fail after the
	// migrations had run rather than before anything happened.
	//
	// Iterated in a fixed order rather than over the map directly: Go randomises
	// map iteration, so a config with two empty ports would report a different
	// one on each run and a test pinning the message would flake.
	for _, port := range []struct {
		name  string
		value string
	}{
		{"server.port.metric", c.Server.Port.Metric},
		{"server.port.health", c.Server.Port.Health},
		{"server.port.mcp", c.Server.Port.MCP},
	} {
		if port.value == "" {
			return fmt.Errorf("%s is required", port.name)
		}
	}

	err := c.validatePorts()
	if err != nil {
		return err
	}

	// A certificate without its key (or the reverse) is a half-applied change
	// that would otherwise silently fall back to plaintext.
	if (c.Server.TLS.CertFile != "") != (c.Server.TLS.KeyFile != "") {
		return errors.New("server.tls.cert_file and server.tls.key_file must be set together")
	}

	if c.Server.TLS.ClientCAFile != "" && !c.Server.TLS.Enabled() {
		return errors.New("server.tls.client_ca_file requires server.tls.cert_file and server.tls.key_file")
	}

	// Rejected at startup rather than skipped. A CIDR with a typo in it would
	// otherwise leave the list effectively empty, which is exactly the
	// misconfiguration this setting exists to prevent — and it would look like
	// it had been configured.
	_, prefixErr := c.Server.TrustedProxyPrefixes()
	if prefixErr != nil {
		return prefixErr
	}

	// Zero is not "no limit", it is a limit of nothing: every generation runs to
	// completion and is then refused with "output limit exceeded (max 0 bytes)".
	// The env tag's default covers an omitted field on both paths now, so this
	// catches a value written down explicitly — and a stack that starts and
	// answers every request with the same puzzle is worse than one that refuses
	// to start.
	if c.Registry.MaxOutputSize <= 0 {
		return fmt.Errorf(
			"registry.max_output_size must be positive, got %d: a plugin's output is measured against it, "+
				"so zero refuses every generation",
			c.Registry.MaxOutputSize,
		)
	}

	// A send limit below what a plugin may produce is a request that does all
	// its work and then cannot be answered. Better to refuse the combination at
	// startup than to discover it on the first large generation.
	if int64(c.Server.MaxSendMsgSize) < c.Registry.MaxOutputSize {
		return fmt.Errorf(
			"server.max_send_msg_size (%d) must be at least registry.max_output_size (%d), "+
				"otherwise a plugin's permitted output cannot be delivered",
			c.Server.MaxSendMsgSize, c.Registry.MaxOutputSize,
		)
	}

	// Checked because an empty DSN is not refused by the driver: lib/pq falls
	// back to the libpq defaults, so on a host with PGHOST and PGDATABASE set the
	// service connects somewhere plausible and runs its migrations there.
	if c.DB.Postgres == "" {
		return errors.New("db.postgres is required: an empty DSN silently falls back to the libpq environment")
	}

	err = validateDSN(c.DB.Postgres)
	if err != nil {
		return err
	}

	err = c.validateWorkerPool()
	if err != nil {
		return err
	}

	// Killing the process before a generation it accepted can finish turns every
	// rolling deploy into severed requests.
	if c.Server.ForceShutdownAfter <= c.WorkerPool.GenerationTimeout {
		return fmt.Errorf(
			"server.force_shutdown_after (%s) must exceed worker_pool.generation_timeout (%s)",
			c.Server.ForceShutdownAfter, c.WorkerPool.GenerationTimeout,
		)
	}

	err = c.validateRateLimit()
	if err != nil {
		return err
	}

	err = c.validateAudit()
	if err != nil {
		return err
	}

	err = c.Auth.Validate()
	if err != nil {
		return err
	}

	err = c.License.Validate()
	if err != nil {
		return err
	}

	if c.Registry.S3.Enabled() {
		if c.Registry.PluginsDir == "" {
			return errors.New("registry.plugins_dir is required as local cache directory when S3 is enabled")
		}
		hasKey := c.Registry.S3.AccessKeyID != ""
		hasSecret := c.Registry.S3.SecretAccessKey != ""
		if hasKey != hasSecret {
			return errors.New("registry.s3.access_key_id and registry.s3.secret_access_key must be set together")
		}
	}

	err = c.validateS3Completeness()
	if err != nil {
		return err
	}

	err = c.validateCacheSize()
	if err != nil {
		return err
	}

	err = c.validateTelemetry()
	if err != nil {
		return err
	}

	err = c.validateLogLevel()
	if err != nil {
		return err
	}

	return c.validateRuntimeZeros()
}

// validateAudit checks the audit worker's numbers and the relationship between
// its retention and pre-creation windows.
func (c *Config) validateAudit() error {
	if c.Audit.BufferSize <= 0 {
		return fmt.Errorf("audit.buffer_size must be positive, got %d", c.Audit.BufferSize)
	}

	if c.Audit.BatchSize <= 0 {
		return fmt.Errorf("audit.batch_size must be positive, got %d", c.Audit.BatchSize)
	}

	if c.Audit.FlushInterval <= 0 {
		return fmt.Errorf("audit.flush_interval must be positive, got %s", c.Audit.FlushInterval)
	}

	if c.Audit.MaxSaveRetries < 0 {
		return fmt.Errorf("audit.max_save_retries must not be negative, got %d", c.Audit.MaxSaveRetries)
	}

	if c.Audit.RetentionMonths < 0 {
		return fmt.Errorf("audit.retention_months must not be negative, got %d", c.Audit.RetentionMonths)
	}

	if c.Audit.PreCreateMonths < 0 {
		return fmt.Errorf("audit.pre_create_months must not be negative, got %d", c.Audit.PreCreateMonths)
	}

	// Creating partitions further ahead than they are kept means the maintainer
	// makes a partition on one pass and drops it on the next. Retention of zero
	// keeps everything, so nothing is ahead of it.
	if c.Audit.RetentionEnabled() && c.Audit.PreCreateMonths > c.Audit.RetentionMonths {
		return fmt.Errorf(
			"audit.pre_create_months (%d) must not exceed audit.retention_months (%d), "+
				"otherwise partitions are created and then dropped by the same maintainer",
			c.Audit.PreCreateMonths, c.Audit.RetentionMonths,
		)
	}

	return nil
}

// validateRateLimit checks the limiter's numbers. Same reason as
// validateWorkerPool: Validate reads better as a list of areas than as one
// long chain of ifs.
func (c *Config) validateRateLimit() error {
	if c.RateLimit.RequestsPerSecond <= 0 {
		return fmt.Errorf("rate_limit.requests_per_second must be positive, got %f", c.RateLimit.RequestsPerSecond)
	}

	if c.RateLimit.Burst <= 0 {
		return fmt.Errorf("rate_limit.burst must be positive, got %d", c.RateLimit.Burst)
	}

	// The cleanup loop feeds this straight to time.NewTicker, which panics on a
	// non-positive interval — from a background goroutine, so it takes the
	// process down after startup has already reported success rather than
	// failing the configuration.
	if c.RateLimit.CleanupInterval <= 0 {
		return fmt.Errorf("rate_limit.cleanup_interval must be positive, got %s", c.RateLimit.CleanupInterval)
	}

	return nil
}

// validateWorkerPool checks the pool's own numbers. Split out of Validate,
// which had grown past the point where a reader could hold it in their head.
func (c *Config) validateWorkerPool() error {
	if c.WorkerPool.Workers <= 0 {
		return fmt.Errorf("worker_pool.workers must be positive, got %d", c.WorkerPool.Workers)
	}

	if c.WorkerPool.QueueSize <= 0 {
		return fmt.Errorf("worker_pool.queue_size must be positive, got %d", c.WorkerPool.QueueSize)
	}

	if c.WorkerPool.MaxConcurrentGenerations <= 0 {
		return fmt.Errorf(
			"worker_pool.max_concurrent_generations must be positive, got %d",
			c.WorkerPool.MaxConcurrentGenerations,
		)
	}

	// Checked before the comparison below, which would otherwise read a negative
	// timeout as "comfortably shorter than the shutdown budget" and pass. A
	// negative duration makes context.WithTimeout return an already-expired
	// context, so every generation fails on the deadline before the plugin runs.
	if c.WorkerPool.GenerationTimeout <= 0 {
		return fmt.Errorf("worker_pool.generation_timeout must be positive, got %s", c.WorkerPool.GenerationTimeout)
	}

	if c.WorkerPool.ShutdownTimeout <= 0 {
		return fmt.Errorf("worker_pool.shutdown_timeout must be positive, got %s", c.WorkerPool.ShutdownTimeout)
	}

	// Negative is not "no retries", it is no attempts at all: the pool runs
	// MaxRetries+1 of them, so -1 skips the loop and returns an empty response
	// with no error — a generation that looks like it succeeded and produced
	// nothing.
	if c.WorkerPool.MaxRetries < 0 {
		return fmt.Errorf(
			"worker_pool.max_retries must not be negative, got %d: the pool runs max_retries+1 attempts, "+
				"so a negative value runs none and returns an empty success",
			c.WorkerPool.MaxRetries,
		)
	}

	return nil
}

// maxConfigFileSize bounds the config file. A YAML larger than this is not a
// config; reading it in full and refusing is better than truncating it, which
// would surface as a parse error somewhere in the middle of the file.
const maxConfigFileSize = 1 << 20

// Result is everything one attempt at resolving a configuration produced: the
// settings themselves, where each of them came from, and what was wrong with the
// input.
//
// It is a struct rather than four return values because the callers that need
// the diagnostics are exactly the ones that used to drop them. With `_` in the
// third position, `config print` and `plugins push` each silently discarded the
// warnings they existed to surface; with a field, ignoring it has to be written
// down on purpose.
type Result struct {
	// Config is the resolved configuration. It is set even when the load
	// failed, so that `config print` can show the operator what the file would
	// have produced alongside the reason it will not start.
	Config *Config

	// Origins says which layer supplied each setting.
	Origins Origins

	// Diagnostics is everything wrong with the input, fatal or not.
	Diagnostics Diagnostics

	// EnvAliases maps a setting's canonical environment variable to the
	// alternative name that actually supplied its value, for the settings where
	// one did. `config print --origin` reports the name that was read, not the
	// name it stands for.
	EnvAliases map[string]string
}

// Load builds the configuration from a YAML file, the environment and the
// defaults in the `env` struct tags, and validates the result.
//
// Precedence, highest first:
//
//	environment  >  file  >  tag default
//
// So a variable that is set beats what the file says, which is what lets a
// secret stay out of a committed YAML, and a default only fills what neither
// supplied. A variable that is set but empty counts as unset — see
// environmentLookuper.
//
// This is the only way the service and the CLI build a Config from a file: both
// must see the same settings, or "plugins push" would talk to one store while
// the server read from another.
//
// The order is carried by an explicit merge rather than by layering the
// environment on top of the decoded file, because that layering cannot express
// it. envconfig sees a field the file set to zero and a field the file never
// mentioned as the same thing — both are the Go zero value by the time it runs —
// and fills each from the tag, so "cache_max_bytes: 0", written down to disable
// eviction, came back as 20 GiB. The document still knows the difference, so the
// decision is made there: see mergeFileValues.
//
// An unrecognised key is an error, not a warning. A configuration that starts
// on defaults after ignoring what the operator wrote is indistinguishable from
// one that was obeyed, and the three spellings this project carries — YAML
// snake_case, environment UPPER_SNAKE, Helm camelCase — guarantee the mistake
// will be made.
func Load(ctx context.Context, path string) (Result, error) {
	lookuper := newAliasLookuper(environmentLookuper())

	res, err := load(ctx, path, lookuper)

	return withEnvironmentFindings(res, lookuper, err)
}

// LoadFromEnv builds the configuration from the environment and the tag defaults
// alone, which is the shape every Helm deployment had before the chart began
// rendering a file, and still the shape of `config print` with no --cfg.
//
// It exists so that this path is not a second, diagnostics-blind loader: a
// mistyped variable has to be reported here too, and it was not reportable at
// all while the environment-only path was a bare call to ApplyEnv.
func LoadFromEnv(ctx context.Context) (Result, error) {
	lookuper := newAliasLookuper(environmentLookuper())

	res, err := loadFromEnv(ctx, lookuper)

	return withEnvironmentFindings(res, lookuper, err)
}

// withEnvironmentFindings adds what only the real process environment can say:
// which alternative variable names were read, and which variables were aimed at
// this service and matched nothing.
//
// Kept out of load and loadFromEnv because those take an injected lookuper, and
// a test that hands them a map has no environment to scan. UnknownEnv is
// exported and tested directly on an environ slice instead.
func withEnvironmentFindings(res Result, lookuper *aliasLookuper, err error) (Result, error) {
	res.EnvAliases = lookuper.used

	// Warnings, so they never turn a working configuration into a failure; they
	// are appended after the fatal check for that reason.
	unknown, envErr := UnknownEnv(os.Environ())
	if envErr != nil {
		return res, envErr
	}

	res.Diagnostics = append(res.Diagnostics, unknown...)

	return res, err
}

// Origin says which layer supplied a setting's value.
type Origin int

const (
	// OriginDefault is the `default=` in the field's env tag, or the zero value
	// where the field has no default.
	OriginDefault Origin = iota
	// OriginFile is a key written in the config file.
	OriginFile
	// OriginEnv is an environment variable that was set to something non-empty.
	OriginEnv
)

// String names the layer as `config print --origin` reports it.
func (o Origin) String() string {
	switch o {
	case OriginEnv:
		return SourceEnv
	case OriginFile:
		return SourceFile
	case OriginDefault:
		return "default"
	default:
		return "unknown"
	}
}

// Origins maps a setting's dotted YAML name to the layer that supplied it.
type Origins map[string]Origin

// environmentOrigins reports where each setting comes from when the service is
// started without a config file. There is no file layer there, so every setting
// is either an environment variable or a default.
func environmentOrigins(lookuper envconfig.Lookuper) (Origins, error) {
	leaves, err := Leaves()
	if err != nil {
		return nil, err
	}

	origins := make(Origins, len(leaves))

	for _, leaf := range leaves {
		origins[leaf.Name()] = OriginDefault

		if _, found := lookuper.Lookup(leaf.EnvKey); found {
			origins[leaf.Name()] = OriginEnv
		}
	}

	return origins, nil
}

// mergeFileValues resolves the three layers into resolved, which arrives holding
// the environment and the tag defaults, and reports where each value came from.
//
// The rule is the documented precedence, applied setting by setting: a variable
// that is set wins; failing that, a key the file names wins, whatever its value;
// failing both, what is already there stands — the tag default, or the zero
// value where the field has none.
//
// The middle clause is the whole point. "Whatever its value" includes zero, and
// zero is a documented setting for several fields: no retries, no cache
// eviction, no per-caller limit, keep audit forever. Decided from the struct
// alone, those are indistinguishable from a field the file never mentioned, and
// each would be silently replaced by its default. Decided from the document, the
// key is either written down or it is not.
//
// This used to be a hand-maintained list of the five fields where it mattered.
// The list was correct, and that was the problem: nothing checked it, so it held
// only as long as everyone adding a field with a default knew to look.
func mergeFileValues(
	resolved *Config,
	fromFile Config,
	doc map[string]any,
	lookuper envconfig.Lookuper,
) (Origins, error) {
	leaves, err := Leaves()
	if err != nil {
		return nil, err
	}

	origins := make(Origins, len(leaves))

	for _, leaf := range leaves {
		if _, found := lookuper.Lookup(leaf.EnvKey); found {
			origins[leaf.Name()] = OriginEnv

			continue
		}

		if documentHasKey(doc, leaf.YAMLPath) {
			leaf.Value(resolved).Set(leaf.Value(&fromFile))
			origins[leaf.Name()] = OriginFile

			continue
		}

		origins[leaf.Name()] = OriginDefault
	}

	return origins, nil
}

// documentHasKey reports whether path names a key that is present in doc,
// whatever its value.
func documentHasKey(doc map[string]any, path []string) bool {
	current := doc

	for depth, key := range path {
		value, ok := current[key]
		if !ok {
			return false
		}

		if depth == len(path)-1 {
			return true
		}

		nested, ok := value.(map[string]any)
		if !ok {
			return false
		}

		current = nested
	}

	return false
}

// load is Load with the environment injected, so that a test can pin what the
// file resolves to instead of inheriting whatever the shell that started it
// happened to export.
func load(ctx context.Context, path string, lookuper envconfig.Lookuper) (Result, error) {
	var res Result

	data, err := os.ReadFile(path)
	if err != nil {
		return res, fmt.Errorf("reading config file %s: %w", path, err)
	}

	if int64(len(data)) > maxConfigFileSize {
		return res, fmt.Errorf("config file %s is %d bytes, over the %d byte limit",
			path, len(data), maxConfigFileSize)
	}

	root, diags, err := parseDocument(data)
	if err != nil {
		return res, err
	}

	fromFile, doc, decodeDiags, err := decodeDocument(root)
	if err != nil {
		return res, err
	}

	diags = append(diags, decodeDiags...)

	docDiags, err := documentDiagnostics(root)
	if err != nil {
		return res, err
	}

	diags = append(diags, docDiags...)
	res.Diagnostics = diags

	// Started from zero rather than from the file: envconfig applies a tag
	// default only to a field that is still zero, so filling it first would let
	// the file suppress the very defaults the merge needs to fall back on.
	cfg := Config{}

	err = applyEnv(ctx, &cfg, lookuper)
	if err != nil {
		return res, err
	}

	origins, err := mergeFileValues(&cfg, fromFile, doc, lookuper)
	if err != nil {
		return res, err
	}

	res.Config = &cfg
	res.Origins = origins

	err = res.Diagnostics.Err()
	if err != nil {
		return res, fmt.Errorf("config file %s: %w", path, err)
	}

	err = cfg.Validate()
	if err != nil {
		return res, fmt.Errorf("config validation: %w", err)
	}

	return res, nil
}

// loadFromEnv is LoadFromEnv with the environment injected.
func loadFromEnv(ctx context.Context, lookuper envconfig.Lookuper) (Result, error) {
	var res Result

	cfg := Config{}

	applyErr := applyEnv(ctx, &cfg, lookuper)
	if applyErr != nil {
		return res, applyErr
	}

	origins, err := environmentOrigins(lookuper)
	if err != nil {
		return res, err
	}

	res.Config = &cfg
	res.Origins = origins

	err = res.Diagnostics.Err()
	if err != nil {
		return res, err
	}

	err = cfg.Validate()
	if err != nil {
		return res, fmt.Errorf("config validation: %w", err)
	}

	return res, nil
}

// parseDocument parses the file into a node tree, which is what carries the line
// number of every key, and reports a second YAML document if the file has one.
//
// A syntax error is fatal and is returned as an error: there is no document to
// diagnose, so there is nothing more useful to say than what the parser said.
func parseDocument(data []byte) (*yaml.Node, Diagnostics, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))

	var root yaml.Node

	err := dec.Decode(&root)

	switch {
	case errors.Is(err, io.EOF):
		// An empty file is a valid configuration: it names nothing, so every
		// setting comes from the environment and the defaults. This used to be
		// reported as "unrecognised field, ignored: EOF".
		return nil, nil, nil
	case err != nil:
		return nil, nil, fmt.Errorf("parsing config YAML: %w", err)
	}

	var diags Diagnostics

	// yaml.Unmarshal reads the first document and drops the rest without a
	// word, so a file whose second half is separated by a --- is half applied.
	// A second document that fails to parse is still a second document — the
	// settings after the --- would vanish all the same, so it gets the same
	// diagnostic rather than a silent pass.
	var extra yaml.Node
	extraErr := dec.Decode(&extra)
	if hasSecondDocument(&extra, extraErr) {
		diags = append(diags, Diagnostic{
			Severity: SeverityError,
			Source:   SourceFile,
			Line:     extra.Line,
			Message:  "the file holds more than one YAML document; only the first one would be read",
		})
	}

	return &root, diags, nil
}

// hasSecondDocument reports whether anything follows the first YAML document.
//
// Three outcomes have to be told apart, and only the first is harmless:
//
//   - io.EOF, or a document holding nothing: the file simply ended. A file that
//     ends in "---" parses as a second document containing a null scalar, which
//     drops nothing and is ordinary punctuation.
//   - any other parse error: there is more in the file and it does not parse.
//     Still a second document, and still silently dropped.
//   - a document with real content: the half of the file nobody would notice
//     was ignored.
func hasSecondDocument(doc *yaml.Node, err error) bool {
	if errors.Is(err, io.EOF) {
		return false
	}

	if err != nil {
		return true
	}

	if doc == nil || len(doc.Content) == 0 {
		return false
	}

	root := doc.Content[0]

	return root.Kind != yaml.ScalarNode || root.Tag != "!!null"
}

// decodeDocument turns the node tree into a Config and into the plain map that
// mergeFileValues consults for which keys the file actually names.
//
// A type error is reported per offending key rather than as one blob, and
// without the word "ignored": nothing is ignored, the configuration is refused.
func decodeDocument(root *yaml.Node) (Config, map[string]any, Diagnostics, error) {
	var fromFile Config

	if root == nil || len(root.Content) == 0 {
		return fromFile, nil, nil, nil
	}

	var diags Diagnostics

	err := root.Decode(&fromFile)
	if err != nil {
		var typeErr *yaml.TypeError
		if !errors.As(err, &typeErr) {
			return fromFile, nil, nil, fmt.Errorf("parsing config YAML: %w", err)
		}

		diags = append(diags, typeErrorDiagnostics(typeErr)...)
	}

	// A document that is not a mapping names no keys, which leaves every
	// setting to the environment and the defaults. The type errors above have
	// already said what is wrong with it.
	var doc map[string]any

	if resolveAlias(root.Content[0]).Kind == yaml.MappingNode {
		err = root.Decode(&doc)
		if err != nil {
			if _, ok := errors.AsType[*yaml.TypeError](err); !ok {
				return fromFile, nil, nil, fmt.Errorf("parsing config YAML: %w", err)
			}
		}
	}

	return fromFile, doc, diags, nil
}

// yamlErrorLine matches the "line N: " prefix yaml.v3 puts on each entry of a
// TypeError, so the number becomes a field rather than staying buried in prose.
var yamlErrorLine = regexp.MustCompile(`^line (\d+): `)

func typeErrorDiagnostics(typeErr *yaml.TypeError) Diagnostics {
	diags := make(Diagnostics, 0, len(typeErr.Errors))

	for _, message := range typeErr.Errors {
		diag := Diagnostic{
			Severity: SeverityError,
			Source:   SourceFile,
			Message:  message,
		}

		if match := yamlErrorLine.FindStringSubmatch(message); match != nil {
			line, err := strconv.Atoi(match[1])
			if err == nil {
				diag.Line = line
				diag.Message = strings.TrimPrefix(message, match[0])
			}
		}

		diags = append(diags, diag)
	}

	return diags
}

// Defaults returns a Config holding nothing but the defaults in the `env`
// struct tags — no file, no environment.
//
// It is what "this setting was not configured" means, and `config print
// --changed` compares against it to answer the question the deployment configs
// made hard: which of these hundred lines actually says something. A field with
// no default comes back as its zero value, which is the same answer.
func Defaults(ctx context.Context) (*Config, error) {
	cfg := Config{}

	err := applyEnv(ctx, &cfg, envconfig.MapLookuper(map[string]string{}))
	if err != nil {
		return nil, err
	}

	return &cfg, nil
}

// ApplyEnv overlays the environment and the struct tag defaults onto cfg,
// leaving values it already holds in place unless a variable is actually set.
//
// Exported so that the file path and the environment-only path resolve settings
// through the same call: a field that reads from the environment in one has to
// read from it in the other, and a manually maintained list of the ones that do
// drifts from the struct the moment a field is added.
func ApplyEnv(ctx context.Context, cfg *Config) error {
	return applyEnv(ctx, cfg, environmentLookuper())
}

// environmentLookuper reads the process environment, treating a variable that is
// set but empty as absent.
//
// This is what makes the layering safe next to docker-compose, where the idiom
// throughout deploy/ is `LICENSE_KEY: "${LICENSE_KEY:-}"` — a variable that is
// always defined and usually empty. os.LookupEnv reports those as found, so
// without this an unset shell variable would overwrite a value written in the
// config file with nothing, and a licence or a DSN in the YAML would vanish the
// moment the stack came up without one exported.
//
// The cost is that a setting cannot be blanked from the environment, only
// changed. Nothing here needs that: emptiness is how a feature is turned off in
// the file, and the file is where that decision belongs.
func environmentLookuper() envconfig.Lookuper {
	return emptyIsUnset{inner: envconfig.OsLookuper()}
}

type emptyIsUnset struct{ inner envconfig.Lookuper }

func (l emptyIsUnset) Lookup(key string) (string, bool) {
	val, found := l.inner.Lookup(key)
	if !found || val == "" {
		return "", false
	}

	return val, true
}

func applyEnv(ctx context.Context, cfg *Config, lookuper envconfig.Lookuper) error {
	err := envconfig.ProcessWith(ctx, &envconfig.Config{
		Target:   cfg,
		Lookuper: lookuper,
		// Without this envconfig leaves any field the file already filled and
		// skips the lookup entirely, so the environment could not override a
		// setting written into the YAML.
		DefaultOverwrite: true,
	})
	if err != nil {
		return fmt.Errorf("envconfig.ProcessWith: %w", err)
	}

	return nil
}
