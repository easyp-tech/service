package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sethvargo/go-envconfig"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

// minimalEnv supplies the one setting that has no default and no value a
// fixture can omit, so that a file saying nothing else still resolves to a
// configuration that would start.
func minimalEnv() envconfig.Lookuper {
	return config.EmptyIsUnset(envconfig.MapLookuper(map[string]string{
		"DB_POSTGRES_DSN": "postgres://u:p@h:5432/d?sslmode=disable",
	}))
}

// fixture writes doc to a temp file and returns its path. Unlike zeroFixture it
// makes no attempt to be a valid configuration: these tests are about what the
// loader says regarding the input, which is decided before validation runs.
func fixture(t *testing.T, doc string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))

	return path
}

// TestUnknownKeyIsFatal is the check the whole strictness change exists for.
//
// A key this build does not know used to be a warning printed next to a
// successful start: the service came up on defaults, `config validate` exited
// zero, and the operator had no way to tell "I configured this" from "I typed it
// wrong". Three spellings for every setting — YAML snake_case, environment
// UPPER_SNAKE, Helm camelCase — mean the mistake is not hypothetical.
func TestUnknownKeyIsFatal(t *testing.T) {
	t.Parallel()

	res, err := config.LoadWith(t.Context(), fixture(t, "server:\n  porrt:\n    grpc: \"8888\"\n"), noEnv())

	require.Error(t, err)
	require.True(t, res.Diagnostics.HasErrors())
	require.ErrorContains(t, err, "server.porrt")
}

// TestUnknownKeyHintsAtTheRealOne covers the two ways a key goes wrong here.
//
// The camelCase rows are not typos at all: they are correct Helm values pasted
// into a service config, which is the single most likely way a key in this
// project ends up unrecognised. Normalising case and separators has to catch
// them, or the hint is absent exactly when it would help most.
func TestUnknownKeyHintsAtTheRealOne(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		doc      string
		wantPath string
		wantHint string
	}{
		{
			name:     "camelCase pasted from the chart's values",
			doc:      "registry:\n  cacheMaxBytes: 1\n",
			wantPath: "registry.cacheMaxBytes",
			wantHint: "did you mean registry.cache_max_bytes?",
		},
		{
			name:     "the chart's plural spelling of the metrics port",
			doc:      "server:\n  port:\n    metrics: \"8081\"\n",
			wantPath: "server.port.metrics",
			wantHint: "did you mean server.port.metric?",
		},
		{
			name:     "an ordinary mistype",
			doc:      "worker_pool:\n  max_retires: 2\n",
			wantPath: "worker_pool.max_retires",
			wantHint: "did you mean worker_pool.max_retries?",
		},
		{
			name:     "a setting written at the wrong level",
			doc:      "cache_max_bytes: 1\n",
			wantPath: "cache_max_bytes",
			wantHint: "did you mean registry.cache_max_bytes?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := config.LoadWith(t.Context(), fixture(t, tc.doc), noEnv())
			require.Error(t, err)

			require.Len(t, res.Diagnostics, 1)
			require.Equal(t, tc.wantPath, res.Diagnostics[0].Path)
			require.Equal(t, tc.wantHint, res.Diagnostics[0].Hint)
			require.Positive(t, res.Diagnostics[0].Line, "a diagnostic without a line makes the reader search")
		})
	}
}

// TestLeafValuedMapsAreNotWalked is the one place the document walk can
// plausibly regress.
//
// license.public_keys and auth.write_tokens are single settings whose values
// happen to be a mapping and a sequence. Descending into them would report every
// key id and every token name as an unrecognised setting, which — now that
// unrecognised is fatal — would refuse every configuration that carries a
// licence key or a write token.
func TestLeafValuedMapsAreNotWalked(t *testing.T) {
	t.Parallel()

	doc := `
license:
  public_keys:
    "2026-08": "` + strings64Hex + `"
auth:
  write_tokens:
    - name: ci
      sha256: "` + strings64Hex + `"
`

	res, _ := config.LoadWith(t.Context(), fixture(t, doc), noEnv())

	for _, diag := range res.Diagnostics {
		require.NotContains(t, diag.Path, "2026-08", "a key id is a value, not a setting name")
		require.NotContains(t, diag.Path, "name", "a token's fields are values, not setting names")
	}

	require.Empty(t, res.Diagnostics)
}

const strings64Hex = "8132000000000000000000000000000000000000000000000000000000006bb5"

// TestDiagnosticsClassifyYAMLErrors pins the distinction the old loader could
// not draw. It put every failure of the strict decode behind one prefix —
// "unrecognised field, ignored" — so a type error and a syntax error, neither of
// which ignores anything and both of which stop the process, were announced as
// though the service had shrugged and carried on. An empty file was announced
// the same way, as "unrecognised field, ignored: EOF".
func TestDiagnosticsClassifyYAMLErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		doc         string
		wantErr     bool
		wantDiags   int
		wantMessage string
	}{
		{
			name:        "an unknown key names itself",
			doc:         "server:\n  nonsense: 1\n",
			wantErr:     true,
			wantDiags:   1,
			wantMessage: "unknown key",
		},
		{
			name:        "a type error does not claim anything was ignored",
			doc:         "registry:\n  max_output_size: four\n",
			wantErr:     true,
			wantDiags:   1,
			wantMessage: "cannot unmarshal",
		},
		{
			name:        "a second document is not silently dropped",
			doc:         "server:\n  host: a\n---\nserver:\n  host: b\n",
			wantErr:     true,
			wantDiags:   1,
			wantMessage: "more than one YAML document",
		},
		{
			name:        "a second document that is itself malformed is still a second document",
			doc:         "server:\n  host: a\n---\nregistry: [unclosed\n",
			wantErr:     true,
			wantDiags:   1,
			wantMessage: "more than one YAML document",
		},
		{
			name:      "an empty file is a configuration that names nothing",
			doc:       "",
			wantErr:   false,
			wantDiags: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := config.LoadWith(t.Context(), fixture(t, tc.doc), minimalEnv())

			require.Len(t, res.Diagnostics, tc.wantDiags)

			if tc.wantMessage != "" {
				require.Contains(t, res.Diagnostics[0].Message, tc.wantMessage)
				require.NotContains(t, res.Diagnostics[0].Message, "ignored",
					"nothing is ignored: the configuration is refused")
			}

			if tc.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err, "an empty file leaves every setting to the environment and the defaults")
		})
	}
}

// TestSyntaxErrorIsNotADiagnostic covers the one input with no document to
// diagnose. There is nothing more useful to say than what the parser said, and
// dressing it up as a per-key finding would invent a key.
func TestSyntaxErrorIsNotADiagnostic(t *testing.T) {
	t.Parallel()

	res, err := config.LoadWith(t.Context(), fixture(t, "server:\n  host a\n  port: {\n"), noEnv())

	require.ErrorContains(t, err, "parsing config YAML")
	require.Empty(t, res.Diagnostics)
	require.Nil(t, res.Config)
}

// TestValidYAMLIdiomsAreNotMistakenForTypos guards the direction in which strict
// checking is dangerous.
//
// Refusing a mistyped key is the point. Refusing a *correct* file over YAML
// syntax is a different thing entirely: the operator has nothing to fix, and
// before strictness these files worked. Each case below was rejected by the
// first version of this check.
func TestValidYAMLIdiomsAreNotMistakenForTypos(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		doc  string
	}{
		{
			name: "an anchor holder at the document root",
			doc:  ".defaults: &d\n  grpc: \"8080\"\nserver:\n  port:\n    grpc: \"8080\"\n",
		},
		{
			name: "the x- extension prefix, as compose spells it",
			doc:  "x-anchors: &d\n  grpc: \"8080\"\nserver:\n  host: \"0.0.0.0\"\n",
		},
		{
			name: "a merge key",
			doc:  "server:\n  tls: &t\n    cert_file: /c.crt\n    key_file: /c.key\n  port:\n    <<: *t\n",
		},
		{
			name: "a file that ends in a document separator",
			doc:  "server:\n  host: \"0.0.0.0\"\n---\n",
		},
		{
			name: "a file that begins with one",
			doc:  "---\nserver:\n  host: \"0.0.0.0\"\n",
		},
		{
			name: "nothing but comments",
			doc:  "# only a comment\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := config.LoadWith(t.Context(), fixture(t, tc.doc), minimalEnv())

			require.NoError(t, err)
			require.Empty(t, res.Diagnostics)
		})
	}
}

// TestASecondDocumentIsStillReported keeps the fix above from disarming the
// check it narrowed: a real second document is silently dropped by the parser,
// so half the file would apply.
func TestASecondDocumentIsStillReported(t *testing.T) {
	t.Parallel()

	doc := "server:\n  host: a\n---\nserver:\n  host: b\n"

	res, err := config.LoadWith(t.Context(), fixture(t, doc), minimalEnv())

	require.Error(t, err)
	require.Len(t, res.Diagnostics, 1)
	require.Contains(t, res.Diagnostics[0].Message, "more than one YAML document")
}

// TestAnchorHoldersAreRootOnly keeps the escape hatch small: a dotted key inside
// a section is a mistake like any other, and treating it as an anchor holder
// everywhere would silence a whole class of typo.
func TestAnchorHoldersAreRootOnly(t *testing.T) {
	t.Parallel()

	res, err := config.LoadWith(t.Context(), fixture(t, "server:\n  .defaults: 1\n"), minimalEnv())

	require.Error(t, err)
	require.Len(t, res.Diagnostics, 1)
	require.Equal(t, "server..defaults", res.Diagnostics[0].Path)
}
