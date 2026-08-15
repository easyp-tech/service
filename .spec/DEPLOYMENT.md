<!-- generated: 2026-05-24, template: deployment.md -->
# Deployment

Deployment and infrastructure configuration for EasyP Service.

## Docker

### Service Dockerfile

`Dockerfile` — multi-stage build:
1. Build stage: `golang:1.26-bookworm` → static binary (`CGO_ENABLED=0`)
2. Runtime stage: `debian:bookworm-slim`, non-root at the distroless UID 65532

The build stage is pinned to `$BUILDPLATFORM` and cross-compiles via `GOOS`/
`GOARCH`. Without that the arm64 release runs the Go compiler itself under QEMU
rather than just producing arm64 output, which costs minutes per release and
buys nothing: CGO is off, so cross-compiling is two environment variables.

### Published images

Releases go to `ghcr.io/easyp-tech/service`, built for `linux/amd64` and
`linux/arm64` and joined under one manifest list, so `docker pull` picks the
architecture on its own. Each release publishes `:vX.Y.Z` and moves `:latest`.
The chart defaults to `Chart.appVersion` and ships no `imagePullSecrets`, which
is only correct while the package is public.

**The package's visibility is not in this repository.** GHCR creates every
package private, and there is no API for changing it — the only way is
`Organization -> Packages -> service -> Package settings -> Danger Zone ->
Change visibility`. Three things follow from that. It is set once and applies
to the package as a whole, so every tag already published and every tag pushed
afterwards is covered; there is no per-version visibility. It is one-way: a
package made public cannot be made private again. And nothing here fails if it
is missed — the release goes green either way, and the symptom appears at the
far end as pods in `ImagePullBackOff`.

### Plugin Dockerfiles

Located in `registry/{group}/{name}/Dockerfile` (versions listed in `plugin.yaml`):
- Multi-stage build → final image with entrypoint at **`/plugin`**
- Optional sidecars (`/app`, `/nodejs`, jars, …) allowed next to the entrypoint
- Used for building local artifacts only (via `docker build --output`)

### Plugin Build Process

Plugins are built as local binaries, not pushed to a Docker registry.

**Contract:** every successful build produces:

```text
plugins/{group}/{name}/{version}/plugin   # required entrypoint
plugins/{group}/{name}/{version}/…        # optional sidecars
```

```bash
# `easyp-svc plugins build` extracts the image filesystem from multi-stage builds.
# For each version in registry/{group}/{name}/plugin.yaml it runs, in effect:
docker build \
  --build-arg VERSION={version} \
  --build-arg KEY=value   # from optional plugin.yaml build_args \
  --output type=local,dest=plugins/{group}/{name}/{version}/ \
  -f registry/{group}/{name}/Dockerfile registry/{group}/{name}/
```

`plugin.yaml` no longer carries a runtime binary name. Dockerfiles that need an
upstream tool name set a default `ARG BINARY_NAME=…` (or take it from `build_args`).

At runtime, the service executes `command` from the DB (after migrate: path ending in `/plugin`) via stdin/stdout (`registry.plugins_dir`).

### Plugin Artifact Storage (S3)

When `registry.s3` is configured, artifacts are distributed through object storage
instead of a shared `plugins/` volume — `plugins_dir` becomes a local cache.

**Storage unit:** the whole version directory (entrypoint + sidecars) packed as
`tar.gz`, stored at `{group}/{name}/{version}/plugin.tgz`.

```bash
# Build machine / CI — needs S3 WRITE access
easyp-svc plugins build registry
easyp-svc plugins push plugins --cfg deploy/config/config.yml   # packs and uploads plugin.tgz
easyp-svc plugins register plugins --cfg deploy/config/config.yml \
  --addr easyp.api.localhost:4443 --tls-ca deploy/certs/ca.crt
```

Registration is metadata-only. The service streams the pushed archive from storage,
computes its sha256 and records it in the plugin config — a client cannot supply
the hash. Registering before pushing fails with `FAILED_PRECONDITION`.

At runtime, when the entrypoint is missing from `plugins_dir`, the service downloads
the archive (concurrent misses collapse into one download), verifies the recorded
sha256, and unpacks it atomically before executing anything. A storage outage
surfaces as `UNAVAILABLE`; a checksum mismatch aborts without unpacking.
The service itself needs only READ (plus DELETE for `DeletePlugin`) access.

### Plugin Registration

Plugins must be registered in PostgreSQL before use:

```bash
# Prefer the CLI (scans plugins/ for files named "plugin")
easyp-svc plugins register plugins/ --plugins-prefix /plugins \
  --addr easyp.api.localhost:4443 --tls-ca deploy/certs/ca.crt

# Or via gRPC CreatePlugin with config.command pointing at the entrypoint
# The server does not serve reflection, so the schema comes from a descriptor
# set built with `easyp-svc api descriptor -o api.protoset`. CreatePlugin is a
# mutating method, so the call also needs a write token.
grpcurl -protoset api.protoset -cacert deploy/certs/ca.crt \
  -H "authorization: Bearer $EASYP_TOKEN" \
  -d '{"group":"grpc","name":"go","version":"v1.5.1","config":{"command":["/plugins/grpc/go/v1.5.1/plugin"]}}' \
  easyp.api.localhost:4443 api.generator.v1.ServiceAPI/CreatePlugin
```

## Docker Compose

`deploy/docker-compose.yml` provides a full dev stack:

| Service | Port | Description |
|---------|------|-------------|
| service | 8081-8083 | EasyP HTTP endpoints; gRPC (8080) is internal only |
| postgres | 5432 | PostgreSQL database |
| traefik | 80, 4443 | Reverse proxy; terminates TLS and fronts the gRPC API |
| rustfs | 9000-9001 | S3-compatible storage (observability backends + plugin archives) |
| grafana | 3000 | Dashboards |
| loki | — | Log aggregation |
| alloy | 12345 | OpenTelemetry collector (replaces Prometheus scraper) |
| tempo | — | Distributed tracing |
| mimir | — | Metrics storage |
| pyroscope | — | Continuous profiling |
| init-buckets | — | One-shot: creates S3 buckets for observability |

### Minimal Stack

For local development:
```bash
task up-minimal  # postgres only (port 5433)
```

### Both tiers at once

`deploy/docker-compose.dev.yml` runs a licensed container next to an unlicensed
one. It exists because almost everything licence-gated — audit, the worker
ceiling, the plugin count — is only visible as a *difference*, and a single
container cannot show you one.

```bash
cp deploy/.env.dev.example deploy/.env.dev   # carries the development licence
task up-dev
task tier-dev                                # assert the tiers really differ
```

|            | gRPC/metrics/health/mcp | Host                         | Database              |
|------------|-------------------------|------------------------------|-----------------------|
| community  | 8080–8083               | `community.easyp.localhost`  | `easyp_community_db`  |
| enterprise | 9080–9083               | `enterprise.easyp.localhost` | `easyp_enterprise_db` |

Both run the published image rather than a local build, and both sit behind the
same traefik with the same mTLS transport, so the only difference between them
is the licence. `LICENSE_*` is set on the enterprise container only; a licence
reaching the community one would make the pair useless as a comparison.

**A missing or unverifiable licence is not a startup failure.** Community is a
legitimate configuration, so the service logs the problem and serves on — which
means a broken `LICENSE_KEY` produces two identical community containers that
look healthy. `task tier-dev` reads `easyp_license_valid` from both and asserts
1 on enterprise and 0 on community. Run it before trusting anything the stack
tells you about tiering.

The development licence — `sub=test-dev-license`, `tier=enterprise`,
`max_workers=16`, unlimited plugins, valid to 2036 — is committed in
`deploy/.env.dev.example`. It cannot be reissued from this repository: the
service only verifies PASETO tokens, and the signing key lives in the licence
registry (`easyp-tech/licenses`).

Storage credentials are *not* committed. `deploy/config/config.*.dev.yml` carry
the endpoint, bucket and region, and take the key pair from
`REGISTRY_S3_ACCESS_KEY_ID` / `REGISTRY_S3_SECRET_ACCESS_KEY` in
`deploy/.env.dev`, which is gitignored.

### On a remote host

`deploy/` is the unit that gets copied. A host running the stack needs no source
checkout, no Go toolchain and no task runner — only Docker and this directory:

```bash
rsync -a --delete \
  --exclude='.env*' --exclude='certs/' --exclude='charts/' \
  --exclude='plugins-community/' --exclude='plugins-enterprise/' \
  --exclude='observability/traefik/traefik.public.yml' \
  deploy/ user@host:~/easyp/
ssh user@host 'cd ~/easyp && ./scripts/gen-dev-certs.sh'
# put the licence and the storage key pair in ~/easyp/.env, mode 600
ssh user@host 'cd ~/easyp && docker compose -f docker-compose.dev.yml up -d'
ssh user@host 'cd ~/easyp && ./scripts/check-tiers.sh'
```

**The excludes are load-bearing, and `--delete` is why.** Everything they protect
lives on the host and in no checkout, so a bare `rsync -a --delete deploy/` does
four things at once. It deletes `~/easyp/.env` — the only copy of the licence
key, the database DSN and the storage credentials — and uploads the developer's
own `deploy/.env.dev` in its place, putting local secrets on a shared host. It
deletes `observability/traefik/traefik.public.yml`, which is what terminates TLS
for the public names. And it empties `plugins-community/` and
`plugins-enterprise/`: those directories are empty in the repository and hold
the built plugin binaries on the host, so generation stops working with no
error that points at the cause. `certs/` and `charts/` are excluded for smaller
reasons — the host generates its own certificates, and the Helm chart is not
part of a compose deployment.

Run it with `-n` first. The list of what `--delete` intends to remove is the
only part of this worth reading, and it takes a second.

For an update to a host that is already running, prefer syncing the
subdirectories that actually changed and leaving `--delete` off entirely. It is
the difference between a deploy that is wrong and a deploy that has destroyed
the only copy of something.

Paths inside the compose file resolve against the file itself, so the copy works
unedited — which is the property the `deploy/` layout was arranged for.

Two things do not travel. `docker-compose.yml`, the full observability stack,
builds the image from source (`context: ..`) and cannot run where there is no
source; the two-tier file pulls the published image instead and is the one to
use remotely. And `easyp-svc` itself is gone from the host, so plugins are
registered from a machine that has the CLI, pointed at the host through traefik.

One thing the observability overlay asks of the host: the `alloy` service mounts
`/proc`, `/sys` and `/` read-only at `/host/...`. That is what lets the embedded
`prometheus.exporter.unix` measure the machine rather than its own container —
the filesystem collector reads the mount list from procfs and then has to stat
those paths, which do not exist inside the container otherwise. Worth stating
plainly: alloy therefore reads the whole host filesystem and holds the docker
socket, which is a lot of reach for one process. It was chosen over a separate
node-exporter container knowingly.

`check-tiers.sh` is deliberately a script rather than a Taskfile target, so the
same implementation runs in both places. `HOST`, `COMMUNITY_METRICS_PORT` and
`ENTERPRISE_METRICS_PORT` let it run over an SSH tunnel once the metrics ports
are on loopback.

**Publishing.** Only traefik binds to all interfaces. Everything else — both
services and the database — binds to `127.0.0.1` unless `EASYP_BIND` says
otherwise. The database password sits in the committed compose file, so on a
host with a public address the default is the difference between a dev stack and
an open database. The dev host ran with 5432 exposed; its traefik log shows the
scanning that follows.

## GoReleaser

`.goreleaser.yaml` configures release builds:
- Cross-compilation targets
- Binary naming
- Archive formats
- Checksum generation

## Configuration

### Priority

CLI flags > environment variables > YAML config file.

### Config Files

| File | Purpose |
|------|---------|
| `deploy/config/config.yml` | Docker-compose service config (internal hostnames) |
| `deploy/config/config.local.yml` | Local development config (localhost, port 5433) |

### YAML Config Structure

```yaml
server:
  host: 0.0.0.0
  port:
    grpc: 8080
    metric: 8081
    health: 8082
    mcp: 8083
db:
  driver: postgres
  postgres: "postgres://easyp_svc:easyp_pass@localhost:5433/easyp_db?sslmode=disable"
  migrate_dir: migrate
registry:
  plugins_dir: "./plugins"        # Directory with built plugin binaries
  max_output_size: 67108864       # 64MB max plugin output
telemetry:
  otlp_endpoint: "localhost:4317"
  pyroscope_endpoint: "http://localhost:4040"
worker_pool:
  workers: 4
  queue_size: 16
  generation_timeout: 120s
  max_retries: 3
  shutdown_timeout: 30s
license:
  cache_ttl: 5m
rate_limit:
  requests_per_second: 10.0
  burst: 20
  cleanup_interval: 10m
```

### Environment Variables

All config fields have `env` tags. Prefix: section name (e.g., `SERVER_HOST`, `DB_POSTGRES_DSN`, `WORKER_POOL_WORKERS`).

Three that are easy to miss and that change behaviour rather than tuning it:

| Variable | Default | Why it matters |
|----------|---------|----------------|
| `SERVER_TRUSTED_PROXIES` | empty | Comma-separated CIDRs whose forwarding headers are believed. Empty behind a proxy collapses every caller into one rate-limit bucket and files the audit log under the proxy. See [SECURITY.md](SECURITY.md#what-per-ip-means-depends-on-servertrusted_proxies). |
| `SERVER_MAX_SEND_MSG_SIZE` | 67108864 | Must cover `REGISTRY_MAX_OUTPUT_SIZE`; startup refuses a smaller value, because a plugin's permitted output would otherwise be generated and then undeliverable. |
| `SERVER_MAX_CONCURRENT_STREAMS` | 256 | gRPC's own default is unlimited, which lets one connection exhaust the pod's memory before any limiter sees the requests. |

Clients need `sdk.WithMaxRecvMsgSize` only if the service is configured above
64 MiB; the SDK defaults to the same figure the service does.

### Customizable Ports (docker-compose)

| Variable | Default | Description |
|----------|---------|-------------|
| `EASYP_POSTGRES_PORT` | 5432 | PostgreSQL host port |
| `EASYP_METRICS_PORT` | 8081 | Metrics host port |
| `EASYP_HEALTH_PORT` | 8082 | Health host port |
| `EASYP_GATEWAY_PORT` | 8083 | MCP/Gateway host port |
| `EASYP_GRAFANA_PORT` | 3000 | Grafana host port |
| `EASYP_TRAEFIK_PORT` | 80 | Traefik host port (HTTP) |
| `EASYP_TRAEFIK_TLS_PORT` | 4443 | Traefik host port (HTTPS) — the only way to the gRPC API |

The gRPC port has no host mapping: the listener requires a client certificate
and traefik is the only party holding one.

### Transport security

`server.tls` configures the listener. `cert_file` and `key_file` must be set
together; adding `client_ca_file` makes the listener require and verify a client
certificate. Leaving `cert_file` empty serves plaintext and logs a warning on
every start.

Traefik holds `client.crt`/`client.key` and reaches the service over that mutual
TLS leg via the `easyp-mtls@file` serversTransport declared in
`deploy/observability/traefik/dynamic.yml`; the docker provider cannot declare one. Outside
the stack traefik serves `edge.crt` on `easyp.api.localhost`.

`deploy/scripts/gen-dev-certs.sh` (`task certs`) issues a throwaway CA and the three
certificates for development. Production certificates come from your own CA and
are mounted at the paths in `deploy/config/config.yml`.

## Backup and restore

Deliberately tool-agnostic: whatever takes your Postgres backups today takes
these. What follows is what has to be in the set and what the service assumes
about it.

### What has to be backed up

| What | Why it cannot be reconstructed |
|------|-------------------------------|
| **The `plugins` table** | The registry itself: which plugin versions exist, their config, their command line, their recorded checksums. Nothing else holds this. |
| **The `audit_log` partitions** | The audit trail. Enterprise sells it; it exists nowhere else and there is no second copy. |
| **The `goose_db_version` table** | Which migrations have run. Restoring data without it makes the next startup either re-run migrations or refuse to start. |
| **The object storage bucket** | Plugin archives. The local cache is a cache — it is evicted — so the bucket is the only copy of the binaries. |
| **`DB_POSTGRES_DSN`, `AUTH_WRITE_TOKENS`, S3 credentials** | Secrets are not in the database and are usually not in the cluster backup either. |
| **`LICENSE_KEY` and `LICENSE_PUBLIC_KEY`** | Recoverable from the licence registry, but not by you at 3am. Without them the restored service runs in community mode: no audit, four workers, ten plugins. |

The database and the bucket have to be recoverable to *roughly* the same point.
They are not transactional with each other, and the direction of the skew is what
matters: a `plugins` row without its archive fails generation with
`BINARY_NOT_UPLOADED`, which is at least legible. An archive with no row is
merely orphaned. **So prefer a bucket snapshot slightly newer than the database
one** — never older.

### How much history to keep

`config.audit.retentionMonths` (12 by default) is enforced by dropping whole
partitions on a schedule. That is a real delete: after it, the only copy of that
month is in a backup taken while it still existed.

So the backup retention has to exceed the audit retention, not match it. Matching
them means the month drops out of the database and out of the archive at
approximately the same time, which leaves nothing anywhere. If you are asked to
keep audit history for a compliance window, that window is a constraint on the
*backups*, and `retentionMonths` is only the size of the working set.

An RPO of hours is fine for `plugins` — plugin registration is a deliberate,
infrequent act and is easily repeated. For `audit_log` the RPO *is* the size of
the gap in the audit trail, so it should be measured in minutes if audit is
being relied on.

### Restoring

1. **Restore the database first**, then the bucket. The reverse order leaves the
   service briefly able to serve plugins it has no rows for.
2. **Do not run migrations by hand.** The service applies them itself at startup
   and serialises that across replicas. Start one replica and let it.
3. **Check `goose_db_version` against the binary you are restoring onto.** A
   database restored from a newer release than the binary is the one case that
   startup cannot resolve on its own: goose will not roll forward from a version
   it does not know, and the service refuses to start rather than run against a
   schema it does not understand. Restore onto the matching release.
4. **Expect the plugin cache to be empty.** It is a cache, and with
   `persistence.enabled=false` it is empty after every restart anyway. The first
   request for each plugin re-downloads it. Watch
   `easyp_plugin_cache_bytes` climb; nothing needs doing.
5. **Verify audit continuity before declaring done.** Partition maintenance runs
   every `config.audit.partitionCheckInterval`, so a restore landing in a month
   whose partition was never created writes into the default partition —
   `easyp_audit_default_partition_used` goes to 1 and stays there. See
   `.spec/RUNBOOKS.md`.

### Verifying

A backup that has never been restored is a hypothesis. The cheap version of the
test: restore into a scratch namespace, start the service against it, and check
that `easyp_business_plugins_total` matches what production reports and that the
newest `audit_log` row is inside the RPO you think you have.

## Requirements

- **PostgreSQL** — required for plugin metadata and audit logs
- **Plugin binaries** — must be built via `task build-plugins` before service can generate code
- **grpcurl** — required for plugin registration via `register-plugins.sh`
