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
├── configs/                           # Observability configs
│   ├── alloy/                        # OpenTelemetry collector
│   ├── grafana/                      # Dashboards + datasources
│   ├── loki/                         # Log aggregation
│   ├── tempo/                        # Distributed tracing
│   ├── mimir/                        # Metrics storage
│   ├── pyroscope/                    # Continuous profiling
│   └── traefik/                      # Reverse proxy
├── build-plugins.sh                   # Build plugin binaries from Dockerfiles
├── register-plugins.sh                # Register plugins via gRPC API
├── config.yml                        # Docker-compose service config
├── config.local.yml                  # Local development config
├── docker-compose.yml               # Development infrastructure
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
./register-plugins.sh localhost:8080

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
with `config.local.yml`. See [Transport security](#transport-security).

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

Implemented tools:
- `plugins_list` — list available plugins with optional filters: `group`, `name`, `version`, `tags`
- `easyp_config_describe` — return structured `easyp.yaml` schema/docs/examples for full config or selected `path`

Testing MCP:
- Contract/integration tests (in-process HTTP MCP server): `go test ./internal/mcpserver -run TestMCPServer -count=1`
- Live smoke check against running endpoint: `go run ./cmd/mcp-smoke --endpoint http://localhost:8083/mcp`
- Task shortcuts: `task test-mcp`, `task smoke-mcp`

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

```yaml
# Server
SERVER_HOST=0.0.0.0
SERVER_PORT_GRPC=8080
SERVER_PORT_METRIC=8081  
SERVER_PORT_HEALTH=8082
SERVER_PORT_MCP=8083

# Database
DB_POSTGRES_DSN="postgres://user:pass@localhost/db"
DB_MIGRATE_DIR="migrate"

# Registry
REGISTRY_PLUGINS_DIR="./plugins"
REGISTRY_MAX_OUTPUT_SIZE=67108864

# S3 binary storage (optional; enabled when bucket is set)
REGISTRY_S3_ENDPOINT="http://rustfs:9000"
REGISTRY_S3_BUCKET="easyp-plugins"
REGISTRY_S3_REGION="us-east-1"
REGISTRY_S3_ACCESS_KEY_ID="rustfsadmin"
REGISTRY_S3_SECRET_ACCESS_KEY="rustfsadmin"
REGISTRY_S3_FORCE_PATH_STYLE=true
```

### Configuration Files

| File | Purpose |
|------|---------|
| `config.yml` | Docker-compose service config (internal hostnames) |
| `config.local.yml` | Local development config (localhost, port 5433) |

```yaml
server:
  host: "0.0.0.0"
  port:
    grpc: 8080
    metric: 8081
    health: 8082
    mcp: 8083
db:
  migrate_dir: "migrate"
  driver: "postgres"
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
  --addr easyp.api.localhost:4443 --tls-ca certs/ca.crt --cfg config.yml
```

Client-side flags: `--tls-ca` overrides the trust store, `--tls-cert`/`--tls-key`
supply a client certificate for a server that enforces mTLS, and `--insecure`
is the explicit opt-out used against a plaintext local service. TLS is the
default — plaintext is never reached by omitting a flag.

`certs/` is gitignored and holds development material only. In production the
paths point at certificates issued by your own CA.

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
./register-plugins.sh localhost:8080

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
go build -o bin/server ./cmd/main.go

# Run
./bin/server -cfg config.local.yml -log_level debug
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

# Check registered plugins via grpcurl.
# Compose stack (TLS through traefik):
grpcurl -cacert certs/ca.crt easyp.api.localhost:4443 api.generator.v1.ServiceAPI/Plugins
# Service run from source with config.local.yml (plaintext):
grpcurl -plaintext localhost:8080 api.generator.v1.ServiceAPI/Plugins
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

Apache 2.0. See [LICENSE](LICENSE) for details.

## Support

For questions and suggestions, please create Issues in the repository.
