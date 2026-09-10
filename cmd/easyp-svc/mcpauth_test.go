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

	cases := []struct {
		name       string
		credential string
		wantCode   int
		wantReach  bool
	}{
		{
			name:      "no credential is refused",
			wantCode:  http.StatusUnauthorized,
			wantReach: false,
		},
		{
			name:       "a wrong credential is refused",
			credential: "Bearer wrong",
			wantCode:   http.StatusUnauthorized,
			wantReach:  false,
		},
		{
			name:       "the right credential is served",
			credential: "Bearer good",
			wantCode:   http.StatusOK,
			wantReach:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reached := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
			if tc.credential != "" {
				req.Header.Set("Authorization", tc.credential)
			}

			rec := httptest.NewRecorder()
			requireBearer(inner, acceptGood{}, true, slog.New(slog.DiscardHandler)).ServeHTTP(rec, req)

			require.Equal(t, tc.wantCode, rec.Code)
			require.Equal(t, tc.wantReach, reached)

			if tc.wantCode == http.StatusUnauthorized {
				// The same reticence the gRPC path shows: a caller learns
				// nothing from the difference between missing and wrong.
				require.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
				require.NotContains(t, rec.Body.String(), "wrong")
			}
		})
	}
}

// TestRequireBearerIsInertByDefault pins that the default costs nothing: the
// public catalogue keeps serving MCP anonymously, and the handler is returned
// untouched rather than wrapped in a check that always passes.
func TestRequireBearerIsInertByDefault(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	requireBearer(inner, acceptGood{}, false, slog.New(slog.DiscardHandler)).
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil))

	require.Equal(t, http.StatusOK, rec.Code)
}
