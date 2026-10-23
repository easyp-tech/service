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

Released versions are published to the OCI registry alongside the image, so a
checkout is not required:

```bash
helm install easyp oci://ghcr.io/easyp-tech/charts/easyp-service \
  --version 1.1.0 \
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

They render into the `license.public_keys` section of the config file the
service reads — not into an environment variable. The chart ships easyp.tech's
own published key as the default, so an installation with a `LICENSE_KEY` needs
no further configuration.

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

The claims outlive the release: they are created by the StatefulSet rather than
by Helm, so `helm uninstall` leaves them, because refilling the cache costs more
than the disk. `persistence.retentionPolicy.whenDeleted=Delete` changes that.

## Scaling

The workload is a StatefulSet. The pods keep no state of their own — plugin
metadata and audit live in Postgres, archives in object storage — and nothing
addresses one by name; the StatefulSet is there for `volumeClaimTemplates`, the
one way to give each replica a ReadWriteOnce volume that outlives the pod.

| | Replicas | Cache after a restart |
|---|---|---|
| `persistence.enabled=true` (default) | any | warm: a claim per replica, `plugins-<release>-easyp-service-N` |
| `persistence.enabled=false` | any | cold: an emptyDir per pod |

Replicas never share a volume, and the chart has no ReadWriteMany option.
Unpacking a plugin is serialised by an in-process lock only, so two pods on one
volume race on a concurrent miss for the same plugin and corrupt it; each would
also apply `cacheMaxBytes` to the same bytes on its own. `persistence.accessMode`
accepts `ReadWriteOnce` and `ReadWriteOncePod` and refuses anything else. That
also means scaling needs nothing but ordinary block storage — no NFS, no RWX
class.

More than one replica needs object storage:

```bash
helm install easyp oci://ghcr.io/easyp-tech/charts/easyp-service \
  --set secrets.existingSecret=easyp-env \
  --set config.registry.s3.bucket=easyp-plugins \
  --set config.registry.s3.endpoint=https://s3.example.com \
  --set autoscaling.enabled=true \
  ...
```

Every replica's volume starts empty and S3 is the only thing that fills it;
without S3 the plugins directory is the one copy of every plugin, and only one
replica can hold it. The chart refuses more than one replica — or an autoscaler
allowed more than one — without `config.registry.s3.bucket`, rather than
starting pods that have no plugins.

Storage is `replicas × persistence.size`, and each new replica downloads what it
serves. `persistence.retentionPolicy.whenScaled` decides what happens to a claim
when the StatefulSet scales down: `Retain` (the default) keeps the warm cache for
the next scale-up, `Delete` stops an autoscaler from leaving idle disks behind.
It needs Kubernetes 1.27; older API servers ignore it and retain.

### Upgrades roll

A StatefulSet deletes a pod before starting its replacement, so a ReadWriteOnce
claim is released before the next pod asks for it. With one replica that is a
short gap; with several, the others serve through it. Pods start in parallel
(`podManagementPolicy: Parallel`) — migrations are serialised by an advisory
lock, so there is nothing to order.

The chart used to render a Deployment, which needed `strategy: Recreate` on a
ReadWriteOnce claim: a rolling update started the replacement first, and one
scheduled on another node waited on a Multi-Attach error forever.

### Upgrading from a release that rendered a Deployment

Earlier releases rendered a Deployment and one standalone claim,
`<release>-easyp-service-plugins`, or mounted the claim named by
`persistence.existingClaim`. Both are gone: the StatefulSet's pods mount claims
from the template, `plugins-<release>-easyp-service-N`, and `existingClaim` is
refused rather than ignored, so an upgrade that still sets it stops before it
starts pods on an empty volume. The chart's own old claim carries
`helm.sh/resource-policy: keep`, so Helm leaves it in place, unmounted, and the
install notes say so when they find it.

What to do with the old claim depends on whether the release uses object
storage:

- **With S3**, nothing is lost. The new claims fill on demand, a cold start per
  replica. Upgrade, then delete the old claim once the pods are serving.
- **Without S3**, the old claim holds the only copy of every plugin. Before
  upgrading, rebind its PersistentVolume to the name the StatefulSet will look
  for; the StatefulSet then adopts it as replica 0's claim instead of creating
  an empty one.

```bash
release=easyp
old=easyp-easyp-service-plugins            # or the claim existingClaim named
new=plugins-easyp-easyp-service-0

pv="$(kubectl get pvc "$old" -o jsonpath='{.spec.volumeName}')"
class="$(kubectl get pvc "$old" -o jsonpath='{.spec.storageClassName}')"
size="$(kubectl get pvc "$old" -o jsonpath='{.spec.resources.requests.storage}')"

# Keep the volume when its claim goes away, and stop the pod that mounts it.
kubectl patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'
kubectl scale deploy/easyp-easyp-service --replicas=0
kubectl delete pvc "$old"

# Free the volume, then claim it under the StatefulSet's name.
kubectl patch pv "$pv" --type=json -p '[{"op":"remove","path":"/spec/claimRef"}]'
kubectl create -f - <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $new
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: "$class"
  resources: {requests: {storage: "$size"}}
  volumeName: "$pv"
YAML

# Give the chart the volume's real size, so its check against cacheMaxBytes
# compares the disk that is actually there.
helm upgrade "$release" oci://ghcr.io/easyp-tech/charts/easyp-service \
  --version <new> --reuse-values \
  --set persistence.existingClaim=null --set persistence.size="$size"
```

`--reuse-values` works for this upgrade, with one thing to know. It does not
merge the new chart's defaults: it replaces them with the old release's values,
so every key added since — `autoscaling`, `persistence.retentionPolicy` — is
absent rather than defaulted. The templates read an absent key as off (or, for
retention, as Retain), so the release comes up as it was. Turning autoscaling on
in the same upgrade is the exception: `--set autoscaling.enabled=true` brings
that one key and none of its bounds, and the chart refuses it by name. Use
`--reset-then-reuse-values` (Helm 3.14+) for that, which starts from the new
defaults and applies the old release's values over them.

If the release was already upgraded and its pod started on an empty claim, the
same steps apply with `kubectl scale statefulset/easyp-easyp-service
--replicas=0` in place of the Deployment, deleting the empty
`plugins-easyp-easyp-service-0` alongside the old claim, and scaling back to one
afterwards.

Tooling that addressed `deploy/<release>-easyp-service` — `kubectl rollout`,
`kubectl logs`, dashboards, alert selectors on `kube_deployment_*` — needs
`statefulset/` instead.

### Autoscaling

`autoscaling.enabled` renders a HorizontalPodAutoscaler on CPU — every
generation is a plugin process charged to this container, so CPU is where load
shows. It needs metrics-server; without it the HPA reports `<unknown>` and never
scales. `replicaCount` is ignored while it is on, so `helm upgrade` does not
reset what the HPA chose.

Every limit in the service is per pod: the worker pool, the concurrent
generation cap and the per-client rate limit. Capacity therefore scales with
replicas, and so does what one client can use — a client whose requests land on
N pods gets up to N times `rateLimit`. The Community plugin cap counts rows in
the database and holds across replicas; the per-pod ceilings apply to each one.

### Resizing the plugin volumes

A StatefulSet's claim templates are immutable, so raising `persistence.size`
fails `helm upgrade`. With a storage class that allows expansion:

```bash
# 1. Grow the existing claims in place.
for pvc in $(kubectl get pvc -l app.kubernetes.io/instance=easyp -o name); do
  kubectl patch "$pvc" -p '{"spec":{"resources":{"requests":{"storage":"50Gi"}}}}'
done

# 2. Drop the StatefulSet object, leaving its pods and claims running.
kubectl delete statefulset easyp-easyp-service --cascade=orphan

# 3. Create it again with the new template; it adopts the pods and claims.
helm upgrade easyp ... --reuse-values --set persistence.size=50Gi
```

Raise `config.registry.cacheMaxBytes` in the same upgrade if the extra room is
meant for the cache.

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

## MCP

Off by default. `mcp.enabled: true` makes the container listen on `ports.mcp`
(8083), publishes it on the Service, and enables it in the rendered config —
all three together, so the port is never half-open. The endpoint is read-only
HTTP for AI tooling (plugin catalog, `easyp.yaml` schema) and exposes nothing
the anonymous gRPC reads do not, but it bypasses the gRPC interceptor chain:
no TLS, no rate limit, no audit. Enable it inside a trusted network or behind
an ingress that terminates TLS in front of it.

## Alerting

`prometheusRule.enabled` ships eleven alerts. It defaults to off only because it
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
  `TELEMETRY_OTLP_ENDPOINT`. The standard `OTEL_EXPORTER_OTLP_ENDPOINT` is
  read as an alias, but the canonical name wins when both are set.

Anything the chart does not model can still be set through `extraEnv`, which
overrides the file.

## Values

See `values.yaml`; every key is commented. The install-time checks in
`_helpers.tpl` reject combinations that would otherwise fail confusingly at
runtime — missing DSN, plaintext router against a TLS listener, a shared
volume, more than one replica without object storage, grace period shorter than the generation timeout, an ingress
with no trusted proxies, peak generation buffers larger than the memory limit.
