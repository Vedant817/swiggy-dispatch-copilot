# Local k6 results — 2026-10-25

These are measurements of the unchanged [k6 workload](../load/assign_latency.js) against the local Go API, not a hosted deployment or production capacity estimate. The script creates an order, prepares it, marks it ready, then requests an assignment. It does not accept offers or complete deliveries. HTTP latency includes successful responses and errors; low latency alone does not establish successful dispatch throughput.

## Machine and runtime

- Windows amd64; Intel Core i7-13700HX, 16 physical cores / 24 logical processors.
- Host RAM reported by Windows: 16,858,472,448 bytes. Docker Linux VM: 24 CPUs, 2,062,827,520 bytes RAM.
- Go `go1.25.4 windows/amd64`; API launched with `go run ./cmd/api`, including the default in-process worker. No standalone worker.
- k6 `v2.3.0 (commit/e088784614, go1.26.8, windows/amd64)`; Docker Engine `29.9.2`, Compose `v5.6.1`; local `postgres:16-alpine` and `redis:7-alpine` containers.
- Seed 42, 40 restaurants; rider counts and VUs are stated for each run. Default application configuration and thresholds were unchanged. No warm-up period or discarded samples.

## Reproduction

Start from the repository root in PowerShell, with k6 and Make on PATH. The local environment has no role tokens configured. This resets only disposable synthetic data.

```powershell
docker compose up -d postgres redis
$env:DATABASE_URL='postgres://dispatch:dispatch@127.0.0.1:5432/dispatch?sslmode=disable'
$env:REDIS_ADDR='127.0.0.1:6379'
$env:APP_ENV='local'
cd go
go run ./cmd/api
```

In a second PowerShell terminal, first seed the Quickstart world:

```powershell
cd go
go run ./cmd/gen world
cd ..
$env:K6_SUMMARY_TREND_STATS='avg,min,med,max,p(50),p(90),p(95),p(99)'
$env:K6_SUMMARY_EXPORT='load/results/2026-10-25-default.json'
$env:NO_COLOR='1'
make load VUS=10 DURATION=60s *> load/results/2026-10-25-default.txt
```

For the expanded-fleet rerun, reset and seed through the existing local admin API. The longer client timeout allows all 20,000 inserts to finish:

```powershell
Invoke-RestMethod -Method Delete http://127.0.0.1:8080/admin/reset
Invoke-RestMethod -Method Post http://127.0.0.1:8080/admin/seed/world `
  -ContentType application/json `
  -Body '{"seed":42,"restaurants":40,"riders":20000}' -TimeoutSec 180
$env:K6_SUMMARY_EXPORT='load/results/2026-10-25-expanded.json'
make load VUS=10 DURATION=60s *> load/results/2026-10-25-expanded.txt

# Reset/reseed the same expanded world again before the separate 1-VU baseline.
Invoke-RestMethod -Method Delete http://127.0.0.1:8080/admin/reset
Invoke-RestMethod -Method Post http://127.0.0.1:8080/admin/seed/world `
  -ContentType application/json `
  -Body '{"seed":42,"restaurants":40,"riders":20000}' -TimeoutSec 180
$env:K6_SUMMARY_EXPORT='load/results/2026-10-25.json'
make load VUS=1 DURATION=60s *> load/results/2026-10-25.txt
```

## Single-VU baseline

With a freshly reset 20,000-rider world, the unchanged script passed all configured thresholds at **1 VU for 60 seconds** (Make/k6 exit 0). This is a separate lower-concurrency baseline, not a repair of the 10-VU result. k6 reported:

```text
http_req_duration..............: avg=12.58ms min=2.62ms  med=8.83ms  max=49.14ms p(50)=8.83ms  p(90)=25.62ms p(95)=27.94ms p(99)=32.38ms
  { op:assign }................: avg=25.18ms min=7.68ms  med=24.85ms max=49.14ms p(50)=24.85ms p(90)=29.7ms  p(95)=31.65ms p(99)=36.3ms
  { op:create }................: avg=10.07ms min=6.18ms  med=9.86ms  max=19.91ms p(50)=9.86ms  p(90)=12.23ms p(95)=13.29ms p(99)=16.23ms
http_req_failed................: 0.25%  12 out of 4704
http_reqs......................: 4704   78.387033/s
vus............................: 1      min=1          max=1
vus_max........................: 1      min=1          max=1
default ✓ [ 100% ] 1 VUs  1m0s
```

The 12 HTTP failures were also 12 failed `assign 201` checks: 1,164 assignments returned 201 out of 1,176 iterations. Thus passing the `<1%` overall HTTP error threshold does not mean every assignment succeeded; the overall rate's denominator includes create, prepare, ready and assign requests. No explicit threshold is configured for the check success rate. Raw [JSON](../load/results/2026-10-25.json) and [terminal output](../load/results/2026-10-25.txt) retain the complete evidence.

## Failed concurrency runs

| Setup | VUs / duration | Requests / rate from k6 | HTTP failures from k6 | Overall p50 / p95 / p99 |
| --- | --- | --- | --- | --- |
| Quickstart: 120 riders | 10 / 60s | `48716  811.096848/s` | `24.03% 11708 out of 48716` | `11.65ms` / `18.65ms` / `28.26ms` |
| Expanded: 20,000 riders | 10 / 60s | `34196  569.333019/s` | `3.92%  1341 out of 34196` | `12.08ms` / `37.59ms` / `44.88ms` |

Both runs passed the create/assign latency thresholds but failed `http_req_failed: rate<0.01` (k6 exit 99, surfaced by Make as exit 2). The first run also overlapped Go tests on a separate database in the same Postgres instance; treat it as a setup diagnostic, not an isolated performance comparison. Subsequent k6 measurements ran without Go tests.

After the first run, a direct assignment request returned `409 NO_RIDERS_AVAILABLE`. The script accumulates outstanding orders, so the default 120-rider fleet runs out of capacity. Enlarging the fleet removed that setup bottleneck but did not make the concurrency test pass. A separate 200-order diagnostic with 10 concurrent clients after the expanded run produced 172 assignment responses with status 201 and 28 with `409 RIDER_STATE_CONFLICT`. This diagnostic is not included in the k6 figures. [TryAssign](../go/internal/assign/service.go) selects a candidate before its transactional conditional claim; competing orders can select the same rider, and the loser returns a conflict. The script treats those conflicts as failed HTTP requests and does not retry.

A passing 10-VU run was not obtained. Fixing candidate contention or teaching the workload a retry/completion lifecycle would change service behavior or the existing scenario, outside this task. The separate 1-VU baseline must not be presented as proof that the 10-VU workload passes.

Raw evidence: [default JSON](../load/results/2026-10-25-default.json), [default terminal output](../load/results/2026-10-25-default.txt), [expanded JSON](../load/results/2026-10-25-expanded.json), [expanded terminal output](../load/results/2026-10-25-expanded.txt). Raw JSON retains full precision; terminal summaries round displayed durations and rates. Request rate counts all four HTTP operations, not completed deliveries or successful assignments per second.
