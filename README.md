# Swiggy Dispatch Copilot

**Status: specification and implementation plan only.** The API, worker, agent, infrastructure, tests, and demo have not been implemented. [PROJECT.md](PROJECT.md) is the source of truth for product scope, contracts, and acceptance criteria. This README is the execution backlog; check off items only after their stated gate passes. This is a synthetic, local dark-kitchen dispatch project, not an integration with Swiggy.

> **Interview narrative:** Agent suggests; typed Go service decides and commits.

## Intended system

```text
seeded Go generator / load client ──HTTP──> Go API ──transactions──> PostgreSQL
                                           │
                                           ├── coordination ──> Redis
                                           └── durable jobs ──> Go worker
Python planner ──read and propose HTTP tools──> Go API
operator ──explicit confirmation / commit──> Go API policy gate
```

Orders move through the kitchen and dispatch lifecycle; workers offer available riders, handle acceptance, expiry, rejection, and cancellation; a planner can read state and submit proposals but cannot write assignments or money-path state directly. Generated worlds, traffic, and evaluation scenarios exercise live APIs. See [PROJECT.md §§6–16](PROJECT.md) for the full domain and API specification.

## Start here / current usage

There is no runnable application yet. To begin implementation, take **A0** below, then implement phases A–F in order. Do not run the future commands below until their corresponding tasks are complete. The planned developer flow is `docker compose up`, `make gen-world gen-traffic`, `make race`, `make eval`, `make load`, and `scripts/demo.sh`; `make stop` will shut down the local stack. The Go module lives under `go/`, the Python planner under `agent/`, and all additional code goes in the layout specified by [PROJECT.md §4](PROJECT.md).

## Research-backed engineering decisions to settle first

The following are **implementation proposals or open contract questions**, not changes to PROJECT.md. Resolve them in A0, update PROJECT.md and its document-control version where required, then freeze the API and schema before feature work.

1. **PostgreSQL is the correctness boundary.** Use short transactions, consistently ordered row locks on order/rider, conditional updates, and a partial unique index on active assignments per order. For capacity 1, enforce one active assignment per rider with another partial unique index; if `capacity > 1` is supported, lock the rider row and atomically check/count occupancy instead. Redis `SET NX` with TTL and token-checked release coordinates worker attempts but cannot make a 5-second lease authoritative over a 15-second offer: it can expire, be lost, or be reacquired while an older worker continues. Revalidate all state in PostgreSQL before every commit, including proposal commits. Decide whether MVP fixes rider capacity to 1 (recommended) and document that decision. Sources: [PostgreSQL row locks](https://www.postgresql.org/docs/current/explicit-locking.html#LOCKING-ROWS), [unique indexes](https://www.postgresql.org/docs/current/indexes-unique.html), [Redis distributed locks and fencing caveat](https://redis.io/docs/latest/develop/clients/patterns/distributed-locks/).
2. **Durable dispatch and expiry.** Do not hold a DB transaction or Redis lock while waiting for a rider response. Persist offer expiry and retry intent in PostgreSQL; a polling worker can claim due work with `FOR UPDATE SKIP LOCKED` and recover after restarts. Redis is an accelerator/coordination layer, not the only record of pending work. Define bounded retries, no-candidate behavior, and shutdown/resume semantics. Source: [PostgreSQL locking clause](https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE).
3. **Define idempotency consistently.** For each mutating route, scope the key to caller + operation (and target where applicable), save a canonical request hash and first status/body atomically with the effect, replay identical requests, and reject a reused key with a different payload. Specify behavior for concurrent in-flight requests and failed responses. The webhook ledger in the spec covers webhooks but does not cover other mutating endpoints; include a generic idempotency record or explicitly extend that model.
4. **Close the state-machine gaps.** `created → preparing → ready_for_assign` lacks a documented kitchen-start action; `assigned → picked_up → delivered` and cancel from `offering`/`assigned` lack endpoints and transitions; a rejected offer is not explicitly listed in the order transition diagram. Decide which transitions are in MVP, how they are triggered (API/webhook/test-local), and what the demo and eval actually require. Clarify `reassign` of an `offering` order: supersede prior offer atomically and invalidate its later accept, or restrict to `ready_for_assign`. Clarify whether a rejected `batch_hint`/`delay_explain` commit is meaningful or remains informational.
5. **Make authentication and trust boundaries explicit.** `ADMIN_TOKEN` gates seed/reset; a separate local ops identity must authorize proposal commit/reject, and the agent credential must only read and propose. A CLI confirmation is UX, not server authorization. Define webhook authentication for local simulator, response/error schemas, request-id behavior, and denial tests. Disable destructive admin routes outside local/test mode.
6. **Make reproducibility measurable.** Seed drives all entity attributes, selection, traffic schedule, and scenario definitions; server UUIDs and actual timestamps need not repeat, but normalized generated content and assertions should. Avoid resetting shared DB during parallel eval cases; use isolated DBs or sequential cases. Correct the `evals/uites` typo in §8.1 to `evals/suites`; choose whether frozen generated scenarios are committed and how prompt/model revisions are compared. Reserve live-model eval for an explicit environment and keep deterministic policy/Go gates separate from stochastic LLM scores.
7. **Define measurable latency and CI boundaries.** Measure enqueue p95 from HTTP request start to response, and first-offer p95 from accepted enqueue to committed offer; specify sample size, warm-up, hardware/container context, and failed/no-rider outcomes. `go test -race` finds executed in-process races, **not** cross-process double-booking; pair it with DB-backed contention/restart tests. On Windows the race detector also needs a compatible C compiler; CI can run it on Linux. Source: [Go race detector requirements and limitations](https://go.dev/doc/articles/race_detector).
8. **Make agent confirmation replay-safe.** If LangGraph is selected, an interrupt requires a checkpointer and stable thread ID; its node restarts on resume, so create proposals in a separate idempotent step and perform commit only after confirmation. Never place a non-idempotent proposal creation before an interrupt without a stable idempotency key. Source: [LangGraph interrupts](https://docs.langchain.com/oss/python/langgraph/interrupts).

## Dependency-ordered implementation backlog

Each task is a small reviewable unit. **Gate** means the evidence needed to mark the task complete. Phase D must wait for Phase B's race and integration gates, as required by PROJECT.md.

### Phase A — Contracts and skeleton

- [ ] **A0 · Freeze unresolved contracts (blocks A1–E).** Decide the eight points above; draw an event/state transition table and a matrix for every mutating route (auth, idempotency scope, request/response, failure code). Decide worker process topology (separate worker in MVP), capacity-1 constraint, ready/terminal events, proposal expiry and commit semantics, eval isolation, and dependency versions. Correct the suite typo. Update PROJECT.md and increment document control if changing public paths/state machine. **Gate:** one unambiguous written contract and no demo step depending on an undefined endpoint.
  - [ ] Record state/event matrix, error codes, and route-by-route auth/idempotency matrix.
  - [ ] Resolve capacity, worker, eval, proposal, and dependency decisions; reconcile PROJECT.md.
- [ ] **A1 · Project tooling.** Create only the specified `go/`, `agent/`, `configs/`, `scripts/`, `evals/`, `load/` locations as needed; add `go.mod`, Python `pyproject.toml`, pinned dependencies, `docker-compose.yml` with health checks, `Makefile`, `.gitignore`, and environment example (never actual secrets). Provide `make up`, `stop`, `test`, `race` targets and platform-appropriate script invocation. **Gate:** clean checkout boots Postgres/Redis and tooling help works without an LLM key.
  - [ ] Pin Go/Python dependencies and add compose health checks and secret-free env template.
  - [ ] Wire Makefile targets and run a clean-checkout infrastructure smoke test.
- [ ] **A2 · Typed config and readiness.** Implement config structs for all keys in §7 with environment overrides, validation (TTLs, pool sizes, bbox, ratios), shutdown deadlines, and separate liveness/readiness endpoints. **Gate:** invalid config fails fast; readiness reflects unavailable dependencies; graceful stop finishes in configured timeout.
  - [ ] Parse and validate every documented config key and env override.
  - [ ] Implement health handlers, dependency probes, and bounded shutdown.
- [ ] **A3 · Schema and migrations.** Add `restaurants`, `riders`, `orders`, `assignments`, `proposals`, webhook and generic idempotency ledgers, assignment-event trace, and durable work/expiry records (if agreed in A0). Add FKs, status constraints, timestamps, indexes for queue/snapshot reads, and active-uniqueness enforcement. **Gate:** migrate up/down on disposable DB; constraint tests reject duplicate active assignments.
  - [ ] Create migration for entities, idempotency, trace, and durable dispatch records.
  - [ ] Add FK/check/partial-unique constraints and queue/read indexes; test migrations and conflicts.

### Phase B — Deterministic Go dispatch core

- [ ] **B1 · Domain state machine.** Implement typed statuses, allowed transitions, stable 409 codes, clock abstraction, scoring breakdown, and boundary validation. **Gate:** table-driven unit tests cover legal/illegal transitions and score ordering/ties.
  - [ ] Encode transitions, domain errors, and clock-injected state changes.
  - [ ] Implement score breakdown and table-driven state/scoring tests.
- [ ] **B2 · API foundation and order lifecycle.** Implement request IDs, JSON validation/errors, scoped auth, generic idempotency middleware/storage, order create/get/ready, and the A0-approved kitchen/terminal actions. **Gate:** HTTP tests prove identical request replay, hash mismatch refusal, error shape, and forbidden operations.
  - [ ] Build HTTP middleware, auth, input/error contracts, and transactional idempotency.
  - [ ] Add order lifecycle handlers and request replay/authorization tests.
- [ ] **B3 · Seeded world and traffic generators.** Implement shared seeded specs and `/admin/seed/world`/test-only reset, then `gen world`, `traffic`, `burst` as HTTP clients; location updates and optional cancel injection use generated IDs returned by APIs. **Gate:** same seed yields identical normalized world/traffic specs; invalid seed/admin token is rejected; no fixture IDs in business logic.
  - [ ] Implement seedable factories and local-only admin world/reset routes.
  - [ ] Implement generator CLI modes, location/cancel traffic, and determinism tests.
- [ ] **B4 · Candidate selection and durable offer worker.** Implement bbox selection, distance/scoring, enqueue, retry/no-candidate backoff, Redis token locks, PostgreSQL transaction + capacity/active constraints, TTL expiration sweeper, idempotent accept/reject, and restart recovery. Persist attempt trace and score inputs. **Gate:** generated-world integration proves ready → offer → accept, timeout → reoffer, rejection, unavailable riders, and worker restart without stuck orders.
  - [ ] Implement enqueue/claim, candidate scoring, and transactional offer creation.
  - [ ] Add accept/reject/expiry, retry sweeper, traces, and restart tests.
- [ ] **B5 · Webhook/cancellation ledger.** Validate simulated rider events, dedupe by key+payload, atomically invalidate active offer/assignment, free capacity, requeue eligible order, and safely ignore stale accepts/duplicates. **Gate:** repeated/colliding webhooks return specified responses with one side effect; a mid-offer cancel recovers.
  - [ ] Implement authenticated event parsing and transactionally stored webhook responses.
  - [ ] Cover cancellation/offline races, duplicates, and old-offer accept attempts.
- [ ] **B6 · Contention and CI gate.** Run parallel assign/accept/cancel across independent API/worker processes backed by real Postgres/Redis; assert at most one active assignment per order/rider, capacity never exceeded, and recovery after killed worker. Run `go test -race ./...` from `go/` with supported toolchain. **Gate:** all DB invariants and race tests pass; record test seed and tooling used.
  - [ ] Build multi-process contention and crash-recovery integration tests.
  - [ ] Add race/integration commands to CI and capture failing seeds on errors.

### Phase C — Ops visibility and safe proposals

- [ ] **C1 · Read projections.** Implement bounded `/v1/ops/snapshot`, order detail, and chronological `/trace` with attempt/expiry/cancel/score evidence. **Gate:** read-only endpoints reflect generator-backed live state without leaking admin data.
  - [ ] Implement indexed snapshot queries and bounded pagination/limits.
  - [ ] Return chronological per-order traces; test generated state visibility.
- [ ] **C2 · Proposal lifecycle.** Validate typed `reassign`, `batch_hint`, and `delay_explain` payloads; persist pending/rejected/expired state and rationale; prevent invented/unknown IDs. **Gate:** malformed, stale, repeated, and unauthorized proposals receive stable results without assignment changes.
  - [ ] Add proposal schemas, validation, storage, and expiry/rejection handling.
  - [ ] Test unknown IDs, invalid payloads, idempotency, and authorization.
- [ ] **C3 · Server-side commit.** Require ops authorization, re-read/lock order and target rider, check proposal status + offer freshness + rider availability, atomically supersede/reassign or reject, write trace/idempotency record. **Gate:** conflicting parallel commit/accept/cancel produces at most one active assignment and never lets an old offer accept after supersession.
  - [ ] Implement guarded transactional commit with status/version rechecks.
  - [ ] Prove concurrent commit/accept/cancel and stale proposal behavior.

### Phase D — Python planner (after B6)

- [ ] **D1 · Restricted HTTP tools.** Implement typed wrappers for only §11.2 read/propose endpoints and a separately authorized, confirmation-bound commit path; enforce request timeout, tool-call budget, request IDs, and latency/error telemetry. **Gate:** schema tests reject malformed payloads and the agent credential cannot call admin/commit directly.
  - [ ] Build typed read/propose clients and separated ops confirmation client.
  - [ ] Add timeouts, call-budget accounting, tracing, and capability-denial tests.
- [ ] **D2 · Planner graph and CLI.** Implement goal → gather → draft → policy check → present proposal → explicit ops confirmation → commit/stop. Ground IDs in tool responses; use structured output, stable idempotency keys, and restart-safe confirmation state if supported. **Gate:** declined confirmation leaves assignment unchanged; accepted generated stalled-VIP case commits through Go.
  - [ ] Build grounded graph, structured proposal output, and local policy checks.
  - [ ] Add CLI confirmation/resume flow; test both approval and denial paths.
- [ ] **D3 · Agent trajectory tests.** Test delay explanation, valid reassign, stale target, invented ID, tool budget exhaustion, denial, and retries with a generated scenario; record tool sequence and cost estimate. **Gate:** no bypass path and deterministic policy tests green without a paid model.
  - [ ] Generate trajectory cases and assert tool ordering, IDs, and call budget.
  - [ ] Test error, retry, denial, and telemetry with deterministic tool responses.

### Phase E — Evaluation, load, and observability

- [ ] **E1 · Generate frozen scenarios.** Write JSON Schema, `configs/eval.yaml`, seeded suite compiler, and a documented generated-output policy. Resolve predicate-based setup references at runtime. **Gate:** schema validates all 24 default cases; identical seed yields identical scenario documents.
  - [ ] Write schema and seeded scenario generator with predicate-based setup.
  - [ ] Validate scenario count, repeatability, and versioned suite policy.
- [ ] **E2 · Go/agent runners and CI.** Isolate cases, drive real HTTP APIs, assert outcomes, tool subset, no invented IDs, and zero double-assignments; write timestamped JSON + human summary. Split deterministic checks from optional live-LLM scoring; enforce configured `>= 0.85` pass rate only on the declared suite. **Gate:** `make eval` fails on threshold/invariant breach and reports seed/model/config; a known-bad policy fails.
  - [ ] Build isolated API/agent runners and invariant/trajectory assertions.
  - [ ] Generate reports, implement threshold exit code, and add CI negative control.
- [ ] **E3 · Logs, metrics, and traces.** Add JSON logs with request/order/rider/offer/proposal IDs, counters in §15, bounded snapshots, and agent latency/token estimates; exclude tokens/secrets. **Gate:** cancel/reoffer and proposal-commit can be traced end-to-end from a request ID.
  - [ ] Instrument API, worker, store transitions, and planner tools.
  - [ ] Verify correlation IDs, metric increments, and secret redaction.
- [ ] **E4 · Reproducible load.** Add parameterized k6/hey script; record warm-up, duration, concurrency, samples, p95 enqueue and first-offer, failures, and machine/compose versions. Compare against `<100 ms` and `<500 ms` local targets, investigate violations before publishing a number. **Gate:** `make load` produces a reproducible report and README metrics below are replaced with actual measured evidence.
  - [ ] Write parameterized workload and define timing/sample methodology.
  - [ ] Run load gate, analyze failures/p95, and publish reproducible results.

### Phase F — Demonstration and release review

- [ ] **F1 · End-to-end demo.** Implement `scripts/demo.sh` per §17: boot, generate world + traffic, snapshot, cancel live offering rider, obtain agent proposal, confirm through ops identity, show committed reassignment and latest eval/load summary. Provide cleanup via `make stop`. **Gate:** fresh checkout executes the full scripted story with no pasted IDs or manual DB edits.
  - [ ] Wire startup, generated traffic, cancel, agent plan, commit, and reporting.
  - [ ] Run full clean-checkout demo and verify cleanup and dynamic IDs.
- [ ] **F2 · Documentation and acceptance.** Replace planned commands with tested quickstart, publish architecture diagram and measured metrics, cite exact seed/config, record supported platforms and LLM key requirements, and run §21 acceptance checklist. **Gate:** second developer can repeat demo and verify race, eval, correctness, and latency evidence.
  - [ ] Write verified quickstart, setup/cleanup, architecture, and limitations.
  - [ ] Record real metrics, run §21 checklist, and obtain independent replay.

## Tracking and verification

Implement A0 → A1–A3 → B1–B6 → C1–C3 → D1–D3 → E1–E4 → F1–F2. Split tasks into PRs where helpful, keeping each PR's tests and spec/README updates together. The dependency-critical path is A0 → A3 → B4/B5/B6 → C3 → D2 → E2 → F1. Suggested evidence is a seeded test run, command and exit status, and generated report; never mark a live API, performance, race, or agent gate complete from documentation alone.

| Measure | Target from PROJECT.md | Current result |
| --- | --- | --- |
| Assignment enqueue p95 | < 100 ms | Not measured — application not implemented |
| Time to first offer p95 | < 500 ms | Not measured — application not implemented |
| Duplicate webhook side effects | 0 | Not tested |
| Double assignments | 0 | Not tested |
| Go race detector | Clean | Not run |
| Generated evaluation pass rate | ≥ 0.85, 0 invented IDs | Not run |

## Interview talk track

1. The planner suggests; only Go commits assignment state under database-enforced invariants.
2. Scoped idempotency keys and the webhook ledger make duplicate rider events safe.
3. Go race tests cover in-process races; transactional contention tests prove cross-process double-book prevention.
4. Eval scenarios are generated from seeds and frozen before comparing policy or prompt revisions.
5. A rider cancel during an offer releases capacity, returns the order to the eligible queue, and reoffers.
6. Greedy score versus latency is a conscious trade-off; weights and offer windows live in config.
