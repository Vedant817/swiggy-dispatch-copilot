# Swiggy Dispatch Copilot

A local, synthetic dark-kitchen dispatch system designed to demonstrate reliable Go order orchestration alongside an AI-assisted operations planner. The core idea is simple: **the agent suggests; the typed service decides and commits**. This is an independent engineering project, not a connection to Swiggy's production systems.

> **Current status:** Design stage. This repository contains documentation only. The services, infrastructure, CLI, tests, and demo described below are planned and cannot be run yet; no performance or evaluation results have been measured.

## What it does

The envisioned system accepts kitchen orders, finds suitable riders, sends time-limited offers, and handles acceptance, rejection, expiry, and rider cancellation without double-booking. An operations planner inspects live orders and assignment traces, explains delays, and proposes a rider reassignment or batching hint. A separate authorized operator or service-side policy gate decides whether to commit a proposal. The planner never writes assignment state or performs payments.

Synthetic restaurants, riders, order traffic, and evaluation scenarios are generated from configurable seeds. This makes the dispatch flow demonstrable without real customer data and lets concurrency, recovery, and agent behavior be tested repeatedly.

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
| Go HTTP API | Orders, riders, assignments, webhooks, proposals, ops reads, authentication, and idempotency |
| Go worker | Candidate scoring, time-limited offers, expiry, reoffers, and cancellation recovery |
| PostgreSQL | Authoritative orders, assignments, proposal records, deduplication ledgers, and assignment traces |
| Redis | Short-lived coordination for assignment attempts; database transactions enforce correctness |
| Go generator | Seeded world creation, order traffic, rider location updates, and burst workloads through HTTP |
| Python planner | Grounded read/propose tools, structured proposal output, and an operator-confirmed commit flow |
| Evaluation and load drivers | Generated scenarios, concurrency/race checks, and dispatch latency measurements |

### Assignment lifecycle

An order moves through `created → preparing → ready_for_assign → offering → assigned → picked_up → delivered`. While an order is ready, the worker selects nearby available riders, scores candidates by distance, load, rating, and VIP priority, then creates a time-limited offer. A rider's acceptance commits the assignment. On rejection, expiry, or cancellation, the offer is closed, rider capacity is released, and an eligible order returns to the assignment queue. Invalid state transitions return an HTTP `409` with a stable error code.

The correctness goals are **at most one active assignment per order** and **no rider capacity oversubscription**. PostgreSQL constraints, conditional updates, and short transactions are intended to enforce these even when requests race or a Redis lease expires. The Redis lock coordinates attempts; it is not a substitute for a database commit. Offers and retry intent must survive worker restarts rather than existing only in memory.

### Proposal boundary

The planner can read an ops snapshot, order details, available riders, and the order's assignment trace. It can create typed `reassign`, `batch_hint`, and `delay_explain` proposals via the Go API. Creating a proposal has no assignment side effect. A commit requires explicit confirmation or an authorized policy decision; the Go service then rechecks current order and rider state before making any change. Stale offers, unavailable riders, invented IDs, and conflicting concurrent operations are rejected server-side.

### Idempotency and visibility

Mutating HTTP operations use an `Idempotency-Key`, and inbound rider events are recorded in a webhook ledger so a duplicate can return the original result without a second side effect. Request IDs connect structured API logs, worker events, assignment traces, and planner tool calls. A local admin token gates synthetic seeding and reset operations; an agent credential should not grant admin or commit privileges.

## Planned HTTP surface

All application endpoints use JSON. The principal routes are:

| Area | Routes |
| --- | --- |
| Orders | `POST /v1/orders`, `GET /v1/orders/{id}`, `POST /v1/orders/{id}/ready` |
| Dispatch | `POST /v1/orders/{id}/assign`, `GET /v1/assignments/{id}`, `POST /v1/assignments/{id}/accept`, `POST /v1/assignments/{id}/reject` |
| Riders and events | `GET /v1/riders`, `POST /v1/riders/{id}/location`, `POST /v1/webhooks/rider` |
| Planner and ops | `GET /v1/ops/snapshot`, `GET /v1/orders/{id}/trace`, `POST /v1/proposals`, `GET /v1/proposals/{id}`, `POST /v1/proposals/{id}/commit`, `POST /v1/proposals/{id}/reject` |
| Local administration | `POST /admin/seed/world`, `DELETE /admin/reset` (local/test environments only) |

Mutating requests will accept `Idempotency-Key`; `X-Request-Id` will be echoed for tracing. Errors will use a stable JSON shape, for example `{"code":"ORDER_STATE_CONFLICT","message":"...","request_id":"..."}`. Some kitchen and terminal-state transitions still require a finalized event/API contract before implementation.

## Technology and repository layout

- **Go** for the HTTP service, dispatch worker, deterministic generator, domain logic, and race/integration tests.
- **PostgreSQL** for transactional state and durable deduplication; **Redis** for bounded coordination and offer-related ephemeral data.
- **Python** for a tool-calling planner (LangGraph or Agents SDK) and scenario evaluation.
- **Docker Compose** for a local Postgres/Redis/API/worker stack; **Make** for development, test, eval, and load commands.

The planned layout places Go code in `go/cmd/` and `go/internal/`, planner code in `agent/app/`, configuration in `configs/`, generated-scenario tooling in `evals/`, load scripts in `load/`, and local demo scripts in `scripts/`. None of those directories has been scaffolded yet.

## Reproducibility, verification, and performance

Generators will build restaurants, riders, locations, and order streams from a seed and config; test scenarios will describe **predicates and actions**, then resolve IDs from live API responses rather than embedding fixed UUIDs. Integration tests will use PostgreSQL and Redis to prove a full offer-to-accept path and cancellation recovery. `go test -race` will cover executed in-process concurrency paths, while multi-process database tests will check the double-assignment invariants. Generated evaluation cases will assert proposal validity, tool usage, no invented IDs, and recovery outcomes.

The intended local targets are:

| Measure | Target | Measured result |
| --- | --- | --- |
| Assignment enqueue p95 | < 100 ms | Not measured |
| Time to first offer p95 | < 500 ms | Not measured |
| Duplicate webhook side effects | 0 | Not tested |
| Double assignments / invented agent IDs | 0 / 0 | Not tested |
| Generated eval suite pass rate | ≥ 85% | Not run |
| Go race detector | Clean | Not run |

Once implemented, the intended local flow is to boot the stack with `docker compose up`, generate a world and traffic with `make gen-world gen-traffic`, inspect dispatch and proposals, then run `make race`, `make eval`, and `make load`. A scripted demo will show a rider cancellation, a planner proposal, operator confirmation, and a committed reassignment. `make stop` will stop the stack. These commands are **not yet available**.

## Design trade-offs

- **Greedy assignment versus latency:** score nearby candidates using configurable weights and a bounded candidate pool; avoid a global optimization service in the first version.
- **Safety versus coordination speed:** rely on PostgreSQL to prevent conflicting writes; use Redis leases only to reduce redundant work.
- **Agent usefulness versus authority:** allow the planner to explain and propose, but keep policy checks and state mutation in the Go service.
- **Reproducibility versus realism:** simulate traffic from seeds rather than using production data; report actual local hardware, load parameters, and sample sizes alongside any future latency claims.
