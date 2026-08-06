#!/usr/bin/env bash
#
# Generates a throwaway CA and the three certificates the dev stack needs:
#
#   ca.crt      trust anchor for everything below
#   server.*    served by easyp-api-service on the gRPC port
#   client.*    presented by traefik to easyp-api-service (the mTLS leg)
#   edge.*      served by traefik to the outside world
#
# These are development credentials only. The keys are deliberately left
# world-readable because the service container runs as UID 65532 and reads them
# through a bind mount, where host ownership carries over on Linux. Production
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

echo
echo "Wrote $(pwd)"
echo "Register plugins through traefik with:"
echo "  --addr easyp.api.localhost:\${EASYP_TRAEFIK_TLS_PORT:-4443} --tls-ca $CERT_DIR/ca.crt"
