#!/usr/bin/env bash
# Helper: boot postgres+redis, wait for a running API, seed the default world.
# Start the API first in another shell: cd go && go run ./cmd/api
# (Full stack incl. built images: docker compose up --build -d ; see make up.)
set -euo pipefail
BASE="${BASE_URL:-http://127.0.0.1:8080}"
CURL="curl -sf -m 15"

docker compose up -d postgres redis
echo "waiting for API at $BASE ..."
for i in $(seq 1 30); do
  if $CURL "$BASE/healthz" >/dev/null 2>&1; then break; fi
  if [ "$i" = 30 ]; then
    echo "API never became healthy at $BASE. Start it with: cd go && go run ./cmd/api" >&2
    exit 1
  fi
  sleep 2
done
$CURL -X POST "$BASE/admin/seed/world" -H 'Content-Type: application/json' \
  -d '{"seed":42,"restaurants":40,"riders":120}'
echo "seeded"
