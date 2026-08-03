# Roadmap

What is planned but not built. Nothing here exists in the code — that is the
point of the file.

Both entries below were previously declared as `core.Feature` constants and
listed in the Enterprise claim set while nothing implemented them. A constant
with nothing behind it reads as a shipped feature to everyone including its
authors, so they were removed and written down here instead. They come back as
code when they are code.

---

## Response caching

**Why.** The same `CodeGeneratorRequest` run through the same plugin produces
the same output. In CI that happens constantly — the same protos, regenerated on
every push, by every branch.

**Shape.**

- **Key:** sha256 over `{group}/{name}/{version}`, the sha256 of the plugin
  binary (already stored per plugin), the serialised request, and the plugin's
  `Command`/`Env`. Including the binary hash makes a re-pushed plugin invalidate
  its own entries; no separate invalidation path is needed.
- **Store:** in-memory per pod to begin with, reusing the LRU-with-byte-limit
  already written in `internal/adapters/registry/cache.go`. A shared cache
  (Postgres or S3) only if hit rates justify it — with several replicas the same
  request lands on the same pod rarely.
- **Opt in per plugin, not out.** Plugins that embed a timestamp or an absolute
  path in their output return different bytes for identical input. A `cacheable`
  column defaulting to false is the safe direction: wrongly cached codegen is
  worse than slow codegen, because it is silently wrong.
- Gated by an Enterprise feature. Metrics
  `easyp_response_cache_{hits,misses}_total`, `easyp_response_cache_bytes`.

**Size:** small. This is the one to do first.

---

## Multi-tenancy

**Why.** One installation serving several teams that should not see each other's
plugins.

**The decision that has to come first.** Reads are anonymous today — deliberately:
authentication covers writes, clients read without credentials. A reader's tenant
cannot be derived from an anonymous request, so tenancy needs one of:

- **(a)** tenant applies to writes and audit only, reads stay global. Cheap, but
  it is not isolation: everyone still sees everything.
- **(b)** with multi-tenancy enabled, reads authenticate too; with it disabled,
  they stay anonymous. Community and single-tenant installations notice nothing.

**(b) is the recommendation**, with the licence feature acting as the switch.
This is a product decision, not a technical one, and it is unresolved.

**What it touches.**

- Migration: a `tenant` column on `plugins` and on audit rows, existing rows
  taking a default tenant.
- Plugin uniqueness becomes `(tenant, group, name, version)` instead of
  `(group, name, version)`.
- **Object storage keys need a tenant prefix.** Without it two tenants with a
  same-named plugin overwrite each other's archives. This is the least visible
  trap here and the most damaging: it corrupts artifacts rather than failing.
- `AUTH_WRITE_TOKENS` grows from `name=sha256` to `name:tenant=sha256`, and read
  tokens appear.
- Every query in `internal/adapters/registry` filters by tenant.
- The accepted risks in `SECURITY.md` need revisiting: today a malicious plugin
  is an insider threat because registration needs a write token. Under
  multi-tenancy one tenant could reach another's data through a plugin, so the
  process would need real isolation — separate uid, a mount namespace excluding
  `/certs`, a disk quota.

**Size:** comparable to a full release stage on its own.

---

## Not planned

Recorded so the question is not reopened from scratch:

- **A licensing service.** Verification is offline by design; the token carries
  the tier and the release decides what it unlocks. See `.spec/AUTH.md` and the
  `easyp-tech/licenses` registry.
- **A shared Go package for the token format.** Six string literals written down
  on both sides, guarded by tests either end. A dependency from the private
  registry onto this module costs more than it saves.
