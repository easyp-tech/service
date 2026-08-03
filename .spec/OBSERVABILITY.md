<!-- generated: 2026-05-24, template: infrastructure.md -->
# Observability

OpenTelemetry, Prometheus, and Pyroscope stack for EasyP Service.

## Overview

```
Application
  → OpenTelemetry SDK (traces + metrics)
    → OTLP exporter → Alloy collector
      → Tempo (traces)
      → Mimir (metrics)
  → Prometheus client
    → /metrics endpoint → Alloy scraper
  → Pyroscope SDK
    → Pyroscope server (continuous profiling)
  → slog (structured JSON logs)
    → stdout → Alloy → Loki
```

## Initialization (`internal/telemetry`)

```go
shutdownTelemetry, telLog, err := telemetry.Init(ctx, telCfg, baseHandler)
```

`Init()` sets up:
1. OTLP trace provider (gRPC exporter to `cfg.OTLPEndpoint`)
2. OTLP metric provider (gRPC exporter)
3. Pyroscope profiler (HTTP to `cfg.PyroscopeEndpoint`)
4. Trace-aware slog handler (injects `trace_id`, `span_id` into log records)

Returns a `shutdownTelemetry` function for graceful cleanup.

## Tracing Decorators

Non-invasive tracing via decorator pattern:

| Decorator | Wraps | Package |
|-----------|-------|---------
| `TracingCore` | `core.Service` | `internal/telemetry/tracing_core.go` |
| `TracingRegistry` | `core.Registry` | `internal/telemetry/tracing_registry.go` |
| `TracingPlugin` | `core.Plugin` | `internal/telemetry/tracing_plugin.go` |

Each decorator creates OTel spans with relevant attributes (plugin name, version, etc.) and passes through errors without modification.

## Prometheus Metrics

### gRPC Metrics (`internal/grpchelper`)

Standard grpc-prometheus metrics:
- `grpc_server_handled_total` — RPCs by method and code
- `grpc_server_handling_seconds` — RPC duration histogram
- `grpc_server_msg_received_total` / `grpc_server_msg_sent_total`

### Application Metrics

| Metric | Type | Source |
|--------|------|--------|
| `panics_total` | Counter | `cmd/main.go` (panic recovery) |
| `pool_active_workers` | Gauge | `core/pool.go` |
| `pool_queue_depth` | Gauge | `core/pool.go` |
| `pool_rejected_total` | Counter | `core/pool.go` |
| `pool_jobs_total` | Counter | `core/pool.go` |
| `db_*_connections` | Gauge | `adapters/metrics/db_collector.go` |
| `operations_total` | Counter | `core/core.go` (`sendAudit`) |
| Business metrics (plugin counts) | Gauge | `adapters/metrics/business_collector.go` |
| Audit worker metrics | Counter/Gauge | `adapters/audit/worker.go` |
| `audit_default_partition_used` | Gauge | `adapters/audit/partitions.go` |
| License metrics | Counter/Gauge | `internal/license/manager.go` |

### Endpoint

`GET /metrics` on port 8081 (default 23411).

### What is counted in the process, and what is asked of the database

The split is not stylistic. Every query in `business_collector.go` runs
synchronously on each scrape, so anything there is paid for every 30 seconds,
forever.

**Events are counted in the process.** `easyp_operations_total{operation,status}`
is incremented in `Core.sendAudit`, above the audit gate, so it is recorded in
every tier — a community installation still needs to see its own error rate.
Rates and windows are Prometheus's job: activity over a day is
`increase(easyp_operations_total[24h])`, not a query.

**State is asked of the database.** How many plugins exist is not something a
replica can know: an in-memory gauge starts at zero after a restart and never
learns the truth until someone happens to create a plugin, and with several
replicas each would answer only for itself. These queries stay, and stay cheap —
the table is bounded by the licence.

`easyp_business_audit_log_total` is the planner's row estimate, not a count. It
lags bulk changes until autovacuum runs; it answers whether retention is working,
which tolerates that.

Three metrics were removed from the scrape path because they were events wearing
a gauge's clothing — running totals over the whole audit table. Measured at
sixteen million rows they cost 2.3 seconds per scrape and grew linearly, against
a five-second query timeout: left alone they would eventually have timed out and
blanked the dashboards at the worst possible moment. Indexes do not help, because
an aggregate with no `WHERE` reads every row by definition.

## Logging

- **Format:** JSON (structured)
- **Library:** `log/slog` (stdlib)
- **Handler:** `slog.NewJSONHandler` with `AddSource: true`
- **Trace correlation:** `telemetry.TraceHandler` wraps the JSON handler, injecting `trace_id` and `span_id`
- **Context propagation:** `monitor.FromContext(ctx)` / `monitor.WithContext(ctx, log)`

## Configuration

```yaml
telemetry:
  otlp_endpoint: "localhost:4317"
  pyroscope_endpoint: "http://localhost:4040"
```

## Observability Stack (docker-compose)

| Service | Image | Role |
|---------|-------|------|
| Alloy | `grafana/alloy:v1.9.1` | OpenTelemetry collector (OTLP receiver, log collection, Prometheus remote write) |
| Grafana | `grafana/grafana:12.3.0` | Dashboards |
| Loki | `grafana/loki:3.5.0` | Log storage |
| Tempo | `grafana/tempo:2.7.2` | Trace storage |
| Mimir | `grafana/mimir:2.16.0` | Metrics storage (Prometheus-compatible) |
| Pyroscope | `grafana/pyroscope:1.13.5` | Continuous profiling |
| RustFS | `rustfs/rustfs:latest` | S3-compatible storage backend for Tempo, Mimir, Loki, Pyroscope |
| Traefik | `traefik:v3.6` | Reverse proxy with labels-based routing |

### Traefik Routes

| Host | Target |
|------|--------|
| `easyp.grafana.localhost` | Grafana (port 3000) |
| `easyp.s3.localhost` | RustFS console (port 9001) |

### S3 Buckets (created by init-buckets)

`tempo`, `mimir-blocks`, `mimir-ruler`, `mimir-alertmanager`, `loki-chunks`, `loki-ruler`, `pyroscope`

Configs: `configs/` directory (alloy, grafana, loki, tempo, mimir, pyroscope, traefik)
