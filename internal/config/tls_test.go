package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

// baseConfig returns a config that passes validation, so each case below can
// change exactly one thing.
func baseConfig() config.Config {
	cfg := config.Config{}
	cfg.Server.Port.GRPC = "8080"
	cfg.Server.Port.Metric = "8081"
	cfg.Server.Port.Health = "8082"
	cfg.Server.Port.MCP = "8083"
	// An empty DSN is rejected: lib/pq would fall back to the libpq environment
	// and connect somewhere plausible rather than refusing.
	cfg.DB.Postgres = "postgres://user:pass@localhost:5432/db?sslmode=disable"
	cfg.WorkerPool.Workers = 1
	cfg.WorkerPool.QueueSize = 1
	cfg.WorkerPool.MaxConcurrentGenerations = 1
	cfg.WorkerPool.GenerationTimeout = time.Minute
	cfg.WorkerPool.ShutdownTimeout = time.Second
	cfg.Server.ForceShutdownAfter = 2 * time.Minute
	cfg.RateLimit.RequestsPerSecond = 1
	cfg.RateLimit.Burst = 1
	// Zero would reach time.NewTicker and panic from a background goroutine.
	cfg.RateLimit.CleanupInterval = time.Minute
	cfg.Audit.BufferSize = 1
	cfg.Audit.BatchSize = 1
	cfg.Audit.FlushInterval = 1
	// Zero is rejected: it refuses every generation rather than lifting the cap.
	cfg.Registry.MaxOutputSize = 1
	cfg.Server.MaxSendMsgSize = 1
	// Only checked once S3 is enabled, but set here so a case that turns S3 on
	// reaches the rule it is actually about.
	cfg.Registry.PluginsDir = "/plugins"
	// The fields below all carry struct-tag defaults, so a loaded config always
	// has them; this literal is built by hand and would otherwise present zeros
	// that no file could produce. Each is one the runtime used to silently
	// replace, which is why zero is now refused rather than carried.
	cfg.License.CacheTTL = 5 * time.Minute
	cfg.Server.MaxRecvMsgSize = 1
	cfg.Server.MaxConcurrentStreams = 1
	cfg.Audit.FlushTimeout = time.Second
	cfg.Audit.PartitionCheckInterval = time.Hour
	cfg.Audit.PartitionOpTimeout = 30 * time.Second
	cfg.Log.Level = "info"

	return cfg
}

func TestTLSConfigPredicates(t *testing.T) {
	t.Parallel()

	empty := config.TLSConfig{}
	require.False(t, empty.Enabled())
	require.False(t, empty.MutualTLS())

	serverOnly := config.TLSConfig{CertFile: "a.crt", KeyFile: "a.key"}
	require.True(t, serverOnly.Enabled())
	require.False(t, serverOnly.MutualTLS())

	mutual := config.TLSConfig{CertFile: "a.crt", KeyFile: "a.key", ClientCAFile: "ca.crt"}
	require.True(t, mutual.Enabled())
	require.True(t, mutual.MutualTLS())
}

func TestValidateTLS(t *testing.T) {
	t.Parallel()

	t.Run("no TLS is valid", func(t *testing.T) {
		t.Parallel()

		cfg := baseConfig()
		require.NoError(t, cfg.Validate())
	})

	t.Run("full mTLS is valid", func(t *testing.T) {
		t.Parallel()

		cfg := baseConfig()
		cfg.Server.TLS = config.TLSConfig{CertFile: "a.crt", KeyFile: "a.key", ClientCAFile: "ca.crt"}
		require.NoError(t, cfg.Validate())
	})

	t.Run("certificate without key is rejected", func(t *testing.T) {
		t.Parallel()

		cfg := baseConfig()
		cfg.Server.TLS.CertFile = "a.crt"
		require.ErrorContains(t, cfg.Validate(), "must be set together")
	})

	t.Run("key without certificate is rejected", func(t *testing.T) {
		t.Parallel()

		cfg := baseConfig()
		cfg.Server.TLS.KeyFile = "a.key"
		require.ErrorContains(t, cfg.Validate(), "must be set together")
	})

	t.Run("client CA without a server certificate is rejected", func(t *testing.T) {
		t.Parallel()

		cfg := baseConfig()
		cfg.Server.TLS.ClientCAFile = "ca.crt"
		require.ErrorContains(t, cfg.Validate(), "client_ca_file requires")
	})
}
