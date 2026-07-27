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
}

// Server holds HTTP/gRPC server settings.
type Server struct {
	Host string `env:"HOST, default=0.0.0.0" yaml:"host"`
	Port Ports  `env:", prefix=PORT_"        yaml:"port"`
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
	PluginsDir    string   `env:"PLUGINS_DIR, default=/plugins"     yaml:"plugins_dir"`
	MaxOutputSize int64    `env:"MAX_OUTPUT_SIZE, default=67108864" yaml:"max_output_size"`
	S3            S3Config `env:", prefix=S3_"                      yaml:"s3"`
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
type WorkerPoolConfig struct {
	Workers           int           `env:"WORKERS,default=4"               yaml:"workers"`
	QueueSize         int           `env:"QUEUE_SIZE,default=16"           yaml:"queue_size"`
	GenerationTimeout time.Duration `env:"GENERATION_TIMEOUT,default=120s" yaml:"generation_timeout"`
	MaxRetries        int           `env:"MAX_RETRIES,default=2"           yaml:"max_retries"`
	ShutdownTimeout   time.Duration `env:"SHUTDOWN_TIMEOUT,default=30s"    yaml:"shutdown_timeout"`
}

// LicenseConfig configures the license cache.
type LicenseConfig struct {
	CacheTTL time.Duration `env:"LICENSE_CACHE_TTL" yaml:"cache_ttl"`
}

// RateLimitConfig configures per-IP rate limiting.
type RateLimitConfig struct {
	RequestsPerSecond float64       `env:"REQUESTS_PER_SECOND,default=10.0" yaml:"requests_per_second"`
	Burst             int           `env:"BURST,default=20"                 yaml:"burst"`
	CleanupInterval   time.Duration `env:"CLEANUP_INTERVAL,default=10m"     yaml:"cleanup_interval"`
}

// Validate performs structural validation of the configuration.
// Called by the server at startup and by "epctl config validate".
func (c *Config) Validate() error {
	if c.Server.Port.GRPC == "" {
		return fmt.Errorf("server.port.grpc is required")
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

	if c.RateLimit.RequestsPerSecond <= 0 {
		return fmt.Errorf("rate_limit.requests_per_second must be positive, got %f", c.RateLimit.RequestsPerSecond)
	}

	if c.RateLimit.Burst <= 0 {
		return fmt.Errorf("rate_limit.burst must be positive, got %d", c.RateLimit.Burst)
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
