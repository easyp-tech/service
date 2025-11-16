FROM golang:alpine3.22 AS builder

RUN apk update && apk add --no-cache ca-certificates

COPY . /app

WORKDIR /app

RUN go build -o back ./cmd/main.go

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/back /back
COPY migrate migrate/

ENTRYPOINT ["/back"]
