<!-- generated: 2026-05-24, template: security.md -->
# Security

Security considerations for EasyP Service.

## Access to the API

Reads are anonymous; the three mutating RPCs require a write token. Rationale for
the shape this takes:

- **Allow-list, not deny-list.** Only `GenerateCode`, `Plugins` and health are
  named as anonymous. An RPC added to the proto is protected until someone
  changes that, so forgetting to update the list makes a method unavailable
  rather than exposing it.
- **Digests in configuration, tokens outside it.** `auth.write_tokens` stores
  sha256 digests only, so leaking the config file does not leak access. Plain
  sha256 with no work factor is adequate because the token is 32 random bytes,
  not a password.
- **Named tokens.** Rotation without downtime, and the audit log records which
  credential acted (`AuditEntry.Metadata["actor"]`).
- **Fail closed.** An empty token list denies every write. A forgotten
  configuration breaks plugin registration instead of leaving the registry open.
- **Uninformative rejections.** Failures return `Unauthenticated` without saying
  whether the token was missing or wrong; the distinction is logged and counted
  in `easyp_auth_failures_total{reason}` instead.
- **Tokens require TLS.** The credential travels in the `authorization` header
  and is only as protected as the connection. `config.local.yml` disables TLS and
  ships a publicly known throwaway token — local use only.
- **No server reflection.** `grpc.reflection.v1.ServerReflection` is not
  registered, so the method and message inventory cannot be read off the
  network. Clients use the generated stubs; ad-hoc tooling gets the schema from
  `easyp-svc api descriptor -o api.protoset` and passes `grpcurl -protoset`.

### Remaining unauthenticated surface

- `GenerateCode` is anonymous by necessity: it is the product. Anyone reaching
  the service can consume workers. The per-IP rate limiter (10 rps, burst 20)
  bounds accidental floods but is not a quota and not an identity.
- The MCP endpoint on :8083 is plaintext HTTP with no authentication. It is
  read-only — `internal/api/mcp_tools.go` exposes only `Plugins` — but it is a
  surface, and it is not covered by the gRPC interceptor chain.

Identity beyond a shared token is deliberately out of scope; see
[features/auth-roadmap.md](features/auth-roadmap.md).

## Plugin Isolation

Plugin binaries are executed as local processes with the following characteristics:

| Aspect | Detail |
|--------|--------|
| Execution | Local binary execution via stdin/stdout |
| Source | Built from multi-stage Dockerfiles in `registry/` |
| Binary type | Static, compressed with `upx` |
| Base image (build) | `golang:alpine` → `scratch` |
| User (build) | `nobody` (non-root) |
| Communication | stdin (CodeGeneratorRequest) → stdout (CodeGeneratorResponse) |
| Timeout | Configurable per-request deadline (`generation_timeout: 120s`) |
| Output limit | `max_output_size: 67108864` (64MB) |

**Note:** Plugin binaries are built from Dockerfiles but executed as local processes, not in Docker containers at runtime. The WorkerPool limits concurrent executions.

## Input Validation

### Plugin Name Format

Strictly validated via regex: `^[a-z][a-z0-9-]*/[a-z][a-z0-9-]*:(v\d+\.\d+\.\d+|latest)$`

### Protobuf Validation

gRPC interceptor validates all incoming protobuf messages before handler execution.

### String Sanitization

All string inputs are `strings.TrimSpace()` cleaned before processing.

## Rate Limiting

Per-IP token bucket rate limiter (`internal/ratelimiter`):

| Parameter | Default |
|-----------|---------|
| Requests per second | 10.0 |
| Burst | 20 |
| Cleanup interval | 10m |

Rate limits are integrated with FeatureGate for tier-based configuration.

## Secrets Management

- **Database credentials** — via environment variable `DB_POSTGRES_DSN` or YAML config
- **License keys** — via PASETO v4 tokens (public-key cryptography), passed via `LICENSE_KEY` env var
- **No secrets in source** — `.env.example` provides template; `.gitignore` excludes `.env`
- **Plugin binaries** — built from auditable Dockerfiles in `registry/`

## Error Information Exposure

- gRPC responses include error messages from domain errors
- Internal stack traces are NOT exposed to clients
- Panic recovery interceptor catches panics and returns `Internal` status

## Audit Trail

All operations are audit-logged asynchronously:
- Caller IP address
- Operation type
- Plugin name
- Success/error status
- Error code and message
- Duration
- Custom metadata

See `BACKGROUND_JOBS.md` for audit worker details.

## WorkerPool Security

The WorkerPool bounds two different resources, and the distinction matters:

- **`workers` (default 4)** limits concurrent plugin *lookups* — a database read
  and, on a cache miss, a download and unpack from object storage. A worker is
  released as soon as the plugin is located.
- **`max_concurrent_generations` (default 16)** limits concurrent plugin
  *processes*. Execution happens on the caller's goroutine, after the worker is
  done, so `workers` never constrained it.

Beyond either limit a request waits up to `queue_size` deep and is then refused
with `ErrServerOverloaded` rather than adding load the host cannot carry.

- **Timeout enforcement** — per-request deadline prevents runaway processes
- **Retry limits** — configurable max retries for transient failures
