# syntax=docker/dockerfile:1.23

# Pinned to the machine doing the building, not to the image being built. The
# release builds linux/amd64 and linux/arm64, and without this the arm64 pass
# runs the whole Go toolchain under QEMU — the compiler, not just the output.
# CGO is off, so cross-compiling is a matter of two environment variables and
# the emulator is pure cost.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

# Supplied by BuildKit from the requested platform. They default to the host's
# when nothing is requested, so a plain `docker build` is unaffected.
ARG TARGETOS
ARG TARGETARCH

# The release passes the tag; a plain `docker build` gets "dev". Without this
# the binary has no idea what it is: every log line and every trace reported
# version "dev", so the only way to tell which build was running was to read the
# image label from outside the container.
ARG VERSION=dev

WORKDIR /app

# Dependencies are their own layer, so editing source does not re-download them.
# go.sum has to travel with go.mod or the download runs unverified.
#
# api/ and sdk/ are separate modules that the root module reaches through
# `replace` directives pointing into the working tree. Those directories do not
# exist yet at this point in the build — `COPY . .` comes later — and a replace
# aimed at a missing directory fails the download outright. Copying the two
# manifests first keeps the dependency layer cacheable and satisfies them.
COPY go.mod go.sum ./
COPY api/go.mod api/go.sum ./api/
COPY sdk/go.mod sdk/go.sum ./sdk/
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o easyp-svc ./cmd/easyp-svc/

# No --platform here on purpose: this stage is the image being shipped, so it
# has to be the target's. Only the apt step is emulated, which is seconds.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

# Fixed UID/GID (the distroless "nonroot" values) so a bind-mounted /plugins can
# be chowned to a known owner on the host.
RUN groupadd --gid 65532 nonroot \
 && useradd --uid 65532 --gid 65532 --home-dir /home/nonroot --create-home nonroot \
 && mkdir -p /plugins \
 && chown 65532:65532 /plugins

COPY --from=builder --chown=65532:65532 /app/easyp-svc /easyp-svc

# The Elastic License 2.0 requires that anyone receiving a copy of the software
# also receives the terms. Shipping the text in the image is the only way a
# downstream redistributor can satisfy that without hunting for the repository.
COPY --from=builder /app/LICENSE /LICENSE

VOLUME ["/plugins"]

USER 65532:65532

ENTRYPOINT ["/easyp-svc"]
