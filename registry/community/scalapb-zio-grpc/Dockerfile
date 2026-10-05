# syntax=docker/dockerfile:1.10
FROM debian:bookworm-20241016 AS build
ARG VERSION

ARG TARGETARCH

RUN apt-get update \
 && apt-get install -y curl

#This script embeds the the .class files and is a self contained jvm protoc plugin. See https://scalapb.github.io/docs/scalapbc/#using-scalapb-as-a-proper-protoc-plugin for more details
RUN curl -fsSL -o protoc-gen-zio.jar https://repo1.maven.org/maven2/com/thesamet/scalapb/zio-grpc/protoc-gen-zio/${VERSION#v}/protoc-gen-zio-${VERSION#v}-unix.sh

FROM gcr.io/distroless/java17-debian12:latest@sha256:26054428ef0fa1b71d28018e35823060c9e89d4b2f120d8efe1964669f44fccc AS base
ARG VERSION

FROM scratch
ARG VERSION
COPY --from=base --link / /
COPY --from=build --link /protoc-gen-zio.jar .
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
for JAVA in "$DIR"/usr/lib/jvm/*/bin/java; do exec "$JAVA" -jar "$DIR/protoc-gen-zio.jar" "$@"; done
echo "no JRE in $DIR/usr/lib/jvm" >&2
exit 127
EOF
USER nobody
ENTRYPOINT ["/plugin"]
