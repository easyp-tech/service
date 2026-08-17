#!/usr/bin/env bash
#
# Generates a throwaway CA and the three certificates the dev stack needs:
#
#   ca.crt      trust anchor for everything below
#   server.*    served by easyp-api-service on the gRPC port
#   client.*    presented by traefik to easyp-api-service (the mTLS leg)
#   edge.*      served by traefik to the outside world
#
# Who gets what, because it is the whole point of splitting them:
#
#   service   server.crt, server.key, ca.crt
#   traefik   ca.crt, client.crt, client.key, edge.crt, edge.key
#   ca.key    nobody
#
# ca.key signs the rest and is used only here, on the host. It reaches no
# container, and that matters because the service container executes plugin
# binaries: anything mounted beside them is readable by them, and whoever holds
# this key can issue a client certificate the service will accept — the mTLS
# boundary is checked for a signature by this CA and nothing else.
#
# The other keys are deliberately left world-readable: the service container
# runs as UID 65532 and reads them through a bind mount, where host ownership
# carries over on Linux. That is a development compromise. Production
# certificates come from your own CA and are mounted with restrictive modes.

set -euo pipefail

# Resolved from this script rather than from the caller's directory: the compose
# files mount ./certs relative to deploy/, so writing them anywhere else produces
# a stack that starts and then fails its TLS handshake with nothing obvious to
# look at.
DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CERT_DIR="${CERT_DIR:-$DEPLOY_DIR/certs}"
DAYS="${DAYS:-825}"

mkdir -p "$CERT_DIR"
cd "$CERT_DIR"

if [ -f ca.crt ] && [ "${FORCE:-0}" != "1" ]; then
    echo "certs already exist in $CERT_DIR (FORCE=1 to regenerate)"
    exit 0
fi

echo "==> certificate authority"
openssl req -x509 -newkey rsa:4096 -nodes -days "$DAYS" \
    -keyout ca.key -out ca.crt \
    -subj "/CN=easyp-dev-ca" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null

# issue <name> <subject-cn> <san> <extended-key-usage>
issue() {
    local name="$1" cn="$2" san="$3" eku="$4"

    echo "==> $name ($san)"
    openssl req -newkey rsa:2048 -nodes \
        -keyout "$name.key" -out "$name.csr" \
        -subj "/CN=$cn" 2>/dev/null

    openssl x509 -req -in "$name.csr" -days "$DAYS" \
        -CA ca.crt -CAkey ca.key -CAcreateserial \
        -out "$name.crt" \
        -extfile <(printf 'subjectAltName=%s\nextendedKeyUsage=%s\n' "$san" "$eku") 2>/dev/null

    rm -f "$name.csr"
}

# The server certificate covers every name the service is reached by: the
# compose service name (what traefik dials), the container name, and localhost
# for a direct connection from the host.
issue server easyp-api-service \
    "DNS:service,DNS:easyp-api-service,DNS:localhost,IP:127.0.0.1" \
    "serverAuth"

# Traefik's identity on the backend leg. The service accepts it because it is
# signed by the CA above; nothing else about it is checked.
issue client traefik "DNS:traefik,DNS:easyp-traefik" "clientAuth"

# The public-facing certificate. Clients reach the stack at this name.
issue edge easyp.api.localhost \
    "DNS:easyp.api.localhost,DNS:localhost,IP:127.0.0.1" \
    "serverAuth"

chmod 644 ./*.key ./*.crt

# The CA's private key is the exception, and the reason for the two lines: it is
# mounted into no container, so nothing needs to read it but this script, and
# leaving it world-readable next to keys that must be would invite it back into
# a mount by resemblance. ca.srl is not secret but is equally useless in a
# container, so it goes the same way.
chmod 600 ./ca.key ./ca.srl 2>/dev/null || chmod 600 ./ca.key

echo
echo "Wrote $(pwd)"
echo "Register plugins through traefik with:"
echo "  --addr easyp.api.localhost:\${EASYP_TRAEFIK_TLS_PORT:-4443} --tls-ca $CERT_DIR/ca.crt"
