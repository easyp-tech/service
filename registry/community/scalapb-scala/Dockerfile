# syntax=docker/dockerfile:1.17
FROM debian:bookworm-20250811 AS build
ARG VERSION

ARG TARGETARCH

RUN apt-get update \
 && apt-get install -y curl

#This script embeds the the .class files and is a self contained jvm protoc plugin. See https://scalapb.github.io/docs/scalapbc/#using-scalapb-as-a-proper-protoc-plugin for more details
RUN curl -fsSL -o protoc-gen-scala.jar https://repo1.maven.org/maven2/com/thesamet/scalapb/protoc-gen-scala/${VERSION#v}/protoc-gen-scala-${VERSION#v}-unix.sh

FROM gcr.io/distroless/java21-debian12:latest@sha256:914d2e4d0aef6afe6167a11de8d87a4bfcd9325f36d1b45c03c04e6f16ba94d8 AS base
ARG VERSION

FROM scratch
ARG VERSION
COPY --link --from=base / /
COPY --link --from=build /protoc-gen-scala.jar .
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
for JAVA in "$DIR"/usr/lib/jvm/*/bin/java; do exec "$JAVA" -jar "$DIR/protoc-gen-scala.jar" "$@"; done
echo "no JRE in $DIR/usr/lib/jvm" >&2
exit 127
EOF
USER nobody
ENTRYPOINT ["/plugin"]
