# syntax=docker/dockerfile:1.4
FROM python:3.11.4-alpine3.18 AS build
ARG VERSION
WORKDIR /app
RUN python -mvenv /app
ADD /requirements.txt requirements.txt
RUN source ./bin/activate \
 && pip install --no-cache-dir -r requirements.txt \
 && pip uninstall --yes pip setuptools \
 && rm -f requirements.txt bin/activate.fish bin/activate.csh bin/Activate.ps1

FROM python:3.11.4-alpine3.18
ARG VERSION
COPY --from=build --link /app /app
COPY --chmod=0755 <<"EOF" /plugin
#!/bin/sh
# Paths are relative to this script: the service runs the bundle from its
# plugins directory, not from the image it was built in.
DIR=${0%/*}
# Alpine build: run under the bundle's own musl loader, the service image is glibc.
PYTHONHOME="$DIR/usr/local" PYTHONPATH="$DIR/app/lib/python3.11/site-packages" \
  exec "$DIR"/lib/ld-musl-*.so.1 --library-path "$DIR/lib:$DIR/usr/lib:$DIR/usr/local/lib" "$DIR/usr/local/bin/python3.11" "$DIR/app/bin/protoc-gen-python_betterproto" "$@"
EOF
USER nobody
# Plugin uses os.makedirs - needs to run in a directory it can write to
WORKDIR /tmp
ENTRYPOINT ["/plugin"]
