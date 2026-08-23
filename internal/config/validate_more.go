package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
)

// ErrInvalidDSN marks a connection string the driver could not use at all.
var ErrInvalidDSN = errors.New("invalid database DSN")

// ParsePort turns a configured port into the number a listener needs, refusing
// what a listener would refuse.
//
// Ports are strings in the configuration and stay strings: the chart renders
// them quoted (`grpc: "{{ .Values.ports.grpc | int }}"`), and a numeric field
// would refuse every ConfigMap already deployed. So the check lives here rather
// than in the type, and lives in one place rather than at each of the four
// call sites that used to do their own strconv after startup was under way.
func ParsePort(name, value string) (uint16, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 16)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number between 1 and 65535, got %q", name, value)
	}

	// Zero is not a port. The kernel reads it as "assign me any free one", so a
	// service that took it would listen somewhere nobody can predict — and the
	// health probe, the Service and the compose port mapping all name a fixed
	// number.
	if parsed == 0 {
		return 0, fmt.Errorf("%s must be between 1 and 65535, got 0: "+
			"zero asks the kernel for an arbitrary port, which nothing else could find", name)
	}

	return uint16(parsed), nil
}

func (c *Config) validatePorts() error {
	ports := []struct {
		name  string
		value string
	}{
		{"server.port.grpc", c.Server.Port.GRPC},
		{"server.port.metric", c.Server.Port.Metric},
		{"server.port.health", c.Server.Port.Health},
		{"server.port.mcp", c.Server.Port.MCP},
	}

	seen := make(map[uint16]string, len(ports))

	for _, port := range ports {
		parsed, err := ParsePort(port.name, port.value)
		if err != nil {
			return err
		}

		// Two listeners on one port fail at bind, in a message from the network
		// stack that names neither setting.
		if other, clash := seen[parsed]; clash {
			return fmt.Errorf("%s and %s are both %d: each listener needs its own port",
				other, port.name, parsed)
		}

		seen[parsed] = port.name
	}

	return nil
}

// validateDSN refuses a connection string the driver could not use.
//
// "i am not a dsn" used to be valid: the only check was that it was not empty,
// so the failure arrived after the process had started, as a connection error
// during migrations. The person who mistyped it is running `config validate`,
// not reading the pod's logs an hour later.
//
// A URL-shaped DSN is parsed by the driver's own parser. The keyword-value form
// ("host=localhost user=easyp") is equally valid and lib/pq exports no parser
// for it, so it is checked only for shape: at least one key=value pair. That is
// enough to reject the case this was written for — a DSN of ordinary prose,
// which used to pass because the only test was that it was not empty — without
// second-guessing a connection string the driver would have accepted.
func validateDSN(dsn string) error {
	trimmed := strings.TrimSpace(dsn)

	if strings.HasPrefix(trimmed, "postgres://") || strings.HasPrefix(trimmed, "postgresql://") {
		// url.Parse rather than pq.ParseURL, which is deprecated now that the
		// driver accepts a URL directly. The check is the same shape either
		// way: a URL the driver could not use at all, not a semantic review of
		// its parameters.
		parsed, parseErr := url.Parse(trimmed)
		if parseErr != nil {
			return fmt.Errorf("db.postgres is not a usable connection string: %w", parseErr)
		}

		if parsed.Host == "" {
			return fmt.Errorf("%w: db.postgres names no host", ErrInvalidDSN)
		}

		return nil
	}

	for field := range strings.FieldsSeq(trimmed) {
		if key, _, found := strings.Cut(field, "="); found && key != "" {
			return nil
		}
	}

	return fmt.Errorf("db.postgres is not a usable connection string: %q is neither a postgres:// URL "+
		`nor a keyword/value DSN such as "host=localhost user=easyp dbname=easyp"`, trimmed)
}

// validateS3Completeness refuses a half-configured object store.
//
// S3 counts as enabled when the bucket is set (S3Config.Enabled), so an endpoint
// and a key pair with no bucket is a section that was filled in, passes
// validation and does nothing: plugins keep going to the local directory, and
// the operator has every reason to believe otherwise. TLS has been refused for
// the same shape of mistake since before this; object storage was not.
//
// Region is deliberately not part of the trigger. It carries a default, so it is
// never empty, and including it would fire on every configuration in existence.
func (c *Config) validateS3Completeness() error {
	if c.Registry.S3.Enabled() {
		return nil
	}

	filled := map[string]string{
		"registry.s3.endpoint":          c.Registry.S3.Endpoint,
		"registry.s3.access_key_id":     c.Registry.S3.AccessKeyID,
		"registry.s3.secret_access_key": c.Registry.S3.SecretAccessKey,
	}

	for _, name := range []string{
		"registry.s3.endpoint",
		"registry.s3.access_key_id",
		"registry.s3.secret_access_key",
	} {
		if filled[name] != "" {
			return fmt.Errorf("%s is set but registry.s3.bucket is empty, "+
				"so object storage is off and this setting does nothing", name)
		}
	}

	return nil
}

// validateCacheSize checks the plugin cache ceiling against what it has to hold.
//
// Zero stays a setting: it disables eviction, which is documented and is what a
// deployment with its own disk management wants. Negative is not a setting, and
// a ceiling below one plugin's permitted output is a cache that evicts whatever
// it just wrote.
func (c *Config) validateCacheSize() error {
	if c.Registry.CacheMaxBytes < 0 {
		return fmt.Errorf("registry.cache_max_bytes must not be negative, got %d "+
			"(zero disables eviction)", c.Registry.CacheMaxBytes)
	}

	if c.Registry.CacheMaxBytes > 0 && c.Registry.CacheMaxBytes < c.Registry.MaxOutputSize {
		return fmt.Errorf("registry.cache_max_bytes (%d) is below registry.max_output_size (%d): "+
			"a cache that cannot hold one plugin's output evicts what it has just written",
			c.Registry.CacheMaxBytes, c.Registry.MaxOutputSize)
	}

	return nil
}

// validateTelemetry checks the tier label against the tiers that exist.
//
// It was a free-form string compared with nothing, so TELEMETRY_SERVICE_TIER
// with a typo produced traces and profiles labelled with a value no dashboard
// and no alert rule matches — which looks exactly like a service that stopped
// reporting.
func (c *Config) validateTelemetry() error {
	switch c.Telemetry.ServiceTier {
	case "", serviceTierCommunity, serviceTierEnterprise:
		return nil
	default:
		return fmt.Errorf("telemetry.service_tier must be %q or %q, got %q",
			serviceTierCommunity, serviceTierEnterprise, c.Telemetry.ServiceTier)
	}
}

// The tier names are spelled here rather than imported from internal/core: the
// dependency would run the wrong way, and these are the strings that appear in
// a config file and on a metric label, which is a narrower contract than the
// domain type.
const (
	serviceTierCommunity  = "community"
	serviceTierEnterprise = "enterprise"
)

// validateRuntimeZeros refuses the zeros that the runtime would quietly replace
// with something else.
//
// The loader was taught to carry an explicit zero through untouched, because
// several fields document zero as a setting. For these fields it is not: the
// consumer substitutes a default when it sees zero, so `config print` would
// report 0 while the service ran on 5m, 64 MiB or 256. A configuration report
// that disagrees with the running service is worse than no report, so the input
// is refused instead.
func (c *Config) validateRuntimeZeros() error {
	checks := []struct {
		name    string
		ok      bool
		value   any
		explain string
	}{
		{
			name:    "license.cache_ttl",
			ok:      c.License.CacheTTL > 0,
			value:   c.License.CacheTTL,
			explain: "the licence manager replaces a non-positive TTL with 5m",
		},
		{
			name:    "server.max_recv_msg_size",
			ok:      c.Server.MaxRecvMsgSize > 0,
			value:   c.Server.MaxRecvMsgSize,
			explain: "the gRPC server replaces a non-positive limit with 64 MiB",
		},
		{
			name:    "server.max_concurrent_streams",
			ok:      c.Server.MaxConcurrentStreams > 0,
			value:   c.Server.MaxConcurrentStreams,
			explain: "the gRPC server replaces zero with 256; unlimited is not expressible here",
		},
		{
			name:    "audit.flush_timeout",
			ok:      c.Audit.FlushTimeout > 0,
			value:   c.Audit.FlushTimeout,
			explain: "a zero timeout fails every write immediately",
		},
		{
			name:    "audit.partition_check_interval",
			ok:      c.Audit.PartitionCheckInterval > 0,
			value:   c.Audit.PartitionCheckInterval,
			explain: "a zero interval panics the partition ticker after startup has reported success",
		},
		{
			name:    "audit.partition_op_timeout",
			ok:      c.Audit.PartitionOpTimeout > 0,
			value:   c.Audit.PartitionOpTimeout,
			explain: "a zero timeout cancels every partition operation before it starts",
		},
	}

	for _, check := range checks {
		if !check.ok {
			return fmt.Errorf("%s must be positive, got %v: %s", check.name, check.value, check.explain)
		}
	}

	return nil
}

// validateLogLevel refuses a level the logger could not read.
//
// Deliberately stricter than the --log_level flag, which warns and falls back to
// info. A typo on the command line at three in the morning should not stop the
// service from coming up; a typo committed to a config file should be caught by
// the check that exists to catch it.
func (c *Config) validateLogLevel() error {
	var level slog.Level

	err := level.UnmarshalText([]byte(c.Log.Level))
	if err != nil {
		return fmt.Errorf("log.level must be one of debug, info, warn, error, got %q", c.Log.Level)
	}

	return nil
}
