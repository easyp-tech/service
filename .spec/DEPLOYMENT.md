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
grpcurl -cacert certs/ca.crt -d '{"group":"grpc","name":"go","version":"v1.5.1","config":{"command":["/plugins/grpc/go/v1.5.1/plugin"]}}' \
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

## Requirements

- **PostgreSQL** — required for plugin metadata and audit logs
- **Plugin binaries** — must be built via `task build-plugins` before service can generate code
- **grpcurl** — required for plugin registration via `register-plugins.sh`
