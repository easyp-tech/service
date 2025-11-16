FROM golang:1.25-alpine3.22 AS build
RUN --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 \
    go install -ldflags "-s -w" -trimpath github.com/pseudomuto/protoc-gen-doc/cmd/protoc-gen-doc@v1.5.1

FROM scratch
COPY --from=build --link --chown=root:root /etc/passwd /etc/passwd
COPY --from=build --link --chown=root:root /go/bin/protoc-gen-doc /
USER nobody
ENTRYPOINT [ "/protoc-gen-doc" ]
