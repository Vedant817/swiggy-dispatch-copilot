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

Go tests that create/reset data require an explicit **disposable** `TEST_DATABASE_URL`; they skip if it is missing or `APP_ENV=prod`. `make race-docker` supplies one for its local Compose database. Never point this variable at a production database.

Admin routes (`/admin/seed/world`, `/admin/reset`) are local-only and disabled when `APP_ENV=prod`. Set `ADMIN_TOKEN`/`OPS_TOKEN` to enforce; empty means open local mode. The agent credential reads/proposes only and never touches admin routes.

### Production-mode boundary

`APP_ENV=prod` makes the API fail startup unless five distinct secrets of at least 24 characters are present: `SERVICE_TOKEN`, `AGENT_TOKEN`, `OPS_TOKEN`, `WEBHOOK_TOKEN`, and `ADMIN_TOKEN`. Supply them through your deployment's secret manager; the background worker only needs database/Redis credentials. Production requests use `X-Service-Token` for ordinary API writes, `X-Agent-Token` for reads/proposals, `X-Ops-Token` for commit/reject and `/metrics`, and `X-Webhook-Token` for rider events. Every production `POST /v1/*` needs an `Idempotency-Key`. Production rider events must include `assignment_id` so a delayed event cannot be mistaken for a replacement offer. The agent image is an optional CLI (`docker compose --profile agent run --rm agent`).

This mode is an authentication and consistency boundary, **not** an internet-ready deployment: terminate TLS at a trusted ingress, supply a restricted database account with backups/PITR, enforce network isolation and rate limits, and rotate credentials before exposing it publicly. The Compose defaults are for local development only.

For a disposable Postgres container, `scripts/backup_drill.ps1 -Container <container-name>` exports a real `pg_dump` artifact out of the container, reimports and restores it into a throwaway database, and verifies eight domain tables. Add `-Quiesced` to compare source/restore counts **only after writers are stopped**; a live source can change after the dump snapshot. This is a **restore drill**, not an off-host retained backup or point-in-time recovery solution. Configure those with your production database operator. Schema migrations are transactionally serialized and checksummed; existing records from an ID-only migration table are pinned on first upgrade.

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
| Assignment create p95 (HTTP, 10-way concurrent, n=67 offered + warm-up 10, Windows, `go run` dev, 10 rest/120 riders) | < 100 ms | 19.0 ms |
| Assign-call p95 (HTTP, same run, 0 failures, 33 backpressure rejections at fleet capacity) | < 500 ms | 32.2 ms |
| Generated Go eval suite | ≥ 85% | 24/24 (1.00), 0 double-assigns |
| Agent trajectory eval | ≥ 85% | 8/8 (1.00), exact tool subset |
| Go race detector | clean | clean via `make race-docker` (Linux); contention tests green locally |
| Duplicate webhook side effects | 0 | tested: replay returns original, hash mismatch 409 |

`go test -race` needs a Linux C toolchain — on Windows run `make race-docker`. The k6 script (`load/assign_latency.js`, `make load`) covers the same thresholds; `go/cmd/load` (`go run ./cmd/load --concurrency 10 --count 100`) is the executed runner — report in `load/report.json` with warm-up, samples, failures, and machine context.

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
- No managed TLS ingress, credential rotation, off-host backup retention/PITR, multi-region failover, or production incident runbook is supplied in this repository. The local restore drill, Docker checks and GitHub Actions evidence do not prove a hosted rollout.
- Agent heuristic picks the first available non-current rider (grounded, not optimal); LLM narration is optional and never authoritative.
