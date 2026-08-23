# Upgrading

Only releases that need a hand are listed. A version absent from this file
upgrades by pulling the new image.

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
