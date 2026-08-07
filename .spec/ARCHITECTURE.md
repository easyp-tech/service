<!-- generated: 2026-05-24, template: core.md -->
# Architecture

## 1. Overview

EasyP Service is a centralized protobuf/gRPC plugin execution service using a **layered architecture** with decorator pattern for cross-cutting concerns.

```
gRPC Request
  → Interceptor Chain (trace_logging → realip → prometheus → structured_logging
                       → panic_recovery → validation → error_code_conversion
                       → rate_limit → license → audit)
    → API Layer (internal/api)
      → Core Layer (internal/core) — business logic + WorkerPool
        → Tracing Decorator (internal/telemetry)
          → Adapters Layer (internal/adapters/registry)
            → PostgreSQL (plugin metadata) + Local Binary Execution (plugins/)
          ← CodeGeneratorResponse
        ← traced response
      ← domain response
    ← gRPC response
```

## 2. Component Deep Dive

### Transport Layer: `internal/api`

Translates gRPC requests to domain types and maps domain errors to gRPC status codes.

| File | Description |
|------|-------------|
| `api.go` | gRPC handler implementing `generator.ServiceAPIServer`; `ErrorToStatus()` error mapping |
| `license_interceptor.go` | gRPC interceptor that checks license-based feature access |
| `mcp.go` | MCP HTTP handler factory (`newMCPHandler`) |
| `mcp_tools.go` | MCP tool definitions (plugins_list, easyp_config_describe) |

### Business Logic Layer: `internal/core`

Domain types, interfaces, and thin business logic. Delegates heavy work to Registry.

| File | Description |
|------|-------------|
| `domain.go` | All domain types, interfaces, sentinel errors — single source of truth |
| `core.go` | `Core` struct: Generate, ListPlugins, CRUD + audit + feature gate |
| `pool.go` | `WorkerPool`: bounded concurrency for plugin execution (implements `Registry`) |
| `context.go` | Context helpers (CallerIP extraction) |

### Adapters Layer: `internal/adapters`

| Package | Description |
|---------|-------------|
| `adapters/registry` | PostgreSQL plugin metadata + local binary execution from `plugins/` directory |
| `adapters/audit` | Async audit log writer (channel → background worker → DB) |
| `adapters/metrics` | Prometheus collectors: DB pool stats, business metrics |

### Infrastructure Layer

| Package | Description |
|---------|-------------|
| `internal/database` | `database.SQL` wrapper with metrics/tracing; connectors; migration engine |
| `internal/grpchelper` | gRPC server factory with full middleware stack |
| `internal/license` | PASETO v4 license manager, FeatureGate, mock client |
| `internal/ratelimiter` | Per-IP token bucket with FeatureGate integration |
| `internal/telemetry` | OTLP + Pyroscope init; tracing decorators (`TracingCore`, `TracingRegistry`, `TracingPlugin`) |
| `internal/monitor` | Context-aware `slog.Logger` (get/set via context) |
| `internal/flags` | CLI flag types: `File` (with max size), `Level` (slog level) |
| `sdk/` | Go client SDK with retry, health check, filtering, interceptors |

## 3. Directory Structure

```
service/
├── cmd/
│   ├── main.go                        # Entry point: config → telemetry → DB → registry → core → gRPC
│   └── mcp-smoke/main.go             # MCP smoke test client
├── api/generator/v1/                  # Proto contract + generated Go stubs + MCP bindings
│   ├── generator.proto
│   ├── generator.pb.go
│   ├── generator_grpc.pb.go
│   └── generator.mcp.go
├── internal/
│   ├── core/                          # Domain types + business logic
│   │   ├── domain.go                  # Types, interfaces, errors
│   │   ├── core.go                    # Core struct (thin orchestration)
│   │   ├── pool.go                    # WorkerPool (bounded concurrency)
│   │   └── context.go                # CallerIP context helpers
│   ├── api/                           # gRPC handlers + MCP handler
│   │   ├── api.go                     # gRPC handler + ErrorToStatus()
│   │   ├── license_interceptor.go     # License feature check middleware
│   │   ├── mcp.go                     # MCP HTTP handler factory
│   │   └── mcp_tools.go              # MCP tool definitions
│   ├── adapters/                      # Interface implementations
│   │   ├── registry/                  # Plugin DB + local binary execution
│   │   ├── audit/                     # Async audit writer
│   │   └── metrics/                   # Prometheus collectors
│   ├── database/                      # DB abstraction
│   │   ├── sql.go                     # database.SQL wrapper
│   │   ├── connectors/                # Connection string providers
│   │   └── migrations/                # Migration engine
│   ├── grpchelper/                    # gRPC server/client factory
│   ├── license/                       # PASETO v4 licensing
│   ├── ratelimiter/                   # Per-IP rate limiting
│   ├── telemetry/                     # Tracing decorators + OTLP init
│   ├── monitor/                       # Context-aware slog
│   └── flags/                         # CLI flag types
├── sdk/                               # Go client SDK
├── migrate/                           # SQL migrations (numbered)
├── registry/                          # Plugin Dockerfiles (used for building)
├── plugins/                           # Built plugin binaries (gitignored)
├── cmd/easyp-svc/                     # Service + plugins CLI (service start, plugins build, plugins migrate)
├── Taskfile.yml                       # Task runner commands
├── deploy/                            # Compose stacks, service configs, observability configs, Helm chart, cert script
├── easyp.yaml                         # Protobuf lint + code generation config
├── easyp.local.yaml                   # Local easyp config for development
└── .spec/                             # This documentation directory
```

## 4. Key Design Decisions

### 4.1 Decorator Pattern for Tracing

Tracing is never mixed into business logic. Instead, `telemetry.TracingCore`, `TracingRegistry`, and `TracingPlugin` wrap the core interfaces and add OpenTelemetry spans transparently.

```go
// In cmd/main.go:
tracedRegistry := telemetry.NewTracingRegistry(repo)
pool := core.NewWorkerPool(tracedRegistry, ...)
module := core.New(metricsAdapter, pool, gate, auditCh, log)
tracedCore := telemetry.NewTracingCore(module)
```

### 4.2 WorkerPool for Bounded Plugin Execution

Plugin execution is resource-intensive. `WorkerPool` implements `Registry` interface, wrapping the real registry with:
- **Bounded lookups** — `workers` goroutines resolve plugins (DB, plus download and unpack on a miss)
- **Bounded executions** — `max_concurrent_generations` caps how many plugin processes run at once. This is a separate limit because `Generate` runs on the caller's goroutine, not on a worker: the worker is released once the plugin is located
- **Non-blocking backpressure** — returns `ErrServerOverloaded` immediately if queue is full
- **Automatic retries** — transient errors (exit codes 125–127, connection refused) are retried
- **Generation timeout** — per-request deadline for plugin execution

### 4.3 Local Binary Execution

Plugins are built as static binaries from Dockerfiles in `registry/` using multi-stage builds. At runtime, the service executes plugins from the local `plugins/` directory (configured via `registry.plugins_dir`). This avoids the overhead of Docker container creation per request.

**Build flow:** `registry/{group}/{name}/{version}/Dockerfile` → `docker build --output` → `plugins/{group}/{name}/{version}/plugin`

**Registration flow:** `register-plugins.sh` → gRPC `CreatePlugin` API → PostgreSQL

### 4.4 Feature Gate for Two-Tier Licensing

`FeatureGate` enables/disables features based on the current license (community vs enterprise). It's checked in Core business logic and in the rate limiter, without coupling these components to the license implementation.

### 4.5 Interceptor Chain

The gRPC middleware stack is composed via `grpchelper.NewServer`:
```
trace_logging → realip → prometheus → structured_logging → panic_recovery
→ validation → error_code_conversion → rate_limit → license → audit
```
Each interceptor is independent and testable. Order matters: rate limiting before license checking, etc.

### 4.6 Config Priority

CLI flags > environment variables > YAML config file. The `start()` function in `cmd/main.go` tries YAML first (if `-cfg` provided), falls back to `envconfig`.

## 5. Data Flow

### Code Generation Request

```
gRPC Request (CodeGeneratorRequest)
  → api.GenerateCode()
    → core.Generate()
      → getGroup() + getNameAndVersion() — parse plugin name
      → pool.Get() — non-blocking queue submission
        → worker goroutine picks up job
          → tracedRegistry.Get() — DB lookup + load plugin binary
            → registry.Get() — PostgreSQL query + binary path resolution
          ← Plugin instance
        ← poolPlugin (wrapped with timeout + retry)
      → poolPlugin.Generate() — with timeout + retry
        → plugin.Generate() — execute binary: stdin(CodeGeneratorRequest) → stdout(CodeGeneratorResponse)
      ← CodeGeneratorResponse
      → metrics.GenerateCode() — record metrics
      → auditSuccess() — async audit entry via channel
    ← GenerateCodeResponse
  ← gRPC Response
```

### MCP Request

```
HTTP POST /mcp
  → api.MCPHandler()
    → MCP SDK router (streamable HTTP transport)
      → tools: plugins_list → core.ListPlugins()
      → tools: easyp_config_describe → static config schema
    ← MCP tool result (ProtoJSON)
  ← HTTP Response
```
