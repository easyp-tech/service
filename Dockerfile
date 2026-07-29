# syntax=docker/dockerfile:1.23
FROM golang:1.26-bookworm AS builder

WORKDIR /app

# Dependencies are their own layer, so editing source does not re-download them.
# go.sum has to travel with go.mod or the download runs unverified.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o easyp-svc ./cmd/easyp-svc/

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

# Fixed UID/GID (the distroless "nonroot" values) so a bind-mounted /plugins can
# be chowned to a known owner on the host.
RUN groupadd --gid 65532 nonroot \
 && useradd --uid 65532 --gid 65532 --home-dir /home/nonroot --create-home nonroot \
 && mkdir -p /plugins \
 && chown 65532:65532 /plugins

COPY --from=builder --chown=65532:65532 /app/easyp-svc /easyp-svc

VOLUME ["/plugins"]

USER 65532:65532

ENTRYPOINT ["/easyp-svc"]
