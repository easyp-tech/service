package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

const validHexKey = "2e80e973708c58959e9cb575856094e9fa94bfeec29692b249df502750e1fb3a"

func TestLicenseConfigValidate(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cfg     config.LicenseConfig
		wantErr string
	}{
		"no licence configured": {
			cfg: config.LicenseConfig{},
		},
		"one key": {
			cfg: config.LicenseConfig{PublicKeys: map[string]string{"2026-08": validHexKey}},
		},
		"a single key": {
			cfg: config.LicenseConfig{PublicKey: validHexKey},
		},
		"surrounding whitespace is tolerated": {
			cfg: config.LicenseConfig{PublicKeys: map[string]string{"2026-08": "  " + validHexKey + "\n"}},
		},
		"a key that is too short": {
			cfg:     config.LicenseConfig{PublicKeys: map[string]string{"2026-08": validHexKey[:32]}},
			wantErr: "expected 64 hex characters",
		},
		"a key that is not hex": {
			cfg:     config.LicenseConfig{PublicKeys: map[string]string{"2026-08": strings.Repeat("z", 64)}},
			wantErr: "not valid hex",
		},
		"a bad single key": {
			cfg:     config.LicenseConfig{PublicKey: "nonsense"},
			wantErr: "license.public_key",
		},
		// A key id carrying either separator would decode into a different map
		// than the one written down, on the environment path.
		"a key id containing a colon": {
			cfg:     config.LicenseConfig{PublicKeys: map[string]string{"2026:08": validHexKey}},
			wantErr: "must not contain",
		},
		"a key id containing a comma": {
			cfg:     config.LicenseConfig{PublicKeys: map[string]string{"2026,08": validHexKey}},
			wantErr: "must not contain",
		},
		"an empty key id": {
			cfg:     config.LicenseConfig{PublicKeys: map[string]string{"": validHexKey}},
			wantErr: "must not be empty",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := test.cfg.Validate()

			if test.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			require.Contains(t, err.Error(), test.wantErr)
		})
	}
}

// TestConfigValidateRejectsBadLicenseKeys checks the section is actually reached
// from the root Validate, not just valid in isolation.
func TestConfigValidateRejectsBadLicenseKeys(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.License.PublicKeys = map[string]string{"2026-08": "much too short"}

	require.Error(t, cfg.Validate())
}
