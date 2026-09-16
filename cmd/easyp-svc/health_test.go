package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func liveServer(t *testing.T, status int) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != healthLivePath {
			http.NotFound(w, r)

			return
		}

		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	return strings.TrimPrefix(srv.URL, "http://")
}

func TestProbeLiveAcceptsOK(t *testing.T) {
	t.Parallel()

	require.NoError(t, probeLive(t.Context(), liveServer(t, http.StatusOK)))
}

func TestProbeLiveRejectsNon200(t *testing.T) {
	t.Parallel()

	err := probeLive(t.Context(), liveServer(t, http.StatusServiceUnavailable))

	require.ErrorIs(t, err, errNotLive)
	require.ErrorContains(t, err, "503")
}

func TestProbeLiveRejectsUnreachable(t *testing.T) {
	t.Parallel()

	err := probeLive(t.Context(), "127.0.0.1:1")

	require.ErrorIs(t, err, errNotLive)
}

func TestResolveHealthAddrPrefersExplicit(t *testing.T) {
	t.Parallel()

	addr, err := resolveHealthAddr(t.Context(), "10.0.0.5:9999", "")

	require.NoError(t, err)
	require.Equal(t, "10.0.0.5:9999", addr)
}

func TestResolveHealthAddrFallsBackToDefaultPort(t *testing.T) {
	t.Setenv("DB_POSTGRES_DSN", "postgres://u:p@h:5432/d?sslmode=disable")

	addr, err := resolveHealthAddr(t.Context(), "", "")

	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:23412", addr)
}

func TestHealthCommandReadsConfigPathFromEnvironment(t *testing.T) {
	t.Setenv("DB_POSTGRES_DSN", "postgres://u:p@h:5432/d?sslmode=disable")

	_, port, err := net.SplitHostPort(liveServer(t, http.StatusOK))
	require.NoError(t, err)

	cfgPath := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("server:\n  port:\n    health: "+port+"\n"), 0o600))
	t.Setenv(envCfg, cfgPath)

	require.NoError(t, getHealthCommand().Run(t.Context(), []string{"health"}))
}
