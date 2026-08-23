package api_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	generator "github.com/easyp-tech/service/api/generator/v1"
	"github.com/easyp-tech/service/internal/api"
	"github.com/easyp-tech/service/internal/auth"
	"github.com/easyp-tech/service/internal/core"
)

// fakeAuthenticator accepts exactly one token.
type fakeAuthenticator struct {
	accept string
}

func (f fakeAuthenticator) Authenticate(_ context.Context, md metadata.MD) (auth.Actor, error) {
	values := md.Get("authorization")
	if len(values) == 0 {
		return auth.Actor{}, auth.ErrNoCredentials
	}

	if values[0] != "Bearer "+f.accept {
		return auth.Actor{}, auth.ErrUnknownToken
	}

	return auth.Actor{Name: "ci", Kind: auth.KindToken}, nil
}

// recordingHandler notes whether it ran and what actor the context carried.
type recordingHandler struct {
	called bool
	actor  string
}

func (h *recordingHandler) unary(ctx context.Context, _ any) (any, error) {
	h.called = true
	h.actor = core.ActorFromContext(ctx)

	return "ok", nil
}

// fakeStream is the minimum ServerStream an interceptor needs.
type fakeStream struct {
	grpc.ServerStream

	ctx context.Context //nolint:containedctx // mirrors how grpc carries it
}

func (s *fakeStream) Context() context.Context { return s.ctx }

func newInterceptor(t *testing.T) *api.AuthInterceptor {
	t.Helper()

	return api.NewAuthInterceptor(
		fakeAuthenticator{accept: "good"},
		slog.New(slog.DiscardHandler),
		prometheus.NewRegistry(),
		"test",
	)
}

func ctxWith(t *testing.T, authorization string) context.Context {
	t.Helper()

	if authorization == "" {
		return t.Context()
	}

	return metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", authorization))
}

func TestAuthInterceptor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		method        string
		authorization string
		wantCalled    bool
		wantActor     string
	}{
		{
			name:       "GenerateCode is anonymous",
			method:     generator.ServiceAPI_GenerateCode_FullMethodName,
			wantCalled: true,
			wantActor:  "unknown",
		},
		{
			name:       "Plugins is anonymous",
			method:     generator.ServiceAPI_Plugins_FullMethodName,
			wantCalled: true,
			wantActor:  "unknown",
		},
		{
			name:       "health probes carry no credentials",
			method:     healthpb.Health_Check_FullMethodName,
			wantCalled: true,
			wantActor:  "unknown",
		},
		{
			name:       "CreatePlugin without a token is rejected",
			method:     generator.ServiceAPI_CreatePlugin_FullMethodName,
			wantCalled: false,
		},
		{
			name:          "UpdatePlugin with a wrong token is rejected",
			method:        generator.ServiceAPI_UpdatePlugin_FullMethodName,
			authorization: "Bearer wrong",
			wantCalled:    false,
		},
		{
			name:       "DeletePlugin without a token is rejected",
			method:     generator.ServiceAPI_DeletePlugin_FullMethodName,
			wantCalled: false,
		},
		{
			name:          "DeletePlugin with a valid token runs and names the actor",
			method:        generator.ServiceAPI_DeletePlugin_FullMethodName,
			authorization: "Bearer good",
			wantCalled:    true,
			wantActor:     "ci",
		},
		{
			name:       "an unlisted method is protected by default",
			method:     "/api.generator.v1.ServiceAPI/SomethingNew",
			wantCalled: false,
		},
	}

	for _, tt := range tests {
		t.Run("unary/"+tt.name, func(t *testing.T) {
			t.Parallel()

			handler := &recordingHandler{}
			interceptor := newInterceptor(t)

			_, err := interceptor.UnaryServerInterceptor()(
				ctxWith(t, tt.authorization),
				nil,
				&grpc.UnaryServerInfo{FullMethod: tt.method},
				handler.unary,
			)

			assert.Equal(t, tt.wantCalled, handler.called, "handler reached")

			if !tt.wantCalled {
				require.Equal(t, codes.Unauthenticated, status.Code(err))

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantActor, handler.actor)
		})

		t.Run("stream/"+tt.name, func(t *testing.T) {
			t.Parallel()

			called := false
			interceptor := newInterceptor(t)

			err := interceptor.StreamServerInterceptor()(
				nil,
				&fakeStream{ctx: ctxWith(t, tt.authorization)},
				&grpc.StreamServerInfo{FullMethod: tt.method},
				func(_ any, _ grpc.ServerStream) error {
					called = true

					return nil
				},
			)

			assert.Equal(t, tt.wantCalled, called, "handler reached")

			if !tt.wantCalled {
				require.Equal(t, codes.Unauthenticated, status.Code(err))

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestAuthInterceptorErrorCarriesStatus pins the constraint that the error must
// be built with status.Errorf: the code-converting interceptor runs earlier in
// the chain, so a plain error from here would reach clients as codes.Unknown.
func TestAuthInterceptorErrorCarriesStatus(t *testing.T) {
	t.Parallel()

	handler := &recordingHandler{}

	_, err := newInterceptor(t).UnaryServerInterceptor()(
		t.Context(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: generator.ServiceAPI_DeletePlugin_FullMethodName},
		handler.unary,
	)

	_, ok := status.FromError(err)
	require.True(t, ok, "error must carry a gRPC status")
	require.NotContains(t, status.Convert(err).Message(), "token",
		"the rejection must not tell an anonymous caller why it failed")
}

// TestEveryRPCIsClassified fails as soon as an RPC is added to the proto, forcing
// a deliberate decision about whether it is anonymous or requires credentials.
// Without it a new method would silently inherit whichever default we picked.
func TestEveryRPCIsClassified(t *testing.T) {
	t.Parallel()

	anonymous := map[string]struct{}{
		"GenerateCode": {},
		"Plugins":      {},
	}

	for _, method := range generator.ServiceAPI_ServiceDesc.Methods {
		t.Run(method.MethodName, func(t *testing.T) {
			t.Parallel()

			handler := &recordingHandler{}
			fullMethod := "/" + generator.ServiceAPI_ServiceDesc.ServiceName + "/" + method.MethodName

			_, err := newInterceptor(t).UnaryServerInterceptor()(
				t.Context(),
				nil,
				&grpc.UnaryServerInfo{FullMethod: fullMethod},
				handler.unary,
			)

			if _, public := anonymous[method.MethodName]; public {
				require.NoError(t, err, "%s is listed as anonymous", method.MethodName)

				return
			}

			require.Equal(t, codes.Unauthenticated, status.Code(err),
				"%s is not listed as anonymous, so it must require credentials", method.MethodName)
		})
	}
}
