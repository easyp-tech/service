# EasyP API Service

A service for executing protobuf/gRPC code generation plugins as isolated processes.

**Module:** `github.com/easyp-tech/service`

## Why EasyP API Service?

### The Problem: Plugin Management Chaos

Managing protobuf/gRPC code generation across development teams becomes increasingly complex as organizations scale:

**Version Inconsistencies**
- Developers use different plugin versions locally, causing build failures and inconsistent generated code
- "Works on my machine" syndrome when generated code differs between environments
- Manual coordination required to keep entire teams synchronized on plugin versions

**Operational Overhead**
- DevOps teams spend significant time managing plugin installations across developer machines
- Each new team member requires manual setup of correct plugin versions
- Plugin updates require coordinating with every developer individually
- No centralized control over which plugin versions are approved for use

**Security & Compliance Risks**
- Developers install plugins from various sources without security validation
- No audit trail of which plugins were used for which builds
- Difficult to enforce security policies on code generation tools

### The Solution: Centralized Plugin Execution

EasyP API Service eliminates these operational headaches by centralizing plugin management:

**🎯 Instant Version Control**
- Deploy new plugin versions to entire team instantly
- Operations team controls plugin rollouts without touching developer machines
- Zero developer coordination required for plugin updates

**🔒 Security & Consistency**
- All plugins built from auditable Dockerfiles with security constraints
- Centralized approval process for new plugins
- Consistent execution environment regardless of developer's local setup

**⚡ Developer Experience**
- No local plugin installation or maintenance required
- Works identically across all environments (local, CI/CD, production)
- New team members productive immediately without plugin setup

## Overview

EasyP API Service provides centralized management and execution of protobuf/gRPC plugins. The service accepts `google.protobuf.compiler.CodeGeneratorRequest` via gRPC API and returns generated code by executing plugin binaries in an isolated environment with bounded concurrency.

### Key Features

- 🔧 **Plugin binary execution** with bounded worker pool
- 📦 **Plugin registry** with PostgreSQL metadata storage
- 🔄 **Plugin versioning** with "latest" support
- 📊 **Full observability** with Prometheus, Grafana, OpenTelemetry, Pyroscope
- 🗄️ **Persistence** with PostgreSQL
- 🌐 **gRPC + MCP** API
- 📈 **Health checks** and metrics
- 🔑 **Two-tier licensing** (Community / Enterprise)
- 🔐 **Token-authenticated writes**, anonymous reads
- 📝 **Audit logging** for all operations

## Architecture

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   gRPC Client   │───▶│   API Service   │───▶│  Plugin Binary   │
└─────────────────┘    └─────────────────┘    └─────────────────┘
                               │
                               ▼
                       ┌─────────────────┐
                       │   PostgreSQL    │
                       └─────────────────┘
```

The service executes plugins as local binaries, passing protobuf data through stdin/stdout.

### Plugin Artifact Delivery

The unit of delivery is the **plugin version directory**, not a single file: the contract requires `plugins/{group}/{name}/{version}/plugin` as the entrypoint and allows sidecars next to it (jars, shared libraries, scripts). It is packed as a `tar.gz` and stored at `{group}/{name}/{version}/plugin.tgz`.

```
build machine / CI                       service
──────────────────                       ───────
plugins build   → plugins/{g}/{n}/{v}/…
plugins push    → s3://…/plugin.tgz
plugins register ──── CreatePlugin ────→ streams the archive from S3,
                      (metadata only)    computes sha256, stores it in the DB

                      GenerateCode ────→ entrypoint missing locally?
                                         download archive → verify sha256
                                         → unpack → execute
```

Key properties:

- **Push before register.** Registering a plugin whose archive is absent fails with `FAILED_PRECONDITION` — a registered plugin always has its artifact.
- **The service computes the checksum**, reading the object itself, so a client cannot register a bogus hash. It is re-verified after every download, before anything is executed.
- **Credentials split:** the build pipeline needs S3 write access; the service only needs read (plus delete for `DeletePlugin`). Clients of the gRPC API need no S3 access at all.
- **Concurrent misses collapse** into a single download (singleflight); `plugins_dir` acts as a local cache.
- With S3 disabled, nothing changes from the classic flow: artifacts are read straight from `plugins_dir`.

#### Pushing a packed tree

`plugins pack --out <dir>` writes the same `{group}/{name}/{version}/plugin.tgz` layout to disk, which `plugins push --packed <dir>` uploads as it is. Packing on the build machine and uploading later — or from elsewhere — is then two commands instead of one repeated:

```bash
# Build machine: pack once.
easyp-svc plugins pack plugins --out plugin-archives

# Anywhere with the archives and S3 credentials.
export AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=…
easyp-svc plugins push plugin-archives --packed \
  --endpoint https://storage.example.com --bucket easyp-plugins --force-path-style \
  --parallel 24
```

Uploads run `--parallel` at a time (8 by default). Object storage commonly rate-limits a single connection far below the link it arrives on, so throughput comes from streams rather than from any one of them: measure one stream, then set `--parallel` to about the ratio between your uplink and that figure. An interrupted run is resumed by re-running it — archives already in storage are skipped without being re-read.

The S3 settings can also come from a config file: `plugins push --cfg` accepts a **full server configuration** and runs it through the same validation as `service start` — a fragment holding only `registry.s3` is refused. That is deliberate: a config the server would reject must not quietly keep working for push, or the two stop agreeing about which store they talk to. To push without a server config, pass the storage settings as flags, as above.

## Project Structure

```
.
├── api/                                 # API contracts (protobuf)
│   └── generator/v1/                   # Main code generation API
│       ├── generator.proto
│       ├── generator.pb.go
│       ├── generator_grpc.pb.go
│       └── generator.mcp.go
├── cmd/
│   ├── main.go                         # Server entry point
│   └── mcp-smoke/main.go              # MCP smoke test client
├── internal/                           # Internal logic
│   ├── adapters/                       # External system adapters
│   │   ├── audit/                     # Async audit log writer
│   │   ├── metrics/                   # Prometheus metrics collection
│   │   └── registry/                  # DB + binary execution
│   ├── api/                           # Transport layer (gRPC + MCP)
│   ├── core/                          # Business logic + domain types
│   ├── database/                      # DB abstraction (sqlx wrapper)
│   ├── grpchelper/                    # gRPC server/client factories
│   ├── license/                       # PASETO v4 licensing
│   ├── ratelimiter/                   # Per-IP rate limiting
│   ├── telemetry/                     # OpenTelemetry + tracing decorators
│   ├── monitor/                       # Context-aware logging
│   └── flags/                         # CLI flag processing
├── sdk/                               # Go client SDK
├── migrate/                           # SQL migrations
├── registry/                          # Plugin Dockerfiles (for building)
│   ├── protocolbuffers/go/v1.36.10/
│   ├── grpc/go/v1.5.1/
│   ├── grpc-ecosystem/gateway/v2.27.3/
│   └── grpc-ecosystem/openapiv2/v2.27.3/
├── plugins/                           # Built plugin binaries (gitignored)
├── deploy/                            # Everything that runs the service somewhere
│   ├── docker-compose.yml            # Full dev stack
│   ├── docker-compose.dev.yml        # Community and enterprise side by side
│   ├── .env.example                  # Template for the full stack
│   ├── .env.dev.example              # Template for the two-tier stack
│   ├── config/                       # Service configs, one per way of running it
│   ├── observability/                # Alloy, Grafana, Loki, Tempo, Mimir, Pyroscope, Traefik
│   ├── charts/easyp-service/         # Helm chart
│   ├── scripts/                      # gen-dev-certs.sh
│   └── certs/                        # Throwaway dev TLS material (gitignored)
├── easyp.yaml                       # Protobuf lint + generation config
├── easyp.local.yaml                 # Local easyp config
└── Taskfile.yml                     # Task automation
```

## Quick Start

### Prerequisites

- Docker and docker-compose
- [Task](https://taskfile.dev/) (optional, but recommended)
- Go 1.26+ (for development)
- [grpcurl](https://github.com/fullstorydev/grpcurl) (for plugin registration)

### Running with Full Stack

```bash
# Build plugin binaries from Dockerfiles
task build-plugins

# Start all services
task up

# Wait for service to be ready, then register plugins
task register-plugins

# Or do it all at once:
task run
```

### Minimal Local Run

For local `easyp generate`, gRPC testing and MCP smoke you do not need the full observability stack.

```bash
# 1. Build plugin binaries
task build-plugins

# 2. Start only postgres
# If port 5432 is already occupied, the task uses 5433 by default.
task up-minimal

# 3. In a separate terminal run the service from source
# config.local.yml is tuned for this mode.
task run-local

# 4. Register plugins
task register-plugins

# 5. Generate code
easyp --cfg easyp.local.yaml generate

# 6. Optional MCP smoke check
go run ./cmd/mcp-smoke --endpoint http://localhost:8083/mcp
```

### One-Command Setup

```bash
# Build plugins, start stack, register — no log tailing
task setup
```

### Health Check

```bash
# Health check
curl http://localhost:8082/health

# Metrics
curl http://localhost:8081/metrics

# MCP (streamable HTTP transport)
curl -i http://localhost:8083/mcp

# Grafana (admin/admin)
open http://localhost:3000
```

## API

### Generator API (Primary)

**Endpoint:** `easyp.api.localhost:4443` (gRPC over TLS, through traefik) in the
compose stack; `localhost:8080` (plaintext) when the service runs from source
with `deploy/config/config.local.yml`. See [Transport security](#transport-security).

```protobuf
service ServiceAPI {
  rpc GenerateCode(GenerateCodeRequest) returns (GenerateCodeResponse);
  rpc Plugins(PluginsRequest) returns (PluginsResponse);
  rpc CreatePlugin(CreatePluginRequest) returns (CreatePluginResponse);
  rpc UpdatePlugin(UpdatePluginRequest) returns (UpdatePluginResponse);
  rpc DeletePlugin(DeletePluginRequest) returns (DeletePluginResponse);
}

message GenerateCodeRequest {
  google.protobuf.compiler.CodeGeneratorRequest code_generator_request = 1;
  string plugin_name = 2;  // Format: "group/name:version"
}

message GenerateCodeResponse {
  google.protobuf.compiler.CodeGeneratorResponse code_generator_response = 1;
}
```

### MCP API (HTTP Transport)

**Endpoint:** `http://localhost:8083/mcp` (streamable MCP over HTTP)

**Opt-in.** The listener is off unless `mcp.enabled: true` (env `MCP_ENABLED`)
is set: it serves plain HTTP outside the gRPC interceptor chain — no TLS, no
rate limit, no audit — so a deployment decides whether that surface exists.
It is read-only and exposes nothing the anonymous gRPC reads do not.
easyp-tech's own configs under `deploy/config/` enable it; the Helm chart
ships it off (`mcp.enabled` value).

Implemented tools:
- `plugins_list` — list available plugins with optional filters: `group`, `name`, `version`, `tags`, paginated (`pageSize`/`pageToken`)
- `easyp_config_describe` — return structured `easyp.yaml` schema/docs/examples for full config or selected `path`

Testing MCP:
- Handler tests: `go test ./internal/api -count=1` (`task test-mcp`)
- Live smoke check against running endpoint: `go run ./cmd/mcp-smoke --endpoint http://localhost:8083/mcp` (`task smoke-mcp`)

## Plugin Naming Format

Plugins are identified in the format: `{group}/{name}:{version}`

### Examples:
- `protocolbuffers/go:v1.36.10` - Go protobuf plugin
- `grpc/go:v1.5.1` - Go gRPC plugin  
- `grpc-ecosystem/gateway:v2.27.3` - gRPC Gateway
- `grpc-ecosystem/openapiv2:v2.27.3` - OpenAPI v2 generator
- `protocolbuffers/go:latest` - Latest version of Go plugin

### Plugin Groups:
- `protocolbuffers` - Core protobuf plugins
- `grpc` - gRPC plugins 
- `grpc-ecosystem` - gRPC ecosystem plugins
- `community` - Community plugins

## Configuration

### Environment Variables

Settings resolve in one order, whether the service was started with `--cfg` or
without it:

1. the YAML file, if one was given;
2. the environment, which **overrides** the file;
3. the `default=` on the field, which fills only what neither supplied.

A variable that is set but empty counts as not set, so the `"${VAR:-}"` form used
throughout `deploy/` leaves the file's value alone when the variable is not
exported. This is what lets a secret — `DB_POSTGRES_DSN`, `AUTH_WRITE_TOKENS`,
`LICENSE_KEY`, `REGISTRY_S3_SECRET_ACCESS_KEY` — stay out of a committed config.

One name does not follow the field it sets: `db.postgres` is `DB_POSTGRES_DSN`.
Everything else is the dotted key upper-cased, with `.` becoming `_`.

`telemetry.otlp_endpoint` also accepts the standard `OTEL_EXPORTER_OTLP_ENDPOINT`
when `TELEMETRY_OTLP_ENDPOINT` is unset; `config print --origin` names whichever
one supplied the value.

An unrecognised key is an error, not a warning: the service refuses to start and
`config validate` exits non-zero. It used to be a warning next to a successful
start, which made "I configured this" and "I mistyped this" produce the same
running service. The same goes for a key copied from the chart's `values.yaml` —
those are camelCase and this file is snake_case, and the error says which key was
meant.

An unrecognised **environment variable** carrying a section prefix is reported as
a warning, and named on startup.

```bash
# The variables an operator actually sets. For the rest — every setting has one —
# ask the binary: `easyp-svc config print --origin`.
LOG_LEVEL=info

DB_POSTGRES_DSN="postgres://user:pass@localhost/db"

# Credentials for the mutating RPCs: "<name>=<64 hex>,<name>=<64 hex>".
# Generate with `easyp-svc auth new-token`.
AUTH_WRITE_TOKENS="ci=<64 hex>"

# Licence. Absent means community mode.
LICENSE_KEY=
LICENSE_PUBLIC_KEYS="<kid>:<64 hex>"

# Object storage; enabled by the bucket being set.
REGISTRY_S3_ENDPOINT="http://rustfs:9000"
REGISTRY_S3_BUCKET="easyp-plugins"
REGISTRY_S3_ACCESS_KEY_ID="rustfsadmin"
REGISTRY_S3_SECRET_ACCESS_KEY="rustfsadmin"

# Telemetry; empty means no exporter is built.
TELEMETRY_OTLP_ENDPOINT="easyp-alloy:4317"
```

### Checking a configuration

Two commands answer the two questions a config file raises, and neither needs the
service to be running:

```bash
# Would the service start on this? Exits non-zero if not.
easyp-svc config validate --cfg deploy/config/config.yml

# What will actually apply, and which layer supplied each value?
easyp-svc config print --cfg deploy/config/config.yml --origin

# What does this deployment change from the built-in defaults? The output is
# exactly what the file needs to contain.
easyp-svc config print --cfg deploy/config/config.yml --origin --changed
```

Secrets print as `***` unless `--show-secrets` is given. With no `--cfg`, both
commands read the environment alone, which is the shape a Helm deployment had
before the chart began rendering a file.

The service prints the same summary itself, at `info`, on every start — so the
question is answerable inside a container where these commands are not to hand.

### Configuration Files

| File | Purpose |
|------|---------|
| `deploy/config/config.yml` | Docker-compose service config (internal hostnames) |
| `deploy/config/config.local.yml` | Local development config (localhost, port 5433) |
| `deploy/config/config.community.dev.yml` | Two-tier dev stack, unlicensed container |
| `deploy/config/config.enterprise.dev.yml` | Two-tier dev stack, licensed container |

The two tier configs ship no write tokens and no telemetry endpoints on purpose:
that stack is what `deploy/docker-compose.public.yml` puts on the internet, and a
committed credential is a published one. Supply them through `deploy/.env.dev`.

```yaml
server:
  host: "0.0.0.0"
  port:
    grpc: 8080
    metric: 8081
    health: 8082
    mcp: 8083
db:
  postgres: "postgres://easyp_svc:easyp_pass@localhost:5433/easyp_db?sslmode=disable"
registry:
  plugins_dir: "./plugins"
  max_output_size: 67108864
  # Optional S3-compatible binary storage. When enabled, plugin archives
  # pushed by `easyp-svc plugins push` are lazily downloaded into plugins_dir
  # (acting as a local cache) and sha256-verified before unpacking.
  s3:
    endpoint: "http://localhost:9000"
    bucket: "easyp-plugins"
    region: "us-east-1"
    access_key_id: "rustfsadmin"
    secret_access_key: "rustfsadmin"
    force_path_style: true
worker_pool:
  workers: 4
  queue_size: 16
  generation_timeout: 120s
  max_retries: 3
  shutdown_timeout: 30s
rate_limit:
  requests_per_second: 10.0
  burst: 20
  cleanup_interval: 10m
```

### Transport security

The gRPC listener is configured by `server.tls`:

```yaml
server:
  tls:
    cert_file: "/certs/server.crt"
    key_file: "/certs/server.key"
    # Present ⇒ mutual TLS: only certificates signed by this CA are accepted.
    client_ca_file: "/certs/ca.crt"
```

Leaving `cert_file` empty serves plaintext; the service logs a warning on every
start so that never happens unnoticed. `cert_file` and `key_file` must be set
together, and `client_ca_file` alone is rejected at startup.

In the compose stack traefik is the only client holding a certificate, and the
gRPC port is not published to the host — the way in is `easyp.api.localhost` on
`EASYP_TRAEFIK_TLS_PORT` (4443 by default), where traefik terminates the edge
certificate and re-establishes mutual TLS toward the service.

```bash
# Generate a development CA plus the server, client and edge certificates.
# `task up` runs this for you; FORCE=1 regenerates.
task certs

# Talk to the service through traefik
easyp-svc plugins register plugins \
  --addr easyp.api.localhost:4443 --tls-ca deploy/certs/ca.crt --cfg deploy/config/config.yml
```

Client-side flags: `--tls-ca` overrides the trust store, `--tls-cert`/`--tls-key`
supply a client certificate for a server that enforces mTLS, and `--insecure`
is the explicit opt-out used against a plaintext local service. TLS is the
default — plaintext is never reached by omitting a flag.

`certs/` is gitignored and holds development material only. In production the
paths point at certificates issued by your own CA.

### Authentication

Reads are anonymous. The three mutating methods — `CreatePlugin`,
`UpdatePlugin`, `DeletePlugin` — require a write token:

```bash
# Generates the token and prints the config entry that authorises it
easyp-svc auth new-token --name ci
```

The command prints the token once and a `sha256` digest. Only the digest goes
into the configuration, so `deploy/config/config.yml` stays safe to commit; the token belongs
in your secret manager:

```yaml
auth:
  write_tokens:
    - name: "ci"
      sha256: "…"
```

Clients pass it with `--token`, via `EASYP_TOKEN`, or `sdk.WithToken(...)`. It
travels in the `authorization` header, so it is only as protected as the
connection — use it over TLS.

Two properties worth knowing:

- **An empty token list denies every write.** A forgotten configuration breaks
  plugin registration rather than leaving the registry open.
- **Any method not explicitly anonymous requires a token.** A new RPC is
  protected until someone decides otherwise.

The token's name appears in the audit log, so `SELECT metadata FROM audit_log`
shows which credential performed an operation. Multiple tokens let you rotate
without downtime: add the new one, deploy, remove the old.

### Licensing

Without a token the service runs in **community** mode: no audit log, at most 4
workers and 10 registered plugins. Enterprise needs two things — a token and the
public key it is verified against:

```bash
LICENSE_PUBLIC_KEYS=<kid>:<hex> LICENSE_KEY=<paseto-token> task up
```

Both are read at runtime. The token comes from `license.key`, then
`license.file`, then `LICENSE_KEY`; the public keys from `license.public_keys`
or `LICENSE_PUBLIC_KEYS`. Without a public key no token is honoured.

`license.public_keys` is keyed by the key id in the token's footer. The reserved
key id `"*"` verifies any token the other entries do not cover, which is what the
removed `license.public_key` setting used to do.

This service only verifies licences; it does not issue them. Issuing lives in the
licence registry (`easyp-tech/licenses`), which holds the private signing key,
the record of who was given what, and the `easyp-license` tool that signs.

Several keys can be configured at once, keyed by the key id in the token footer:
`LICENSE_PUBLIC_KEYS="2026-08:<hex>,2026-09:<hex>"`. That is what lets a signing
key be rotated without every deployment having to change key on the same day.
A key that is not a valid hex Ed25519 key stops startup rather than quietly
dropping the service to community mode.

Because the verification key is configuration, whoever can edit `deploy/config/config.yml` can
point the service at a different signing authority — protect that file the way
you protect the database password next to it.

Verification is offline and happens in-process: the PASETO v4.public signature
is checked against the configured public key, and a token that fails — expired,
signed by an unknown key, or malformed — leaves the deployment in community
mode rather than stopping it.

## Contributing Plugins

We welcome contributions of new plugins! Here's how to add your plugin to the registry:

### 1. Create Plugin Structure

```bash
# Create plugin directory structure
mkdir -p registry/{group}/{plugin-name}/{version}
cd registry/{group}/{plugin-name}/{version}
```

### 2. Create Dockerfile

Your plugin must be packaged as a Dockerfile that produces a static binary:
- Reads protobuf `CodeGeneratorRequest` from stdin
- Writes protobuf `CodeGeneratorResponse` to stdout
- Final stage should output the binary for extraction

#### Example: Go-based Plugin

```dockerfile
FROM --platform=$BUILDPLATFORM golang:1.25-alpine3.22 AS build

ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

# Install upx for binary compression (optional but recommended)
RUN apk add upx=5.0.2-r0 --no-cache

# Install your protoc plugin
RUN --mount=type=cache,target=/go/pkg/mod \
    go install -ldflags "-s -w" -trimpath example.com/protoc-gen-yourplugin@v1.0.0 \
 && mv /go/bin/${GOOS}_${GOARCH}/protoc-gen-yourplugin /go/bin/protoc-gen-yourplugin || true \
 && upx --best --lzma /go/bin/protoc-gen-yourplugin

FROM scratch

COPY --from=build --link /go/bin/protoc-gen-yourplugin /plugin

ENTRYPOINT ["/plugin"]
```

### 3. Build and Test

```bash
# Build plugin binary
task build-plugins

# Start service
task up-minimal && task run-local

# Register plugin
task register-plugins

# Test with easyp generate
easyp --cfg easyp.local.yaml generate
```

### 4. Submit Pull Request

```bash
git add registry/{group}/{plugin-name}/
git commit -m "Add {group}/{plugin-name}:{version} plugin"
```

### Plugin Requirements

**Build:**
- ✅ Multi-stage Dockerfile (build → scratch or minimal)
- ✅ Static binary (CGO_ENABLED=0)
- ✅ UPX compression (recommended)
- ✅ Supports standard protoc plugin protocol
- ✅ Reads from stdin, writes to stdout
- ✅ Returns proper exit codes

**Performance:**
- ✅ Fast startup (< 5 seconds)
- ✅ Small binary size
- ✅ Efficient memory usage

## Development

### Building Service

```bash
# Local build
go build -o bin/easyp-svc ./cmd/easyp-svc

# Run
./bin/easyp-svc service start --cfg deploy/config/config.local.yml

# The level is a setting, so it needs no flag; --log_level still overrides it.
LOG_LEVEL=debug ./bin/easyp-svc service start --cfg deploy/config/config.local.yml
```

### Generating Protobuf Code

```bash
# Generate from proto files (requires running service)
easyp --cfg easyp.yaml generate

# Or with local config
easyp --cfg easyp.local.yaml generate
```

## Monitoring

### Available Services

| Service | URL | Description |
|---------|-----|-------------|
| Grafana | http://localhost:3000 | Dashboards (admin/admin) |
| Health | http://localhost:8082 | Health checks |
| Metrics | http://localhost:8081 | Prometheus metrics |
| MCP | http://localhost:8083/mcp | MCP streamable HTTP endpoint |

### Key Metrics

- `grpc_server_handled_total` - gRPC request count
- `pool_active_workers` - Active worker goroutines
- `pool_queue_depth` - Jobs waiting in queue
- `pool_rejected_total` - Jobs rejected (overloaded)
- `pool_jobs_total` - Total jobs processed
- `panics_total` - Recovered panics

## Client Usage

### Go SDK

```go
import "github.com/easyp-tech/service/sdk"

// Create client. The SDK defaults to TLS with the system trust store; add
// sdk.WithTransportCredentials for a private CA, or sdk.WithInsecure() when
// talking to a plaintext local service.
client, err := sdk.New(
    "localhost:8080",
    sdk.WithInsecure(),
    sdk.WithRetry(3, time.Second),
    sdk.WithHealthCheck(true),
)

// Generate code
response, err := client.GenerateCode(ctx, &generator.GenerateCodeRequest{
    CodeGeneratorRequest: codeGenRequest,
    PluginName:          "protocolbuffers/go:v1.36.10",
})
```

### CLI Usage with easyp

```yaml
# easyp.yaml
generate:
  plugins:
    - remote: "localhost:8080/protocolbuffers/go:latest"
      out: .
      opts:
        paths: source_relative
    - remote: "localhost:8080/grpc/go:v1.5.1"  
      out: .
      opts:
        paths: source_relative
```

### MCP Client Configuration

```json
{
  "mcpServers": {
    "easyp": {
      "url": "http://localhost:8083/mcp"
    }
  }
}
```

## Management Commands

```bash
# Build plugin binaries
task build-plugins

# Start infrastructure
task up

# Upload plugin archives to S3 storage
task push-plugins

# Upload an already packed archive tree to a remote store
S3_ENDPOINT=https://storage.example.com AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=… task push-archives

# Register plugins
task register-plugins

# Full cycle
task run

# Stop with cleanup
task down

# Run from source
task run-local
```

## Troubleshooting

### Service Issues

```bash
# Check running containers  
docker ps

# Service logs
docker compose logs service

# Restart with rebuild
task down && task up
```

### Plugin Issues

```bash
# Check built plugins
ls -la plugins/

# Rebuild plugins
task build-plugins
# or directly, with a filter:
# go run ./cmd/easyp-svc/ plugins build registry --filter 'protocolbuffers/*'

# Re-register plugins
task register-plugins

# Check registered plugins via grpcurl. The server does not serve reflection, so
# the schema comes from a descriptor set. Generate it once:
easyp-svc api descriptor -o api.protoset

# Compose stack (TLS through traefik):
grpcurl -protoset api.protoset -cacert deploy/certs/ca.crt \
  easyp.api.localhost:4443 api.generator.v1.ServiceAPI/Plugins
# Service run from source with config.local.yml (plaintext):
grpcurl -protoset api.protoset -plaintext \
  localhost:8080 api.generator.v1.ServiceAPI/Plugins
```

### Database Issues

```bash
# Connect to PostgreSQL
docker exec -it easyp-postgres psql -U easyp_svc -d easyp_db

# Check plugins in database
SELECT * FROM plugins;

# Check schema
\d plugins
```

## Available Plugins

### Core Plugins
- `protocolbuffers/go:v1.36.10` - Go Protocol Buffers compiler
- `grpc/go:v1.5.1` - Go gRPC compiler

### Ecosystem Plugins  
- `grpc-ecosystem/gateway:v2.27.3` - gRPC-Gateway HTTP transcoding
- `grpc-ecosystem/openapiv2:v2.27.3` - OpenAPI v2 documentation generator

## License

EasyP Service is **source available**, not open source.

| Part | License |
|------|---------|
| `api/` — the generated gRPC contract | [Apache License 2.0](api/LICENSE) |
| `sdk/` — the Go client library | [Apache License 2.0](sdk/LICENSE) |
| Everything else — the service itself | [Elastic License 2.0](LICENSE) |

The Elastic License 2.0 lets you use, copy, modify and redistribute the service
free of charge, production included. It forbids three things: offering the
service to third parties as a hosted or managed service, circumventing the
license key mechanism that gates Enterprise features (see [Licensing](#licensing)),
and removing license notices.

Community mode needs no license key and stays free under those terms. What
Enterprise adds today is the audit log and the removal of the community limits
(4 workers, 10 registered plugins).

The client SDK and the API contract it is generated from are both Apache 2.0, so
they can be imported into your own code without inheriting any of the above. The
two go together deliberately: `sdk/` imports `api/`, and licensing only the
client would leave anyone writing one compiling Elastic-licensed code anyway.
Talking to this service is not restricted; running it is.

Releases up to and including `v0.8.0` were published under Apache 2.0 and remain
available under those terms; the Elastic License 2.0 applies from the next
release onward.

## Support

For questions and suggestions, please create Issues in the repository.
