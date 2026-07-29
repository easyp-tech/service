package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

// validDigest is 64 hex characters, the shape a sha256 digest has.
const validDigest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func TestAuthConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tokens  config.TokenList
		wantErr error
	}{
		{
			name:   "empty list is valid and denies writes at runtime",
			tokens: nil,
		},
		{
			name:   "single well-formed token",
			tokens: config.TokenList{{Name: "ci", SHA256: validDigest}},
		},
		{
			name:    "missing name",
			tokens:  config.TokenList{{Name: "", SHA256: validDigest}},
			wantErr: config.ErrTokenNameRequired,
		},
		{
			name:    "missing digest",
			tokens:  config.TokenList{{Name: "ci", SHA256: ""}},
			wantErr: config.ErrTokenHashRequired,
		},
		{
			name:    "digest too short",
			tokens:  config.TokenList{{Name: "ci", SHA256: "deadbeef"}},
			wantErr: config.ErrTokenHashMalformed,
		},
		{
			name:    "digest right length but not hex",
			tokens:  config.TokenList{{Name: "ci", SHA256: strings.Repeat("z", 64)}},
			wantErr: config.ErrTokenHashMalformed,
		},
		{
			name: "duplicate names would make the audit trail ambiguous",
			tokens: config.TokenList{
				{Name: "ci", SHA256: validDigest},
				{Name: "ci", SHA256: strings.Repeat("a", 64)},
			},
			wantErr: config.ErrTokenNameDuplicate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := config.AuthConfig{WriteTokens: tt.tokens}.Validate()

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestTokenListEnvDecode(t *testing.T) {
	t.Parallel()

	t.Run("parses name=digest pairs", func(t *testing.T) {
		t.Parallel()

		var list config.TokenList
		require.NoError(t, list.EnvDecode("ci="+validDigest+", edgar="+validDigest))

		require.Len(t, list, 2)
		require.Equal(t, "ci", list[0].Name)
		require.Equal(t, validDigest, list[0].SHA256)
		require.Equal(t, "edgar", list[1].Name)
	})

	t.Run("empty value yields no tokens", func(t *testing.T) {
		t.Parallel()

		list := config.TokenList{{Name: "stale", SHA256: validDigest}}
		require.NoError(t, list.EnvDecode("  "))
		require.Empty(t, list)
	})

	t.Run("a pair without a separator is an error", func(t *testing.T) {
		t.Parallel()

		var list config.TokenList
		require.ErrorIs(t, list.EnvDecode("ci"), config.ErrTokenMalformedEnv)
	})
}

// TestConfigValidateRejectsBadTokens checks the section is actually reached from
// the root Validate, not just valid in isolation.
func TestConfigValidateRejectsBadTokens(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.Auth.WriteTokens = config.TokenList{{Name: "ci", SHA256: "short"}}

	require.ErrorIs(t, cfg.Validate(), config.ErrTokenHashMalformed)
}
