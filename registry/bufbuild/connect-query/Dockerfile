# syntax=docker/dockerfile:1.4
FROM node:18.17.1-alpine3.18 AS build
ARG VERSION
WORKDIR /app
COPY --link package*.json .
RUN npm ci
RUN sed -i -e 's|/usr/bin/env node|/nodejs/bin/node|g' /app/node_modules/@bufbuild/protoc-gen-connect-query/bin/protoc-gen-connect-query

FROM gcr.io/distroless/nodejs18-debian11
ARG VERSION
COPY --link --from=build /app /app
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
exec "$DIR/nodejs/bin/node" "$DIR/app/node_modules/.bin/protoc-gen-connect-query" "$@"
EOF
USER nobody
ENTRYPOINT ["/plugin"]
