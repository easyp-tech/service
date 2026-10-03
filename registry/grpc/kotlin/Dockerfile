# syntax=docker/dockerfile:1.18
FROM debian:bookworm-20250908 AS build
ARG VERSION

ARG TARGETARCH

WORKDIR /build
RUN apt-get update \
 && apt-get install -y curl
RUN curl -fsSL -o protoc-gen-grpc-kotlin.jar https://repo1.maven.org/maven2/io/grpc/protoc-gen-grpc-kotlin/${VERSION#v}/protoc-gen-grpc-kotlin-${VERSION#v}-jdk8.jar

FROM gcr.io/distroless/java21-debian12:latest@sha256:418b2e2a9e452aa9299511427f2ae404dfc910ecfa78feb53b1c60c22c3b640c AS base
ARG VERSION

FROM maven:3.9.11-eclipse-temurin-21 AS maven-deps
ARG VERSION
COPY pom.xml /tmp/pom.xml
RUN cd /tmp && mvn -f pom.xml dependency:go-offline

FROM scratch
ARG VERSION
COPY --link --from=base / /
COPY --link --from=build --chmod=0644 --chown=root:root /build/protoc-gen-grpc-kotlin.jar .
COPY --from=maven-deps /root/.m2/repository /maven-repository
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
for JAVA in "$DIR"/usr/lib/jvm/*/bin/java; do exec "$JAVA" -jar "$DIR/protoc-gen-grpc-kotlin.jar" "$@"; done
echo "no JRE in $DIR/usr/lib/jvm" >&2
exit 127
EOF
USER nobody
ENTRYPOINT ["/plugin"]
