#!/usr/bin/env bash
set -euo pipefail

HOST="${1:?usage: deploy-dev.sh user@host}"
REMOTE_DIR="${REMOTE_DIR:-easyp}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

rsync -az --delete \
  --exclude 'certs/' \
  --exclude '.env' \
  --exclude '.env.dev' \
  --exclude 'plugins-community/' \
  --exclude 'plugins-enterprise/' \
  --exclude 'charts/' \
  "$REPO/deploy/" "$HOST:$REMOTE_DIR/"

ssh "$HOST" bash -s "$REMOTE_DIR" <<'REMOTE'
set -euo pipefail
cd "$1"

compose() {
  docker compose \
    -f docker-compose.dev.yml \
    -f docker-compose.observability.yml \
    -f docker-compose.public.yml \
    --env-file .env.dev "$@"
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
