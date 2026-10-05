# syntax=docker/dockerfile:1.7
FROM node:20.11.1-alpine3.19 AS build
ARG VERSION
WORKDIR /app
COPY --link package*.json .
RUN npm ci \
 && ./node_modules/.bin/esbuild ./node_modules/.bin/protoc-gen-knit-ts --bundle --external:typescript --platform=node --outfile=protoc-gen-knit-ts.js

FROM gcr.io/distroless/nodejs20-debian12:latest@sha256:e0edb89411e3f93863e9bd97c15cddcbac968d7a16b7a82af6ae6b264da65de9 AS node
ARG VERSION

FROM gcr.io/distroless/cc-debian12:latest@sha256:e6ae66a5a343d7112167f9117c4e630cfffcd80db44e44302759ec13ddd2d22b
ARG VERSION
COPY --link --from=node --chmod=0755 /nodejs/bin/node /nodejs/bin/node
COPY --link --from=build --chmod=0755 /app/protoc-gen-knit-ts.js /app/protoc-gen-knit-ts.js
COPY --link --from=build /app/node_modules/typescript /app/node_modules/typescript
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
exec "$DIR/nodejs/bin/node" "$DIR/app/protoc-gen-knit-ts.js" "$@"
EOF
USER nobody
ENTRYPOINT ["/plugin"]
