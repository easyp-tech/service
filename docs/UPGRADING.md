# Upgrading

Only releases that need a hand are listed. A version absent from this file
upgrades by pulling the new image.

## v0.13.x → v0.14.0

The largest set of breaking changes this project will make. They are grouped
here rather than spread over several releases on purpose: the surfaces freeze at
1.0, and after that every one of them costs a major version.

**Read [The gRPC service was renamed](#the-grpc-service-was-renamed) first.** It
breaks every client at once, including `easyp` itself, and no other item matters
until that one is handled.

### The gRPC service was renamed

The proto package and the service both changed name:

| | Before | After |
|---|---|---|
| Package | `api.generator.v1` | `easyp.generator.v1` |
| Service | `ServiceAPI` | `GeneratorAPI` |
| Method path | `/api.generator.v1.ServiceAPI/GenerateCode` | `/easyp.generator.v1.GeneratorAPI/GenerateCode` |
| Go import | `…/service/api/generator/v1` | `…/service/api/easyp/generator/v1` |

The old package name carried no vendor prefix and collides with anything else
called the same in a shared schema registry; `ServiceAPI` is two words for
"thing". Both are frozen forever at 1.0, which is why they move now.

**A client that is not rebuilt gets `Unimplemented` on every call.** The service
does not answer the old method path. That includes `easyp` itself: `remote:`
plugins in `easyp.yaml` reach this service over gRPC, so an `easyp` older than
the release that tracks this change cannot generate against a v0.14.0 server.
Upgrade `easyp` alongside the service.

### `api` and `sdk` are separate Go modules

Both carried an Apache-2.0 `LICENSE`, but Go tooling reads licences per module,
not per directory — so a client importing the SDK had its licence scanner report
the service's Elastic-2.0 on their own build.

```
require (
    github.com/easyp-tech/service/api v0.14.0
    github.com/easyp-tech/service/sdk v0.14.0
)
```

`go get github.com/easyp-tech/service` no longer supplies either. They are tagged
as `api/v0.14.0` and `sdk/v0.14.0`.

### `PluginsResponse.total` is gone

It held the number of plugins in *this page*, which `plugins.length` already
says, under a name that promises the size of the collection. Field number 2 is
reserved and will not be reused.

### `UpdatePlugin` takes a field mask

`update_mask` selects what to replace: `config`, `tags`, or both. **An omitted
mask replaces both, exactly as before** — existing callers need no change.

It exists because config was validated before anything else and an absent config
was an empty one, so changing a tag meant resending the plugin's whole command
line. An unknown path in the mask is rejected rather than ignored.

### A plugin's `command[0]` must be inside `plugins_dir`

The check used to accept a command where *any* element resolved inside the
plugins directory, so `["/bin/sh", "-c", "…", "/plugins/x"]` passed on the
strength of its last argument while `/bin/sh` was what ran. Arguments are still
unconstrained; only the executable is checked.

**A registration whose `command[0]` is a wrapper script outside `plugins_dir`
now fails** with `INVALID_ARGUMENT`. Re-register it with the plugin binary as
`command[0]` and the wrapper's work expressed in arguments, or move the wrapper
inside the plugins directory.

### Plugin archives may not contain symlinks pointing outside themselves

An archive could ship its `plugin` entrypoint as a symlink to `/bin/sh`: the
link file landed inside `plugins_dir`, so every containment check was satisfied.
Such an archive is now refused at unpack with `unsafe path in archive`. Links
that stay inside the archive still work.

### `UpdatePlugin` recomputes the archive checksum

Creating a plugin records the sha256 of its archive; updating one did not, and
because the checksum lives inside the config document, replacing the config
dropped it. An empty expected checksum means "nothing to check", so **one
`UpdatePlugin` with a hand-written config permanently disabled verification of
that plugin's binary** — silently.

Update now recomputes it, as Create does. **A plugin whose verification had been
switched off this way is checked again on its next download**, and if the object
in storage no longer matches what was registered it fails with
`plugin archive checksum mismatch` instead of running. That is the correct
outcome, but it can surface as a plugin that "suddenly broke": re-push the
archive and re-register, or confirm why the object changed.

### Error messages changed shape

Statuses used to carry the service's internal call chain —
`api.app.Generate: c.registry.Get: ensureBinary: …`. They now carry only the
terminal cause, and every non-OK status gains a `google.rpc.ErrorInfo` with a
stable `reason` and domain `easyp.tech`.

**A client matching on message text will break.** Match on `reason` instead:
`NOT_FOUND`, `INVALID_PLUGIN_NAME`, `INVALID_CONFIG`, `GENERATION_FAILED`,
`SERVER_OVERLOADED`, `ALREADY_EXISTS`, `MAX_PLUGINS_EXCEEDED`, `SHUTTING_DOWN`,
`STORAGE_UNAVAILABLE`, `BINARY_NOT_UPLOADED`, `FEATURE_DENIED`,
`DEADLINE_EXCEEDED`, `CANCELED`, `INTERNAL`. Adding a reason is a compatible
change; changing what one means is not.

Malformed plugin configurations also return `INVALID_ARGUMENT` rather than
`INTERNAL`. They were the caller's mistake reported as the server's fault.

### Go SDK

- `ListPlugins(ctx, filter ...PluginFilter)` → `ListPlugins(ctx, ...ListOption)`.
  Pass a filter as `sdk.WithFilter(sdk.PluginFilter{…})`.
- The client no longer re-filters results it received; the server already did.
- **Transient errors are `Unavailable` only.** `ResourceExhausted` and
  `DeadlineExceeded` are no longer retried: the first is the service saying it
  has no capacity (or that a licence ceiling was reached, which no wait clears),
  and the second re-runs a generation that already spent the full timeout. A
  caller who wants either retried can add an interceptor.
- `UpdatePlugin` and `DeletePlugin` exist.
- `WithRetryMaxDelay` and `WithCreatePluginTimeout` exist.
- A large `WithMaxRetries` no longer panics inside the interceptor: the backoff
  shift overflowed past roughly attempt 40 and `rand.Int64N` rejects the result.

### The MCP endpoint lost the easyp.yaml schema tool

`plugins_list` remains; the schema helpers are gone. They came from
`easyp/mcp/easypconfig`, and taking that package meant taking the `easyp`
module — which imports this service's own generated API in order to call it.
A service that cannot compile without its own client is the wrong shape, so the
dependency was removed rather than worked around. The tool is planned to return
from a standalone library both sides can depend on.

This only affects deployments that set `mcp.enabled=true`; the endpoint is off
by default.

## v0.13.0 → v0.13.1

The service is unchanged; this release exists to ship **chart 0.3.2**.

Chart 0.3.1 was published with its `runbookBaseUrl` pointing into `.spec/`,
which was deleted days after the release — so every alert it renders links to a
404, which is the page someone opens at 3am. A published chart cannot be
corrected in place, so the fix needed a new version:

```bash
helm upgrade easyp oci://ghcr.io/easyp-tech/charts/easyp-service --version 0.3.2
```

Nothing else changed in the chart, so an upgrade with unchanged values only
replaces the runbook links.

## v0.12.x → v0.13.0

Six changes need action before the upgrade, and one of them is silent — read
[Listing plugins](#listing-plugins-returns-100-rows-now) even if you change
nothing else.

The configuration loader also became strict in this release: **an unrecognised
YAML key now refuses the start** instead of warning and continuing on defaults.
That is what turns the renames below from a silent degradation into a message
naming the key and its line. Two of them arrive as errors that spell out the
fix; the rest are environment variables, which no schema can catch.

Check a configuration before deploying it:

```bash
easyp-svc config validate --cfg /path/to/config.yml
easyp-svc config print --cfg /path/to/config.yml --origin --changed
```

### Environment variables renamed

| Old | New | What happens if you miss it |
|-----|-----|-----------------------------|
| `TELEMETRY_S3_URL`, `TELEMETRY_S3_ENDPOINT`, `TELEMETRY_S3_ACCESS_KEY_ID`, `TELEMETRY_S3_SECRET_ACCESS_KEY` | `OBS_S3_URL`, `OBS_S3_ENDPOINT`, `OBS_S3_ACCESS_KEY_ID`, `OBS_S3_SECRET_ACCESS_KEY` | The observability stack refuses to start: `required variable OBS_S3_… is missing a value` |
| `TELEMETRY_OTEL_EXPORTER_OTLP_ENDPOINT` | `TELEMETRY_OTLP_ENDPOINT` | The service starts and exports nothing — no traces, no metrics, one warning |

The `OBS_S3_*` group belongs to Loki, Tempo, Mimir and Pyroscope, not to the
service: they were named after the wrong thing. The standard
`OTEL_EXPORTER_OTLP_ENDPOINT` is now read as an alias of the canonical name, so
a collector-injected variable works, and the URL form (`http://host:4317`) is
accepted alongside bare `host:port`.

### `license.public_key` removed

One setting, not two. Move the key into `license.public_keys` under its key id,
or under `"*"` to verify a token whose key id matches nothing else:

```yaml
license:
  public_keys:
    "2026-08": "<64 hex characters>"
```

The environment spelling is `LICENSE_PUBLIC_KEYS="<kid>:<hex>,<kid>:<hex>"`.
`LICENSE_PUBLIC_KEY` (singular) is gone and is no longer passed through by
docker-compose. **A deployment that only sets the singular form runs as
community** — no audit, four workers, ten plugins — without an error, because
an environment variable that reaches nothing cannot be diagnosed. The YAML
spelling is caught at startup and names the fix.

### `db.driver` removed

Postgres is the only driver this service has ever supported and the migrations
hard-code it. Delete the key; the startup error says the same.

### The MCP endpoint is off by default

`:8083` used to be served unconditionally. It is read-only and exposes nothing
the anonymous gRPC reads do not, but it sits outside the gRPC interceptor chain
— no TLS, no rate limit, no audit — so it is now a decision:

```yaml
mcp:
  enabled: true
```

Environment: `MCP_ENABLED=true`. Helm: `--set mcp.enabled=true`, which
publishes the container port, the Service port and the config together. With it
off the service logs `MCP endpoint disabled; set mcp.enabled to serve it` at
startup and nothing listens on the port.

### Listing plugins returns 100 rows now

`Plugins` is paginated. **A client that sends no `page_size` gets the first 100
plugins instead of all of them** — the call still succeeds, so a registry with
more than 100 entries quietly looks smaller than it is. Nothing in the wire
format broke; what changed is how much one response carries.

- `page_size` defaults to 100, is capped at 1000, and out-of-range values are
  normalised rather than rejected.
- `next_page_token` is empty on the last page; pass it back as `page_token`
  with **the same filters** to continue.
- `total` is the number of plugins in *this* response, as it always was.
- The Go SDK's `ListPlugins` walks every page itself, so SDK callers see the
  complete list and need no change.

The listing is now ordered by `(group, name, version)` — it had no `ORDER BY`
before, despite the documented promise of one.

### Deployment

**Helm chart 0.3.1** — upgrading rolls the pod once even with unchanged values:
the chart-managed secret is now hashed into the pod template, so rotating a
write token or the DSN actually restarts the service. Previously it did not,
and the new token was rejected until someone restarted by hand. The value
`ports.metrics` was also renamed to `ports.metric` (singular, matching
`server.port.metric`); an override using the old spelling is silently ignored
by Helm, so check yours.

Released charts are published alongside the image:

```bash
helm upgrade easyp oci://ghcr.io/easyp-tech/charts/easyp-service --version 0.3.1
```

**docker-compose** — the network now declares an `ip_range`, so that traefik's
fixed address cannot be handed to another container while the proxy is down.
Applying it **recreates the network**: the stack is down for a few seconds, and
`docker compose up -d` alone is not enough — run `down` first, or the proxy
fails to start with `Address already in use` and nothing listening to explain
it.

### Documentation

The `.spec/` directory was removed. Runbooks moved to
[docs/RUNBOOKS.md](RUNBOOKS.md) — the `runbook_url` on every alert points at the
new path. Links into `.spec/` from anywhere else are dead.
