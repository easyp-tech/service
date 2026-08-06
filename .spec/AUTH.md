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
| Enterprise | Configurable | Unlimited (-1) | All community + Audit |

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

**Implementation:** `PasetoLicenseClient`. Verification is offline — nothing
reaches the network, so an air-gapped installation is not a special case.

| Token | Configured public key | Result |
|-------|----------------------|--------|
| absent | any | community |
| present | absent | community (logs a warning) |
| present | does not decode | **startup fails** |
| present | present, signature verifies, current | enterprise |
| present | present, signature verifies, expired within `grace_days` | enterprise (logs a warning) |
| present | present, anything else | community (logs the reason) |

A licence problem must not be able to take the service down, so every runtime
failure resolves to community mode. The one exception is a public key that is
not a key: that can only be an operator error, and quietly downgrading would
hide it.

The key id in the token footer selects which configured key to verify against.
It is read before anything is verified, which is safe because it selects a key
and decides nothing else — a forged key id merely points at a key the signature
then fails against.

Time is decided in one place, against the service clock, with a minute of skew
tolerance at both ends. The PASETO parser is deliberately built *without* its
own `NotExpired` rule: leaving it in place rejects an expired token before the
grace period it may still be entitled to can be considered, which is exactly the
bug that shipped first time round.

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
  public_keys:     # key id -> hex-encoded Ed25519 verification key
    "2026-08": ""
  public_key: ""   # single verification key, used when the key id matches nothing above
  cache_ttl: 5m    # how long to cache license claims
```

Environment: `LICENSE_KEY`, `LICENSE_FILE`, `LICENSE_PUBLIC_KEYS`,
`LICENSE_PUBLIC_KEY`, `LICENSE_CACHE_TTL`.

`LICENSE_PUBLIC_KEYS` is encoded `<kid>:<hex>,<kid>:<hex>`. This is what the Helm
chart renders from `config.license.publicKeys`; a key id may therefore contain
neither `:` nor `,`, and configuration naming one is rejected.

All of these are also consulted directly as a last resort, because the `--cfg`
startup path decodes YAML and skips envconfig entirely — without that fallback,
values passed to the container through the environment would be dropped. Order
of precedence:

- token: `license.key` → contents of `license.file` → `LICENSE_KEY`
- keys: `license.public_keys` → `LICENSE_PUBLIC_KEYS`
- single key: `license.public_key` → `LICENSE_PUBLIC_KEY`

All are read at runtime. A deployment with no public key cannot honour any token
and stays in community mode.

Two keys can be configured at once so that a signing key can be rotated without
every deployment having to change key on the same day: issue under the new key
id while the old one is still accepted, then drop the old entry.

> **Threat model.** Because the verification key is configuration rather than a
> property of the build, anyone able to edit `deploy/config/config.yml` or set
> `LICENSE_PUBLIC_KEYS` can substitute their own signing authority and issue
> themselves a licence. Protect the config file accordingly.

## Issuing

Not here. This service verifies; the licence registry (`easyp-tech/licenses`,
private) issues. It holds the private signing key, the record of who was given
what, and `cmd/easyp-license`, which signs from a spec file in CI.

The two sides therefore agree on the token format by writing the same six values
down twice: `iss`, `aud`, `tier`, and the claim names `grace_days` and
`customer_name`, plus the footer shape `{"kid": "..."}`. Everything else —
signature, encoding, key format — is `go-paseto`, which both use.

Sharing a Go package for six string literals was considered and rejected: it
would couple a private repository to this module for less than it costs, and
`internal/` cannot be imported across modules anyway. What guards the format
instead is a test on each side. Here that is `TestTokenContents` and
`TestValidityWindow` in `internal/license/paseto_client_test.go`, which pin the
issuer, audience, tier and validity window by asserting that anything else
resolves to community. In the registry it is `easyp-license verify`, and, once
that repository has CI, a step that runs an issued token through a real service
binary.

## PASETO Token Format

Tokens are `v4.public` PASETO tokens containing `LicenseClaims`:
- `tier` — "community" or "enterprise"
- `features` — list of Feature enum values
- `max_workers` — concurrent worker limit
- `max_plugins` — registered plugin limit
