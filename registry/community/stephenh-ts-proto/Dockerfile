# syntax=docker/dockerfile:1.12
FROM node:22.14.0-bookworm AS build
ARG VERSION
WORKDIR /app
COPY --link package*.json .
RUN npm ci
RUN sed -i -e 's|/usr/bin/env node|/nodejs/bin/node|g' /app/node_modules/ts-proto/protoc-gen-ts_proto \
 && rm -f /app/node_modules/dprint-node/*darwin*.node /app/node_modules/dprint-node/*win32*.node

FROM gcr.io/distroless/nodejs22-debian12:latest@sha256:881157f8399d3ab71c54068f148c25296f7f9bee6d36279febad5a6f46f41c2b AS node
ARG VERSION

FROM gcr.io/distroless/cc-debian12:latest@sha256:b7550f0b15838de14c564337eef2b804ba593ae55d81ca855421bd52f19bb480 AS base
ARG VERSION

FROM scratch
ARG VERSION
COPY --link --from=base / /
COPY --link --from=node --chmod=0755 /nodejs/bin/node /nodejs/bin/node
COPY --link --from=build /app /app
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
exec "$DIR/nodejs/bin/node" "$DIR/app/node_modules/.bin/protoc-gen-ts_proto" "$@"
EOF
USER nobody
ENTRYPOINT ["/plugin"]
