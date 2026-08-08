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
	cfg := config.Config{} //nolint:exhaustruct // only the validated fields matter
	cfg.Server.Port.GRPC = "8080"
	cfg.DB.Driver = "postgres"
	cfg.WorkerPool.Workers = 1
	cfg.WorkerPool.QueueSize = 1
	cfg.WorkerPool.MaxConcurrentGenerations = 1
	cfg.WorkerPool.GenerationTimeout = time.Minute
	cfg.Server.ForceShutdownAfter = 2 * time.Minute
	cfg.RateLimit.RequestsPerSecond = 1
	cfg.RateLimit.Burst = 1
	cfg.Audit.BufferSize = 1
	cfg.Audit.BatchSize = 1
	cfg.Audit.FlushInterval = 1
	// Zero is rejected: it refuses every generation rather than lifting the cap.
	cfg.Registry.MaxOutputSize = 1
	cfg.Server.MaxSendMsgSize = 1

	return cfg
}

func TestTLSConfigPredicates(t *testing.T) {
	t.Parallel()

	empty := config.TLSConfig{} //nolint:exhaustruct
	require.False(t, empty.Enabled())
	require.False(t, empty.MutualTLS())

	serverOnly := config.TLSConfig{CertFile: "a.crt", KeyFile: "a.key"} //nolint:exhaustruct
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
