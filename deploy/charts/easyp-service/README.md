# easyp-service

Helm chart for EasyP Service — a gRPC code generation service for Protobuf schemas.

The service is licensed under the Elastic License 2.0; see `LICENSE` at the
repository root. Running without a licence key is free and puts the service in
community mode.

## Requirements

- Kubernetes 1.25+
- A PostgreSQL database the pod can reach. The chart does not deploy one, and
  the service applies its own migrations at startup (serialised across replicas
  by a Postgres advisory lock, so concurrent starts are safe).
- Optional: Traefik for ingress, cert-manager for certificates,
  Prometheus Operator for `ServiceMonitor` and `PrometheusRule`,
  stakater/Reloader for certificate rotation.

## Install

The chart refuses to install without a database DSN. Bring a secret:

```bash
kubectl create secret generic easyp-env \
  --from-literal=DB_POSTGRES_DSN='postgres://user:pass@host:5432/easyp?sslmode=require'

helm install easyp ./charts/easyp-service \
  --set secrets.existingSecret=easyp-env \
  --set tls.enabled=false
```

That gets you a running service with no transport security and no writes
enabled — enough to confirm it works, not enough to expose.

## Secrets

The pod loads credentials with `envFrom`, so **the keys of the secret are the
environment variable names**:

| Key | Required | Notes |
|-----|----------|-------|
| `DB_POSTGRES_DSN` | yes | |
| `REGISTRY_S3_ACCESS_KEY_ID` | when S3 is configured | must be set together with the secret key |
| `REGISTRY_S3_SECRET_ACCESS_KEY` | when S3 is configured | |
| `LICENSE_KEY` | no | absent ⇒ community mode |
| `LICENSE_PUBLIC_KEY` | no | single-key alternative to `config.license.publicKeys`; without one of the two, `LICENSE_KEY` is ignored |
| `AUTH_WRITE_TOKENS` | no | absent ⇒ all writes rejected |

`AUTH_WRITE_TOKENS` holds sha256 digests, never tokens:
`ci=<64 hex>,release=<64 hex>`. Mint one with `easyp-svc auth new-token --name ci`;
the digest is safe to store, the token is not.

`secrets.create=true` renders a secret from `secrets.data`, but those values end
up in Helm release history and in `helm get values`. It exists for throwaway
clusters; bring your own secret anywhere else.

## Licence

The token is a secret and arrives through the secret above. The keys it is
verified against are not, and live in values:

```yaml
config:
  license:
    publicKeys:
      "2026-08": "<64 hex characters>"
```

They render into `LICENSE_PUBLIC_KEYS`. The chart ships easyp.tech's own
published key as the default, so an installation with a `LICENSE_KEY` needs no
further configuration.

More than one entry is allowed, which is how a signing key gets rotated without
every deployment having to change key on the same day: issue under the new key
id while the old one is still accepted, then drop the old entry.

These keys are the trust anchor — whoever sets them decides which authority may
issue licences for this installation. Replacing the default means you issue your
own licences; clearing it (`--set config.license.publicKeys=null`) means no token
is honoured at all.

Without a key, `LICENSE_KEY` is ignored and the service runs in community mode.
A key that is not 64 hex characters fails `helm install` rather than producing a
pod that starts and quietly serves community.

## Transport security

`tls.enabled` makes the gRPC listener serve TLS. Supplying a CA for client
certificates turns it into mutual TLS, and that is what keeps the listener
private: the ingress controller is then the only party holding a certificate
that gets in.

Traefik expresses the mutual-TLS backend leg through a `ServersTransport`
referenced by annotations on the Service. Other controllers need their own
mechanism — nginx, for example, uses `nginx.ingress.kubernetes.io/proxy-ssl-secret`
— and the chart does not template those.

**Rotation requires a pod restart.** The service reads its key pair once, at
startup. `reloader.enabled` adds the stakater/Reloader annotation so a changed
secret triggers a rollout; without that controller installed, renewal means
`kubectl rollout restart`.

## Memory

Peak memory is not a guess — it follows from two settings you can change:

```
maxConcurrentGenerations × maxOutputSize × 2  ≤  resources.limits.memory
        16              ×    64 MiB     × 2  =  2 GiB   ≤  4 GiB
```

A plugin's output is read into memory in one piece, and the same bytes exist a
second time once marshalled into the gRPC response — hence the factor of two.
Everything else the pod does comes out of the remaining headroom.

The chart refuses the install when that product exceeds the limit. It is worth
refusing because the failure is otherwise unreadable: the pod is OOMKilled,
which looks like a crash and not like overload. Nothing is logged,
`easyp_pool_generations_rejected_total` does not move, and the saturation alerts
stay quiet. That combination shipped once, at 16 × 64 MiB against a 1Gi limit,
where the buffers alone accounted for the whole limit.

Raising `config.registry.maxOutputSize` or
`config.workerPool.maxConcurrentGenerations` therefore means raising the memory
limit with it. Leaving `resources.limits.memory` unset skips the check entirely,
for clusters that set limits by namespace policy.

CPU is sized alongside: four cores for sixteen concurrent plugin processes. With
fewer, each process runs proportionally slower and generations start reaching
`generationTimeoutSeconds` — which surfaces as `DeadlineExceeded` and reads as a
broken plugin, rather than as the `ResourceExhausted` the limiter returns when it
is genuinely the load.

## Storage

Plugin archives are downloaded on demand and unpacked into
`config.registry.pluginsDir`. Once the unpacked total passes
`config.registry.cacheMaxBytes` (20 GiB by default) the least recently used
plugins are removed. Only local files go: the archive in object storage stays,
so an evicted plugin is one download away rather than lost.

Whatever the cache is written to must exceed `cacheMaxBytes`, and the chart
refuses the install otherwise: eviction begins at the limit, so storage sized
exactly to it is already full by the time the cache first needs room. A full
volume fails generation with an I/O error that names nothing useful. The
defaults leave 5 GiB of headroom.

That check applies to both storage paths — `persistence.size` with a volume,
`persistence.ephemeralSizeLimit` without one. It once applied only to the first,
which meant disabling persistence quietly removed the ceiling while
`cacheMaxBytes` stayed where it was.

Running without persistence needs one more number raised. An emptyDir counts
against the pod's `resources.limits.ephemeral-storage`, so that limit and
`ephemeralSizeLimit` sit over the same bytes and the lower one decides; the
shipped figures assume a volume, where only logs and the writable layer are
charged. The chart refuses the combination rather than letting the kubelet evict
the pod well short of the limit the operator set.

A plugin used within the last few minutes is never evicted, even if that means
overshooting the limit: removing a binary out from under a running process would
fail the request in a way that looks like a corrupt artifact. Watch
`easyp_plugin_cache_bytes` against the limit, and
`easyp_plugin_cache_evictions_total` for churn.

The PVC carries `helm.sh/resource-policy: keep`, because refilling the cache
costs more than the disk.

`replicaCount > 1` needs `persistence.accessMode=ReadWriteMany`; the chart fails
the install otherwise rather than leaving pods stuck in Pending.

### Upgrades take the service down briefly

With a `ReadWriteOnce` volume the deployment uses `strategy: Recreate`, so an
upgrade stops the running pod before starting its replacement. That gap is
deliberate. A rolling update would start the new pod first, and because a
ReadWriteOnce volume attaches to one node at a time, a replacement scheduled
anywhere else waits on a Multi-Attach error indefinitely — `helm upgrade` neither
completes nor fails.

If the gap is unacceptable, the answer is a `ReadWriteMany` volume, not a
different strategy: with a shared volume the chart rolls, and `replicaCount` can
exceed one.

## Behind an ingress: `config.server.trustedProxies`

`ingress.enabled: true` requires it, and the chart refuses the install without
it.

Every request then reaches the pod from the ingress controller's address. The
rate limiter and the per-caller concurrency limiter key on who is calling, so
with nothing configured they see one caller: 10 requests per second and two
concurrent requests shared by every client at once, and no protection against
any individual one. The audit log records the ingress as the actor for the same
reason. Nothing about that fails visibly — hence the refusal rather than a note.

Set it to the pod CIDR of the node pool your ingress controller runs in:

```yaml
config:
  server:
    trustedProxies:
      - 10.42.0.0/16
```

`X-Forwarded-For` and `X-Real-IP` are then believed for connections from that
range and ignored from anywhere else, so a client cannot pick its own identity
to escape a limit. Keep the range tight: whatever is listed here can claim to be
any caller.

## Network policy

On by default. This pod's job is executing third-party binaries, so where it may
connect is stated rather than inherited.

Outbound is limited to DNS plus `networkPolicy.egressPorts` — 5432, 443 and 4317,
covering PostgreSQL, object storage and OTLP. **If your database, object storage
or collector listens elsewhere, add the port**, or the pod goes quiet in a way
that reads as a hang rather than a refusal. That is the first thing to check
after a fresh install stalls.

Inbound, gRPC is restricted to `networkPolicy.ingressNamespaceSelector` when set,
and open cluster-wide when it is not. Set it once you know which namespace your
ingress controller runs in — the mutual-TLS leg is what actually protects the
listener, but two locks are better than one.

## Alerting

`prometheusRule.enabled` ships ten alerts. It defaults to off only because it
needs the Prometheus Operator CRDs and would otherwise fail the install where
they are absent — turn it on wherever you actually run this.

The licence ones matter most, because a lapsed licence breaks nothing loudly:
the tier drops to community, audit stops being written, and the plugin limit
starts refusing registrations, all silently. `EasypLicenceExpiringSoon` fires
two weeks out (`prometheusRule.licenceExpiryWarningDays`) and
`EasypLicenceInGrace` once the token is running on borrowed time. Installations
with no licence at all are excluded rather than alerted on forever.

The rest cover capacity (`EasypGenerationsRejected`,
`EasypGenerationQueueSaturated`, `EasypPluginCacheAtLimit`), the audit pipeline
(`EasypAuditEventsLost`, `EasypAuditMaintenanceStale`) and failures
(`EasypGenerationErrorRate`, `EasypPanics`, `EasypAuthFailures`). Thresholds are
values, not template edits.

## Shutdown timing

Three values have to line up, and the chart refuses to install if they do not:

```
generationTimeoutSeconds  <  forceShutdownAfterSeconds  <  terminationGracePeriodSeconds
        120                          150                            180
```

A generation the service accepted must be able to finish; the process must then
be able to exit on its own; and Kubernetes must wait for both rather than
reaching for SIGKILL first. Raise `generationTimeoutSeconds` and the other two
have to follow.

## Configuration reference

Non-secret settings come from `config.*` in `values.yaml`, which the chart
renders into a ConfigMap mounted at `/etc/easyp/config.yml` — the same config
file `docker compose` and a local run use. Secrets do *not* go there: they
arrive as environment variables from the secret, and the environment beats the
file on every startup path.

Since chart 0.2.0. Before that the settings were forty environment variables
written out by hand in `deployment.yaml`, a second partial copy of the config
structure that had drifted: `db.driver`, `license.cache_ttl` and `license.file`
could not be set through the chart at all, and `worker_pool.max_retries`
disagreed with the compose configs for months without anyone choosing it.

To see what an install resolves to, defaults and origins included:

```sh
helm template my-release . --set … \
  | awk '/^  config\.yml: \|$/{f=1;next} f&&/^(---|[^ ])/{exit} f{sub(/^    /,"");print}' \
  > /tmp/config.yml
DB_POSTGRES_DSN=… easyp-svc config print --cfg /tmp/config.yml --origin
```

Two things are easy to get wrong when setting them by hand:

- Ports default to **23410–23413** in the service, not 8080–8083. The chart
  always sets them explicitly.
- The OTLP endpoint key is `telemetry.otlp_endpoint`, and its variable is
  `TELEMETRY_OTEL_EXPORTER_OTLP_ENDPOINT`. The standard
  `OTEL_EXPORTER_OTLP_ENDPOINT` is *not* read: the field sits inside a section
  prefixed `TELEMETRY_`.

Anything the chart does not model can still be set through `extraEnv`, which
overrides the file.

## Values

See `values.yaml`; every key is commented. The install-time checks in
`_helpers.tpl` reject combinations that would otherwise fail confusingly at
runtime — missing DSN, plaintext router against a TLS listener, multi-replica
`ReadWriteOnce`, grace period shorter than the generation timeout, an ingress
with no trusted proxies, peak generation buffers larger than the memory limit.
