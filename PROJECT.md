# Swiggy Dispatch Copilot

**Working title:** Dark-kitchen order orchestrator (Go) + assignment planner agent (AI)

**Audience:** Humans and coding agents implementing this repo. Treat this document as the source of truth for scope, architecture, contracts, and acceptance criteria. If code and this doc disagree, update the doc in the same change.

**Role fit:** Demonstrates Golang backend ownership (assignment correctness, concurrency, idempotency, SLOs) and production-shaped AI (tool-calling planner that proposes; typed Go service that decides and commits).

---

## 1. Product intent

Build a local, runnable system that:

1. Accepts food / dark-kitchen orders.
2. Assigns riders under concurrency, timeouts, and cancellation.
3. Exposes a planner agent that **proposes** batching or reassignment using tools against live APIs.
4. Ensures the agent **never** mutates money-path or assignment state directly — only the Go service commits.
5. Proves behaviour with generated workloads, race tests, frozen evaluation scenarios (generated from schemas), and latency budgets.

Interview narrative: *“Agent suggests, typed service decides.”*

---

## 2. Non-goals (explicit)

- Real Swiggy production traffic, OAuth, or MCP cloud deployment.
- Full Kafka mesh, multi-city geo partitioning, or ML ETA models.
- Agent autonomous refunds / payments.
- Pixel-perfect consumer UI (CLI + minimal HTTP status views are enough).
- Multiple half-finished side projects in this repo.

---

## 3. Design principles

| Principle | Rule |
| --- | --- |
| Single writer | Only Go service writes `orders`, `assignments`, `webhooks_ledger`. |
| Propose ≠ commit | Agent tools are read + `Propose*` APIs that create **proposal** records; humans or a policy gate call `CommitProposal` / service-side assign. |
| Deterministic generation | All restaurants, riders, orders, and eval cases are produced by **generators** driven by config + seed. No entity IDs or payloads hand-pasted into business logic. |
| Real-time path | Demos and tests hit running HTTP APIs and live DB/Redis state. Generators feed the system through the same APIs (or authorized seed endpoints) used in normal operation. |
| Idempotency | Mutating endpoints accept `Idempotency-Key`; duplicates are no-ops with the original response. |
| Observability | Structured logs, request IDs, assignment traces; agent tool calls traced with latency and token/cost estimates. |
| Test as contract | `go test -race`, generator-backed integration tests, and eval CI are merge gates. |

---

## 4. Repository layout

```text
swiggy-dispatch-copilot/
├── PROJECT.md                 # this file
├── README.md                  # quickstart, metrics table, demo script
├── docker-compose.yml         # postgres, redis, api, agent (optional)
├── Makefile                   # gen-data, run, test, race, eval, load
├── configs/
│   ├── default.yaml           # ports, timeouts, pool sizes, generator defaults
│   └── eval.yaml              # eval suite size, seeds, thresholds
├── go/
│   ├── go.mod
│   ├── cmd/
│   │   ├── api/main.go        # HTTP API process
│   │   ├── worker/main.go     # assignment worker (may be same binary with flag)
│   │   └── gen/main.go        # CLI: generate world + enqueue load via APIs
│   ├── internal/
│   │   ├── domain/            # entities, state machine, errors
│   │   ├── assign/            # scoring, offer loop, locks
│   │   ├── api/               # HTTP handlers, middleware
│   │   ├── store/             # postgres repos
│   │   ├── redisx/            # locks, offer TTL, optional GEO
│   │   ├── webhook/           # inbound webhook + ledger
│   │   ├── proposal/          # agent proposals + policy gate
│   │   ├── gen/               # world + workload generators (shared lib)
│   │   └── obs/               # logging, metrics
│   └── tests/
│       ├── race/
│       └── integration/
├── agent/
│   ├── pyproject.toml
│   ├── app/
│   │   ├── graph.py           # LangGraph (or Agents SDK) planner
│   │   ├── tools.py           # HTTP tools → Go APIs only
│   │   ├── policy.py          # local checks before calling Propose*
│   │   └── main.py            # CLI / HTTP for chat-style planning
│   └── tests/
├── evals/
│   ├── schema/                # JSON Schema for scenario files
│   ├── generate_suite.py      # builds suite from configs + seed
│   ├── runners/
│   │   ├── go_scenarios.py    # hits Go APIs
│   │   └── agent_scenarios.py # hits agent + asserts tool trajectory
│   ├── suites/                # generated output (gitignored or committed locks)
│   └── reports/
├── load/
│   └── assign_latency.js      # k6 or hey script; params from env
└── scripts/
    ├── demo.sh                # generate world → traffic → agent plan → show metrics
    └── seed_and_run.sh
```

Agents and developers must place new code in the packages above. Do not invent parallel top-level apps.

---

## 5. Runtime topology

```text
                    ┌─────────────┐
   gen CLI / load ──┤  HTTP API   │── Postgres
   agent tools  ────┤  (Go)       │── Redis
   demo script  ────┤             │
                    └──────┬──────┘
                           │ enqueue / watches
                    ┌──────▼──────┐
                    │ Assign      │
                    │ workers     │
                    └─────────────┘

   User / interviewer
        │
   ┌────▼────┐   tools (HTTP)   ┌─────────┐
   │ Agent   │ ───────────────► │ Go API  │
   │ planner │ ◄── proposals ── │         │
   └─────────┘                  └─────────┘
```

Processes:

| Process | Responsibility |
| --- | --- |
| `api` | REST, authn stub (local token), proposals, webhooks, reads |
| `worker` | Offer loop, timeouts, reoffer, cancel handling (can be in-process goroutine pool in MVP) |
| `gen` | Creates world + streaming order load through public APIs |
| `agent` | Conversational planner; tools call Go only |
| `eval runner` | Generates suite, executes, writes report JSON |

---

## 6. Domain model

### 6.1 Entities

**Restaurant**

| Field | Type | Notes |
| --- | --- | --- |
| `id` | UUID | Generated |
| `name` | string | From generator name templates |
| `lat`, `lng` | float | Within configured city bounding box |
| `prep_minutes_p50` | int | Generator distribution |
| `capacity` | int | Concurrent prep slots |

**Rider**

| Field | Type | Notes |
| --- | --- | --- |
| `id` | UUID | Generated |
| `status` | `available` \| `offered` \| `busy` \| `offline` | |
| `lat`, `lng` | float | Updated by location publisher in gen/load |
| `capacity` | int | Usually 1 for MVP |
| `rating` | float | Scoring feature |

**Order**

| Field | Type | Notes |
| --- | --- | --- |
| `id` | UUID | |
| `restaurant_id` | UUID | |
| `status` | see state machine | |
| `priority` | `normal` \| `vip` | Affects scoring |
| `sla_deliver_by` | timestamptz | |
| `idempotency_key` | string | Client-supplied on create |

**Assignment / Offer**

| Field | Type | Notes |
| --- | --- | --- |
| `id` | UUID | |
| `order_id`, `rider_id` | UUID | |
| `status` | `offered` \| `accepted` \| `expired` \| `cancelled` \| `superseded` | |
| `offered_at`, `expires_at` | timestamptz | Config `offer_ttl` |
| `score` | float | Snapshot of scoring inputs |

**Proposal** (agent output, not yet committed)

| Field | Type | Notes |
| --- | --- | --- |
| `id` | UUID | |
| `type` | `reassign` \| `batch_hint` \| `delay_explain` | |
| `payload` | JSONB | Typed per `type` |
| `status` | `pending` \| `accepted` \| `rejected` \| `expired` | |
| `reason` | string | Agent rationale |
| `created_by` | `agent` | |

**Webhook ledger**

| Field | Type | Notes |
| --- | --- | --- |
| `idempotency_key` | string | PK |
| `event_type` | string | e.g. `rider.cancelled`, `order.ready` |
| `payload_hash` | string | |
| `result_code` | int | |
| `response_body` | JSONB | Cached first response |

### 6.2 Order state machine

```text
created → preparing → ready_for_assign → offering → assigned → picked_up → delivered
                         │                  │
                         │                  └→ ready_for_assign (offer expired / rider cancel)
                         └→ cancelled
```

Illegal transitions return `409` with a stable error code.

Worker may move `ready_for_assign` → `offering` → `assigned`. Rider cancel webhook moves `assigned`/`offering` back to `ready_for_assign` and releases rider lock.

---

## 7. Configuration

`configs/default.yaml` (illustrative keys — implement exactly these names unless README documents a rename):

```yaml
server:
  addr: ":8080"
  shutdown_timeout: 10s

postgres:
  dsn_env: DATABASE_URL

redis:
  addr_env: REDIS_ADDR

assign:
  worker_count: 8
  offer_ttl: 15s
  max_candidates: 20
  lock_ttl: 5s
  reoffer_backoff: 1s

scoring:
  weights:
    distance_km: -1.0
    rider_load: -0.5
    rating: 0.2
    vip_bonus: 0.8

generator:
  seed: 42
  city_bbox: [12.90, 77.50, 13.00, 77.70]  # lat/lng window
  restaurants: 40
  riders: 120
  order_rate_per_min: 30
  vip_ratio: 0.08
  location_tick_ms: 2000

agent:
  base_url: "http://127.0.0.1:8080"
  model_env: OPENAI_API_KEY   # or compatible provider
  max_tool_calls: 12

evals:
  seed: 99
  scenario_count: 24
  min_pass_rate: 0.85
```

All tunables live in config or env. Business logic reads config structs — never magic numbers scattered in handlers without named constants sourced from config.

---

## 8. Data generation (mandatory approach)

### 8.1 Why generators exist

World state (restaurants, riders, geospatial layout) and traffic (orders, location updates, cancels) must be **synthesized at runtime** from `generator.seed` + distributions in config. Evaluation scenarios are **compiled** from schema + seed into `evals/suites/*.json` by `evals/generate_suite.py`.

This keeps demos and tests:

- reproducible (`seed` fixed → same world),
- scalable (raise counts in config),
- exercised through real APIs (generators are clients).

### 8.2 Generator responsibilities (`go/internal/gen` + `cmd/gen`)

| Command | Behaviour |
| --- | --- |
| `gen world` | Creates N restaurants and M riders via `POST /admin/seed/restaurant` and `POST /admin/seed/rider` (or bulk seed endpoint). IDs allocated by server. |
| `gen traffic` | Poisson (or ticker) order creation via `POST /v1/orders`; periodically patches rider locations via `POST /v1/riders/{id}/location`; injects cancel webhooks at a configured rate. |
| `gen burst` | Short high QPS order spike for load + race observation. |

Admin/seed routes are disabled when `APP_ENV=prod` style flag is set; local/demo compose leaves them on.

### 8.3 Rules for implementers (humans and agents)

1. Do **not** embed fixed UUIDs, restaurant names, or order payloads inside `internal/assign`, handlers, or agent prompts as standing fixtures.
2. Tests obtain entities by calling generators or factories that use the same generators with a test seed.
3. Eval scenarios reference **predicates** (e.g. “order in `offering`, rider cancel”) and the runner creates matching state through APIs — scenarios describe intent and assertions, not pasted primary keys (IDs are filled in at run time).
4. README demo steps always start with `make gen-world gen-traffic` (or `scripts/demo.sh`) so the system is live before the agent runs.

### 8.4 Factory helpers

```text
gen.RestaurantSpec → API create
gen.RiderSpec → API create
gen.OrderSpec{Priority, RestaurantPicker} → API create
```

Pickers may be `random`, `nearest_to_rider`, `hotspot` — implemented with RNG from seed.

---

## 9. HTTP API contract (Go)

Base: `/v1`. JSON request/response. Header `Idempotency-Key` on mutating routes. Header `X-Request-Id` echoed.

### 9.1 Orders

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/orders` | Create order (`restaurant_id` or `restaurant_picker` resolved server-side if using seed helpers) |
| `GET` | `/v1/orders/{id}` | Fetch order + current assignment |
| `POST` | `/v1/orders/{id}/prepare` | `created → preparing` (kitchen start) |
| `POST` | `/v1/orders/{id}/ready` | `preparing → ready_for_assign` (kitchen signal) |
| `POST` | `/v1/orders/{id}/pickup` | `assigned → picked_up` |
| `POST` | `/v1/orders/{id}/deliver` | `picked_up → delivered` |
| `POST` | `/v1/orders/{id}/cancel` | Customer cancel to `cancelled` from non-terminal states |

### 9.2 Assignment

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/orders/{id}/assign` | Enqueue assignment attempt (idempotent) |
| `GET` | `/v1/assignments/{id}` | Offer/assignment detail |
| `POST` | `/v1/assignments/{id}/accept` | Rider accept |
| `POST` | `/v1/assignments/{id}/reject` | Rider reject |

### 9.3 Riders

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/riders/{id}/location` | Update lat/lng |
| `GET` | `/v1/riders` | Filter `status=available` |

### 9.4 Webhooks

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/webhooks/rider` | Events: `cancelled`, `offline` — ledger keyed by Idempotency-Key |

### 9.5 Proposals (agent-facing)

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/proposals` | Create proposal (`type` + payload). Validates schema; **does not** assign. |
| `POST` | `/v1/proposals/{id}/commit` | Policy gate + execute (reassign path). Rejects if unsafe. |
| `POST` | `/v1/proposals/{id}/reject` | Mark rejected with reason |
| `GET` | `/v1/proposals/{id}` | Status |

### 9.6 Read tools for agent

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/v1/ops/snapshot` | Compact view: open orders, available riders, offering timeouts |
| `GET` | `/v1/orders/{id}/trace` | Assignment attempts, scores, cancels |

### 9.7 Admin (local)

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/admin/seed/world` | Body: `{ "seed": 42, "restaurants": 40, "riders": 120 }` — server-side generation |
| `DELETE` | `/admin/reset` | Wipe transactional tables (test only) |

Error body shape:

```json
{ "code": "ORDER_STATE_CONFLICT", "message": "...", "request_id": "..." }
```

---

## 10. Assignment algorithm (Go)

### 10.1 Offer loop (per order)

1. Acquire Redis lock `assign:{order_id}` with `lock_ttl`.
2. Load order; require `ready_for_assign`.
3. Query candidate riders (GEO radius or bbox + `available`).
4. Score top `max_candidates`; pick best.
5. CAS rider `available → offered`; create assignment `offered` with `expires_at = now + offer_ttl`.
6. Order → `offering`.
7. Wait accept / reject / timeout (worker timer or poller).
8. On accept: rider `busy`, order `assigned`, release lock.
9. On timeout/reject/cancel: release rider, assignment `expired`/`cancelled`, order `ready_for_assign`, schedule reoffer.

### 10.2 Concurrency invariants

- At most one **active** assignment per order.
- At most one **offered/busy** assignment per rider capacity.
- Proven by tests that fire parallel `assign` + duplicate webhooks under `-race`.

### 10.3 Scoring

```text
score = w_distance * distance_km
      + w_load * rider_active_load
      + w_rating * rating
      + w_vip * (1 if order.vip else 0)
```

Weights from config. Persist score breakdown JSON on the assignment for traces and agent explanations.

---

## 11. Agent specification

### 11.1 Role

Planner assistant for ops. It may:

- Explain delays using `/trace` and snapshot tools.
- Propose reassignment when offers stall.
- Propose batching hints (group orders by restaurant/geo) as **proposals only**.

It must not:

- Call raw SQL.
- Bypass `CommitProposal` policy.
- Invent order/rider IDs not returned by tools.

### 11.2 Tools (HTTP wrappers only)

| Tool | Maps to |
| --- | --- |
| `get_ops_snapshot` | `GET /v1/ops/snapshot` |
| `get_order` | `GET /v1/orders/{id}` |
| `get_order_trace` | `GET /v1/orders/{id}/trace` |
| `list_available_riders` | `GET /v1/riders?status=available` |
| `propose_reassign` | `POST /v1/proposals` type `reassign` |
| `propose_batch_hint` | `POST /v1/proposals` type `batch_hint` |
| `commit_proposal` | `POST /v1/proposals/{id}/commit` — only when policy allows and user/ops confirms in CLI flow |

### 11.3 Graph sketch

```text
intake → understand_goal → gather_tools → (loop) → draft_proposal → policy_check
    → optional_confirm → commit_or_stop → respond
```

Use structured output (Pydantic / JSON schema) for proposals. Cap tool calls via config.

### 11.4 Policy checks (agent local + server)

Before `commit_proposal`:

- Target rider must be `available` at read time (server re-checks under lock).
- Order must be `offering` or `ready_for_assign`.
- Reject if proposal references unknown IDs.
- Server is authoritative; agent policy is belt-and-suspenders.

---

## 12. Evaluation suite

### 12.1 Generation

`python -m evals.generate_suite --config configs/eval.yaml` writes scenarios:

Each scenario document:

```json
{
  "id": "scn_reassign_after_cancel_001",
  "seed": 99001,
  "setup": {
    "world": { "restaurants": 10, "riders": 25 },
    "script": [
      { "action": "create_order", "priority": "vip" },
      { "action": "mark_ready" },
      { "action": "assign" },
      { "action": "webhook_rider_cancel", "when": "after_offer" }
    ]
  },
  "agent_goal": "Propose a valid reassignment after rider cancel.",
  "assert": {
    "order_eventually": "assigned",
    "proposal_committed": true,
    "no_double_assign": true,
    "tools_used_subset": ["get_order_trace", "propose_reassign", "commit_proposal"]
  }
}
```

IDs in `script` are resolved at execution by the runner (create → capture id → later steps).

### 12.2 Pass criteria

| Gate | Threshold (config) |
| --- | --- |
| Suite pass rate | `>= evals.min_pass_rate` |
| Double-assign incidents | `0` |
| Agent invented ID rate | `0` |
| Go `-race` | clean |

Reports land in `evals/reports/{timestamp}.json` and a short Markdown summary for README.

---

## 13. Load and SLOs

| Metric | Target (local) |
| --- | --- |
| Assign enqueue p95 | `< 100 ms` API time |
| Time to first offer p95 | `< 500 ms` under configured worker_count |
| Duplicate webhook | same response, single side effect |
| Race tests | pass |

`make load` runs k6/hey against generated traffic. Record results in README metrics table when refreshed.

---

## 14. Testing requirements

### Go

- Unit: state machine transitions; scoring; idempotency ledger.
- Race: parallel assign same order; parallel cancel + assign; capacity exhaustion.
- Integration: docker-compose Postgres+Redis; generator world; full offer → accept path.

### Agent

- Tool schema tests (malformed proposal rejected).
- Trajectory test on one generated scenario with mocked HTTP **or** live compose (prefer live in CI nightly; mocked in PR if speed requires — still driven by generator specs).

### Eval CI

`make eval` must fail the build when pass rate &lt; threshold.

---

## 15. Observability

- JSON logs: `request_id`, `order_id`, `rider_id`, `assignment_id`, `event`.
- Counter metrics: offers_created, offers_expired, assigns_committed, webhook_duplicates, proposals_committed, proposals_rejected.
- Agent: per-tool latency, token usage estimate, final proposal id.

---

## 16. Security / safety (local product bar)

- Admin routes gated by `ADMIN_TOKEN`.
- Agent cannot reach admin routes.
- Secrets only via env; never logged.
- Out-of-band stop: documenting `make stop` / process group kill in README is enough for MVP; optional hardening appendix.

---

## 17. Demo script (interview)

`scripts/demo.sh` must:

1. Boot compose / api / worker.
2. `gen world` + short `gen traffic`.
3. Print snapshot (open orders, available riders).
4. Trigger one rider cancel against an offering order (via generator action).
5. Run agent with goal: reassign stalled VIP order.
6. Show proposal → commit → order assigned.
7. Print eval latest summary + p95 snippet.

Operators narrate using the talk track in §19.

---

## 18. Implementation phases (agents: complete in order)

### Phase A — Skeleton

- [ ] Repo layout, modules, docker-compose, config loading
- [ ] Postgres migrations for entities in §6
- [ ] Health endpoints

### Phase B — Go core

- [ ] Order CRUD + state machine
- [ ] Seed/world admin + `cmd/gen world|traffic`
- [ ] Assign worker + Redis locks + offer TTL
- [ ] Webhook ledger + rider cancel path
- [ ] Race + integration tests green

### Phase C — Proposals

- [ ] Proposal APIs + commit under lock
- [ ] Ops snapshot + trace endpoints

### Phase D — Agent

- [ ] Tools wired to live API
- [ ] Graph with propose/confirm/commit
- [ ] CLI entrypoint for demo

### Phase E — Evals & load

- [ ] Suite generator + runner
- [ ] `make eval` gate
- [ ] Load script + README metrics

### Phase F — Polish

- [ ] README architecture diagram
- [ ] Demo script
- [ ] Interview talk track pasted into README

Do not start Phase D before Phase B race tests pass.

---

## 19. Interview talk track (ship in README)

1. Agent never commits assignment bytes; Go does, under lock.
2. Idempotency-Key makes duplicate rider webhooks safe.
3. `go test -race` protects double-book invariants.
4. Eval scenarios are generated from seed; we freeze the suite and only change policy/prompts.
5. Rider cancel mid-offer returns order to `ready_for_assign` and reoffers.
6. Trade-off: greedy score vs latency — weights are config, not folklore.

---

## 20. Coding standards for agents and developers

1. Read this file before changing architecture.
2. Prefer small PRs mapped to phases A→F.
3. Every mutating API test uses a unique Idempotency-Key from the test RNG.
4. New behaviours need: config key, test, and a line in README metrics or eval assertions when user-visible.
5. Do not add frameworks that are not listed here without updating §4–§5 in the same change.
6. Prefer clarity over cleverness in assignment code — interviewers read this file and the assign package.
7. When unsure, implement the narrower behaviour that preserves invariants in §10.2.

---

## 21. Acceptance checklist (definition of done)

- [ ] `docker compose up` + `make gen-world gen-traffic` yields live assignable orders
- [ ] Rider cancel mid-offer recovers without stuck `offering`
- [ ] Parallel assign tests pass with `-race`
- [ ] Agent can propose + commit reassign on a generated stalled order
- [ ] `make eval` ≥ configured pass rate with zero double-assigns
- [ ] README shows architecture, how to generate data, metrics, demo script
- [ ] No business-logic dependency on hand-pasted entity payloads

---

## 22. Open parameters (decide once, then freeze in config)

| Parameter | Default | Owner |
| --- | --- | --- |
| Offer TTL | 15s | assign |
| Worker count | 8 | assign |
| GEO vs bbox candidates | bbox MVP; GEO optional | assign |
| LLM provider | env-driven | agent |
| Eval scenario_count | 24 | evals |

Frozen for MVP (A0, 2026-09-28):

- Rider capacity is fixed to `1`. One active (`offered`/`accepted`) assignment per rider is enforced by a partial unique index; multi-capacity is out of scope.
- Worker topology: `api` runs an in-process goroutine pool (`assign.worker_count`); `worker` binary runs the same `assign` package as a standalone expiry/reoffer poller. PostgreSQL is authoritative; Redis `assign:{order_id}` token locks only coordinate attempts.
- Idempotency: every mutating `/v1/*` route accepts `Idempotency-Key`. Scope is `key + method + path-template + target-id`. Identical request hash replays the first response; different payload with the same key returns `409 IDEMPOTENCY_KEY_REUSED`. Rider webhooks additionally persist in `webhooks_ledger` keyed by `Idempotency-Key`.
- Kitchen/terminal transitions: `POST /v1/orders/{id}/prepare` (`created → preparing`), `POST /v1/orders/{id}/ready` (`preparing → ready_for_assign`), `POST /v1/orders/{id}/pickup` (`assigned → picked_up`), `POST /v1/orders/{id}/deliver` (`picked_up → delivered`), `POST /v1/orders/{id}/cancel` (customer cancel from `created`/`preparing`/`ready_for_assign`/`offering` to `cancelled`). Rider `cancelled`/`offline` webhooks move `offering`/`assigned` back to `ready_for_assign` and release the rider; they never terminally cancel the order.
- Reassign commit: supersedes the current active offer atomically (`superseded`) and invalidates late accepts on the old assignment id. `batch_hint`/`delay_explain` commits are informational and return `422 PROPOSAL_NOT_COMMITTABLE` unless the type is `reassign`.
- Proposal expiry: `proposal_ttl` default `10m`; lazy expiry on read/commit plus a periodic sweeper.
- Auth: `ADMIN_TOKEN` gates `/admin/*`; `OPS_TOKEN` gates proposal commit/reject; agent credential may read/propose only and must never access `/admin/*`. Empty tokens mean open local mode; when set, mismatches return `401`/`403`.
- Eval isolation: scenarios run sequentially with `DELETE /admin/reset` between cases unless the runner is told otherwise.
- Dependencies: Go 1.25, Postgres 16, Redis 7, Python 3.11+, k6 for load. Planner runs deterministically without an LLM key; `OPENAI_API_KEY` only enables optional LLM explanations.
- Latency definitions: enqueue p95 is HTTP request-start to response; first-offer p95 is accepted enqueue to committed offer row. Both require sample size, warm-up, and machine context in the report.
- Error codes: `ORDER_STATE_CONFLICT`, `ORDER_NOT_FOUND`, `RIDER_NOT_FOUND`, `RIDER_STATE_CONFLICT`, `ASSIGNMENT_NOT_FOUND`, `ASSIGNMENT_STATE_CONFLICT`, `PROPOSAL_NOT_FOUND`, `PROPOSAL_STATE_CONFLICT`, `PROPOSAL_NOT_COMMITTABLE`, `IDEMPOTENCY_KEY_REUSED`, `VALIDATION_ERROR`, `UNAUTHORIZED`, `FORBIDDEN`.

---

## 23. Document control

| Version | Date | Notes |
| --- | --- | --- |
| 1.0 | 2026-09-28 | Initial spec for Go orchestrator + planner agent |
| 1.1 | 2026-09-28 | A0 freeze: capacity=1, worker topology, generic idempotency, kitchen/terminal transitions, proposal TTL, auth matrix, eval isolation, error codes; fix evals suite path |

Changes to public API paths or state machine require a version bump in this section and README.
