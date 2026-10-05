# syntax=docker/dockerfile:1.10
FROM python:3.11.10-alpine3.20 AS build
ARG VERSION
WORKDIR /app
RUN python -mvenv /app
ADD /requirements.txt requirements.txt
RUN source ./bin/activate \
    && pip install --no-cache-dir -r requirements.txt \
    && pip uninstall --yes pip \
    && rm -f requirements.txt bin/activate.fish bin/activate.csh bin/Activate.ps1 \
    && ln -sf /usr/bin/python /app/bin/python

FROM gcr.io/distroless/python3-debian12:latest@sha256:55283e240bb5bd506e711a395092b61acf1fb14e63977ee24336285dd9e68fb8 AS base
ARG VERSION

FROM scratch
ARG VERSION
COPY --link --from=base / /
COPY --link --from=build --chmod=0755 /app /app
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
# Run under the bundle's own loader and libraries: the interpreter needs
# libexpat and friends the service image does not have, and must not mix its
# glibc with the host's.
for L in "$DIR"/lib/*-linux-gnu; do :; done
PYTHONHOME="$DIR/usr" PYTHONPATH="$DIR/app/lib/python3.11/site-packages" \
  exec "$L"/ld-linux-*.so.* --library-path "$L:$DIR/usr/lib/${L##*/}" "$DIR/usr/bin/python3.11" "$DIR/app/bin/protoc-gen-nanopb" "$@"
EOF
USER nobody
ENTRYPOINT ["/plugin"]
