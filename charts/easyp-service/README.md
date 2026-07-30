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
  Prometheus Operator for `ServiceMonitor`, stakater/Reloader for certificate
  rotation.

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
| `LICENSE_PUBLIC_KEY` | no | without it `LICENSE_KEY` is ignored |
| `AUTH_WRITE_TOKENS` | no | absent ⇒ all writes rejected |

`AUTH_WRITE_TOKENS` holds sha256 digests, never tokens:
`ci=<64 hex>,release=<64 hex>`. Mint one with `easyp-svc auth new-token --name ci`;
the digest is safe to store, the token is not.

`secrets.create=true` renders a secret from `secrets.data`, but those values end
up in Helm release history and in `helm get values`. It exists for throwaway
clusters; bring your own secret anywhere else.

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

## Storage

Plugin archives are downloaded on demand and unpacked into
`config.registry.pluginsDir`. Once the unpacked total passes
`config.registry.cacheMaxBytes` (20 GiB by default) the least recently used
plugins are removed. Only local files go: the archive in object storage stays,
so an evicted plugin is one download away rather than lost.

Keep `persistence.size` above `cacheMaxBytes` — eviction begins at the limit, so
a volume sized exactly to it would already be full when the cache first needs
room. The defaults leave 5 GiB of headroom.

A plugin used within the last few minutes is never evicted, even if that means
overshooting the limit: removing a binary out from under a running process would
fail the request in a way that looks like a corrupt artifact. Watch
`easyp_plugin_cache_bytes` against the limit, and
`easyp_plugin_cache_evictions_total` for churn.

The PVC carries `helm.sh/resource-policy: keep`, because refilling the cache
costs more than the disk.

`replicaCount > 1` needs `persistence.accessMode=ReadWriteMany`; the chart fails
the install otherwise rather than leaving pods stuck in Pending.

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

Non-secret settings map onto environment variables through `config.*` in
`values.yaml`. Two names are easy to get wrong when setting them by hand:

- Ports default to **23410–23413** in the service, not 8080–8083. The chart
  always sets them explicitly.
- The OTLP endpoint variable is `TELEMETRY_OTEL_EXPORTER_OTLP_ENDPOINT`. The
  standard `OTEL_EXPORTER_OTLP_ENDPOINT` is *not* read: the field sits inside a
  section prefixed `TELEMETRY_`.

Anything the chart does not model can be added through `extraEnv`.

## Values

See `values.yaml`; every key is commented. The install-time checks in
`_helpers.tpl` reject combinations that would otherwise fail confusingly at
runtime — missing DSN, plaintext router against a TLS listener, multi-replica
`ReadWriteOnce`, grace period shorter than the generation timeout.
