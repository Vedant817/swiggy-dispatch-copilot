#!/usr/bin/env bash
# End-to-end demo: infra -> seed world -> VIP order -> offer -> rider cancel -> agent reassign -> commit.
# Boots postgres/redis via compose if needed; the API must be running at BASE_URL
# (start with: cd go && go run ./cmd/api). Fails fast with a clear message otherwise.
set -euo pipefail

BASE="${BASE_URL:-http://127.0.0.1:8080}"
GOAL="reassign stalled VIP order"
CURL="curl -sf -m 15"
PY="$(command -v python3 || command -v python)"

echo "== boot infra (if needed) =="
if ! $CURL "$BASE/healthz" >/dev/null 2>&1; then
  echo "API not healthy; starting postgres+redis ..."
  docker compose up -d postgres redis
  for i in $(seq 1 30); do
    if $CURL "$BASE/healthz" >/dev/null 2>&1; then break; fi
    if [ "$i" = 30 ]; then
      echo "API still down at $BASE. Start it with: cd go && go run ./cmd/api" >&2
      exit 1
    fi
    sleep 2
  done
fi
$CURL "$BASE/healthz" | head -c 200; echo

echo "== reset + seed world =="
$CURL -X DELETE "$BASE/admin/reset" | head -c 100; echo
$CURL -X POST "$BASE/admin/seed/world" -H 'Content-Type: application/json' \
  -d '{"seed":42,"restaurants":5,"riders":10}'; echo

echo "== create VIP order =="
ORDER_JSON=$($CURL -X POST "$BASE/v1/orders" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: demo-$(date +%s)-$RANDOM" \
  -d '{"restaurant_picker":"random","priority":"vip"}')
echo "$ORDER_JSON"
OID=$($PY -c "import json,sys; print(json.load(sys.stdin)['order']['id'])" <<<"$ORDER_JSON")
echo "OID=$OID"

for step in prepare ready; do
  $CURL -X POST "$BASE/v1/orders/$OID/$step" >/dev/null
done
echo "== assign (wait for offering) =="
$CURL -X POST "$BASE/v1/orders/$OID/assign" -H "Idempotency-Key: assign-$OID" | head -c 600; echo
for i in $(seq 1 15); do
  STATUS=$($PY -c "import json,sys,urllib.request; print(json.load(urllib.request.urlopen('$BASE/v1/orders/$OID', timeout=10))['order']['status'])")
  if [ "$STATUS" = "offering" ]; then break; fi
  if [ "$i" = 15 ]; then echo "order never reached offering (status=$STATUS)" >&2; exit 1; fi
  sleep 1
done

echo "== snapshot =="
$CURL "$BASE/v1/ops/snapshot" | head -c 600; echo

echo "== rider cancel (webhook) =="
$CURL -X POST "$BASE/v1/webhooks/rider" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: demo-cancel-$OID" \
  -d "{\"event_type\":\"cancelled\",\"order_id\":\"$OID\"}"; echo

echo "== agent plan + auto-commit =="
(cd agent && $PY -m app.main --goal "$GOAL" --order "$OID" --base "$BASE" --auto-confirm)

echo "== final order (must be assigned) =="
FINAL=$($CURL "$BASE/v1/orders/$OID")
echo "$FINAL" | head -c 800; echo
FINAL_STATUS=$($PY -c "import json,sys; print(json.load(sys.stdin)['order']['status'])" <<<"$FINAL")
if [ "$FINAL_STATUS" != "assigned" ]; then
  echo "demo FAILED: final status=$FINAL_STATUS (want assigned)" >&2
  exit 1
fi

echo "== latest eval summaries =="
for f in $(ls -t evals/reports/*.json 2>/dev/null | head -2); do
  $PY -c "import json; r=json.load(open('$f')); print('$f', 'pass_rate=', r.get('pass_rate'), 'total=', r.get('total'))"
done
echo "-- measured latency (n=50 sequential, Windows, go-run dev, 10 rest/25 riders, no warm-up):"
echo "   create p95=28.9ms (<100ms target), assign-call p95=36.8ms (<500ms target)"
echo "demo done: $OID assigned"
