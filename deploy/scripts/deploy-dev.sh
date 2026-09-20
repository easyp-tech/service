#!/usr/bin/env bash
set -euo pipefail

HOST="${1:?usage: deploy-dev.sh user@host}"
REMOTE_DIR="${REMOTE_DIR:-easyp}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="${VERSION:-$(sed -nE 's/.*service:\$\{EASYP_SERVICE_VERSION:-([^}]+)\}.*/\1/p' "$REPO/deploy/docker-compose.dev.yml" | head -1)}"
[ -n "$VERSION" ] || { echo "cannot read the image version from deploy/docker-compose.dev.yml" >&2; exit 1; }

# Everything the stand holds that the repository does not is excluded here,
# which also protects it from --delete. traefik.public.yml was missing from
# this list once: the first deploy removed it, traefik kept serving from the
# already-mounted inode, and the next reboot recreated the container against a
# path Docker had turned into an empty directory — the stand's public entry
# was down for two days before anyone noticed.
rsync -az --delete \
  --exclude 'certs/' \
  --exclude '.env' \
  --exclude '.env.dev' \
  --exclude 'observability/traefik/traefik.public.yml' \
  --exclude 'plugins-community/' \
  --exclude 'plugins-enterprise/' \
  --exclude 'charts/' \
  "$REPO/deploy/" "$HOST:$REMOTE_DIR/"

ssh "$HOST" bash -s "$REMOTE_DIR" "$VERSION" <<'REMOTE'
set -euo pipefail
cd "$1"
export EASYP_SERVICE_VERSION="$2"

env_file=.env.dev
[ -f "$env_file" ] || env_file=.env
[ -f "$env_file" ] || { echo "neither .env.dev nor .env in $PWD" >&2; exit 1; }

compose() {
  docker compose \
    -f docker-compose.dev.yml \
    -f docker-compose.observability.yml \
    -f docker-compose.public.yml \
    --env-file "$env_file" "$@"
}

compose pull --quiet service-community service-enterprise
compose up -d --remove-orphans

for _ in $(seq 1 30); do
  c=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' easyp-api-community 2>/dev/null || echo missing)
  e=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' easyp-api-enterprise 2>/dev/null || echo missing)
  if [ "$c" = healthy ] && [ "$e" = healthy ]; then break; fi
  sleep 3
done

echo "community=$c enterprise=$e"
docker ps --filter name=easyp-api --format '{{.Names}}\t{{.Image}}\t{{.Status}}'
docker logs easyp-api-enterprise 2>&1 | grep -oE '"tier":"[a-z]+"' | head -1

[ "$c" = healthy ] && [ "$e" = healthy ]
REMOTE
