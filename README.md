# Swiggy Dispatch Copilot

Go dispatch service with concurrency-safe rider assignment, idempotent APIs, and an AI planner that can only propose changes.

[![CI](https://github.com/Vedant817/swiggy-dispatch-copilot/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/Vedant817/swiggy-dispatch-copilot/actions/workflows/ci.yml)
[![Go 1.25](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](go/go.mod)
[![MIT License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

This is an independent engineering project using synthetic data, not affiliated with or connected to Swiggy or any production food-delivery system. **The agent suggests; the typed service decides.**

## Results

Local k6 run on 2026-10-25, using the unchanged workload for 60 seconds on Windows / Intel i7-13700HX (16 cores, 24 logical processors; 16,858,472,448 bytes host RAM):

| Setup | Request rate | HTTP p50 / p95 / p99 | HTTP error rate | Outcome |
| --- | --- | --- | --- | --- |
| 1 VU, 20,000 synthetic riders | `78.387033/s` | `8.83ms` / `27.94ms` / `32.38ms` | `0.25%` (12 / 4704) | All configured thresholds passed |
| 10 VUs, 20,000 synthetic riders | `569.333019/s` | `12.08ms` / `37.59ms` / `44.88ms` | `3.92%` (1341 / 34196) | Error threshold failed |
| 10 VUs, default 120 riders | `811.096848/s` | `11.65ms` / `18.65ms` / `28.26ms` | `24.03%` (11708 / 48716) | Error threshold failed; fleet exhausted |

The 1-VU baseline passed the script's `<1%` HTTP error threshold, with 12 failed assignment checks; it was not error-free. A passing 10-VU run was **not** obtained: concurrent requests can select the same rider and return `409 RIDER_STATE_CONFLICT`. Rates count all HTTP operations, including failures, rather than successful dispatches. Latency includes error responses. [Methodology, machine details, exact k6 output and raw JSON](docs/LOAD_RESULTS.md) include all three runs and their limitations.

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

## Correctness guarantees

- **No double-booking:** [partial unique indexes](go/internal/store/migrations/003_indexes.sql) allow at most one `offered` or `accepted` assignment per order and per rider (capacity 1).
- **Atomic assignment transitions:** [assignment transactions](go/internal/assign/service.go) use `SELECT ... FOR UPDATE` on orders/assignments, recheck state, and claim riders with a conditional update. [Proposal commits](go/internal/api/commit.go) lock and validate the proposal, order, assignment and target rider before committing.
- **Redis only coordinates:** [token-owned Redis leases](go/internal/redisx/redisx.go) reduce duplicate assignment attempts; [TryAssign](go/internal/assign/service.go) revalidates inside PostgreSQL even when Redis fails. PostgreSQL transactions and constraints enforce correctness.
- **Request replay protection:** [Idempotency-Key handling](go/internal/api/middleware.go) and the [durable claim/response store](go/internal/store/repos.go) scope keys to method, route and target; changed payloads conflict. [Order creation](go/internal/store/orders_atomic.go) and [customer cancellation](go/internal/store/cancel.go) commit state and replay responses in one transaction. Other routes use claims with stale-claim cleanup; this is not a blanket exactly-once guarantee after crashes.
- **Webhook deduplication:** the [webhook ledger](go/internal/api/webhook.go) serializes same-key requests with a transaction advisory lock, checks the normalized payload hash, and commits side effects plus the recorded response atomically. Identical replays return the recorded response; changed payloads return 409.

## Quickstart

Prerequisites: Go 1.25, Python 3.11+, Docker with Compose, Make, `curl`, and (for `make load`) k6. Commands below use Bash and start from the repository root. PowerShell uses `$env:NAME='value'` instead of `export NAME='value'`. See [.env.example](.env.example) for configuration; `go run` does not automatically load that file.

```bash
# Terminal 1: Start Postgres + Redis, then API (migrations run on boot)
docker compose up -d postgres redis
export DATABASE_URL='postgres://dispatch:dispatch@127.0.0.1:5432/dispatch?sslmode=disable'
export REDIS_ADDR='127.0.0.1:6379'
export APP_ENV=local
cd go && go run ./cmd/api        # :8080, worker loop in-process
# (optional) standalone worker: go run ./cmd/worker
```

```bash
# Terminal 2, from repository root: seed a world and send traffic
cd go
go run ./cmd/gen world
go run ./cmd/gen --config ../configs/default.yaml --duration 60s traffic

# 3. Plan a reassignment (needs an offering order id)
python -m pip install httpx pydantic pyyaml
cd ../agent && python -m app.main --goal "reassign stalled VIP order" --order <ORDER_ID> --auto-confirm

# 4. Verify, from the repository root (test DB setup below)
cd ..
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

## Testing

**38 top-level Go tests**, counted with `go test -list '^Test' ./...` in `go/` (subtests excluded). On 2026-10-25, all 38 passed without test skips using a separate disposable PostgreSQL database and real Redis: `go test -json -p 1 ./... -count=1`.

Run from the repository root in Bash (PowerShell: use `$env:TEST_DATABASE_URL='...'` for the environment assignment):

```bash
docker compose up -d postgres redis
# Create this disposable test database once; never use a production database.
docker compose exec -T postgres createdb -U dispatch dispatch_test
export TEST_DATABASE_URL='postgres://dispatch:dispatch@127.0.0.1:5432/dispatch_test?sslmode=disable'
export REDIS_ADDR='127.0.0.1:6379'
make test
(cd go && go test -list '^Test' ./...)
(cd go && go vet ./...)
make race          # requires a supported CGO C compiler
# Windows alternative, Linux container; uses the disposable local dispatch DB:
make race-docker  # stop API/worker first: these tests reset their database

# With the local API running (these scenarios reset synthetic state):
python -m pip install httpx pydantic pyyaml pytest
make eval
(cd agent && python -m pytest tests -q)

# k6 workload, setup and measured results: docs/LOAD_RESULTS.md
make load VUS=10 DURATION=60s
```

Database-dependent Go tests skip without `TEST_DATABASE_URL` or when `APP_ENV=prod`; a run with skips is not integration acceptance. CI runs build, vet, Go tests with the race detector, generated Go/agent evaluations, agent unit tests, and a production-mode Docker authorization/workflow smoke test. The evaluation gates require at least 85% passing scenarios and zero double-assignments. These are local/CI checks; no hosted deployment is claimed.

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

- Single-capacity riders in one metro bounding box; candidates are bounded by availability/rating, then scored by distance, load, rating and priority. No spatial index or global optimizer.
- No real payments or customer identity system. Role-specific shared tokens protect production mode; admin routes are local-only.
- No managed TLS ingress, credential rotation, off-host backup retention/PITR, multi-region failover, or production incident runbook is supplied in this repository. The local restore drill, Docker checks and GitHub Actions evidence do not prove a hosted rollout.
- The planner is a deterministic heuristic that picks the first available non-current rider (grounded, not optimal). Live LLM integration/narration is future work: setting `OPENAI_API_KEY` currently adds a marker, without making a provider call.
