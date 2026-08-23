package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sethvargo/go-envconfig"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

// shippedConfigs are the files the repository actually deploys with. The paths
// are relative to this package.
var shippedConfigs = []string{
	"config.yml",
	"config.local.yml",
	"config.community.dev.yml",
	"config.enterprise.dev.yml",
}

func shippedPath(name string) string {
	return filepath.Join("..", "..", "deploy", "config", name)
}

// noEnv is an empty environment: the configs must stand on their own, since a
// developer runs them with `task run-local` and a container starts with only
// what its compose file passes in.
func noEnv() envconfig.Lookuper {
	return envconfig.MapLookuper(map[string]string{})
}

// TestShippedConfigsAreValid is the test whose absence let a broken config reach
// HEAD: deploy/config/config.local.yml omitted server.max_send_msg_size, which
// arrived as zero and failed the check against registry.max_output_size, so
// `task run-local` did not start for anyone who cloned the repository. Every
// rule in Validate was covered by nothing, and no test loaded these files at all.
func TestShippedConfigsAreValid(t *testing.T) {
	t.Parallel()

	for _, name := range shippedConfigs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res, err := config.LoadWith(t.Context(), shippedPath(name), noEnv())
			cfg, warnings := res.Config, res.Diagnostics
			require.NoError(t, err)
			require.NotNil(t, cfg)
			require.Empty(t, warnings, "unknown fields in a shipped config are settings that do nothing")
		})
	}
}

// TestShippedConfigsHaveNoWriteTokensOnPublicStacks pins the decision that the
// two tier configs ship no credentials. They are what docker-compose.public.yml
// puts on the internet, and they used to carry the digest of the literal
// "local-dev-token" — published in this repository, so anyone who read it could
// create, replace and delete plugins. An empty list denies every write; real
// ones arrive through AUTH_WRITE_TOKENS.
func TestShippedConfigsHaveNoWriteTokensOnPublicStacks(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"config.community.dev.yml", "config.enterprise.dev.yml"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res, err := config.LoadWith(t.Context(), shippedPath(name), noEnv())
			cfg := res.Config
			require.NoError(t, err)
			require.Empty(t, cfg.Auth.WriteTokens,
				"a publicly reachable stack must not ship a credential in a committed file")
		})
	}
}

// TestShippedConfigsTrustTheProxy guards the setting whose empty value is
// invisible: behind traefik, with no trusted range, the library short-circuits
// to the connecting address and never reads a forwarding header, so every caller
// shares one rate-limit bucket and the audit log names the proxy. The same
// defect was already fixed once in the extractor; the configs kept it alive.
func TestShippedConfigsTrustTheProxy(t *testing.T) {
	t.Parallel()

	// config.local.yml is deliberately absent: it runs without a proxy, where an
	// empty list is the correct and safe answer.
	for _, name := range []string{"config.yml", "config.community.dev.yml", "config.enterprise.dev.yml"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res, err := config.LoadWith(t.Context(), shippedPath(name), noEnv())
			cfg := res.Config
			require.NoError(t, err)
			require.NotEmpty(t, cfg.Server.TrustedProxies,
				"this stack is reached through traefik, so the proxy range has to be trusted")

			prefixes, err := cfg.Server.TrustedProxyPrefixes()
			require.NoError(t, err)
			require.NotEmpty(t, prefixes)
		})
	}
}

// TestPrecedence covers the whole layering in one table. The order is the point:
// the file is a starting position, the environment beats it, and a default from
// a struct tag fills only what neither supplied. Getting any cell wrong is
// silent — the service starts either way and serves with the wrong value.
func TestPrecedence(t *testing.T) {
	t.Parallel()

	const (
		fromFile = "eu-central-1"
		fromEnv  = "eu-west-1"
		// The default on S3Config.Region.
		fromTag = "us-east-1"
		key     = "REGISTRY_S3_REGION"
	)

	cases := []struct {
		name    string
		inFile  string
		inEnv   map[string]string
		want    string
		explain string
	}{
		{
			name:    "neither means the struct tag default",
			inFile:  "",
			inEnv:   map[string]string{},
			want:    fromTag,
			explain: "an omitted field used to arrive as zero on this path",
		},
		{
			name:    "environment fills what the file left empty",
			inFile:  "",
			inEnv:   map[string]string{key: fromEnv},
			want:    fromEnv,
			explain: "",
		},
		{
			name:    "the file wins when nothing is in the environment",
			inFile:  fromFile,
			inEnv:   map[string]string{},
			want:    fromFile,
			explain: "a struct tag default must never overwrite a value someone wrote down",
		},
		{
			name:    "the environment beats the file",
			inFile:  fromFile,
			inEnv:   map[string]string{key: fromEnv},
			want:    fromEnv,
			explain: "this is what lets a secret stay out of a committed config",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Config{}
			cfg.Registry.S3.Region = tc.inFile

			require.NoError(t, config.ApplyEnvWith(t.Context(), &cfg, envconfig.MapLookuper(tc.inEnv)))
			require.Equal(t, tc.want, cfg.Registry.S3.Region, tc.explain)
		})
	}
}

// TestTelemetryHasNoDefault pins a deliberate absence. Every other field with a
// sensible fallback carries one; these two must not, because a default makes
// "no collector" impossible to express — an empty value in a config file is the
// zero value, so the default fills it straight back in. The exporters connect
// lazily, so the endpoint nobody asked for costs an endless retry rather than an
// error, and a dev stack without an observability overlay fills its log with
// connection failures while otherwise working.
func TestTelemetryHasNoDefault(t *testing.T) {
	t.Parallel()

	cfg := config.Config{}

	require.NoError(t, config.ApplyEnvWith(t.Context(), &cfg, envconfig.MapLookuper(map[string]string{})))
	require.Empty(t, cfg.Telemetry.OTLPEndpoint, "an unset collector must stay unset")
	require.Empty(t, cfg.Telemetry.PyroscopeEndpoint)

	// And the tier configs, which ship it empty on purpose, must stay that way.
	for _, name := range []string{"config.community.dev.yml", "config.enterprise.dev.yml"} {
		res, err := config.LoadWith(t.Context(), shippedPath(name), noEnv())
		loaded := res.Config
		require.NoError(t, err)
		require.Empty(t, loaded.Telemetry.OTLPEndpoint,
			"%s has no collector unless the observability overlay is layered on", name)
		require.Empty(t, loaded.Telemetry.PyroscopeEndpoint, name)
	}
}

// TestEmptyVariableDoesNotClobber covers the interaction with docker-compose,
// where the idiom throughout deploy/ is `LICENSE_KEY: "${LICENSE_KEY:-}"`: the
// variable is always defined in the container and usually empty. os.LookupEnv
// reports those as found, so without treating empty as absent an unset shell
// variable would wipe whatever the config file said.
func TestEmptyVariableDoesNotClobber(t *testing.T) {
	t.Parallel()

	const dsn = "postgres://easyp_svc:easyp_pass@postgres:5432/easyp_db?sslmode=disable"

	cfg := config.Config{}
	cfg.DB.Postgres = dsn

	// Set, but empty — exactly what compose produces for an unexported variable.
	env := config.EmptyIsUnset(envconfig.MapLookuper(map[string]string{
		"DB_POSTGRES_DSN": "",
	}))

	require.NoError(t, config.ApplyEnvWith(t.Context(), &cfg, env))
	require.Equal(t, dsn, cfg.DB.Postgres,
		"an empty variable means the stack did not supply one, not that the DSN should be erased")

	// A variable with a value in it still wins.
	override := config.EmptyIsUnset(envconfig.MapLookuper(map[string]string{
		"DB_POSTGRES_DSN": "postgres://other/db",
	}))
	require.NoError(t, config.ApplyEnvWith(t.Context(), &cfg, override))
	require.Equal(t, "postgres://other/db", cfg.DB.Postgres)
}

// TestEnvOverridesNonZeroCollections is the regression guard for the trap that
// makes the layering work at all. A YAML `{}` or `[]` decodes to a non-nil empty
// map or slice, for which reflect.IsZero is false; without DefaultOverwrite
// envconfig skips such a field and never looks the variable up. The licence keys
// are the case that matters — the enterprise container would have run as
// community, and only deploy/scripts/check-tiers.sh would have noticed.
func TestEnvOverridesNonZeroCollections(t *testing.T) {
	t.Parallel()

	const (
		kid    = "2026-08"
		hexKey = "81322461987167d5cfd529e9cb8b96f4797f12fce6be4399a0866e250c9b6bb5"
		digest = "0000000000000000000000000000000000000000000000000000000000000001"
	)

	cfg := config.Config{}
	// Exactly what the shipped configs decode to.
	cfg.License.PublicKeys = map[string]string{}
	cfg.Server.TrustedProxies = []string{}
	cfg.Auth.WriteTokens = config.TokenList{}

	env := envconfig.MapLookuper(map[string]string{
		"LICENSE_PUBLIC_KEYS":    kid + ":" + hexKey,
		"SERVER_TRUSTED_PROXIES": "10.0.0.0/8,192.168.0.0/16",
		"AUTH_WRITE_TOKENS":      "ci=" + digest,
	})

	require.NoError(t, config.ApplyEnvWith(t.Context(), &cfg, env))

	require.Equal(t, map[string]string{kid: hexKey}, cfg.License.PublicKeys,
		"an empty map from YAML must not block the environment")
	require.Equal(t, []string{"10.0.0.0/8", "192.168.0.0/16"}, cfg.Server.TrustedProxies)
	require.Equal(t, config.TokenList{{Name: "ci", SHA256: digest}}, cfg.Auth.WriteTokens,
		"AUTH_WRITE_TOKENS is the only way to give a --cfg deployment a credential")
}

// TestShippedConfigsAcceptEnvOverrides walks the real files rather than a
// hand-built struct: the tier configs ship an empty write-token list and empty
// telemetry endpoints precisely because the environment is expected to fill
// them, and that expectation is worth pinning.
func TestShippedConfigsAcceptEnvOverrides(t *testing.T) {
	t.Parallel()

	const digest = "0000000000000000000000000000000000000000000000000000000000000002"

	env := envconfig.MapLookuper(map[string]string{
		"AUTH_WRITE_TOKENS":             "ci=" + digest,
		"TELEMETRY_OTLP_ENDPOINT":       "easyp-alloy:4317",
		"REGISTRY_S3_ACCESS_KEY_ID":     "key",
		"REGISTRY_S3_SECRET_ACCESS_KEY": "secret",
	})

	res, err := config.LoadWith(t.Context(), shippedPath("config.enterprise.dev.yml"), env)
	cfg := res.Config
	require.NoError(t, err)

	require.Equal(t, config.TokenList{{Name: "ci", SHA256: digest}}, cfg.Auth.WriteTokens)
	require.Equal(t, "easyp-alloy:4317", cfg.Telemetry.OTLPEndpoint)
	require.Equal(t, "key", cfg.Registry.S3.AccessKeyID)
	require.Equal(t, "secret", cfg.Registry.S3.SecretAccessKey)
	// Not overridden, so the file's value stands.
	require.Equal(t, "easyp-plugins", cfg.Registry.S3.Bucket)
}

// TestS3CredentialsValidatedAfterEnv covers the check that existed but could not
// fire: registry.s3.access_key_id and secret_access_key are validated as a pair,
// and on the --cfg path both were always empty at validation time because the
// keys were spliced in afterwards. A half-filled .env therefore started cleanly,
// logged "S3 plugin storage enabled" and failed every plugin download with a 403.
func TestS3CredentialsValidatedAfterEnv(t *testing.T) {
	t.Parallel()

	env := envconfig.MapLookuper(map[string]string{
		"REGISTRY_S3_ACCESS_KEY_ID": "key-without-its-secret",
	})

	_, err := config.LoadWith(t.Context(), shippedPath("config.enterprise.dev.yml"), env)
	require.ErrorContains(t, err, "must be set together")
}

// zeroFixture writes a config with the five zero-is-a-setting fields either
// omitted or spelled out, so a test can tell the two apart the way the loader
// has to.
func zeroFixture(t *testing.T, registry, workerPool, rateLimit, audit string) string {
	t.Helper()

	doc := `
server:
  host: "0.0.0.0"
  port: {grpc: "8080", metric: "8081", health: "8082", mcp: "8083"}
  force_shutdown_after: 150s
  max_send_msg_size: 67108864
db: {postgres: "postgres://u:p@h:5432/d?sslmode=disable"}
registry: {plugins_dir: /plugins, max_output_size: 67108864` + registry + `}
worker_pool:
  workers: 4
  queue_size: 16
  max_concurrent_generations: 16
  generation_timeout: 120s
  shutdown_timeout: 30s` + workerPool + `
rate_limit:
  requests_per_second: 10.0
  burst: 20
  cleanup_interval: 10m` + rateLimit + `
audit:
  buffer_size: 1000
  batch_size: 100
  flush_interval: 1s` + audit + `
`

	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))

	return path
}

// TestExplicitZerosSurvive is the regression guard for the sharpest edge of the
// layering. Five fields document zero as a setting — no retries, no cache
// eviction, no per-caller limit, keep audit forever — and every one of them also
// carries a default in its `env` tag. envconfig sees a field the file set to 0
// and a field the file never mentioned as the same thing, so left alone it fills
// both from the tag: "cache_max_bytes: 0", written down to disable eviction,
// came back as 20 GiB.
//
// It is worth a test rather than a comment because nothing else catches it. The
// pool no longer substitutes a default for max_retries, so zero reaches it
// meaning zero — but only if it survives the loader, and a unit test that builds
// the struct by hand never goes through the loader at all.
func TestExplicitZerosSurvive(t *testing.T) {
	t.Parallel()

	const (
		withZeros    = true
		withoutZeros = false
	)

	read := func(t *testing.T, spelledOut bool, env map[string]string) *config.Config {
		t.Helper()

		var registry, workerPool, rateLimit, audit string
		if spelledOut {
			registry = ", cache_max_bytes: 0"
			workerPool = "\n  max_retries: 0"
			rateLimit = "\n  max_concurrent_per_ip: 0"
			audit = "\n  max_save_retries: 0\n  retention_months: 0"
		}

		res, err := config.LoadWith(
			t.Context(),
			zeroFixture(t, registry, workerPool, rateLimit, audit),
			config.EmptyIsUnset(envconfig.MapLookuper(env)),
		)
		cfg := res.Config
		require.NoError(t, err)

		return cfg
	}

	t.Run("a zero the file states outright is kept", func(t *testing.T) {
		t.Parallel()

		cfg := read(t, withZeros, map[string]string{})

		require.Zero(t, cfg.WorkerPool.MaxRetries, "max_retries: 0 means one attempt, not the default two")
		require.Zero(t, cfg.Registry.CacheMaxBytes, "cache_max_bytes: 0 disables eviction")
		require.Zero(t, cfg.RateLimit.MaxConcurrentPerIP, "max_concurrent_per_ip: 0 disables the check")
		require.Zero(t, cfg.Audit.RetentionMonths, "retention_months: 0 keeps everything")
		require.False(t, cfg.Audit.RetentionEnabled())
		require.Zero(t, cfg.Audit.MaxSaveRetries, "max_save_retries: 0 means no extra attempts")
	})

	t.Run("an omitted key still takes the default", func(t *testing.T) {
		t.Parallel()

		cfg := read(t, withoutZeros, map[string]string{})

		require.Equal(t, 2, cfg.WorkerPool.MaxRetries)
		require.Equal(t, int64(21474836480), cfg.Registry.CacheMaxBytes)
		require.Equal(t, 2, cfg.RateLimit.MaxConcurrentPerIP)
		require.Equal(t, 12, cfg.Audit.RetentionMonths)
		require.Equal(t, 3, cfg.Audit.MaxSaveRetries)
	})

	t.Run("the environment still beats a zero in the file", func(t *testing.T) {
		t.Parallel()

		cfg := read(t, withZeros, map[string]string{"WORKER_POOL_MAX_RETRIES": "5"})

		require.Equal(t, 5, cfg.WorkerPool.MaxRetries, "the environment is the layer above the file")
		require.Zero(t, cfg.Registry.CacheMaxBytes, "the fields it did not name keep what the file said")
	})

	t.Run("a non-zero value from the file is untouched", func(t *testing.T) {
		t.Parallel()

		res, err := config.LoadWith(
			t.Context(),
			zeroFixture(t, ", cache_max_bytes: 268435456", "\n  max_retries: 7",
				"\n  max_concurrent_per_ip: 4", "\n  max_save_retries: 9\n  retention_months: 6"),
			noEnv(),
		)
		cfg := res.Config
		require.NoError(t, err)

		require.Equal(t, 7, cfg.WorkerPool.MaxRetries)
		require.Equal(t, int64(268435456), cfg.Registry.CacheMaxBytes)
		require.Equal(t, 4, cfg.RateLimit.MaxConcurrentPerIP)
		require.Equal(t, 6, cfg.Audit.RetentionMonths)
		require.Equal(t, 9, cfg.Audit.MaxSaveRetries)
	})
}

// restatedDefaultsAllowed lists the keys a shipped config may state even though
// stating them changes nothing. Each needs a reason, because the cost is real:
// every such key is one more line that looks like it configures something.
var restatedDefaultsAllowed = map[string]string{
	// Written down empty, in the two configs that face the internet, so that the
	// decision is visible in the file. What used to sit there was the digest of
	// a token published in this repository; a reader has to be able to see that
	// it is gone on purpose rather than wonder whether it was lost in an edit.
	"auth.write_tokens": "the absence of a credential is the setting, and it is worth stating",
}

// TestShippedConfigsStateOnlyWhatTheyChange is the guard for the defect that
// went unnoticed longest. worker_pool.max_retries said 3 in all four of these
// files and 2 in the Helm chart and the struct tag, so docker deployments
// retried a generation four times and Kubernetes ones three — a difference
// nobody chose and nothing reported.
//
// It survived because the files restated nearly every default: 48 of 53
// settings in the enterprise config came from the file, and 44 of those said
// exactly what the binary already said. One line that genuinely differed was
// indistinguishable from forty that did not.
//
// So: a key in a shipped config must change something. To see what a file
// actually says, and what it resolves to including the defaults it leaves
// alone, use `easyp-svc config print --cfg <file> --changed` and `--origin`.
func TestShippedConfigsStateOnlyWhatTheyChange(t *testing.T) {
	t.Parallel()

	defaults, err := config.Defaults(t.Context())
	require.NoError(t, err)

	leaves, err := config.Leaves()
	require.NoError(t, err)

	for _, name := range shippedConfigs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res, err := config.LoadWith(t.Context(), shippedPath(name), noEnv())
			cfg, origins := res.Config, res.Origins
			require.NoError(t, err)

			for _, leaf := range leaves {
				if origins[leaf.Name()] != config.OriginFile {
					continue
				}

				if reason, allowed := restatedDefaultsAllowed[leaf.Name()]; allowed {
					t.Logf("%s restates its default deliberately: %s", leaf.Name(), reason)

					continue
				}

				require.False(t, leaf.SameValue(cfg, defaults),
					"%s repeats the default (%v) and can be deleted; if it is meant to pin the value "+
						"against a change in the binary, add it to restatedDefaultsAllowed with the reason",
					leaf.Name(), leaf.Value(defaults).Interface())
			}
		})
	}
}

// TestExplicitZerosSurviveForEveryField is the half TestExplicitZerosSurvive
// could not cover. That test names the five fields a hand-maintained list used
// to enumerate; this one takes a field that was never on it.
//
// audit.pre_create_months carries a default of 3 and was absent from the list,
// correctly — the partition maintainer substitutes its own default for zero
// (internal/adapters/audit/partitions.go), so the service behaved the same
// either way and nobody was hurt. The loader still lied about it: a file saying
// 0 loaded as 3, so anything reporting the configuration back would report a
// value the file did not contain. Presence is now read from the document for
// every setting, so what the file says is what the config holds.
func TestExplicitZerosSurviveForEveryField(t *testing.T) {
	t.Parallel()

	res, err := config.LoadWith(
		t.Context(),
		zeroFixture(t, "", "", "", "\n  pre_create_months: 0"),
		noEnv(),
	)
	cfg := res.Config
	require.NoError(t, err)
	require.Zero(t, cfg.Audit.PreCreateMonths,
		"a key the file states outright is kept, whether or not anyone listed it")

	// And omitting it still takes the default, which is the other half of the
	// distinction being drawn.
	res, err = config.LoadWith(t.Context(), zeroFixture(t, "", "", "", ""), noEnv())
	omitted := res.Config
	require.NoError(t, err)
	require.Equal(t, 3, omitted.Audit.PreCreateMonths)
}

// TestOriginsReportEachLayer covers what `config print --origin` shows. Getting
// this wrong is not visible in the resolved config — the value would be right
// and its provenance wrong — so it is worth pinning directly.
func TestOriginsReportEachLayer(t *testing.T) {
	t.Parallel()

	env := config.EmptyIsUnset(envconfig.MapLookuper(map[string]string{
		"REGISTRY_S3_ACCESS_KEY_ID":     "key",
		"REGISTRY_S3_SECRET_ACCESS_KEY": "secret",
		// Set but empty is not set, so this must not read as coming from the
		// environment — the file's value stands and the origin has to say so.
		"REGISTRY_S3_BUCKET": "",
	}))

	res, err := config.LoadWith(t.Context(), shippedPath("config.enterprise.dev.yml"), env)
	cfg, origins := res.Config, res.Origins
	require.NoError(t, err)

	require.Equal(t, config.OriginEnv, origins["registry.s3.access_key_id"])
	require.Equal(t, config.OriginFile, origins["registry.s3.bucket"],
		"an empty variable did not supply this; the file did")
	require.Equal(t, "easyp-plugins", cfg.Registry.S3.Bucket)
	// One of the few settings the shipped configs do not restate. That it is
	// this hard to find one is what Этап 4 is about.
	require.Equal(t, config.OriginDefault, origins["audit.enqueue_timeout"],
		"a setting the file does not name comes from the tag")

	// Every setting is accounted for, so nothing can be missing from the report.
	leaves, err := config.Leaves()
	require.NoError(t, err)
	require.Len(t, origins, len(leaves))
}

// TestEnvironmentOriginsHaveNoFileLayer covers the path every Helm deployment
// takes today: no config file at all, so a value is either a variable or a
// default and there is no third possibility to report.
func TestEnvironmentOriginsHaveNoFileLayer(t *testing.T) {
	t.Parallel()

	origins, err := config.EnvironmentOriginsWith(config.EmptyIsUnset(envconfig.MapLookuper(map[string]string{
		"DB_POSTGRES_DSN": "postgres://u:p@h:5432/d",
	})))
	require.NoError(t, err)

	require.Equal(t, config.OriginEnv, origins["db.postgres"])
	require.Equal(t, config.OriginDefault, origins["worker_pool.workers"])

	for name, origin := range origins {
		require.NotEqual(t, config.OriginFile, origin, "%s: there is no file on this path", name)
	}
}

// TestValidateRejects covers the rules that had no test at all. Each case starts
// from a config that passes and breaks exactly one thing, so a rule that stops
// working fails here rather than in production.
func TestValidateRejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{
			name:   "grpc port is required",
			mutate: func(c *config.Config) { c.Server.Port.GRPC = "" },
			want:   "server.port.grpc is required",
		},
		{
			name:   "metric port is required",
			mutate: func(c *config.Config) { c.Server.Port.Metric = "" },
			want:   "server.port.metric is required",
		},
		{
			name:   "a non-numeric port is refused before the listener sees it",
			mutate: func(c *config.Config) { c.Server.Port.GRPC = "not-a-number" },
			want:   "server.port.grpc must be a number between 1 and 65535",
		},
		{
			name:   "a port out of range is refused",
			mutate: func(c *config.Config) { c.Server.Port.Metric = "99999" },
			want:   "server.port.metric must be a number between 1 and 65535",
		},
		{
			// The kernel reads 0 as "any free port", so the service would listen
			// somewhere the Service, the probe and the port mapping cannot find.
			name:   "port zero is not a port",
			mutate: func(c *config.Config) { c.Server.Port.Health = "0" },
			want:   "zero asks the kernel for an arbitrary port",
		},
		{
			name:   "two listeners cannot share a port",
			mutate: func(c *config.Config) { c.Server.Port.MCP = c.Server.Port.GRPC },
			want:   "each listener needs its own port",
		},
		{
			// Used to pass: the only check was that the DSN was non-empty, so
			// the failure arrived during migrations, after startup.
			name:   "a DSN that is not one is refused",
			mutate: func(c *config.Config) { c.DB.Postgres = "postgres://%zz" },
			want:   "db.postgres is not a usable connection string",
		},
		{
			// The audit's own example. It passed because the only test was that
			// the DSN was not empty, so the failure arrived during migrations.
			name:   "a DSN of ordinary prose is refused",
			mutate: func(c *config.Config) { c.DB.Postgres = "i am not a dsn" },
			want:   "neither a postgres:// URL",
		},
		{
			name:   "a negative cache ceiling is not a setting",
			mutate: func(c *config.Config) { c.Registry.CacheMaxBytes = -1 },
			want:   "registry.cache_max_bytes must not be negative",
		},
		{
			name: "a cache too small for one plugin's output thrashes",
			mutate: func(c *config.Config) {
				c.Registry.MaxOutputSize = 1024
				c.Server.MaxSendMsgSize = 1024
				c.Registry.CacheMaxBytes = 512
			},
			want: "is below registry.max_output_size",
		},
		{
			// S3 counts as on when the bucket is set, so this section is filled
			// in, valid, and does nothing.
			name:   "S3 without a bucket is a section that does nothing",
			mutate: func(c *config.Config) { c.Registry.S3.Endpoint = "http://rustfs:9000" },
			want:   "registry.s3.bucket is empty",
		},
		{
			name:   "a tier label no dashboard matches is refused",
			mutate: func(c *config.Config) { c.Telemetry.ServiceTier = "platinum-deluxe" },
			want:   `telemetry.service_tier must be "community" or "enterprise"`,
		},
		{
			name:   "an unreadable log level is refused in a file",
			mutate: func(c *config.Config) { c.Log.Level = "trace" },
			want:   "log.level must be one of",
		},
		{
			// These six are the fields whose consumers replace a zero with
			// something else. Carrying the zero would make `config print`
			// disagree with the running service, so the input is refused.
			name:   "a zero licence TTL would be replaced at runtime",
			mutate: func(c *config.Config) { c.License.CacheTTL = 0 },
			want:   "license.cache_ttl must be positive",
		},
		{
			name:   "a zero receive limit would be replaced at runtime",
			mutate: func(c *config.Config) { c.Server.MaxRecvMsgSize = 0 },
			want:   "server.max_recv_msg_size must be positive",
		},
		{
			name:   "a zero stream limit would be replaced at runtime",
			mutate: func(c *config.Config) { c.Server.MaxConcurrentStreams = 0 },
			want:   "server.max_concurrent_streams must be positive",
		},
		{
			name:   "a zero flush timeout fails every audit write",
			mutate: func(c *config.Config) { c.Audit.FlushTimeout = 0 },
			want:   "audit.flush_timeout must be positive",
		},
		{
			name:   "a zero partition interval panics the ticker",
			mutate: func(c *config.Config) { c.Audit.PartitionCheckInterval = 0 },
			want:   "audit.partition_check_interval must be positive",
		},
		{
			name:   "an unparseable trusted proxy is refused",
			mutate: func(c *config.Config) { c.Server.TrustedProxies = []string{"172.28.0.0"} },
			want:   "is not a CIDR",
		},
		{
			name:   "a zero output limit refuses every generation",
			mutate: func(c *config.Config) { c.Registry.MaxOutputSize = 0 },
			want:   "registry.max_output_size must be positive",
		},
		{
			name: "a send limit below the output limit cannot deliver it",
			mutate: func(c *config.Config) {
				c.Registry.MaxOutputSize = 2
				c.Server.MaxSendMsgSize = 1
			},
			want: "must be at least registry.max_output_size",
		},
		{
			name:   "an empty DSN would fall back to the libpq environment",
			mutate: func(c *config.Config) { c.DB.Postgres = "" },
			want:   "db.postgres is required",
		},
		{
			name:   "workers must be positive",
			mutate: func(c *config.Config) { c.WorkerPool.Workers = 0 },
			want:   "worker_pool.workers must be positive",
		},
		{
			name:   "queue size must be positive",
			mutate: func(c *config.Config) { c.WorkerPool.QueueSize = 0 },
			want:   "worker_pool.queue_size must be positive",
		},
		{
			name:   "concurrent generations must be positive",
			mutate: func(c *config.Config) { c.WorkerPool.MaxConcurrentGenerations = 0 },
			want:   "worker_pool.max_concurrent_generations must be positive",
		},
		{
			name:   "a negative generation timeout expires before the plugin runs",
			mutate: func(c *config.Config) { c.WorkerPool.GenerationTimeout = -time.Second },
			want:   "worker_pool.generation_timeout must be positive",
		},
		{
			name:   "a negative shutdown timeout severs in-flight work",
			mutate: func(c *config.Config) { c.WorkerPool.ShutdownTimeout = -time.Second },
			want:   "worker_pool.shutdown_timeout must be positive",
		},
		{
			name:   "negative retries run no attempts at all",
			mutate: func(c *config.Config) { c.WorkerPool.MaxRetries = -1 },
			want:   "worker_pool.max_retries must not be negative",
		},
		{
			name:   "the shutdown budget must outlast a generation",
			mutate: func(c *config.Config) { c.Server.ForceShutdownAfter = time.Second },
			want:   "must exceed worker_pool.generation_timeout",
		},
		{
			name:   "requests per second must be positive",
			mutate: func(c *config.Config) { c.RateLimit.RequestsPerSecond = 0 },
			want:   "rate_limit.requests_per_second must be positive",
		},
		{
			name:   "burst must be positive",
			mutate: func(c *config.Config) { c.RateLimit.Burst = 0 },
			want:   "rate_limit.burst must be positive",
		},
		{
			name:   "a zero cleanup interval would panic the ticker",
			mutate: func(c *config.Config) { c.RateLimit.CleanupInterval = 0 },
			want:   "rate_limit.cleanup_interval must be positive",
		},
		{
			name:   "audit buffer size must be positive",
			mutate: func(c *config.Config) { c.Audit.BufferSize = 0 },
			want:   "audit.buffer_size must be positive",
		},
		{
			name:   "audit batch size must be positive",
			mutate: func(c *config.Config) { c.Audit.BatchSize = 0 },
			want:   "audit.batch_size must be positive",
		},
		{
			name:   "audit flush interval must be positive",
			mutate: func(c *config.Config) { c.Audit.FlushInterval = 0 },
			want:   "audit.flush_interval must be positive",
		},
		{
			name:   "audit save retries must not be negative",
			mutate: func(c *config.Config) { c.Audit.MaxSaveRetries = -1 },
			want:   "audit.max_save_retries must not be negative",
		},
		{
			name:   "retention must not be negative",
			mutate: func(c *config.Config) { c.Audit.RetentionMonths = -1 },
			want:   "audit.retention_months must not be negative",
		},
		{
			name: "partitions created further ahead than they are kept",
			mutate: func(c *config.Config) {
				c.Audit.RetentionMonths = 2
				c.Audit.PreCreateMonths = 3
			},
			want: "must not exceed audit.retention_months",
		},
		{
			name: "an S3 key without its secret",
			mutate: func(c *config.Config) {
				c.Registry.S3.Bucket = "plugins"
				c.Registry.S3.AccessKeyID = "key"
			},
			want: "must be set together",
		},
		{
			name: "S3 enabled without a local cache directory",
			mutate: func(c *config.Config) {
				c.Registry.S3.Bucket = "plugins"
				c.Registry.PluginsDir = ""
			},
			want: "registry.plugins_dir is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := baseConfig()
			tc.mutate(&cfg)
			require.ErrorContains(t, cfg.Validate(), tc.want)
		})
	}
}

// TestValidateAcceptsZeroRetries is the other half of the max_retries rule.
// Zero has to mean zero: the pool used to substitute two for it, so asking for
// no retries silently got you two.
func TestValidateAcceptsZeroRetries(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.WorkerPool.MaxRetries = 0
	require.NoError(t, cfg.Validate())
}

// TestValidateAcceptsAKeywordDSN is the other half of the DSN check. The
// keyword/value form is valid, lib/pq exports no parser for it, and rejecting
// what cannot be parsed would refuse a connection string the driver would have
// accepted — a worse failure than the one being fixed.
func TestValidateAcceptsAKeywordDSN(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.DB.Postgres = "host=localhost port=5432 user=easyp dbname=easyp sslmode=disable"

	require.NoError(t, cfg.Validate())
}

// TestValidateAcceptsRegionOnlyS3 guards the trap in the half-configured-S3
// rule. Region carries a default, so it is never empty; including it in the
// trigger set would refuse every configuration in existence.
func TestValidateAcceptsRegionOnlyS3(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	cfg.Registry.S3.Region = "us-east-1"

	require.NoError(t, cfg.Validate(), "a defaulted region must not look like a half-filled S3 section")
}
