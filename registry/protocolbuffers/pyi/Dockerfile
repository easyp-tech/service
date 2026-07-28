# syntax=docker/dockerfile:1.23
ARG BASE_IMAGE=debian:trixie-20260505@sha256:e2d08da6f42ef4b09b165d55528a12727aeed8240dc9edf888e3ec07e10ef9da
FROM ${BASE_IMAGE} AS build
ARG VERSION

ARG TARGETARCH
ARG BAZEL_STARTUP_OPTS="--host_jvm_args=-Djava.net.preferIPv4Stack=true"
ARG BAZEL_JOBS=4
ARG BAZEL_FALLBACK_VERSION=7.6.1
ARG BAZEL_BUILD_OPTS="--spawn_strategy=local --jobs=${BAZEL_JOBS} --disk_cache=/build/.cache/bazel-disk --repository_cache=/build/.cache/bazel-repo"

RUN apt-get update \
 && apt-get install -y curl git cmake build-essential autoconf clang libc++-dev libtool pkg-config unzip zip
RUN curl -fsSL -o /usr/local/bin/bazelisk https://github.com/bazelbuild/bazelisk/releases/download/v1.28.1/bazelisk-linux-${TARGETARCH} \
 && chmod +x /usr/local/bin/bazelisk \
 && mkdir -p /build/.cache \
 && chown -R nobody:nogroup /build \
 && usermod --home /build nobody

USER nobody
WORKDIR /build
RUN curl -fsSL -o protoc.tar.gz https://github.com/protocolbuffers/protobuf/releases/download/${VERSION}/protobuf-pyi-${VERSION#v}.tar.gz \
 || curl -fsSL -o protoc.tar.gz https://github.com/protocolbuffers/protobuf/releases/download/${VERSION}/protobuf-${VERSION#v}.tar.gz \
 || curl -fsSL -o protoc.tar.gz https://github.com/protocolbuffers/protobuf/releases/download/${VERSION}/protobuf-all-${VERSION#v}.tar.gz \
 && tar --strip-components=1 -zxf protoc.tar.gz \
 && rm protoc.tar.gz
# Release tarballs carry no root .bazelversion, so bazelisk would pull the
# newest Bazel, which is too new for these sources. Pin a working toolchain
# unless the source already declares one.
RUN [ -f .bazelversion ] || [ -f .bazeliskrc ] || echo "${BAZEL_FALLBACK_VERSION}" > .bazelversion
# protobuf releases from v29 on ship a .bazelversion pinning Bazel < 9, which
# has no --repo_contents_cache. Probe the toolchain the source actually pins
# and record the result, so old and new releases share one Dockerfile.
RUN --mount=type=cache,id=bazelisk-protoc,target=/build/.cache/bazelisk,uid=65534,gid=65534,sharing=shared \
    bazelisk ${BAZEL_STARTUP_OPTS} help build 2>/dev/null | grep -q -- --repo_contents_cache \
    && echo "--repo_contents_cache=/tmp/bazel-repo-cache" > /build/.bazelopts \
    || : > /build/.bazelopts
RUN --mount=type=cache,id=bazel-protoc-disk,target=/build/.cache/bazel-disk,uid=65534,gid=65534,sharing=shared \
    --mount=type=cache,id=bazel-protoc-repo,target=/build/.cache/bazel-repo,uid=65534,gid=65534,sharing=shared \
    --mount=type=cache,id=bazelisk-protoc,target=/build/.cache/bazelisk,uid=65534,gid=65534,sharing=shared \
    bazelisk ${BAZEL_STARTUP_OPTS} build ${BAZEL_BUILD_OPTS} $(cat /build/.bazelopts) '//:protoc_lib'
COPY BUILD pyi.cc plugins/
RUN --mount=type=cache,id=bazel-protoc-disk,target=/build/.cache/bazel-disk,uid=65534,gid=65534,sharing=shared \
    --mount=type=cache,id=bazel-protoc-repo,target=/build/.cache/bazel-repo,uid=65534,gid=65534,sharing=shared \
    --mount=type=cache,id=bazelisk-protoc,target=/build/.cache/bazelisk,uid=65534,gid=65534,sharing=shared \
    bazelisk ${BAZEL_STARTUP_OPTS} build ${BAZEL_BUILD_OPTS} $(cat /build/.bazelopts) '//plugins:protoc-gen-pyi.stripped'

FROM gcr.io/distroless/cc-debian13:latest@sha256:8b5d1db6d2253036a53cb8362d3e3fa82a7caf84c247772c46a023166c64e977 AS base
ARG VERSION

FROM scratch
ARG VERSION
COPY --from=base / /
COPY --from=build --link --chmod=0755 /build/bazel-bin/plugins/protoc-gen-pyi /plugin
USER nobody
ENTRYPOINT ["/plugin"]
