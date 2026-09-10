package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/easyp-tech/service/internal/auth"
)

type acceptGood struct{}

func (acceptGood) Authenticate(_ context.Context, md metadata.MD) (auth.Actor, error) {
	if values := md.Get("authorization"); len(values) == 1 && values[0] == "Bearer good" {
		return auth.Actor{Name: "ci", Kind: "static"}, nil
	}

	return auth.Actor{}, auth.ErrNoCredentials //nolint:exhaustruct // the zero actor is the point
}

// TestRequireBearerGatesMCP covers the surface require_authentication would
// otherwise miss. MCP is plain HTTP outside the gRPC interceptor chain, so
// closing the reads without this would close them everywhere except the one
// endpoint that carries no credential at all.
func TestRequireBearerGatesMCP(t *testing.T) {
	t.Parallel()

	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	guarded := requireBearer(inner, acceptGood{}, true, slog.New(slog.DiscardHandler))

	t.Run("no credential is refused", func(t *testing.T) {
		reached = false
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
		require.False(t, reached)
	})

	t.Run("a wrong credential is refused, and says no more than that", func(t *testing.T) {
		reached = false
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer wrong")
		guarded.ServeHTTP(rec, req)

		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.NotContains(t, rec.Body.String(), "wrong")
		require.False(t, reached)
	})

	t.Run("the right credential is served", func(t *testing.T) {
		reached = false
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer good")
		guarded.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.True(t, reached)
	})
}

// TestRequireBearerIsInertByDefault pins that the default costs nothing: the
// public catalogue keeps serving MCP anonymously, and the handler is returned
// untouched rather than wrapped in a check that always passes.
func TestRequireBearerIsInertByDefault(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	requireBearer(inner, acceptGood{}, false, slog.New(slog.DiscardHandler)).
		ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	require.Equal(t, http.StatusOK, rec.Code)
}
