<!-- generated: 2026-05-24, template: auth.md -->
# Auth & Licensing

Authentication and licensing system for EasyP Service.

## Overview

EasyP uses **PASETO v4.public** tokens for license management. There is no user authentication — the service is designed to run within a trusted network. Access control is based on **license tiers** (community vs enterprise).

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

| Token | Embedded public key | Result |
|-------|--------------------|--------|
| absent | any | community |
| present | absent | community (logs a warning) |
| present | present | enterprise, **token accepted unverified** |

The signature is never checked — that is the piece still missing, and the client
logs a warning on every refresh to say so. It will be replaced with a real gRPC
client, or with local PASETO verification against `license.PublicKey()`, when
the license server is ready.

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
  cache_ttl: 5m    # how long to cache license claims
```

Environment: `LICENSE_KEY`, `LICENSE_FILE`, `LICENSE_CACHE_TTL`.

`LICENSE_KEY` is also consulted directly as a last resort, because the `--cfg`
startup path decodes YAML and skips envconfig entirely — without that fallback a
token passed to the container through the environment would be dropped. Order of
precedence: `license.key` → contents of `license.file` → `LICENSE_KEY`.

### Verification key

The Ed25519 public key is linked into the binary rather than read at runtime, so
a running deployment cannot be pointed at a different signing authority without a
rebuild:

```bash
go build -ldflags "-X github.com/easyp-tech/service/internal/license.publicKeyHex=<hex>"
# or, through the image:
docker build --build-arg LICENSE_PUBLIC_KEY=<hex> .
```

A build with no key cannot honour any token and stays in community mode.

## PASETO Token Format

Tokens are `v4.public` PASETO tokens containing `LicenseClaims`:
- `tier` — "community" or "enterprise"
- `features` — list of Feature enum values
- `max_workers` — concurrent worker limit
- `max_plugins` — registered plugin limit
