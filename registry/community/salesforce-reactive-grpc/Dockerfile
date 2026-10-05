# syntax=docker/dockerfile:1.15
FROM debian:bookworm-20250610 AS build
ARG VERSION

WORKDIR /app
RUN apt-get update && apt-get install -y curl
RUN curl -fsSL -o reactor-grpc-protoc.jar https://repo1.maven.org/maven2/com/salesforce/servicelibs/reactor-grpc/${VERSION#v}/reactor-grpc-${VERSION#v}.jar

FROM gcr.io/distroless/java21-debian12:latest@sha256:7c9a9a362eadadb308d29b9c7fec2b39e5d5aa21d58837176a2cca50bdd06609 AS base
ARG VERSION

FROM maven:3.9.11-eclipse-temurin-21 AS maven-deps
ARG VERSION
COPY pom.xml /tmp/pom.xml
RUN cd /tmp && mvn -f pom.xml dependency:go-offline

FROM scratch
ARG VERSION
COPY --from=base --link / /
COPY --from=build --link --chmod=0755 --chown=root:root /app/reactor-grpc-protoc.jar .
COPY --from=maven-deps /root/.m2/repository /maven-repository
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
for JAVA in "$DIR"/usr/lib/jvm/*/bin/java; do exec "$JAVA" -jar "$DIR/reactor-grpc-protoc.jar" "$@"; done
echo "no JRE in $DIR/usr/lib/jvm" >&2
exit 127
EOF
USER nobody
ENTRYPOINT ["/plugin"]
