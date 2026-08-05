<!-- generated: 2026-05-24, template: deployment.md -->
# Deployment

Deployment and infrastructure configuration for EasyP Service.

## Docker

### Service Dockerfile

`Dockerfile` — multi-stage build:
1. Build stage: `golang:alpine` → static binary
2. Runtime stage: minimal image

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
easyp-svc plugins push plugins --cfg config.yml   # packs and uploads plugin.tgz
easyp-svc plugins register plugins --cfg config.yml \
  --addr easyp.api.localhost:4443 --tls-ca certs/ca.crt
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
  --addr easyp.api.localhost:4443 --tls-ca certs/ca.crt

# Or via gRPC CreatePlugin with config.command pointing at the entrypoint
# The server does not serve reflection, so the schema comes from a descriptor
# set built with `easyp-svc api descriptor -o api.protoset`. CreatePlugin is a
# mutating method, so the call also needs a write token.
grpcurl -protoset api.protoset -cacert certs/ca.crt \
  -H "authorization: Bearer $EASYP_TOKEN" \
  -d '{"group":"grpc","name":"go","version":"v1.5.1","config":{"command":["/plugins/grpc/go/v1.5.1/plugin"]}}' \
  easyp.api.localhost:4443 api.generator.v1.ServiceAPI/CreatePlugin
```

## Docker Compose

`docker-compose.yml` provides a full dev stack:

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
| `config.yml` | Docker-compose service config (internal hostnames) |
| `config.local.yml` | Local development config (localhost, port 5433) |

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
`configs/traefik/dynamic.yml`; the docker provider cannot declare one. Outside
the stack traefik serves `edge.crt` on `easyp.api.localhost`.

`scripts/gen-dev-certs.sh` (`task certs`) issues a throwaway CA and the three
certificates for development. Production certificates come from your own CA and
are mounted at the paths in `config.yml`.

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
