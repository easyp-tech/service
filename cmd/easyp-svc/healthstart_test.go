package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hellofresh/health-go/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

// freePort returns a port nothing is listening on. The window between closing
// and rebinding is a race in principle; in a test binary it is not one worth
// engineering around.
func freePort(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := listener.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // Listen on tcp always yields *TCPAddr
	require.NoError(t, listener.Close())

	return strconv.Itoa(port)
}

func healthConfig(t *testing.T) config.Config {
	t.Helper()

	cfg := config.Config{} //nolint:exhaustruct // only the fields the health server reads
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port.Health = freePort(t)

	return cfg
}

func get(t *testing.T, cfg config.Config, path string) int {
	t.Helper()

	url := fmt.Sprintf("http://%s%s", net.JoinHostPort(cfg.Server.Host, cfg.Server.Port.Health), path)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode
}

// The startup probe starts counting the moment the container does, while
// migrations run before the database is usable. Liveness has to answer through
// that window or a slow migration reads as a hung process: the pod is killed and
// restarted straight back into the same migration.
//
// Readiness must not answer positively in the same window — a pod taking traffic
// against a half-migrated schema is worse than one that is briefly unavailable —
// which is why the two are tested together. Serving one handler for both would
// pass half of this.
func TestHealthAnswersBeforeReadinessIsWired(t *testing.T) {
	t.Parallel()

	cfg := healthConfig(t)
	readiness := new(atomic.Pointer[health.Health])

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	wait, err := startHealthServer(ctx, slog.New(slog.DiscardHandler), cfg, readiness)
	require.NoError(t, err, "the listener is bound synchronously, so this covers the port being taken")

	assert.Equal(t, http.StatusOK, get(t, cfg, "/live"),
		"liveness must answer before the database is wired up")
	assert.Equal(t, http.StatusServiceUnavailable, get(t, cfg, "/"),
		"readiness must not claim the pod can serve traffic before the database is wired up")

	checker, err := health.New()
	require.NoError(t, err)

	readiness.Store(checker)

	assert.Equal(t, http.StatusOK, get(t, cfg, "/"),
		"readiness must follow the checker once it is stored")

	cancel()

	require.NoError(t, wait())
}

// A port already in use has to fail the startup rather than be logged while the
// process runs on without a health endpoint — which is what a server started in
// a goroutine would do.
func TestHealthServerFailsOnBusyPort(t *testing.T) {
	t.Parallel()

	cfg := healthConfig(t)

	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.Server.Host, cfg.Server.Port.Health))
	require.NoError(t, err)

	defer func() { _ = listener.Close() }()

	_, err = startHealthServer(t.Context(), slog.New(slog.DiscardHandler), cfg, new(atomic.Pointer[health.Health]))
	require.Error(t, err)
}

// Shutdown is driven by the same context that cancels everything else, so the
// wait must return rather than block on a context it is itself reacting to.
func TestHealthServerStopsWithContext(t *testing.T) {
	t.Parallel()

	cfg := healthConfig(t)

	ctx, cancel := context.WithCancel(t.Context())

	wait, err := startHealthServer(ctx, slog.New(slog.DiscardHandler), cfg, new(atomic.Pointer[health.Health]))
	require.NoError(t, err)

	cancel()

	done := make(chan error, 1)
	go func() { done <- wait() }()

	select {
	case waitErr := <-done:
		assert.NoError(t, waitErr, "a clean shutdown is not an error")
	case <-time.After(5 * time.Second):
		t.Fatal("health server did not stop after its context was cancelled")
	}
}
