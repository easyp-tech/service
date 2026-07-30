// Package config provides shared configuration types and validation
// for both the EasyP server and the epctl CLI utility.
package config

import (
	"bytes"
	"fmt"
	"os"
	"time"

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

// DBConfig holds database connection settings.
type DBConfig struct {
	Driver   string `env:"DRIVER, default=postgres" yaml:"driver"`
	Postgres string `env:"POSTGRES_DSN"             yaml:"postgres"`
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
type S3Config struct {
	Endpoint        string `env:"ENDPOINT"         yaml:"endpoint"`
	Bucket          string `env:"BUCKET"           yaml:"bucket"`
	Region          string `env:"REGION,default=us-east-1" yaml:"region"`
	Prefix          string `env:"PREFIX"           yaml:"prefix"`
	AccessKeyID     string `env:"ACCESS_KEY_ID"    yaml:"access_key_id"`
	SecretAccessKey string `env:"SECRET_ACCESS_KEY" yaml:"secret_access_key"`
	ForcePathStyle  bool   `env:"FORCE_PATH_STYLE" yaml:"force_path_style"`
}

// Enabled returns true if S3 storage is configured.
func (c S3Config) Enabled() bool {
	return c.Bucket != ""
}

// TelemetryConfig configures observability endpoints.
type TelemetryConfig struct {
	OTLPEndpoint      string `env:"OTEL_EXPORTER_OTLP_ENDPOINT, default=localhost:4317" yaml:"otlp_endpoint"`
	PyroscopeEndpoint string `env:"PYROSCOPE_ENDPOINT, default=http://localhost:4040"   yaml:"pyroscope_endpoint"`
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
	// Key is an inline PASETO token. Takes priority over File.
	Key string `env:"KEY"  yaml:"key"`
	// File is a path to a file holding the token.
	File string `env:"FILE" yaml:"file"`

	// PublicKey is the hex-encoded Ed25519 key that licence tokens are verified
	// against. Note that this makes the trust anchor configuration rather than a
	// property of the build: whoever can edit this file — or set
	// LICENSE_PUBLIC_KEY — decides which authority may issue licences.
	PublicKey string `env:"PUBLIC_KEY" yaml:"public_key"`

	CacheTTL time.Duration `env:"CACHE_TTL" yaml:"cache_ttl"`
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
// Called by the server at startup and by "epctl config validate".
func (c *Config) Validate() error {
	if c.Server.Port.GRPC == "" {
		return fmt.Errorf("server.port.grpc is required")
	}

	// A certificate without its key (or the reverse) is a half-applied change
	// that would otherwise silently fall back to plaintext.
	if (c.Server.TLS.CertFile != "") != (c.Server.TLS.KeyFile != "") {
		return fmt.Errorf("server.tls.cert_file and server.tls.key_file must be set together")
	}

	if c.Server.TLS.ClientCAFile != "" && !c.Server.TLS.Enabled() {
		return fmt.Errorf("server.tls.client_ca_file requires server.tls.cert_file and server.tls.key_file")
	}

	if c.DB.Driver == "" {
		return fmt.Errorf("db.driver is required")
	}

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

	// Killing the process before a generation it accepted can finish turns every
	// rolling deploy into severed requests.
	if c.Server.ForceShutdownAfter <= c.WorkerPool.GenerationTimeout {
		return fmt.Errorf(
			"server.force_shutdown_after (%s) must exceed worker_pool.generation_timeout (%s)",
			c.Server.ForceShutdownAfter, c.WorkerPool.GenerationTimeout,
		)
	}

	if c.RateLimit.RequestsPerSecond <= 0 {
		return fmt.Errorf("rate_limit.requests_per_second must be positive, got %f", c.RateLimit.RequestsPerSecond)
	}

	if c.RateLimit.Burst <= 0 {
		return fmt.Errorf("rate_limit.burst must be positive, got %d", c.RateLimit.Burst)
	}

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

	if err := c.Auth.Validate(); err != nil {
		return err
	}

	if c.Registry.S3.Enabled() {
		if c.Registry.PluginsDir == "" {
			return fmt.Errorf("registry.plugins_dir is required as local cache directory when S3 is enabled")
		}
		hasKey := c.Registry.S3.AccessKeyID != ""
		hasSecret := c.Registry.S3.SecretAccessKey != ""
		if hasKey != hasSecret {
			return fmt.Errorf("registry.s3.access_key_id and registry.s3.secret_access_key must be set together")
		}
	}

	return nil
}

// LoadAndValidate reads a YAML file, parses it with strict field checking,
// and returns the config, a list of warnings (for unknown fields), and any error.
func LoadAndValidate(path string) (*Config, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	// First pass: strict parsing to detect unknown fields.
	var warnings []string

	strictDec := yaml.NewDecoder(bytes.NewReader(data))
	strictDec.KnownFields(true)

	var strictCfg Config

	if strictErr := strictDec.Decode(&strictCfg); strictErr != nil {
		// If it's a type error about unknown fields, collect as warnings and re-parse leniently.
		warnings = append(warnings, strictErr.Error())
	}

	// Second pass: lenient parsing to get the actual config.
	lenientDec := yaml.NewDecoder(bytes.NewReader(data))

	var cfg Config
	if err = lenientDec.Decode(&cfg); err != nil {
		return nil, warnings, fmt.Errorf("parsing config YAML: %w", err)
	}

	if err = cfg.Validate(); err != nil {
		return &cfg, warnings, fmt.Errorf("config validation: %w", err)
	}

	return &cfg, warnings, nil
}
