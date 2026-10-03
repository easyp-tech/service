# syntax=docker/dockerfile:1.19
FROM node:24.11.1-bookworm AS build
ARG VERSION

ARG TARGETARCH

WORKDIR /build
COPY --link package* .
RUN npm ci \
 && cp /build/node_modules/grpc-tools/bin/grpc_node_plugin /build

FROM gcr.io/distroless/cc-debian12:latest@sha256:0000f9dc0290f8eaf0ecceafbc35e803649087ea7879570fbc78372df7ac649b AS base
ARG VERSION

FROM scratch
ARG VERSION
COPY --link --from=base / /
COPY --link --from=build --chmod=0755 /build/grpc_node_plugin .
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
exec "$DIR/grpc_node_plugin" "$@"
EOF
USER nobody
ENTRYPOINT ["/plugin"]
