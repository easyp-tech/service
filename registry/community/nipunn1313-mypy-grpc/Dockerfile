# syntax=docker/dockerfile:1.23
FROM python:3.13.13-trixie@sha256:d52f7ef162003e17330f0dd9c3e0b4ac6775b39ea988be0c23de6d1461191569 AS build
ARG VERSION
WORKDIR /app
RUN python -mvenv /app
ADD /requirements.txt requirements.txt
RUN . ./bin/activate \
 && pip install --no-cache-dir -r requirements.txt \
 && pip uninstall --yes pip setuptools \
 && rm -f requirements.txt bin/activate.fish bin/activate.csh bin/Activate.ps1 \
 && rm -f bin/protoc-gen-mypy \
 && ln -sf /usr/bin/python /app/bin/python

FROM gcr.io/distroless/python3-debian13:latest@sha256:ed3a4beb46f8f8baac068743ba1b1f95ea3f793422129cf6dd23967f779b6018 AS base
ARG VERSION

FROM scratch
ARG VERSION
COPY --link --from=base / /
COPY --link --from=build /app /app
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
# Run under the bundle's own loader and libraries: the interpreter needs
# libexpat and friends the service image does not have, and must not mix its
# glibc with the host's.
for L in "$DIR"/lib/*-linux-gnu; do :; done
PYTHONHOME="$DIR/usr" PYTHONPATH="$DIR/app/lib/python3.13/site-packages" \
  exec "$L"/ld-linux-*.so.* --library-path "$L:$DIR/usr/lib/${L##*/}" "$DIR/usr/bin/python3.13" "$DIR/app/bin/protoc-gen-mypy_grpc" "$@"
EOF
USER nobody
ENTRYPOINT ["/plugin"]
