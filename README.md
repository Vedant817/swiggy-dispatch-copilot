# Swiggy Dispatch Copilot

A local, synthetic dark-kitchen dispatch system: a Go service owns order orchestration and assignment correctness, while a Python planner proposes rider reassignments that only the Go service can commit. **The agent suggests; the typed service decides.** This is an independent engineering project with generated data, not a connection to any production food-delivery system.

## What it does

- Accepts kitchen orders and moves them through `created → preparing → ready_for_assign → offering → assigned → picked_up → delivered` (plus `cancelled`).
- Offers orders to nearby riders using configurable scoring (distance, load, rating, VIP bonus) with time-limited offers, expiry sweeps, reoffers, and rider-cancel recovery — without double-booking.
- Lets an operations planner inspect live state, explain delays from assignment traces, and propose a reassignment; an authorized operator confirms, and Go commits it atomically (superseding stale offers).
- Generates restaurants, riders, traffic, and evaluation scenarios from seeds so demos and tests are reproducible without real customer data.
- Deduplicates mutating requests with `Idempotency-Key` and inbound rider events with a webhook ledger.

## Architecture

```text
Generator / load driver ──HTTP──> Go API ──transactions──> PostgreSQL
                                  │  │
                                  │  └── coordination ──> Redis
                                  └── durable jobs ──> Go assignment worker

Ops user ──> Python planner ──read/propose HTTP──> Go API
Ops user ──explicit confirmation──> Go policy gate ──> PostgreSQL
```

| Component | Responsibility |
| --- | --- |
| Go HTTP API (`go/cmd/api`, `go/internal/api`) | Orders, riders, assignments, webhooks, proposals, ops reads, auth, idempotency |
| Go worker (`go/cmd/worker`, `go/internal/assign`) | Scoring, time-limited offers, expiry sweeps, reoffers, cancellation recovery |
| PostgreSQL | Authoritative state: orders, assignments (partial unique indexes prevent double-booking), proposals, dedup ledgers, assignment traces |
| Redis | Short-lived `assign:{order}` coordination locks; correctness revalidated in Postgres |
| Go generator (`go/cmd/gen`, `go/internal/gen`) | Seeded world/traffic/burst through public HTTP |
| Python planner (`agent/app`) | Grounded read/propose tools, local policy checks, operator-confirmed commit |
| Eval + load (`evals/`, `load/`) | Generated scenarios against live APIs, k6 latency script |

Correctness rules: at most one active assignment per order and per rider (capacity 1, DB-enforced); Redis leases coordinate but never substitute for a commit; offers and retry intent survive worker restarts; proposal creation never mutates assignments.

## Quickstart

Prerequisites: Go 1.25, Python 3.11+, Docker with Compose, `curl`, and (for `make load`) k6.

```bash
# 1. Start Postgres + Redis, then the API (migrations run on boot)
docker compose up -d postgres redis
cd go && go run ./cmd/api        # :8080, worker loop in-process
# (optional) standalone worker: go run ./cmd/worker

# 2. Seed a world and send traffic
go run ./cmd/gen world  --config ../configs/default.yaml
go run ./cmd/gen traffic --config ../configs/default.yaml --duration 60s

# 3. Plan a reassignment (needs an offering order id)
cd ../agent && python -m app.main --goal "reassign stalled VIP order" --order <ORDER_ID> --auto-confirm

# 4. Verify
make test        # Go unit + integration (serial, needs DB)
make race        # go test -race (Linux toolchain; on Windows use: make race-docker)
make eval        # 24 generated scenarios + 8 agent trajectories
```

Admin routes (`/admin/seed/world`, `/admin/reset`) are local-only and disabled when `APP_ENV=prod`. Set `ADMIN_TOKEN`/`OPS_TOKEN` to enforce; empty means open local mode. The agent credential reads/proposes only and never touches admin routes.

## HTTP surface

JSON everywhere; `X-Request-Id` echoed; mutating routes accept `Idempotency-Key`.

| Area | Routes |
| --- | --- |
| Orders | `POST /v1/orders`, `GET /v1/orders/{id}`, `POST /v1/orders/{id}/{prepare,ready,pickup,deliver,cancel}` |
| Dispatch | `POST /v1/orders/{id}/assign`, `GET /v1/assignments/{id}`, `POST /v1/assignments/{id}/{accept,reject}` |
| Riders/events | `GET /v1/riders`, `GET /v1/restaurants`, `POST /v1/riders/{id}/location`, `POST /v1/webhooks/rider` |
| Planner/ops | `GET /v1/ops/snapshot`, `GET /v1/orders/{id}/trace`, `POST /v1/proposals`, `GET /v1/proposals/{id}`, `POST /v1/proposals/{id}/{commit,reject}` |
| Local admin | `POST /admin/seed/world`, `DELETE /admin/reset` |

Errors look like `{"code":"ORDER_STATE_CONFLICT","message":"...","request_id":"..."}` with stable codes (`ORDER/RIDER/ASSIGNMENT/PROPOSAL_*`, `IDEMPOTENCY_KEY_REUSED`, `VALIDATION_ERROR`, …).

## Verification and metrics

| Measure | Target | Measured |
| --- | --- | --- |
| Assignment create p95 (HTTP, n=50 sequential, Windows, `go run` dev, 10 rest/25 riders) | < 100 ms | 28.9 ms |
| Assign-call p95 (HTTP, same run) | < 500 ms | 36.8 ms |
| Generated Go eval suite | ≥ 85% | 24/24 (1.00), 0 double-assigns |
| Agent trajectory eval | ≥ 85% | 8/8 (1.00), exact tool subset |
| Go race detector | clean | clean via `make race-docker` (Linux); contention tests green locally |
| Duplicate webhook side effects | 0 | tested: replay returns original, hash mismatch 409 |

`go test -race` needs a Linux C toolchain — on Windows run `make race-docker`. k6 numbers should be recorded with machine, compose versions, warm-up, and sample size; the table above notes its methodology inline.

## Demo script

`scripts/demo.sh` runs the interview story end-to-end (reset → seed → VIP order → offer → snapshot → rider cancel → agent propose/commit → assigned → latest eval summary). It needs `bash`, `curl`, and the agent deps on `PATH`:

```bash
BASE_URL=http://127.0.0.1:8080 bash scripts/demo.sh
```

`scripts/seed_and_run.sh` boots infra and seeds the default world.

## Design trade-offs

- Greedy scoring with a bounded candidate pool instead of global optimization — latency over optimality, weights in config.
- Postgres as the correctness boundary, Redis as coordination — safety over lock speed.
- Planner proposes, service commits — usefulness without authority.
- Seeded simulation over production data — reproducibility over realism.

## Talk track

1. The planner suggests; only Go commits assignment bytes, under lock and constraints.
2. `Idempotency-Key` plus the webhook ledger makes duplicate rider events safe.
3. `go test -race` plus transactional contention tests protect the double-book invariants.
4. Eval scenarios are generated from seeds and frozen before comparing policy or prompt changes.
5. A mid-offer rider cancel returns the order to the queue and reoffers — never stuck in `offering`.
6. Greedy score vs latency is explicit: weights and offer windows live in config, not folklore.

## Limits

- Single-capacity riders, one metro bounding box, bbox (not GEO) candidate search in MVP.
- No real payments, auth is local shared tokens, admin routes are test-only.
- Agent heuristic picks the first available non-current rider (grounded, not optimal); LLM narration is optional and never authoritative.
