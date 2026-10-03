# syntax=docker/dockerfile:1.23
FROM debian:trixie-20260421@sha256:35b8ff74ead4880f22090b617372daff0ccae742eb5674455d542bef71ef1999 AS build
ARG VERSION

ARG TARGETARCH

WORKDIR /build
RUN apt-get update \
 && apt-get install -y curl
RUN arch=${TARGETARCH}; \
    if [ "${arch}" = "arm64" ]; then\
        arch="aarch_64"; \
    elif [ "${arch}" = "amd64" ]; then\
        arch="x86_64"; \
    fi; \
    echo "${arch}"; \
    curl -fsSL -o protoc-gen-grpc-java https://repo1.maven.org/maven2/io/grpc/protoc-gen-grpc-java/${VERSION#v}/protoc-gen-grpc-java-${VERSION#v}-linux-${arch}.exe

FROM gcr.io/distroless/cc-debian13:latest@sha256:56aaf20ab2523a346a67c8e8f8e8dabe447447d0788b82284d14ad79cd5f93cc AS base
ARG VERSION

FROM maven:3.9.11-eclipse-temurin-21 AS maven-deps
ARG VERSION
COPY pom.xml /tmp/pom.xml
RUN cd /tmp && mvn -f pom.xml dependency:go-offline

FROM scratch
ARG VERSION
COPY --link --from=base / /
COPY --link --from=build --chmod=0755 --chown=root:root /build/protoc-gen-grpc-java .
COPY --from=maven-deps /root/.m2/repository /maven-repository
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
exec "$DIR/protoc-gen-grpc-java" "$@"
EOF
USER nobody
ENTRYPOINT ["/plugin"]
