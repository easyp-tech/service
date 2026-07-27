# syntax=docker/dockerfile:1.22
FROM debian:trixie-20260316@sha256:55a15a112b42be10bfc8092fcc40b6748dc236f7ef46a358d9392b339e9d60e8 AS build
ARG VERSION

ARG TARGETARCH
ARG BAZEL_STARTUP_OPTS="--host_jvm_args=-Djava.net.preferIPv4Stack=true"
ARG BAZEL_JOBS=4
ARG BAZEL_BUILD_OPTS="--spawn_strategy=local --jobs=${BAZEL_JOBS} --features=-layering_check --disk_cache=/build/.cache/bazel-disk --repository_cache=/build/.cache/bazel-repo"

RUN apt-get update \
 && apt-get install -y curl git cmake build-essential autoconf clang libc++-dev libtool pkg-config unzip zip
RUN curl -fsSL -o /usr/local/bin/bazelisk https://github.com/bazelbuild/bazelisk/releases/download/v1.26.0/bazelisk-linux-${TARGETARCH} \
 && chmod +x /usr/local/bin/bazelisk \
 && mkdir -p /build/.cache \
 && chown -R nobody:nogroup /build \
 && usermod --home /build nobody

USER nobody
WORKDIR /build

RUN git clone --depth 1 --branch ${VERSION} https://github.com/grpc/grpc
WORKDIR /build/grpc
# grpc pins its own Bazel toolchain via //tools/bazel: releases up to v1.55.x
# pin Bazel 5.3.0, which has no --host_features (added in Bazel 6.5). Probe for
# the flag at build time instead of hard-coding it, so old and new releases
# share one Dockerfile.
RUN --mount=type=cache,id=bazel-grpc-disk,target=/build/.cache/bazel-disk,uid=65534,gid=65534,sharing=shared \
    --mount=type=cache,id=bazel-grpc-repo,target=/build/.cache/bazel-repo,uid=65534,gid=65534,sharing=shared \
    --mount=type=cache,id=bazelisk-grpc,target=/build/.cache/bazelisk,uid=65534,gid=65534,sharing=shared \
    HOST_FEATURES_OPT="$(bazelisk ${BAZEL_STARTUP_OPTS} help build 2>/dev/null | grep -q -- --host_features && echo --host_features=-layering_check || true)"; \
    bazelisk ${BAZEL_STARTUP_OPTS} build ${BAZEL_BUILD_OPTS} ${HOST_FEATURES_OPT} //src/compiler:grpc_plugin_support \
 && bazelisk ${BAZEL_STARTUP_OPTS} build ${BAZEL_BUILD_OPTS} ${HOST_FEATURES_OPT} //src/compiler:grpc_php_plugin
RUN cp /build/grpc/bazel-bin/src/compiler/grpc_php_plugin_binary /build/grpc_php_plugin_binary 2>/dev/null \
 || cp /build/grpc/bazel-bin/src/compiler/grpc_php_plugin /build/grpc_php_plugin_binary

FROM gcr.io/distroless/cc-debian13:latest@sha256:e1cc90d06703f5dc30ae869fbfce78fce688f21a97efecd226375233a882e62f AS base
ARG VERSION

FROM scratch
ARG VERSION
COPY --link --from=base / /
COPY --link --from=build --chmod=0755 /build/grpc_php_plugin_binary /plugin
USER nobody
ENTRYPOINT ["/plugin"]
