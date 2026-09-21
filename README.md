# EasyP API Service

[![Release](https://img.shields.io/github/v/release/easyp-tech/service?sort=semver)](https://github.com/easyp-tech/service/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/easyp-tech/service)](go.mod)
[![License](https://img.shields.io/badge/license-Elastic--2.0-blue)](LICENSE)

A service that runs protobuf code-generation plugins so that developers and CI
do not have to install them. A client sends a
`google.protobuf.compiler.CodeGeneratorRequest` over gRPC and names a plugin;
the service executes that plugin as a local child process — bounded in time,
concurrency and output size, but not sandboxed — and returns the
`CodeGeneratorResponse`. One registry of plugin versions replaces one
installation per machine.

**Module:** `github.com/easyp-tech/service` · **Docs for operators:**
[easyp.tech/docs/api-service](https://easyp.tech/docs/api-service/overview) ·
**Client:** [easyp](https://github.com/easyp-tech/easyp) CLI, or the Go SDK in
[`sdk/`](sdk/)

## Install

Every release publishes an image, a Helm chart and the binaries. Pick one.

**Image** — `ghcr.io/easyp-tech/service`, multi-arch (amd64, arm64):

| Tag | Meaning |
|-----|---------|
| `v1.0.2` | a release; immutable |
| `latest` | the newest release — only moves on a release tag |
| `edge` | the tip of `master`; moves on every push |
| `sha-<short>` | one commit; immutable |

**Helm** — the chart is in the same registry. It refuses to install without a
database DSN, so bring a secret first:

```bash
kubectl create secret generic easyp-env \
  --from-literal=DB_POSTGRES_DSN='postgres://user:pass@host:5432/easyp?sslmode=require'

helm install easyp oci://ghcr.io/easyp-tech/charts/easyp-service \
  --version 1.0.2 \
  --set secrets.existingSecret=easyp-env \
  --set tls.enabled=false
```

That is a running service with no transport security and no writes enabled —
enough to confirm it works, not enough to expose. The chart's
[README](deploy/charts/easyp-service/README.md) covers TLS, mTLS, the licence,
`ServiceMonitor`, `PrometheusRule` and what each secret key means.

**Compose** — `deploy/docker-compose.yml` is the full development stack:
Postgres, an S3-compatible store, the observability suite and traefik in front
of the service. [Quick start](#quick-start) walks through it.

**Binaries** — `easyp-svc` for linux and darwin, amd64 and arm64, on the
[releases page](https://github.com/easyp-tech/service/releases). The same binary
is the server (`service start`) and the operator's CLI (`plugins`, `config`,
`auth`, `api`, `health`).

## Quick start

### Prerequisites

- Docker with the compose plugin (`docker compose`, not `docker-compose`)
- [Task](https://taskfile.dev/)
- Go 1.26+ to run the CLI from source; otherwise a release binary on `PATH`
- [easyp](https://github.com/easyp-tech/easyp) to generate code from the client side

### The compose stack

Plugins are built from the Dockerfiles in `registry/`, pushed to the stack's
object store, then registered with the service. The whole catalogue is 80
plugins in several hundred versions and takes hours; a filter builds one:

```bash
# 1. Build plugin binaries — the two that easyp.local.yaml asks for.
#    `task build-plugins` builds the whole registry.
FILTER='protocolbuffers/go:v1.36.10' task build-plugins-filter
FILTER='grpc/go:v1.6.2' task build-plugins-filter

# 2. Generate development certificates and start everything.
task up

# 3. Upload the archives, then register them.
task push-plugins
task register-plugins

# Or all four in one go, tailing the service log at the end:
task run
```

The gRPC port is not published on the host: the way in is traefik at
`easyp.api.localhost:4443`, which terminates TLS and speaks mutual TLS to the
service. `task register-plugins` already knows that. To see what got
registered:

```bash
easyp-svc api descriptor -o api.protoset
grpcurl -protoset api.protoset -cacert deploy/certs/ca.crt \
  easyp.api.localhost:4443 easyp.generator.v1.GeneratorAPI/Plugins
```

Grafana is on [localhost:3000](http://localhost:3000) (`admin` / `admin`).

### From source, against Postgres alone

For working on the service itself: no traefik, no TLS, no object store.
`task run-local` starts the service from `deploy/config/config.local.yml`,
plaintext on 8080, and registers whatever is in `plugins/` once it is ready.

```bash
FILTER='protocolbuffers/go:v1.36.10' task build-plugins-filter
FILTER='grpc/go:v1.6.2' task build-plugins-filter
task up-minimal        # Postgres only; on 5433 if 5432 is taken (EASYP_POSTGRES_PORT)
task run-local
easyp --cfg easyp.local.yaml generate       # in another terminal
go run ./cmd/mcp-smoke --endpoint http://localhost:8083/mcp
```

**On macOS this registers plugins but cannot run them.** `plugins build` is a
Docker build, so the binaries it extracts are Linux binaries, and a service
running natively on macOS gets `exec format error` from the first
`GenerateCode`. Use the compose stack there — the service runs in a Linux
container — and keep the from-source path for Linux hosts.

### Is it up?

```bash
curl -i http://localhost:8082/live    # liveness: 200 as soon as the listener is bound
curl -i http://localhost:8082/        # readiness: 200 once Postgres answers, 503 before
easyp-svc health --addr localhost:8082   # the same probe as a command; exit 0 or 1
curl http://localhost:8081/metrics
```

`easyp-svc health` is what the image's `HEALTHCHECK` runs. See
[Health and probes](#health-and-probes) for why it asks `/live` and not `/`.

## Using it

### From `easyp.yaml`

A plugin becomes remote by naming the service in front of it:

```yaml
generate:
  plugins:
    - remote: "plugins.example.com/protocolbuffers/go:v1.36.10"
      out: gen/go
      opts:
        paths: source_relative
    - remote: "plugins.example.com/grpc/go:latest"
      out: gen/go
      opts:
        paths: source_relative
```

`easyp.local.yaml` in this repository does exactly that against a service run
from source.

### Go SDK

```go
import "github.com/easyp-tech/service/sdk"

// The SDK defaults to TLS with the system trust store. Add
// sdk.WithTransportCredentials for a private CA, or sdk.WithInsecure() for a
// plaintext local service.
client, err := sdk.NewClient(
    "localhost:8080",
    sdk.WithInsecure(),
    sdk.WithMaxRetries(3),
    sdk.WithRetryBaseDelay(time.Second),
    sdk.WithHealthCheck(30*time.Second),
)
if err != nil {
    return err
}
defer client.Close()

// codeGenRequest is a *pluginpb.CodeGeneratorRequest.
response, err := client.GenerateCode(ctx, "protocolbuffers/go:v1.36.10", codeGenRequest)
```

`sdk/` is its own module under Apache-2.0; see [License](#license) for why.

### gRPC directly

The contract is `easyp.generator.v1.GeneratorAPI` in
[`api/easyp/generator/v1/generator.proto`](api/easyp/generator/v1/generator.proto):

```protobuf
service GeneratorAPI {
  rpc GenerateCode(GenerateCodeRequest) returns (GenerateCodeResponse);
  rpc Plugins(PluginsRequest) returns (PluginsResponse);
  rpc CreatePlugin(CreatePluginRequest) returns (CreatePluginResponse);
  rpc UpdatePlugin(UpdatePluginRequest) returns (UpdatePluginResponse);
  rpc DeletePlugin(DeletePluginRequest) returns (DeletePluginResponse);
}

message GenerateCodeRequest {
  google.protobuf.compiler.CodeGeneratorRequest code_generator_request = 1;
  string plugin_name = 2;  // "group/name:version" or "group/name:latest"
}
```

The server does not serve reflection. `easyp-svc api descriptor` writes a
`FileDescriptorSet` for `grpcurl -protoset`, as in the quick start.

### Errors

Every non-OK status carries a `google.rpc.ErrorInfo` with domain `easyp.tech`
and a `reason` a client can branch on — `NOT_FOUND`, `INVALID_PLUGIN_NAME`,
`INVALID_CONFIG`, `GENERATION_FAILED`, `SERVER_OVERLOADED`, `ALREADY_EXISTS`,
`MAX_PLUGINS_EXCEEDED`, `SHUTTING_DOWN`, `STORAGE_UNAVAILABLE`,
`BINARY_NOT_UPLOADED`, `FEATURE_DENIED`, `DEADLINE_EXCEEDED`. The gRPC code is
a category and the message is prose; the reason is the part that is promised
to stay. They are documented in the proto next to the RPC that returns them.

### MCP

An HTTP endpoint at `/mcp` (streamable transport) for AI tooling, with one tool:
`plugins_list`, the catalogue with optional `group`, `name`, `version` and
`tags` filters, paginated.

**Opt-in.** The listener is off unless `mcp.enabled: true` (env `MCP_ENABLED`):
it serves plain HTTP outside the gRPC interceptor chain — no TLS, no rate limit,
no audit — so a deployment decides whether that surface exists. It is read-only
and exposes nothing the anonymous gRPC reads do not. easyp-tech's own configs
under `deploy/config/` enable it; the Helm chart ships it off.

```json
{
  "mcpServers": {
    "easyp": { "url": "http://localhost:8083/mcp" }
  }
}
```

Handler tests: `task test-mcp`. Live check against a running endpoint:
`task smoke-mcp`.

## Configuration

### Environment Variables

Settings resolve in one order, whether the service was started with `--cfg` or
without it:

1. the YAML file, if one was given;
2. the environment, which **overrides** the file;
3. the `default=` on the field, which fills only what neither supplied.

The file can also be named by `EASYP_CONFIG` instead of `--cfg`. The image's
`HEALTHCHECK` depends on that: the probe runs without a command line of its own
and finds the health port through the same variable.

A variable that is set but empty counts as not set, so the `"${VAR:-}"` form used
throughout `deploy/` leaves the file's value alone when the variable is not
exported. This is what lets a secret — `DB_POSTGRES_DSN`, `AUTH_WRITE_TOKENS`,
`LICENSE_KEY`, `REGISTRY_S3_SECRET_ACCESS_KEY` — stay out of a committed config.

One name does not follow the field it sets: `db.postgres` is `DB_POSTGRES_DSN`.
Everything else is the dotted key upper-cased, with `.` becoming `_`.

`telemetry.otlp_endpoint` also accepts the standard `OTEL_EXPORTER_OTLP_ENDPOINT`
when `TELEMETRY_OTLP_ENDPOINT` is unset; `config print --origin` names whichever
one supplied the value.

An unrecognised key is an error, not a warning: the service refuses to start and
`config validate` exits non-zero. It used to be a warning next to a successful
start, which made "I configured this" and "I mistyped this" produce the same
running service. The same goes for a key copied from the chart's `values.yaml` —
those are camelCase and this file is snake_case, and the error says which key was
meant.

An unrecognised **environment variable** carrying a section prefix is reported as
a warning, and named on startup.

```bash
# The variables an operator actually sets. For the rest — every setting has one —
# ask the binary: `easyp-svc config print --origin`.
LOG_LEVEL=info

DB_POSTGRES_DSN="postgres://user:pass@localhost/db"

# Credentials for the mutating RPCs: "<name>=<64 hex>,<name>=<64 hex>".
# Generate with `easyp-svc auth new-token`.
AUTH_WRITE_TOKENS="ci=<64 hex>"

# Licence. Absent means community mode.
LICENSE_KEY=
LICENSE_PUBLIC_KEYS="<kid>:<64 hex>"

# Object storage; enabled by the bucket being set.
REGISTRY_S3_ENDPOINT="http://rustfs:9000"
REGISTRY_S3_BUCKET="easyp-plugins"
REGISTRY_S3_ACCESS_KEY_ID="rustfsadmin"
REGISTRY_S3_SECRET_ACCESS_KEY="rustfsadmin"

# Telemetry; empty means no exporter is built.
TELEMETRY_OTLP_ENDPOINT="easyp-alloy:4317"
```

### Checking a configuration

Two commands answer the two questions a config file raises, and neither needs the
service to be running:

```bash
# Would the service start on this? Exits non-zero if not.
easyp-svc config validate --cfg deploy/config/config.yml

# What will actually apply, and which layer supplied each value?
easyp-svc config print --cfg deploy/config/config.yml --origin

# What does this deployment change from the built-in defaults? The output is
# exactly what the file needs to contain.
easyp-svc config print --cfg deploy/config/config.yml --origin --changed
```

Secrets print as `***` unless `--show-secrets` is given. With no `--cfg`, both
commands read the environment alone, which is the shape a Helm deployment had
before the chart began rendering a file.

The service prints the same summary itself, at `info`, on every start — so the
question is answerable inside a container where these commands are not to hand.

### Ports

The binary's defaults are `23410` (gRPC), `23411` (metrics), `23412` (health)
and `23413` (MCP). Every configuration under `deploy/` moves them to
`8080`–`8083`, and so do the examples in this file — a port seen in a compose
file or a log line is a deployment's choice, not the default. `plugins register`
dials `localhost:23410` unless told otherwise.

### Configuration Files

| File | Purpose |
|------|---------|
| `deploy/config/config.yml` | Docker-compose service config (internal hostnames) |
| `deploy/config/config.local.yml` | Local development config (localhost, port 5433) |
| `deploy/config/config.community.dev.yml` | Two-tier dev stack, unlicensed container |
| `deploy/config/config.enterprise.dev.yml` | Two-tier dev stack, licensed container |

The two tier configs ship no write tokens and no telemetry endpoints on purpose:
that stack is what `deploy/docker-compose.public.yml` puts on the internet, and a
committed credential is a published one. Supply them through `deploy/.env.dev`.

An example, not the defaults — `config print --changed` against any of the
files above prints exactly what that deployment changes:

```yaml
server:
  host: "0.0.0.0"
  port:
    grpc: 8080
    metric: 8081
    health: 8082
    mcp: 8083
db:
  postgres: "postgres://easyp_svc:easyp_pass@localhost:5433/easyp_db?sslmode=disable"
registry:
  plugins_dir: "./plugins"
  max_output_size: 67108864
  # Optional S3-compatible binary storage. When enabled, plugin archives
  # pushed by `easyp-svc plugins push` are lazily downloaded into plugins_dir
  # (acting as a local cache) and sha256-verified before unpacking.
  s3:
    endpoint: "http://localhost:9000"
    bucket: "easyp-plugins"
    region: "us-east-1"
    access_key_id: "rustfsadmin"
    secret_access_key: "rustfsadmin"
    force_path_style: true
worker_pool:
  workers: 4
  queue_size: 16
  generation_timeout: 120s
  max_retries: 3
  shutdown_timeout: 30s
rate_limit:
  requests_per_second: 10.0
  burst: 20
  cleanup_interval: 10m
```

## Security

### Transport security

The gRPC listener is configured by `server.tls`:

```yaml
server:
  tls:
    cert_file: "/certs/server.crt"
    key_file: "/certs/server.key"
    # Present ⇒ mutual TLS. Every certificate this CA issues for client
    # authentication is accepted — see the warning below before choosing one.
    client_ca_file: "/certs/ca.crt"
```

Leaving `cert_file` empty serves plaintext; the service logs a warning on every
start so that never happens unnoticed. `cert_file` and `key_file` must be set
together, and `client_ca_file` alone is rejected at startup.

**Name a CA that exists for this service, not your corporate root.** The
listener checks that the client certificate chains to `client_ca_file` and
carries the `clientAuth` extended key usage. It does not check *whose*
certificate it is: there is no subject or SAN allow-list. An internal PKI issues
`clientAuth` certificates to many workloads — that is what it is for — so naming
one here makes every one of those workloads able to create, replace and delete
plugins. A CA scoped to this service keeps the set to the clients you issued.

In the compose stack traefik is the only client holding a certificate, and the
gRPC port is not published to the host — the way in is `easyp.api.localhost` on
`EASYP_TRAEFIK_TLS_PORT` (4443 by default), where traefik terminates the edge
certificate and re-establishes mutual TLS toward the service.

```bash
# Generate a development CA plus the server, client and edge certificates.
# `task up` runs this for you; FORCE=1 regenerates.
task certs

# Talk to the service through traefik
easyp-svc plugins register plugins \
  --addr easyp.api.localhost:4443 --tls-ca deploy/certs/ca.crt --cfg deploy/config/config.yml
```

Client-side flags: `--tls-ca` overrides the trust store, `--tls-cert`/`--tls-key`
supply a client certificate for a server that enforces mTLS, and `--insecure`
is the explicit opt-out used against a plaintext local service. TLS is the
default — plaintext is never reached by omitting a flag.

`certs/` is gitignored and holds development material only. In production the
paths point at certificates issued by your own CA.

### Authentication

Reads are anonymous. The three mutating methods — `CreatePlugin`,
`UpdatePlugin`, `DeletePlugin` — require a write token:

```bash
# Generates the token and prints the config entry that authorises it
easyp-svc auth new-token --name ci
```

The command prints the token once and a `sha256` digest. Only the digest goes
into the configuration, so `deploy/config/config.yml` stays safe to commit; the token belongs
in your secret manager:

```yaml
auth:
  write_tokens:
    - name: "ci"
      sha256: "…"
```

Clients pass it with `--token`, via `EASYP_TOKEN`, or `sdk.WithToken(...)`. It
travels in the `authorization` header, so it is only as protected as the
connection — use it over TLS.

Two properties worth knowing:

- **An empty token list denies every write.** A forgotten configuration breaks
  plugin registration rather than leaving the registry open.
- **Any method not explicitly anonymous requires a token.** A new RPC is
  protected until someone decides otherwise.

The token's name appears in the audit log, so `SELECT metadata FROM audit_log`
shows which credential performed an operation. Multiple tokens let you rotate
without downtime: add the new one, deploy, remove the old.

```yaml
auth:
  # Demand a credential for reads as well — GenerateCode, Plugins and the MCP
  # endpoint. Health is never covered: a probe carries none, and a listener that
  # fails its own readiness check never serves anything.
  #
  # Off by default, because the same binary serves the public plugin catalogue.
  # Turn it on for a private registry, where "readable by anything that can
  # reach the pod" is not a property anyone chose.
  require_authentication: true
```

### Licensing

Without a token the service runs in **community** mode: no audit log, and three
ceilings that a licence lifts —

| Setting | Community | Enterprise |
|---|---|---|
| `worker_pool.workers` | 4 | as configured |
| `worker_pool.max_concurrent_generations` | 16 | as configured |
| registered plugins | 10 | unlimited |

Each community ceiling is the shipped default of the setting it caps, so a
deployment that never changed one is not affected by the ceiling existing. A
configuration above it is lowered at startup, and the service logs which setting
was lowered and to what — it is a ceiling, not a substitution, so asking for
less than the tier permits gives you less.

Enterprise needs two things — a token and the public key it is verified against:

```bash
LICENSE_PUBLIC_KEYS=<kid>:<hex> LICENSE_KEY=<paseto-token> task up
```

Both are read at runtime. The token comes from `license.key`, then
`license.file`, then `LICENSE_KEY`; the public keys from `license.public_keys`
or `LICENSE_PUBLIC_KEYS`. Without a public key no token is honoured.

`license.public_keys` is keyed by the key id in the token's footer. The reserved
key id `"*"` verifies any token the other entries do not cover, which is what the
removed `license.public_key` setting used to do.

This service only verifies licences; it does not issue them. Issuing lives in the
licence registry (`easyp-tech/licenses`), which holds the private signing key,
the record of who was given what, and the `easyp-license` tool that signs.

Several keys can be configured at once, keyed by the key id in the token footer:
`LICENSE_PUBLIC_KEYS="2026-08:<hex>,2026-09:<hex>"`. That is what lets a signing
key be rotated without every deployment having to change key on the same day.
A key that is not a valid hex Ed25519 key stops startup rather than quietly
dropping the service to community mode.

Because the verification key is configuration, whoever can edit `deploy/config/config.yml` can
point the service at a different signing authority — protect that file the way
you protect the database password next to it.

Verification is offline and happens in-process: the PASETO v4.public signature
is checked against the configured public key, and a token that fails — expired,
signed by an unknown key, or malformed — leaves the deployment in community
mode rather than stopping it.

## Plugins

### Naming

A plugin is `{group}/{name}:{version}`: `protocolbuffers/go:v1.36.10`,
`grpc/go:latest`. Group and name are `[a-z][a-z0-9-]*`; the version is `vX.Y`
or `vX.Y.Z` — the patch is optional because protobuf ships versions like
`v33.1` — or `latest`, which resolves to the newest registered version at
call time.

### Plugin Artifact Delivery

The unit of delivery is the **plugin version directory**, not a single file: the contract requires `plugins/{group}/{name}/{version}/plugin` as the entrypoint and allows sidecars next to it (jars, shared libraries, scripts). It is packed as a `tar.gz` and stored at `{group}/{name}/{version}/plugin.tgz`.

```
build machine / CI                       service
──────────────────                       ───────
plugins build   → plugins/{g}/{n}/{v}/…
plugins push    → s3://…/plugin.tgz
plugins register ──── CreatePlugin ────→ streams the archive from S3,
                      (metadata only)    computes sha256, stores it in the DB

                      GenerateCode ────→ entrypoint missing locally?
                                         download archive → verify sha256
                                         → unpack → execute
```

Key properties:

- **Push before register.** Registering a plugin whose archive is absent fails with `FAILED_PRECONDITION` — a registered plugin always has its artifact.
- **The service computes the checksum**, reading the object itself, so a client cannot register a bogus hash. It is re-verified after every download, before anything is executed.
- **Credentials split:** the build pipeline needs S3 write access; the service only needs read (plus delete for `DeletePlugin`). Clients of the gRPC API need no S3 access at all.
- **Concurrent misses collapse** into a single download (singleflight); `plugins_dir` acts as a local cache.
- With S3 disabled, nothing changes from the classic flow: artifacts are read straight from `plugins_dir`.

#### Pushing a packed tree

`plugins pack --out <dir>` writes the same `{group}/{name}/{version}/plugin.tgz` layout to disk, which `plugins push --packed <dir>` uploads as it is. Packing on the build machine and uploading later — or from elsewhere — is then two commands instead of one repeated:

```bash
# Build machine: pack once.
easyp-svc plugins pack plugins --out plugin-archives

# Anywhere with the archives and S3 credentials.
export AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=…
easyp-svc plugins push plugin-archives --packed \
  --endpoint https://storage.example.com --bucket easyp-plugins --force-path-style \
  --parallel 24
```

Uploads run `--parallel` at a time (8 by default). Object storage commonly rate-limits a single connection far below the link it arrives on, so throughput comes from streams rather than from any one of them: measure one stream, then set `--parallel` to about the ratio between your uplink and that figure. An interrupted run is resumed by re-running it — archives already in storage are skipped without being re-read.

The S3 settings can also come from a config file: `plugins push --cfg` accepts a **full server configuration** and runs it through the same validation as `service start` — a fragment holding only `registry.s3` is refused. That is deliberate: a config the server would reject must not quietly keep working for push, or the two stop agreeing about which store they talk to. To push without a server config, pass the storage settings as flags, as above.

### The registry

`registry/` holds the recipe for every plugin easyp-tech publishes: 80 plugins
in ten groups — `protocolbuffers`, `grpc`, `grpc-ecosystem`, `connectrpc`,
`bufbuild`, `pluginrpc`, `apple`, `anthropics`, `googlecloudplatform` and
`community` for everything maintained outside those projects. `ls registry/` is
the current list; the README does not repeat it.

One directory per plugin, no version directories:

```
registry/grpc/go/
├── Dockerfile      # takes ARG VERSION, produces /plugin
├── plugin.yaml     # the versions to build, newest first
└── .dockerignore
```

```yaml
# registry/grpc/go/plugin.yaml
versions:
  - v1.6.2
  - v1.6.1
  - v1.5.1
```

`easyp-svc plugins build registry` reads every `plugin.yaml`, builds each listed
version with `--build-arg VERSION=…`, and extracts the image's filesystem to
`plugins/{group}/{name}/{version}/` — that directory is the artifact,
`plugin` inside it the entrypoint. Optional keys: `build_args` (a map passed to
every build), `dockerfile` (a different file), `args` (arguments the entrypoint
is run with); a version may be a mapping that overrides any of those for itself
or sets `skip: true`.

Filters are globs on `group/name`, optionally with a version:
`--filter 'protocolbuffers/*'`, `--filter 'grpc/go:v1.6.2'`. `--parallel` sets
how many build at once, `--force` rebuilds what is already in `plugins/`, and
`--dry-run` prints the plan. `task build-plugins` is the whole registry;
`FILTER=… task build-plugins-filter` is a slice.

### Contributing a plugin

Add a directory under the group it belongs to — `community/<author>-<tool>` if
the project is not one of the named groups — with a `Dockerfile` and a
`plugin.yaml`. This is the `grpc/go` recipe, and it is the template:

```dockerfile
# syntax=docker/dockerfile:1.23
FROM --platform=$BUILDPLATFORM golang:1.26.3-trixie@sha256:d08bf3ed2bd263088ca8e23fefaf10f1b71769f6932f0a4017ba28d2a5baf001 AS build
ARG VERSION
ARG TARGETOS TARGETARCH

WORKDIR /tmp
RUN git clone --depth 1 --branch cmd/protoc-gen-go-grpc/${VERSION} https://github.com/grpc/grpc-go.git
WORKDIR /tmp/grpc-go/cmd/protoc-gen-go-grpc
RUN --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -o protoc-gen-go-grpc -ldflags "-s -w" -trimpath

FROM scratch
ARG VERSION
COPY --from=build --link /etc/passwd /etc/passwd
COPY --from=build --link --chown=root:root /tmp/grpc-go/cmd/protoc-gen-go-grpc/protoc-gen-go-grpc /plugin
USER nobody
ENTRYPOINT ["/plugin"]
```

What the service needs from the result:

- **The entrypoint is `/plugin`.** It reads a `CodeGeneratorRequest` on stdin,
  writes a `CodeGeneratorResponse` on stdout, and exits non-zero on failure.
  Sidecars next to it are fine — a jar, a `node_modules`, a shared library —
  and are packed with it.
- **`ARG VERSION` selects what to build.** The Dockerfile is one recipe for
  every version in `plugin.yaml`; a version that needs a different recipe gets
  its own `dockerfile:` entry.
- **The final stage is `scratch` or distroless, and runs as `nobody`.** The
  build stage's `/etc/passwd` is copied across for that. Interpreted plugins
  (Node, Python, the JVM) use the distroless runtime images — `bufbuild/es`,
  `community/nipunn1313-mypy` and `apple/servicetalk` show the shape.
- **Base images are pinned by digest.** A tag moves; the digest is what was
  built and reviewed.
- **Static where the language allows it.** `CGO_ENABLED=0`, `-trimpath`,
  `-ldflags "-s -w"` for Go.

Then prove it:

```bash
FILTER='grpc/go:v1.6.2' task build-plugins-filter
task up-minimal && task run-local
task register-plugins
easyp --cfg easyp.local.yaml generate
```

Open a pull request with the two files. A request for a plugin someone else
should package is the **plugin request** issue template.

## Operations

### Health and probes

The health listener (`server.port.health`) serves two paths:

| Path | Answers | Use for |
|------|---------|---------|
| `/live` | 200 as soon as the listener is bound | liveness: restart the process if this fails |
| `/` | 200 once Postgres answers, 503 while starting or while the database is unreachable | readiness: take the pod out of rotation |

`easyp-svc health` probes `/live` and exits 0 or 1. It backs the image's
`HEALTHCHECK` and, having no command line of its own, finds the health port
through `EASYP_CONFIG` — set that on the container to the same file the
service starts with, or the probe falls back to the default port and reports a
container unhealthy while it serves normally. It deliberately probes liveness,
not readiness: readiness checks the database, and restarting a container on
every database blip turns a recoverable outage into a crash loop. The Helm
chart wires its own probes to the two paths and ignores the `HEALTHCHECK`, as
Kubernetes does.

### Metrics

Every metric carries the `easyp` namespace; the gRPC ones an `api` subsystem.
The ones an operator watches:

| Metric | What it says |
|--------|--------------|
| `easyp_api_grpc_server_handled_total{grpc_code}` | request count by outcome |
| `easyp_generation_duration_seconds{plugin}` | how long plugins take |
| `easyp_generation_errors_total{plugin,error_type}` | failed generations; the error-rate alert divides this by `easyp_pool_jobs_total` |
| `easyp_pool_active_workers`, `easyp_pool_queue_depth`, `easyp_pool_rejected_total` | the worker pool: busy, waiting, turned away |
| `easyp_plugin_cache_bytes`, `easyp_plugin_cache_limit_bytes`, `easyp_plugin_cache_evictions_total` | the local archive cache against `registry.cache_max_bytes` |
| `easyp_audit_events_lost_total{reason}` | audit entries dropped — a gap in something Enterprise sells, hence its alert |
| `easyp_license_valid`, `easyp_license_expiry_timestamp_seconds`, `easyp_license_in_grace` | the licence, and when it runs out |
| `easyp_auth_failures_total{reason}`, `easyp_rate_limit_requests_total`, `easyp_concurrency_rejected_total` | who was refused, and why |
| `easyp_panics_total` | recovered panics; any value but zero is a bug report |

The chart's `PrometheusRule` alerts on these, and every alert has a section in
the [Runbooks](https://easyp.tech/docs/api-service/runbooks).

### Upgrading

From v1.0.0 the wire contract, the configuration keys, the error reasons and
the public API of the `api` and `sdk` modules are frozen: changing any of them
is a major version. The full list, and what remains possible without one, is at
the top of [Upgrading](https://easyp.tech/docs/api-service/upgrading).

Releases that need more than a new image are described there, newest first.
**v0.14.0 is the one to read** if you are coming from anything older: the gRPC
service was renamed from `ServiceAPI` to `GeneratorAPI`, which broke every
client at once — `easyp` before v0.17.0 included — and the `api` and `sdk`
modules were split out. v0.13.0 renamed two environment variables and made the
MCP endpoint opt-in; v1.0.2 added `EASYP_CONFIG` for the image's health probe.

What a backup has to contain and how to restore it is in
[Backup and restore](https://easyp.tech/docs/api-service/backup).

## Development

### Building and testing

```bash
go build -o bin/easyp-svc ./cmd/easyp-svc
./bin/easyp-svc service start --cfg deploy/config/config.local.yml

# The level is a setting, so it needs no flag; --log_level still overrides it.
LOG_LEVEL=debug ./bin/easyp-svc service start --cfg deploy/config/config.local.yml

go test ./...                                   # unit tests, all three modules from the root
go test -p 1 -tags integration ./test/... ./internal/database/...   # needs a Postgres; -p 1 as in CI
golangci-lint run ./...                         # CI pins the latest v2
```

### The two-tier stack

`deploy/docker-compose.dev.yml` runs a community container and an enterprise
container side by side, from the same image, so a licence change can be seen
rather than assumed. It needs `deploy/.env.dev` — copy `.env.dev.example` and
fill in the licence and the storage keys it explains.

```bash
task up-dev            # the two tiers
task up-dev-full       # plus the observability overlay
task tier-dev          # assert the two containers really are different tiers
task logs-dev
task down-dev
```

`task deploy-dev HOST=user@host` ships `deploy/` to a stand over rsync — never
the certificates or env files — and rolls the stack to the version this
repository's compose file names, waiting for both containers to report healthy.
The stand keeps its own `.env` and certificates; it is not a git checkout.

### Regenerating the API

`api/` is generated from the proto by easyp itself, through the public stand,
and CI fails if the committed code differs from a fresh run:

```bash
easyp --cfg easyp.yaml generate
go run -tags mcpgen ./test/mcpgen
```

The generator's version is part of the generated header, so bumping the `easyp`
CI uses means regenerating even when the proto did not change.

### Project structure

```
api/                    # The wire contract. Its own Go module, Apache-2.0.
  easyp/generator/v1/   # .proto, generated stubs, MCP bindings
cmd/easyp-svc/          # The service and its CLI (service, plugins, auth, api, config, health)
cmd/mcp-smoke/          # MCP smoke test client
internal/
  core/                 # Domain types, interfaces, sentinel errors, worker pool
  api/                  # gRPC handlers, audit & license interceptors, MCP handler
  adapters/             # audit/ metrics/ registry/ storage/ — implement core interfaces
  auth/                 # Authenticator interface, static write-token digests, Actor
  config/               # Loader, diagnostics, env binding, retired keys, aliases
  database/             # sqlx wrapper (metrics/tracing), connectors
    goosemigrate/       # Embedded SQL migrations (goose v3, advisory-locked)
  grpchelper/           # gRPC server/client factories, middleware
  license/              # PASETO v4 management, FeatureGate, claims
  plugarchive/          # tar.gz pack/unpack with the path and symlink rules
  ratelimiter/          # Per-IP token bucket and concurrency cap, both gated
  safe/                 # Panic-guarded goroutine helper
  serve/                # HTTP and gRPC listener lifecycles
  telemetry/            # OTLP + Pyroscope, tracing decorators
  monitor/              # Context-aware slog logger
  flags/                # CLI flag types
sdk/                    # Go client SDK. Its own module, Apache-2.0.
registry/               # Plugin recipes: one directory per plugin, Dockerfile + plugin.yaml
plugins/                # Built plugin binaries (gitignored)
test/                   # Integration tests (build tag `integration`) and CI helpers
test_registry/          # Plugin recipes the tests build against
deploy/
  docker-compose.yml               # Full dev stack
  docker-compose.dev.yml           # Community and enterprise side by side
  docker-compose.observability.yml # Overlay for the two-tier stack
  docker-compose.public.yml        # Overlay that puts the two-tier stack on the internet
  .env.example, .env.dev.example   # Templates for the two stacks
  config/                          # Service configs, one per way of running it
  observability/                   # Alloy, Grafana, Loki, Tempo, Mimir, Pyroscope, Traefik
  charts/easyp-service/            # Helm chart
  scripts/                         # gen-dev-certs.sh, check-tiers.sh, deploy-dev.sh
  certs/                           # Throwaway dev TLS material (gitignored)
easyp.yaml, easyp.local.yaml, easyp.*.dev.yaml   # easyp configs: CI, from-source, the two tiers
Taskfile.yml
```

## Known limitations

Things this service does not do, written down because a version number invites
people to assume otherwise. None of these is a bug report; they are the shape of
the thing as it stands.

### Plugins are not sandboxed

A plugin is a binary the service runs as a local child process. Not a container.
The service bounds its **time** (`worker_pool.generation_timeout`), its
**concurrency** (`worker_pool.max_concurrent_generations`), the **size of its
output** (`registry.max_output_size`), its **environment** (only what the
plugin's own config declares — the service's database and object-storage
credentials are not inherited), and **where its executable may live** (inside
`registry.plugins_dir`).

It does not bound memory, CPU, filesystem access beyond the service account's
own, or **network access**: a plugin reaches whatever the pod reaches.

So registering a plugin is as privileged as running code on the host. Treat the
write token accordingly, and treat a shared registry as a set of people who all
trust each other.

### There is no user model

One list of write tokens (`auth.write_tokens`), stored as digests. No tenants,
no roles, no per-plugin ownership. The audit trail records a token's *label* and
the caller's IP, so two engineers sharing a CI token are indistinguishable in it.

`GenerateCode` and `Plugins` are anonymous **by default**, because the same
binary serves the public catalogue, where demanding a credential to fetch a
well-known plugin would break every client. Set
`auth.require_authentication: true` and every RPC but health needs a write
token — including the MCP endpoint, which is plain HTTP outside the interceptor
chain and is wrapped separately.

That is a single shared credential, not identity: it decides *whether* a caller
may read, never *which* caller is reading.

### It does not run in more than one replica

The database side is safe: migrations take a Postgres session lock and audit
partition maintenance takes an advisory lock, so several processes cannot
collide there.

The plugin cache cannot. Unpacking finishes with a remove followed by a rename —
two steps, not atomic — and the only thing serialising it is an in-process
lock. Two pods on one `ReadWriteMany` volume race: one can delete a directory
the other is reading, and a rename onto a directory recreated in between fails.
Each pod also keeps its own in-memory accounting of the same shared bytes, so
`registry.cache_max_bytes` is applied once per pod to one volume.

The chart's defaults are honest about this — one replica, `ReadWriteOnce`,
`Recreate`. It *permits* `replicaCount > 1` with a `ReadWriteMany` volume, and
that combination is **not supported**: it will appear to work and corrupt the
cache under concurrent misses for the same plugin. Scale by making the single
pod bigger — `maxConcurrentGenerations` and CPU — not by adding pods.

### There is no down-migration path

Migrations run forward automatically at startup. The down-migrations are written
and tested, and nothing can run them: there is no `migrate` subcommand.

Rolling back to an older image is permitted rather than blocked — an older
binary starts against a newer database and applies nothing. Its limit is time:
it has no maintainer for partitions introduced by a migration it does not know,
so once the pre-created months run out, audit rows land in the default
partition, which then blocks creating those months on the way forward. Treat a
rollback as bounded by `audit.pre_create_months` (three by default). See
[Backup and restore](https://easyp.tech/docs/api-service/backup).

### The licence trust anchor is your own configuration

Licence verification is real — PASETO v4 public-key signatures, issuer and
audience checked, expiry with the grace period the token carries. The public
half is supplied by `license.public_keys`, which is part of the deployment's
configuration. Whoever can edit those values decides which authority may issue
licences for that installation.

### Mutual TLS checks the certificate authority, not the caller

Mutual TLS is real: with `server.tls.client_ca_file` set, the listener requires a
client certificate, verifies it chains to that CA, and — through Go's TLS stack —
requires the `clientAuth` extended key usage, so a server or edge certificate
from the same CA is refused.

What it does not do is look at who the certificate says it is. No common name,
no SAN allow-list, no mapping to an identity: the audit trail still records the
write token's label, not the certificate's subject. So the CA named there is the
entire check, and every certificate it issues for client authentication is a
write credential for this installation.

### The Go client's stability is the wire contract's stability

`sdk.Client.ListPlugins` returns `[]*generator.PluginInfo` — the generated type
from the `api` module, not a type the SDK owns. That is the usual shape for a
gRPC client and it avoids a mirror of every message, but the consequence is
permanent: a v2 of the wire contract is a v2 of the client, whatever else the
client's own surface does. The two modules version separately and are released
in lockstep for that reason.

`ListPlugins` also walks every page and returns the whole registry. Each request
gets its own timeout, so the walk finishes on a large registry, but there is no
way to ask for one page — a caller that wants to stream results needs a method
that does not exist yet.

### The Go client carries the MCP libraries

`sdk` depends on `api`, and the generated MCP tool registration lives in the
same Go package as the wire types — protoc-gen-mcp writes into the proto's own
package and offers no way to put it elsewhere. So an SDK consumer links the MCP
SDK, a JSON-schema library and OAuth2 whether or not they use any of it. Moving
that registration into a library both this service and `easyp` can depend on is
planned, and is a change to `api`'s dependencies rather than to its API.

## Releases and versioning

The repository holds three Go modules — the service, `api/` and `sdk/` — tagged
in lockstep as `vX.Y.Z`, `api/vX.Y.Z` and `sdk/vX.Y.Z`, so that one version
number means one state of all three. Only the service module is under the
Elastic License; see [License](#license).

A release tag produces, in this order:

1. binaries for linux and darwin, amd64 and arm64, each with an SBOM
   (`*.sbom.json`) beside it;
2. the image, built for both architectures and **scanned by Trivy before
   anything is pushed** — a HIGH or CRITICAL finding stops the release rather
   than getting pulled after the fact;
3. the image and its manifest list, signed with cosign in keyless mode — the
   signature names the workflow that produced it, not a key anyone holds;
4. the Helm chart, pushed to `oci://ghcr.io/easyp-tech/charts/easyp-service`;
5. the GitHub release with `checksums.txt`.

To verify an image before running it:

```bash
cosign verify ghcr.io/easyp-tech/service:v1.0.2 \
  --certificate-identity-regexp '^https://github.com/easyp-tech/service/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

`edge` and `sha-<short>` tags come from a separate workflow on every push to
`master`; they are built, not released — no SBOM, no signature, no chart.

## Troubleshooting

```bash
# The stack
docker compose -f deploy/docker-compose.yml ps
docker compose -f deploy/docker-compose.yml logs service

# What the service resolved its configuration to, and from where
easyp-svc config print --cfg deploy/config/config.yml --origin

# Plugins: what is built, what is registered
ls plugins/
easyp-svc api descriptor -o api.protoset
grpcurl -protoset api.protoset -cacert deploy/certs/ca.crt \
  easyp.api.localhost:4443 easyp.generator.v1.GeneratorAPI/Plugins
grpcurl -protoset api.protoset -plaintext \
  localhost:8080 easyp.generator.v1.GeneratorAPI/Plugins        # from-source service

# The database
docker exec -it easyp-postgres psql -U easyp_svc -d easyp_db -c 'SELECT group_name, name, version FROM plugins;'
```

`task up` failing with `ports are not available … 9001` means something on the
host already listens there; `EASYP_RUSTFS_CONSOLE_PORT=9011 task up` moves the
object store's console aside. Nothing on the host needs it.

A `CreatePlugin` that fails with `FAILED_PRECONDITION` means the archive was
never pushed: `task push-plugins` comes before `task register-plugins`.
`plugins register` paces itself when the server's rate limit refuses a burst,
but its default `--parallel` of 8 against a server left at the default
`rate_limit.max_concurrent_per_ip` of 2 still loses most of the batch — the
configs under `deploy/` raise the cap; against any other server pass
`--parallel 2`. A
`GenerateCode` that fails with `GENERATION_FAILED` and a plugin's stderr in the
message is the plugin refusing the input — a proto without `go_package`, for
instance — not the service.

## License

EasyP Service is **source available**, not open source.

| Part | License |
|------|---------|
| `api/` — the generated gRPC contract | [Apache License 2.0](api/LICENSE) |
| `sdk/` — the Go client library | [Apache License 2.0](sdk/LICENSE) |
| Everything else — the service itself | [Elastic License 2.0](LICENSE) |

The Elastic License 2.0 lets you use, copy, modify and redistribute the service
free of charge, production included. It forbids three things: offering the
service to third parties as a hosted or managed service, circumventing the
license key mechanism that gates Enterprise features (see [Licensing](#licensing)),
and removing license notices.

Community mode needs no license key and stays free under those terms. What
Enterprise adds today is the audit log and the removal of the three community
ceilings — 4 workers, 16 concurrent generations, 10 registered plugins — as
the [Licensing](#licensing) table says.

The client SDK and the API contract it is generated from are both Apache 2.0, so
they can be imported into your own code without inheriting any of the above. The
two go together deliberately: `sdk/` imports `api/`, and licensing only the
client would leave anyone writing one compiling Elastic-licensed code anyway.
Talking to this service is not restricted; running it is.

Releases up to and including `v0.8.0` were published under Apache 2.0 and remain
available under those terms; the Elastic License 2.0 applies from the next
release onward.

## Support

Issues in this repository, with a template for each kind: a
[bug](https://github.com/easyp-tech/service/issues/new?template=bug_report.yml)
(it asks for the version, the licence tier and how the service is deployed —
the three things every diagnosis starts with), a
[feature](https://github.com/easyp-tech/service/issues/new?template=feature_request.yml),
or a [plugin you would like packaged](https://github.com/easyp-tech/service/issues/new?template=plugin_request.yml).
