<!-- generated: 2026-09-16, template: agents-index.md -->
# EasyP API Service

Centralized protobuf/gRPC plugin execution service. Accepts `CodeGeneratorRequest` via gRPC, executes the named plugin as a local process, returns `CodeGeneratorResponse`.

**Module:** `github.com/easyp-tech/service` | **Go:** 1.26+ | **License:** Elastic 2.0 (`api/` and `sdk/` are Apache 2.0)

## Architecture

**Interceptor chain (order matters):** trace_logging → realip → prometheus → structured_logging → panic_recovery → validation → error_code_conversion → rate_limit → concurrency_limit → auth → license. Audit is not an interceptor; `internal/core` emits it.

| Pattern | Where | Purpose |
|---------|-------|---------|
| Decorator (tracing) | `telemetry.TracingCore`, `TracingRegistry`, `TracingPlugin` | Non-invasive OTel instrumentation |
| Worker Pool | `core.WorkerPool` | Bounded concurrency for plugin process execution |
| Feature Gate | `license.FeatureGate` | Two-tier licensing without hard coupling |
| Interceptor chain | `grpchelper.NewServer` + custom interceptors | Composable cross-cutting concerns |
| Repository pattern | `core.Registry` interface → `adapters/registry` | Postgres, S3 and process execution behind one interface |
| Functional options | SDK `WithXxx()` options | Configurable client construction |

## How to Use (for agents)

1. **Start here** → this file, then [README.md](README.md) for setup and configuration
2. **Before modifying code** → read the package you are changing; the code carries
   its reasoning in comments rather than in a parallel document that drifts
3. **Operational procedures** → [Runbooks](https://easyp.tech/docs/api-service/runbooks), one section per alert

## Three modules, not one

The repository holds three Go modules, and the split is load-bearing rather than
tidy. `api/` and `sdk/` carry their own `go.mod` and their own Apache-2.0
`LICENSE`; the service is Elastic-2.0. Go tooling reads licences per module, so
before the split a client importing the SDK had its licence scanner report
Elastic-2.0 on their own build.

They are tagged separately — `v1.0.2`, `api/v1.0.2`, `sdk/v1.0.2` — and released
in lockstep, so that one version number means one thing. A change to the wire
contract therefore touches a module whose version is a promise to people outside
this repository.

## What the licence tier actually changes

Less than the two-tier scaffolding suggests, and worth knowing before adding a
feature behind the gate.

Audit is the only Enterprise-gated feature. `feature.IsEnterprise` returns true
for exactly one constant, and constants for undeclared features were deliberately
removed: they read as shipped capability when nothing stood behind them. Code
generation, plugin listing, MCP tools, rate limiting and plugin CRUD are all in
Community.

The rest of the difference is ceilings, and Enterprise simply has none:

| Ceiling | Community | Enterprise |
|---|---|---|
| `worker_pool.workers` | 4 | unlimited |
| Plugins in the registry | 10 | unlimited |
| Concurrent generations | 16 | unlimited |

A ceiling caps the resolved configuration at startup rather than rejecting the
request, so an over-ambitious Community config starts and logs what it got.

A token names a tier and nothing else. Which features that tier unlocks is
decided in `core.EnterpriseLicenseClaims`, in the release, so extending the
offering ships as a version rather than as a licence reissue for every customer.

## Project Map

```
api/                    # The wire contract. Its own Go module, Apache-2.0.
  easyp/generator/v1/   # .proto, generated stubs, MCP bindings
cmd/easyp-svc/          # The service and its CLI (service start, plugins, config, auth)
cmd/mcp-smoke/main.go   # MCP smoke test client
internal/
  core/                 # Domain types, interfaces, sentinel errors, worker pool
  api/                  # gRPC handlers, audit & license interceptors, MCP handler
  adapters/             # audit/ metrics/ registry/ storage/ — implement core interfaces
  auth/                 # Authenticator interface, static write-token digests, Actor
  config/               # Loader, diagnostics, env binding, retired keys, aliases
  database/             # sqlx wrapper (metrics/tracing), connectors
    goosemigrate/       # Embedded SQL migrations (goose v3, advisory-locked)
  grpchelper/           # gRPC server/client factories, middleware
  license/              # PASETO v4 management, FeatureGate, claims
  plugarchive/          # tar.gz pack/unpack with the path and symlink rules
  ratelimiter/          # Per-IP token bucket and concurrency cap, both gated
  safe/                 # Panic-guarded goroutine helper
  serve/                # HTTP and gRPC listener lifecycles
  telemetry/            # OTLP + Pyroscope, tracing decorators
  monitor/              # Context-aware slog logger
  flags/                # CLI flag types
sdk/                    # Go client SDK. Its own module, Apache-2.0.
registry/               # Plugin Dockerfiles (multi-stage, scratch, non-root)
plugins/                # Built plugin binaries (gitignored, generated by `easyp-svc plugins build`)
test/                   # Integration tests (build tag `integration`) and CI helpers
test_registry/          # Plugin Dockerfiles the tests build against
deploy/                 # Everything that runs the service somewhere
  docker-compose.yml    # Full dev stack
  docker-compose.dev.yml # Community and enterprise side by side
  config/               # Service configs, one per way of running it
  observability/        # Alloy, Grafana, Loki, Tempo, Mimir, Pyroscope, Traefik
  charts/easyp-service/ # Helm chart
  scripts/              # gen-dev-certs.sh, deploy-dev.sh
```

## Build & Test

See [README.md](README.md#development) for full setup.

```bash
task up                  # Full dev stack (postgres, grafana, loki, alloy, tempo, mimir, pyroscope, traefik)
task up-minimal          # Postgres only (port 5433)
task down                # Stop and clean volumes
task up-dev              # Community and enterprise side by side (needs deploy/.env.dev)
task up-dev-full         # Same plus the observability overlay
task tier-dev            # Assert the two dev tiers really are different tiers
task logs-dev            # Tail the two-tier stack
task down-dev            # Stop the two-tier stack
task deploy-dev HOST=... # Ship deploy/ to a stand and roll it to the version this repo names
task run                 # Full cycle: build-plugins → down → up → register-plugins → logs
task setup               # Same as run but without tailing logs
task run-local           # go run from source against minimal stack
task build-plugins       # Build all plugin binaries from registry/ Dockerfiles (easyp-svc plugins build)
task push-plugins        # Upload packed archives to S3 (easyp-svc plugins push)
task register-plugins    # Register all built plugins via gRPC CreatePlugin API (easyp-svc plugins register)
task generate            # easyp generate against running service (easyp.yaml)
task generate-local      # easyp generate with local config (easyp.local.yaml)
go test ./...            # Standard tests
```

## Conventions

- **Domain types and interfaces** live in `internal/core/domain.go` — single source of truth
- **Business logic** in `internal/core/core.go` — thin, delegates to Registry
- **Tracing** via decorator pattern (`telemetry/tracing_*.go`), never mixed into business logic
- **Adapters** implement core interfaces; placed in `internal/adapters/`
- **Database access** always through `database.SQL` wrapper, never raw `sqlx.DB`
- **Domain errors** are sentinel: `core.ErrNotFound`, `core.ErrInvalidPluginName`, etc.
- `api.ErrorToStatus()` maps domain errors → gRPC status codes
- **Plugin name format:** `{group}/{name}:{version}` — validated by `^[a-z][a-z0-9-]*/[a-z][a-z0-9-]*:(v\d+\.\d+(\.\d+)?|latest)$` — the patch component is optional, because protobuf ships versions like `v33.1`
- **Plugin execution:** local binary execution from `plugins/` directory (built by `easyp-svc plugins build`)
- **Plugin registration:** via gRPC `CreatePlugin` API (automated by `easyp-svc plugins register`) — metadata only; with S3 enabled the service streams the already-pushed archive from storage, computes its sha256 and records it in the plugin config
- **Artifact unit:** the whole version directory packed as `tar.gz` (`internal/plugarchive`), stored at `{group}/{name}/{version}/plugin.tgz` — the entrypoint plus any sidecars, never a bare binary
- **Binary storage:** `core.BinaryStorage` interface (read-only: Download/Open/Exists/Delete), S3 implementation in `internal/adapters/storage`; uploads happen out-of-band via `easyp-svc plugins push`
- **Testing:** standard `go test` with `stretchr/testify` assertions (`assert`/`require`); mocks defined in test files
- **Config priority:** environment > YAML file > `default=` struct tag. The only
  flags that take part are `--cfg`, which chooses the file, and `--log_level`,
  which overrides `log.level`. `--cfg` also reads `EASYP_CONFIG`, which is how
  the image's `HEALTHCHECK` finds the same file the service was started with.
  An unrecognised YAML key refuses the start. See
  [Configuration](README.md#configuration) and `internal/config/config.go`.
- **Comments:** English only; every exported symbol must have a godoc comment starting with its name; no inline comments on `if`/`for`/`return` lines unless genuinely non-obvious.

## Pitfalls & Gotchas

- **Plugins are local binaries** — service executes plugins from `plugins/` directory, not Docker containers at runtime
- **S3 mode** — with `registry.s3` configured, `plugins_dir` is a local cache: archives are pushed out-of-band (`plugins push`), then downloaded on demand (singleflight-deduplicated), sha256-verified and unpacked before execution; **push must precede register** or `CreatePlugin` fails with `FAILED_PRECONDITION`; S3 outage surfaces as `UNAVAILABLE`, not `NOT_FOUND`
- **Pipeline order** — `build` → `push` → `register` (`task run` / `task setup` chain them); `--force` re-push of a registered plugin invalidates its recorded checksum, so re-register it
- **Entrypoint is always named `plugin`** — build output and migrate scan require `plugins/{group}/{name}/{version}/plugin`; Dockerfiles must `COPY` the entrypoint to `/plugin` (sidecars optional)
- **`easyp-svc plugins build <registry-path>`** — uses Docker multi-stage builds to extract image filesystems to the output directory (`plugins/` by default); supports `--filter`, `--parallel`, `--force`, `--dry-run`
- **`easyp-svc plugins push [path]`** — packs each version directory into `plugin.tgz` and uploads it to S3; `--cfg` supplies `registry.s3`, flags override it; `--filter`, `--force`, `--dry-run`
- **`easyp-svc plugins register [path]`** — registers built plugins via gRPC `CreatePlugin` (default path `plugins`); use `--cfg` for `registry.plugins_dir` as command prefix, `--dry-run`, `--fail-on-error` (default true)
- **Port 5432 conflict** — if postgres already runs locally, minimal stack uses port 5433 (`EASYP_POSTGRES_PORT`)
- **Plugin binaries must exist** — run `task build-plugins` before the service can generate code
- **License:** `PasetoLicenseClient` verifies the token offline against `license.public_keys` (kid → hex; an entry under `"*"` verifies any key id). No key configured means community mode; a key that does not decode stops startup. Anything else that goes wrong resolves to community, never to an error
- **`easyp generate` needs running service** — the generate command calls localhost:8080 gRPC
- **Migration order matters** — files are sorted by numeric prefix; never reorder
- **Audit channel capacity** — `audit.buffer_size`, 1000 by default, not fixed. An entry that cannot be queued within `audit.enqueue_timeout` is dropped and counted in `easyp_audit_events_lost_total{reason="enqueue_timeout"}` — the alert exists because a silent drop is a gap in something Enterprise sells
- **The image checks its own health** — `easyp-svc health` probes `/live` on the health port and backs the `HEALTHCHECK` in the Dockerfile. It has no command line of its own, so it finds the config through `EASYP_CONFIG`; without that variable it falls back to the default port and reports a container unhealthy while it serves normally. It deliberately probes liveness, not readiness: readiness checks Postgres, and restarting on a database blip turns an outage into a crash loop
- **WorkerPool `Get()` is non-blocking** — returns `ErrServerOverloaded` immediately if queue is full

## Documentation

Runbooks for every alert live in [Runbooks](https://easyp.tech/docs/api-service/runbooks). Setup,
configuration and the deployment stack are in [README.md](README.md); everything
else is documented next to the code it describes.

## Key Dependencies

| Dependency | Purpose |
|---|---|
| `google.golang.org/grpc` | gRPC framework |
| `google.golang.org/protobuf` | Protobuf serialization |
| `github.com/jmoiron/sqlx` + `github.com/lib/pq` | PostgreSQL access |
| `github.com/modelcontextprotocol/go-sdk` | MCP protocol |
| `go.opentelemetry.io/otel` | OpenTelemetry tracing + metrics |
| `github.com/grafana/pyroscope-go` | Continuous profiling |
| `github.com/prometheus/client_golang` | Prometheus metrics |
| `github.com/grpc-ecosystem/go-grpc-middleware/v2` | gRPC middleware |
| `github.com/sethvargo/go-envconfig` | Environment variable config binding |
| `golang.org/x/time` | Token bucket rate limiter |

## Ports

These are the defaults the binary ships. Every deployment in `deploy/` overrides
them, so a port seen in a compose file or a log line is not evidence of what the
default is.

| Port | Service | Protocol |
|------|---------|----------|
| 23410 | gRPC API | gRPC (H2) |
| 23411 | Metrics | HTTP (`/metrics`) |
| 23412 | Health | HTTP (`/live` liveness, `/` readiness) |
| 23413 | MCP | HTTP (`/mcp`), served only when `mcp.enabled` |
| 5432/5433 | PostgreSQL | TCP |

The two-tier dev stack puts them on 8080 to 8083 instead, which is what
`deploy/config/config.*.dev.yml` says and what the containers log.

## Before changing anything non-trivial

Bug analysis, architectural decisions and anything touching a frozen surface
deserve a hypothesis before an edit. Form one, say what would disprove it, and
check that before writing code — whatever tooling you have for it.

Two things in this repository make that cheaper than guessing. The code carries
its reasoning in comments, so the *why* of a decision is usually next to it
rather than in a document that drifted. And most invariants have a test whose
name is the claim: `TestExplicitZerosSurvive`, `TestRollbackOntoAnOlderBinary`,
`TestReadmeNamesOnlyRealVariables`. If you are about to change behaviour, find
the test that pins it first; if there is none, that is worth knowing too.
