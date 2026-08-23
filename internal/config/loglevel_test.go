package config_test

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/sethvargo/go-envconfig"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

// TestLogLevelIsASettingWithLayers covers the level's move out of the command
// line and into the configuration.
//
// It used to be a flag and nothing else: no YAML key, no variable, absent from
// `config print`, and defaulting to debug — the loudest setting there is, chosen
// by everyone who said nothing. Silencing a compose stack meant editing a
// committed file.
func TestLogLevelIsASettingWithLayers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		file string
		env  map[string]string
		want string
	}{
		{
			name: "nothing anywhere is info, not debug",
			want: "info",
		},
		{
			name: "the file supplies it",
			file: "log:\n  level: warn\n",
			want: "warn",
		},
		{
			name: "the environment beats the file",
			file: "log:\n  level: warn\n",
			env:  map[string]string{"LOG_LEVEL": "error"},
			want: "error",
		},
		{
			name: "the environment alone works",
			env:  map[string]string{"LOG_LEVEL": "debug"},
			want: "debug",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := map[string]string{"DB_POSTGRES_DSN": "postgres://u:p@h:5432/d?sslmode=disable"}
			maps.Copy(env, tc.env)

			path := filepath.Join(t.TempDir(), "config.yml")
			require.NoError(t, os.WriteFile(path, []byte(tc.file), 0o600))

			res, err := config.LoadWith(t.Context(), path, config.EmptyIsUnset(envconfig.MapLookuper(env)))
			require.NoError(t, err)
			require.Equal(t, tc.want, res.Config.Log.Level)
		})
	}
}
