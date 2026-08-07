<!-- generated: 2026-05-24, template: core.md -->
# Packages

Reference of all project packages grouped by architectural layer.

## 1. Application Layer

### `internal/core`
**Domain types, interfaces, business logic, and bounded concurrency.**

| File | Description |
|------|-------------|
| `domain.go` | All domain types, interfaces, sentinel errors — single source of truth |
| `core.go` | `Core` struct: Generate, ListPlugins, CRUD operations + audit + feature gate checks |
| `pool.go` | `WorkerPool`: bounded concurrency for plugin execution, implements `Registry` |
| `context.go` | `CallerIPFromContext()` / `WithCallerIP()` context helpers |

- Implements: `Service` interface
- Key pattern: thin orchestration — delegates to `Registry`, records `Metrics`, sends `AuditEntry`

## 2. Adapters Layer

### `internal/adapters/registry`
**Plugin storage (PostgreSQL) and local binary execution.**

| File | Description |
|------|-------------|
| `registry.go` | `Registry` struct: Get (DB lookup + binary exec), List, Create, Update, Delete |

- Implements: `core.Registry`
- Uses: `database.SQL`, local binary execution from `plugins/` directory
- Plugin execution: stdin (CodeGeneratorRequest) → binary → stdout (CodeGeneratorResponse)

### `internal/adapters/audit`
**Asynchronous audit log writer with channel-based buffering.**

| File | Description |
|------|-------------|
| `audit.go` | `Store` struct implementing `core.AuditLog` (DB persistence) |
| `worker.go` | `Worker`: background goroutine consuming from audit channel → DB |

- Implements: `core.AuditLog`
- Channel capacity: 1000 entries (configurable)
- Graceful shutdown with lost event count reporting

### `internal/adapters/metrics`
**Prometheus metric collectors.**

| File | Description |
|------|-------------|
| `metrics.go` | `Adapter` implementing `core.Metrics` (generation counters/histograms) |
| `db_collector.go` | `DBCollector`: PostgreSQL connection pool stats |
| `business_collector.go` | `BusinessMetricsCollector`: plugin count, active connections |

- Implements: `core.Metrics`

## 3. API Layer

### `internal/api`
**gRPC handler and HTTP MCP handler.**

| File | Description |
|------|-------------|
| `api.go` | `API` struct implementing `generator.ServiceAPIServer`; `ErrorToStatus()` error mapping |
| `license_interceptor.go` | gRPC unary+stream interceptor for license feature checks |
| `mcp.go` | MCP HTTP handler factory (`newMCPHandler`) |
| `mcp_tools.go` | MCP tool definitions: `plugins_list`, `easyp_config_describe` |

### `api/generator/v1/` (generated)
**Protobuf API contract and generated code.**

| File | Description |
|------|-------------|
| `generator.proto` | Service definition: GenerateCode, Plugins, CRUD RPCs |
| `generator.pb.go` | Generated protobuf types |
| `generator_grpc.pb.go` | Generated gRPC client/server stubs |
| `generator.mcp.go` | Generated MCP tool bindings (via `protoc-gen-mcp`) |

## 4. Shared / Internal Packages

### `internal/database`
**Database abstraction layer with metrics and tracing.**

| File | Description |
|------|-------------|
| `sql.go` | `SQL` wrapper around `sqlx.DB` with automatic metrics/tracing |
| `metrics.go` | DAL-level metrics generation |
| `connectors/` | Connection string providers (`Raw`, etc.) |
| `migrations/` | SQL migration engine (parse, run up/down) |

### `internal/grpchelper`
**gRPC server and client factories with middleware.**

| File | Description |
|------|-------------|
| `server.go` | `NewServer()`: constructs gRPC server with full interceptor chain |
| `client.go` | `NewClient()`: constructs gRPC client with interceptors |
| `metrics.go` | `ServerMetrics`: Prometheus metrics for gRPC |

Interceptor chain order: trace_logging → realip → prometheus → structured_logging → panic_recovery → validation → error_code_conversion → rate_limit → license → audit

### `internal/license`
**PASETO v4 license management and feature gating.**

| File | Description |
|------|-------------|
| `manager.go` | `Manager`: license refresh, caching, Prometheus metrics |
| `features.go` | `featureGate` implementing `core.FeatureGate` |
| `gate.go` | `FeatureGate` constructor and interface adapter |
| `claims.go` | PASETO token parsing and validation |
| `paseto_client.go` | `PasetoLicenseClient`: offline PASETO v4.public verification |
| `errors.go` | License-specific error types |
| `metrics.go` | License metrics (cache hits/misses, tier gauge) |

### `internal/ratelimiter`
**Per-IP token bucket rate limiter with FeatureGate integration.**

| File | Description |
|------|-------------|
| `ratelimiter.go` | `RateLimiter` with per-IP buckets, cleanup goroutine, key extraction |

- Integrates with `FeatureGate` for tier-based rate limits
- Implements `ratelimit.Limiter` from grpc-middleware

### `internal/telemetry`
**OpenTelemetry + Pyroscope initialization and tracing decorators.**

| File | Description |
|------|-------------|
| `telemetry.go` | `Init()`: OTLP tracer/meter providers + Pyroscope profiler |
| `config.go` | `Config` struct |
| `trace_handler.go` | slog handler that injects trace/span IDs into log records |
| `tracing_core.go` | `TracingCore`: decorator for `core.Service` |
| `tracing_registry.go` | `TracingRegistry`: decorator for `core.Registry` |
| `tracing_plugin.go` | `TracingPlugin`: decorator for `core.Plugin` |

### `internal/monitor`
**Context-aware structured logging.**

| File | Description |
|------|-------------|
| `log.go` | `FromContext()` / `WithContext()` for `*slog.Logger` |

### `internal/flags`
**CLI flag types.**

| File | Description |
|------|-------------|
| `file.go` | `File` flag type with max size validation |
| `level.go` | `Level` flag type for `slog.Level` parsing |

## 5. Client SDK

### `sdk/`
**Go client SDK for the EasyP gRPC API.**

| File | Description |
|------|-------------|
| `client.go` | `Client` with functional options (`WithXxx()`) |
| `config.go` | Client configuration types |
| `doc.go` | Package documentation |
| `filter.go` | Client-side plugin filtering |
| `health.go` | Health check client |
| `interceptors.go` | Client-side gRPC interceptors |
| `retry.go` | Retry logic with backoff |

## 6. Support Directories

| Directory | Description |
|-----------|-------------|
| `migrate/` | Numbered SQL migration files (`1.init.sql` through `5.disk_plugin_config.sql`) |
| `registry/` | Plugin Dockerfiles for building binaries (protocolbuffers, grpc, grpc-ecosystem) |
| `plugins/` | Built plugin binaries (gitignored, generated by `easyp-svc plugins build`) |
| `deploy/observability/` | Observability stack configs (Alloy, Grafana, Loki, Tempo, Mimir, Pyroscope, Traefik) |

## 7. CLI Commands

| Command | Description |
|--------|-------------|
| `easyp-svc plugins build <registry-path>` | Build static plugin binaries from `registry/` Dockerfiles via `docker build --output` |
| `easyp-svc plugins migrate <path>` | Register all built plugins via gRPC `CreatePlugin` API |
