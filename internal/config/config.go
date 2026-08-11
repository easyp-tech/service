// Package config provides shared configuration types and validation
// for both the EasyP server and the epctl CLI utility.
package config

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
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
// TELEMETRY_OTEL_EXPORTER_OTLP_ENDPOINT and TELEMETRY_PYROSCOPE_ENDPOINT. The
// bare OTel SDK name is read by nothing here.
type TelemetryConfig struct {
	OTLPEndpoint      string `env:"OTEL_EXPORTER_OTLP_ENDPOINT" yaml:"otlp_endpoint"`
	PyroscopeEndpoint string `env:"PYROSCOPE_ENDPOINT"          yaml:"pyroscope_endpoint"`
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
	// against, for installations that trust a single key. Note that this makes
	// the trust anchor configuration rather than a property of the build:
	// whoever can edit this file — or set LICENSE_PUBLIC_KEY — decides which
	// authority may issue licences.
	//
	// It applies to any token whose key id names nothing in PublicKeys.
	PublicKey string `env:"PUBLIC_KEY" yaml:"public_key"`

	// PublicKeys maps key id to hex-encoded Ed25519 public key. The key id in
	// the token footer selects one of these, which is what lets a signing key be
	// rotated without every deployment having to change key on the same day.
	//
	// Through the environment: LICENSE_PUBLIC_KEYS="<kid>:<hex>,<kid>:<hex>".
	PublicKeys map[string]string `env:"PUBLIC_KEYS" yaml:"public_keys"`

	CacheTTL time.Duration `env:"CACHE_TTL" yaml:"cache_ttl"`
}

// hexEd25519KeyLength is the length of a hex-encoded Ed25519 public key.
const hexEd25519KeyLength = 64

// Validate reports configuration that could only be a mistake: a key that
// cannot be a key, or a key id that would not survive being written to an
// environment variable.
func (c LicenseConfig) Validate() error {
	for kid, hexKey := range c.PublicKeys {
		if kid == "" {
			return fmt.Errorf("license.public_keys: key id must not be empty")
		}

		if strings.ContainsAny(kid, ",:") {
			return fmt.Errorf("license.public_keys: key id %q must not contain ',' or ':'", kid)
		}

		if err := validateHexKey(fmt.Sprintf("license.public_keys[%s]", kid), hexKey); err != nil {
			return err
		}
	}

	if c.PublicKey != "" {
		return validateHexKey("license.public_key", c.PublicKey)
	}

	return nil
}

func validateHexKey(name, hexKey string) error {
	trimmed := strings.TrimSpace(hexKey)

	if len(trimmed) != hexEd25519KeyLength {
		return fmt.Errorf("%s: expected %d hex characters, got %d", name, hexEd25519KeyLength, len(trimmed))
	}

	if _, err := hex.DecodeString(trimmed); err != nil {
		return fmt.Errorf("%s: not valid hex: %w", name, err)
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
		return fmt.Errorf("server.port.grpc is required")
	}

	// The remaining ports are parsed long after startup begins — metric and mcp
	// only once the listeners are built — so an empty one would fail after the
	// migrations had run rather than before anything happened.
	for name, port := range map[string]string{
		"server.port.metric": c.Server.Port.Metric,
		"server.port.health": c.Server.Port.Health,
		"server.port.mcp":    c.Server.Port.MCP,
	} {
		if port == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	// A certificate without its key (or the reverse) is a half-applied change
	// that would otherwise silently fall back to plaintext.
	if (c.Server.TLS.CertFile != "") != (c.Server.TLS.KeyFile != "") {
		return fmt.Errorf("server.tls.cert_file and server.tls.key_file must be set together")
	}

	if c.Server.TLS.ClientCAFile != "" && !c.Server.TLS.Enabled() {
		return fmt.Errorf("server.tls.client_ca_file requires server.tls.cert_file and server.tls.key_file")
	}

	// Rejected at startup rather than skipped. A CIDR with a typo in it would
	// otherwise leave the list effectively empty, which is exactly the
	// misconfiguration this setting exists to prevent — and it would look like
	// it had been configured.
	if _, err := c.Server.TrustedProxyPrefixes(); err != nil {
		return err
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

	if c.DB.Driver == "" {
		return fmt.Errorf("db.driver is required")
	}

	// Checked because an empty DSN is not refused by the driver: lib/pq falls
	// back to the libpq defaults, so on a host with PGHOST and PGDATABASE set the
	// service connects somewhere plausible and runs its migrations there. The
	// driver above has a default and is nearly always right; this has neither.
	if c.DB.Postgres == "" {
		return fmt.Errorf("db.postgres is required: an empty DSN silently falls back to the libpq environment")
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

	// The cleanup loop feeds this straight to time.NewTicker, which panics on a
	// non-positive interval — from a background goroutine, so it takes the
	// process down after startup has already reported success rather than
	// failing the configuration.
	if c.RateLimit.CleanupInterval <= 0 {
		return fmt.Errorf("rate_limit.cleanup_interval must be positive, got %s", c.RateLimit.CleanupInterval)
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

	if err := c.Auth.Validate(); err != nil {
		return err
	}

	if err := c.License.Validate(); err != nil {
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

// maxConfigFileSize bounds the config file. A YAML larger than this is not a
// config; reading it in full and refusing is better than truncating it, which
// would surface as a parse error somewhere in the middle of the file.
const maxConfigFileSize = 1 << 20

// LoadAndValidate builds the configuration from a YAML file, the environment and
// the defaults in the `env` struct tags, and validates the result. It returns the
// config, warnings about unknown YAML fields, and any error.
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
// Two mechanisms carry that order, and neither is sufficient alone.
// DefaultOverwrite is what lets the environment win: without it envconfig skips
// any field the file already filled and never even looks the variable up. It is
// not enough on its own, because envconfig sees a field the file set to zero as
// indistinguishable from one the file never mentioned, and fills both from the
// tag — so restoreExplicitZeros reads the document to tell them apart.
func LoadAndValidate(ctx context.Context, path string) (*Config, []string, error) {
	return loadAndValidate(ctx, path, environmentLookuper())
}

// zeroIsASetting lists the fields whose zero value is a documented setting
// rather than an absence, and which also carry a default in their `env` tag.
//
// envconfig cannot tell those two apart. A field omitted from the YAML and a
// field the YAML set to 0 are both the Go zero value by the time it runs, so it
// fills each with the tag default — and "cache_max_bytes: 0", written down to
// disable eviction, comes back as 20 GiB. Reading the document settles it: the
// key is either there or it is not.
//
// Dropping the defaults instead would be simpler and worse. They are what keeps
// the environment-only path safe, and three of these turn a protection off:
// without a default, a deployment that set none of them would silently run with
// no cache eviction, no per-caller concurrency limit and no audit retention.
//
// Every other field with a default either rejects zero in Validate or treats it
// as the same thing the default means, so this list is the whole of it.
var zeroIsASetting = []explicitZero{
	{
		yamlPath: []string{"registry", "cache_max_bytes"},
		envKey:   "REGISTRY_CACHE_MAX_BYTES",
		restore:  func(dst *Config, src Config) { dst.Registry.CacheMaxBytes = src.Registry.CacheMaxBytes },
	},
	{
		yamlPath: []string{"worker_pool", "max_retries"},
		envKey:   "WORKER_POOL_MAX_RETRIES",
		restore:  func(dst *Config, src Config) { dst.WorkerPool.MaxRetries = src.WorkerPool.MaxRetries },
	},
	{
		yamlPath: []string{"rate_limit", "max_concurrent_per_ip"},
		envKey:   "RATE_LIMIT_MAX_CONCURRENT_PER_IP",
		restore:  func(dst *Config, src Config) { dst.RateLimit.MaxConcurrentPerIP = src.RateLimit.MaxConcurrentPerIP },
	},
	{
		yamlPath: []string{"audit", "max_save_retries"},
		envKey:   "AUDIT_MAX_SAVE_RETRIES",
		restore:  func(dst *Config, src Config) { dst.Audit.MaxSaveRetries = src.Audit.MaxSaveRetries },
	},
	{
		yamlPath: []string{"audit", "retention_months"},
		envKey:   "AUDIT_RETENTION_MONTHS",
		restore:  func(dst *Config, src Config) { dst.Audit.RetentionMonths = src.Audit.RetentionMonths },
	},
}

// explicitZero ties a field's place in the YAML document to its environment
// variable and to the assignment that puts the file's value back.
type explicitZero struct {
	yamlPath []string
	envKey   string
	restore  func(dst *Config, src Config)
}

// restoreExplicitZeros puts back the values the file stated outright and the
// environment did not override, for the fields in zeroIsASetting.
//
// The order it enforces is the one the rest of the loader promises: the
// environment beats the file, and the file beats the tag default — including
// when what the file says is zero.
func restoreExplicitZeros(cfg *Config, fromFile Config, data []byte, lookuper envconfig.Lookuper) error {
	var doc map[string]any

	// A document that is not a mapping (an empty file, say) states nothing, so
	// there is nothing to put back.
	if err := yaml.Unmarshal(data, &doc); err != nil || doc == nil {
		//nolint:nilerr // the decode above already parsed this; a shape we cannot walk means no explicit keys
		return nil
	}

	for _, field := range zeroIsASetting {
		if !documentHasKey(doc, field.yamlPath) {
			continue
		}

		// The environment still wins: it is the layer above the file.
		if _, found := lookuper.Lookup(field.envKey); found {
			continue
		}

		field.restore(cfg, fromFile)
	}

	return nil
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

// loadAndValidate is LoadAndValidate with the environment injected, so that a
// test can pin what the file resolves to instead of inheriting whatever the
// shell that started it happened to export.
func loadAndValidate(ctx context.Context, path string, lookuper envconfig.Lookuper) (*Config, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	if int64(len(data)) > maxConfigFileSize {
		return nil, nil, fmt.Errorf("config file %s is %d bytes, over the %d byte limit",
			path, len(data), maxConfigFileSize)
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

	// Kept before the overlay so an explicit zero in the file can be put back
	// afterwards; see restoreExplicitZeros.
	fromFile := cfg

	if err = applyEnv(ctx, &cfg, lookuper); err != nil {
		return nil, warnings, err
	}

	if err = restoreExplicitZeros(&cfg, fromFile, data, lookuper); err != nil {
		return nil, warnings, err
	}

	if err = cfg.Validate(); err != nil {
		return &cfg, warnings, fmt.Errorf("config validation: %w", err)
	}

	return &cfg, warnings, nil
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
	err := envconfig.ProcessWith(ctx, &envconfig.Config{ //nolint:exhaustruct // the rest of the defaults are right
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
