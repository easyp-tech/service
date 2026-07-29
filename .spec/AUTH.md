<!-- generated: 2026-05-24, template: auth.md -->
# Auth & Licensing

Authentication and licensing system for EasyP Service.

## Overview

Two separate mechanisms share this document:

- **Authentication** decides whether a caller may mutate the registry. Reads
  (`GenerateCode`, `Plugins`) are anonymous; `CreatePlugin`, `UpdatePlugin` and
  `DeletePlugin` require a write token. See [Write tokens](#write-tokens).
- **Licensing** decides which features are available, via **PASETO v4.public**
  tokens and **license tiers** (community vs enterprise). It is not an access
  control mechanism and never gates the basic lock.

There is no user model: the service has no users, organisations or sessions, and
no tables for them. When identity arrives it comes from a dedicated service and
this one only verifies what it issues — see
[auth-roadmap.md](features/auth-roadmap.md).

## Write tokens

The interceptor requires credentials for every method except an explicit public
list, so an RPC added to the proto is protected until it is deliberately made
anonymous. A missing configuration denies all writes rather than allowing them.

```yaml
auth:
  write_tokens:
    - name: "ci"
      sha256: "…"   # sha256 of the token; the token itself is never stored
```

Generate a pair with `easyp-svc auth new-token --name ci`. The digest is not a
secret and belongs in version control or a ConfigMap; the token belongs in your
secret manager. Clients pass it as `--token`, `EASYP_TOKEN`, or
`sdk.WithToken(...)`, and it travels in the `authorization` header as
`Bearer <token>` — protected only by the connection, so use it over TLS.

Multiple named tokens exist so that rotation needs no downtime (add, deploy,
remove) and so the audit log records *which* credential acted:
`AuditEntry.Metadata["actor"]` carries the token name.

Rejections are `Unauthenticated` and never say whether the token was absent or
merely wrong; the reason is logged and counted in `easyp_auth_failures_total`.

## License Tiers

| Tier | MaxWorkers | MaxPlugins | Features |
|------|-----------|------------|----------|
| Community | 4 | 10 | CodeGeneration, PluginListing, MCPServerTools, RateLimiting, PluginCRUD |
| Enterprise | Configurable | Unlimited (-1) | All community + MultiTenancy, ResponseCaching, Audit |

## Architecture

```
LicenseClient (gRPC)
  → Manager (cache + refresh watcher)
    → FeatureGate (feature checks)
      → Core (business logic)
      → RateLimiter (tier-based rates)
      → LicenseInterceptor (gRPC middleware)
```

## Components

### LicenseClient (`internal/license`)

Interface for communicating with the license server:

```go
type LicenseClient interface {
    ValidateLicense(ctx context.Context) (LicenseClaims, error)
}
```

**Current implementation:** `MockLicenseClient`. It honours the wiring but not the
cryptography:

| Token | Configured public key | Result |
|-------|----------------------|--------|
| absent | any | community |
| present | absent | community (logs a warning) |
| present | present | enterprise, **token accepted unverified** |

The signature is never checked — that is the piece still missing, and the client
logs a warning on every refresh to say so. It will be replaced with a real gRPC
client, or with local PASETO verification against the configured public key,
when the license server is ready.

### Manager (`internal/license`)

- Caches license claims with configurable TTL
- Runs a background refresh watcher (`StartRefreshWatcher`)
- Reports metrics: cache hits/misses, validation duration, active tier

### FeatureGate (`internal/license`)

```go
type FeatureGate interface {
    Enabled(feature Feature) bool
    MaxWorkers() int
    MaxPlugins() int
}
```

Used by:
- `Core.checkFeature()` — before CRUD operations
- `RateLimiter` — for tier-based rate limits
- `WorkerPool` — for worker count limits
- `LicenseInterceptor` — gRPC middleware

### LicenseInterceptor (`internal/api`)

gRPC interceptor that checks feature availability before request processing. Applies to both unary and streaming RPCs.

## Configuration

```yaml
license:
  key: ""          # inline PASETO token; takes priority over file
  file: ""         # path to a file holding the token
  public_key: ""   # hex-encoded Ed25519 verification key
  cache_ttl: 5m    # how long to cache license claims
```

Environment: `LICENSE_KEY`, `LICENSE_FILE`, `LICENSE_PUBLIC_KEY`,
`LICENSE_CACHE_TTL`.

`LICENSE_KEY` and `LICENSE_PUBLIC_KEY` are also consulted directly as a last
resort, because the `--cfg` startup path decodes YAML and skips envconfig
entirely — without that fallback values passed to the container through the
environment would be dropped. Order of precedence:

- token: `license.key` → contents of `license.file` → `LICENSE_KEY`
- key: `license.public_key` → `LICENSE_PUBLIC_KEY`

Both are read at runtime. A deployment with no public key cannot honour any
token and stays in community mode.

> **Threat model.** Because the verification key is configuration rather than a
> property of the build, anyone able to edit `config.yml` or set
> `LICENSE_PUBLIC_KEY` can substitute their own signing authority and issue
> themselves a licence. Protect the config file accordingly.

## PASETO Token Format

Tokens are `v4.public` PASETO tokens containing `LicenseClaims`:
- `tier` — "community" or "enterprise"
- `features` — list of Feature enum values
- `max_workers` — concurrent worker limit
- `max_plugins` — registered plugin limit
