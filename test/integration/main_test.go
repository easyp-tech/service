//go:build integration

// Package integration exercises the service against a real PostgreSQL and a
// real plugin binary.
//
// It exists because the defects that reached production so far were not the kind
// unit tests catch: a chart value rendered in a form the service could not parse,
// a release build missing a directory, a worker pool that bounded the wrong
// thing. All three needed something to actually run.
//
// Guarded by a build tag so `go test ./...` stays hermetic. Supply a database:
//
//	docker run -d --name easyp-it -e POSTGRES_PASSWORD=pg -e POSTGRES_DB=easyp \
//	  -p 5439:5432 postgres:16-alpine
//	EASYP_TEST_DSN='postgres://postgres:pg@localhost:5439/easyp?sslmode=disable' \
//	  go test -tags integration ./test/integration/...
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	adapter_metrics "github.com/easyp-tech/service/internal/adapters/metrics"
	"github.com/easyp-tech/service/internal/adapters/registry"
	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/database"
	"github.com/easyp-tech/service/internal/database/connectors"
	"github.com/easyp-tech/service/internal/database/goosemigrate"
)

// dsnEnv names the database to run against. Absent means skip: a developer
// without Docker running should not see a wall of failures.
const dsnEnv = "EASYP_TEST_DSN"

// TestMain migrates once for the whole package. Per-test migration works, but
// goose takes a Postgres session lock, so parallel tests would queue behind each
// other for no benefit.
func TestMain(m *testing.M) {
	dsn := os.Getenv(dsnEnv)
	if dsn != "" {
		if err := goosemigrate.Up(context.Background(), dsn); err != nil {
			fmt.Fprintf(os.Stderr, "migrating %s: %v\n", dsnEnv, err)
			os.Exit(1)
		}
	}

	os.Exit(m.Run())
}

func requireDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set; see the package comment for how to supply a database", dsnEnv)
	}

	return dsn
}

// harness is everything the service needs to answer a request, minus the gRPC
// layer: the transport is thin and well covered, whereas the path from a request
// through the pool into an actual process is not covered anywhere else.
type harness struct {
	core       *core.Core
	pool       *core.WorkerPool
	pluginsDir string
}

func newHarness(t *testing.T, gate core.FeatureGate) *harness {
	t.Helper()

	ctx := t.Context()
	dsn := requireDSN(t)

	reg := prometheus.NewRegistry()
	metrics := adapter_metrics.New(reg, "easyp")

	db, err := database.NewSQL(ctx, "postgres",
		database.SQLConfig{Metrics: database.NewMetrics(reg, "easyp", "repo", new(core.Registry))},
		&connectors.Raw{Query: dsn})
	require.NoError(t, err)

	pluginsDir := t.TempDir()

	// No binary storage: the plugin is already on disk, which is the same state
	// the service reaches after a cache miss and a successful download.
	repo, err := registry.New(ctx, db, pluginsDir, 64<<20, nil, registry.CacheOptions{})
	require.NoError(t, err)

	t.Cleanup(func() { _ = repo.Close() })

	logger := slog.New(slog.DiscardHandler)

	pool := core.NewWorkerPool(repo, core.WorkerPoolConfig{
		Workers:                  2,
		QueueSize:                8,
		MaxConcurrentGenerations: 4,
		GenerationTimeout:        30 * time.Second,
		MaxRetries:               1,
	}, logger, metrics, reg, "easyp")

	// Without Start the workers never exist and Get blocks on its result channel
	// until the context dies — which is exactly how this was first written, and
	// it cost a three-minute test timeout to notice.
	pool.Start(t.Context())

	t.Cleanup(func() { pool.Shutdown(5 * time.Second) })

	return &harness{
		core:       core.New(metrics, pool, gate, nil, logger),
		pool:       pool,
		pluginsDir: pluginsDir,
	}
}

// buildStubPlugin compiles a plugin that speaks the real protocol — a
// CodeGeneratorRequest on stdin, a CodeGeneratorResponse on stdout — and puts it
// where the registry expects to find it.
//
// Compiled rather than faked with a shell script so the exec path, the pipes and
// the protobuf round trip are all the real ones.
func buildStubPlugin(t *testing.T, h *harness, group, name, version string) string {
	t.Helper()

	versionDir := filepath.Join(h.pluginsDir, group, name, version)
	require.NoError(t, os.MkdirAll(versionDir, 0o750))

	binary := filepath.Join(versionDir, "plugin")

	// By file path, not package path: the source carries a build tag that
	// keeps it out of `go build ./...`, and building a file explicitly ignores
	// the tag while still resolving imports against this module.
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "stubplugin/main.go")
	build.Dir = packageDir(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")

	out, err := build.CombinedOutput()
	require.NoError(t, err, "building the stub plugin: %s", out)

	return binary
}

func registerPlugin(t *testing.T, h *harness, group, name, version, binary string) {
	t.Helper()

	cfg, err := json.Marshal(registry.PluginConfig{Command: []string{binary}})
	require.NoError(t, err)

	_, err = h.core.CreatePlugin(t.Context(), core.CreatePluginRequest{
		Group:   group,
		Name:    name,
		Version: version,
		Config:  cfg,
		// Not nil: the column is NOT NULL, and a nil slice marshals to NULL.
		Tags: []string{},
	})
	require.NoError(t, err)
}

// uniqueVersion keeps runs independent without truncating tables between them.
func uniqueVersion() string {
	return fmt.Sprintf("v0.0.%d", time.Now().UnixNano()%1_000_000)
}

// packageDir returns this package's directory, so the stub plugin can be built
// by a path relative to it no matter where the test was invoked from.
func packageDir(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	return wd
}
