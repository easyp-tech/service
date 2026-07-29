package auth_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/easyp-tech/service/internal/auth"
	"github.com/easyp-tech/service/internal/config"
)

// digestOf returns the hex digest a token would be configured under.
func digestOf(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

// mdWith builds request metadata carrying a raw authorization value.
func mdWith(authorization string) metadata.MD {
	return metadata.Pairs("authorization", authorization)
}

func testTokens() config.TokenList {
	return config.TokenList{
		{Name: "ci", SHA256: digestOf("ci-token")},
		{Name: "edgar", SHA256: digestOf("human-token")},
	}
}

func TestStaticTokenAuthenticate(t *testing.T) {
	t.Parallel()

	authenticator := auth.NewStaticTokenAuthenticator(testTokens())

	tests := []struct {
		name          string
		authorization string
		wantActor     string
		wantErr       error
	}{
		{name: "known token", authorization: "Bearer ci-token", wantActor: "ci"},
		{name: "second known token", authorization: "Bearer human-token", wantActor: "edgar"},
		{name: "scheme is case insensitive", authorization: "bearer ci-token", wantActor: "ci"},
		{name: "unknown token", authorization: "Bearer nope", wantErr: auth.ErrUnknownToken},
		{name: "token case matters", authorization: "Bearer CI-TOKEN", wantErr: auth.ErrUnknownToken},
		{name: "no scheme", authorization: "ci-token", wantErr: auth.ErrNoCredentials},
		{name: "wrong scheme", authorization: "Basic ci-token", wantErr: auth.ErrNoCredentials},
		{name: "empty token", authorization: "Bearer ", wantErr: auth.ErrNoCredentials},
		{name: "empty header", authorization: "", wantErr: auth.ErrNoCredentials},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actor, err := authenticator.Authenticate(t.Context(), mdWith(tt.authorization))

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Empty(t, actor.Name)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantActor, actor.Name)
			require.Equal(t, auth.KindToken, actor.Kind)
		})
	}
}

func TestStaticTokenMissingMetadata(t *testing.T) {
	t.Parallel()

	authenticator := auth.NewStaticTokenAuthenticator(testTokens())

	_, err := authenticator.Authenticate(t.Context(), metadata.MD{})
	require.ErrorIs(t, err, auth.ErrNoCredentials)
}

// TestStaticTokenEmptyListDeniesEverything pins the fail-closed property: a
// service with no tokens configured must refuse writes, not allow them.
func TestStaticTokenEmptyListDeniesEverything(t *testing.T) {
	t.Parallel()

	authenticator := auth.NewStaticTokenAuthenticator(nil)
	require.True(t, authenticator.Empty())

	_, err := authenticator.Authenticate(t.Context(), mdWith("Bearer ci-token"))
	require.ErrorIs(t, err, auth.ErrUnknownToken)
}

// TestStaticTokenSkipsMalformedDigest documents the constructor's behaviour when
// validation was bypassed: the bad entry is dropped rather than matching anything.
func TestStaticTokenSkipsMalformedDigest(t *testing.T) {
	t.Parallel()

	authenticator := auth.NewStaticTokenAuthenticator(config.TokenList{
		{Name: "broken", SHA256: "not-hex"},
		{Name: "ci", SHA256: digestOf("ci-token")},
	})

	actor, err := authenticator.Authenticate(t.Context(), mdWith("Bearer ci-token"))
	require.NoError(t, err)
	require.Equal(t, "ci", actor.Name)
}
