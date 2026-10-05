# syntax=docker/dockerfile:1.12
FROM node:22.14.0-bookworm AS build
ARG VERSION
WORKDIR /app
COPY --link package*.json .
RUN npm ci \
 && ./node_modules/.bin/esbuild ./node_modules/.bin/protoc-gen-ts --bundle --external:typescript --platform=node --outfile=protoc-gen-ts.js

FROM gcr.io/distroless/nodejs22-debian12:latest@sha256:176a1a417bd00cf01952c2854a3ff0b11bfb118ff91a7ab0b7307899df239d4e AS node
ARG VERSION

FROM gcr.io/distroless/cc-debian12:latest@sha256:85dac24dd2f03e841d986d5ed967385d3a721dcd9dbd21b602ddd82437f364c9 AS base
ARG VERSION

FROM scratch
ARG VERSION
COPY --link --from=base / /
COPY --link --from=node --chmod=0755 /nodejs/bin/node /nodejs/bin/node
COPY --link --from=build --chmod=0755 /app/protoc-gen-ts.js /app/protoc-gen-ts.js
COPY --link --from=build /app/node_modules/typescript /app/node_modules/typescript
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
exec "$DIR/nodejs/bin/node" "$DIR/app/protoc-gen-ts.js" "$@"
EOF
USER nobody
ENTRYPOINT ["/plugin"]
