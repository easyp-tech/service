# syntax=docker/dockerfile:1.23
FROM debian:trixie-20260505@sha256:e2d08da6f42ef4b09b165d55528a12727aeed8240dc9edf888e3ec07e10ef9da AS build
ARG VERSION

WORKDIR /app
RUN apt-get update \
 && apt-get install -y curl
RUN curl -fsSL -o servicetalk-grpc-protoc.jar https://repo1.maven.org/maven2/io/servicetalk/servicetalk-grpc-protoc/${VERSION#v}/servicetalk-grpc-protoc-${VERSION#v}-all.jar

FROM gcr.io/distroless/java25-debian13:latest@sha256:3e0a1496b365a18d2c01ccfe27c8bc93b1a6b8ca7460c02b8badb791bf296fce AS base
ARG VERSION

FROM maven:3.9.11-eclipse-temurin-21 AS maven-deps
ARG VERSION
COPY pom.xml /tmp/pom.xml
RUN cd /tmp && mvn -f pom.xml dependency:go-offline

FROM scratch
ARG VERSION
COPY --from=base --link / /
COPY --from=build --link --chmod=0755 --chown=root:root /app/servicetalk-grpc-protoc.jar .
COPY --from=maven-deps /root/.m2/repository /maven-repository
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
for JAVA in "$DIR"/usr/lib/jvm/*/bin/java; do exec "$JAVA" -jar "$DIR/servicetalk-grpc-protoc.jar" "$@"; done
echo "no JRE in $DIR/usr/lib/jvm" >&2
exit 127
EOF
USER nobody
ENTRYPOINT ["/plugin"]
