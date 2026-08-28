package api

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"

	"github.com/easyp-tech/service/internal/core"
)

// TestClientMessageStripsCallChain covers what a client actually receives.
//
// Every layer wraps with fmt.Errorf("<call>: %w", err), which is right for a
// log and wrong for the wire: the status message spelled out this service's
// internal call graph, and a client reading it made that spelling a public
// interface.
func TestClientMessageStripsCallChain(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ in, want string }{
		"a real generation failure": {
			in:   "api.app.Generate: c.registry.Get: ensureBinary: plugin archive not found in binary storage: grpc/go/v1.5.1",
			want: "plugin archive not found in binary storage: grpc/go/v1.5.1",
		},
		"a config rejection": {
			in:   "api.app.CreatePlugin: c.registry.Create: ValidateConfig: invalid plugin configuration: command[0] must be the plugin executable inside /plugins, got \"/bin/sh\"",
			want: "invalid plugin configuration: command[0] must be the plugin executable inside /plugins, got \"/bin/sh\"",
		},
		"a message with no call chain is untouched": {
			in:   "server overloaded",
			want: "server overloaded",
		},
		"prose is not mistaken for a call site": {
			// The segment before the colon has spaces, so it is a sentence.
			in:   "max plugins exceeded: current 10, limit 10",
			want: "max plugins exceeded: current 10, limit 10",
		},
		"a method expression is still a call site": {
			in:   "(*Registry).Update: not found",
			want: "not found",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, clientMessage(errors.New(tc.in)))
		})
	}
}

// TestErrorToStatusCarriesReason pins the machine-readable half of the error
// contract. A code is a category — NotFound covers a missing plugin and a
// missing archive alike — so the reason is what a client can branch on.
func TestErrorToStatusCarriesReason(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err        error
		wantCode   codes.Code
		wantReason string
	}{
		"missing plugin":     {core.ErrNotFound, codes.NotFound, ReasonNotFound},
		"bad config":         {core.ErrInvalidConfig, codes.InvalidArgument, ReasonInvalidConfig},
		"bad plugin name":    {core.ErrInvalidPluginName, codes.InvalidArgument, ReasonInvalidPluginName},
		"overloaded":         {core.ErrServerOverloaded, codes.ResourceExhausted, ReasonServerOverloaded},
		"licence ceiling":    {core.ErrMaxPluginsExceeded, codes.ResourceExhausted, ReasonMaxPluginsExceeded},
		"archive missing":    {core.ErrBinaryNotUploaded, codes.FailedPrecondition, ReasonBinaryNotUploaded},
		"enterprise feature": {core.ErrFeatureDenied, codes.PermissionDenied, ReasonFeatureDenied},
		"timeout":            {context.DeadlineExceeded, codes.DeadlineExceeded, ReasonDeadlineExceeded},
		"anything else":      {errors.New("boom"), codes.Internal, ReasonInternal},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Wrapped the way the real call path wraps it.
			st := ErrorToStatus(fmt.Errorf("api.app.Do: inner.Call: %w", tc.err))

			assert.Equal(t, tc.wantCode, st.Code())

			var info *errdetails.ErrorInfo
			for _, d := range st.Details() {
				if got, ok := d.(*errdetails.ErrorInfo); ok {
					info = got
				}
			}

			require.NotNil(t, info, "every non-OK status carries an ErrorInfo")
			assert.Equal(t, tc.wantReason, info.GetReason())
			assert.Equal(t, errorDomain, info.GetDomain())

			// And the internal call chain is not on the wire.
			assert.NotContains(t, st.Message(), "api.app.Do")
			assert.NotContains(t, st.Message(), "inner.Call")
		})
	}
}

// ReasonAlreadyExists and the two Unavailable reasons round out the table; kept
// separate because they share codes with entries above.
func TestErrorToStatusDistinguishesSharedCodes(t *testing.T) {
	t.Parallel()

	shutting := ErrorToStatus(core.ErrShuttingDown)
	storage := ErrorToStatus(core.ErrStorageUnavailable)

	assert.Equal(t, codes.Unavailable, shutting.Code())
	assert.Equal(t, codes.Unavailable, storage.Code())

	// Same code, different reason — which is the whole point of the reason.
	assert.NotEqual(t, reasonOf(t, shutting), reasonOf(t, storage))
}

func reasonOf(t *testing.T, st interface{ Details() []any }) string {
	t.Helper()

	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}

	t.Fatal("no ErrorInfo on status")

	return ""
}
