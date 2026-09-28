GO_DIR=go
PY=python
CONFIG?=./configs/default.yaml

.PHONY: up down stop build test race gen-world gen-traffic gen-burst eval load demo help

help:
	@echo "up        - docker compose up (postgres, redis, api, worker)"
	@echo "down      - docker compose down"
	@echo "stop      - docker compose stop"
	@echo "build     - build Go binaries"
	@echo "test      - go test ./..."
	@echo "race      - go test -race ./..."
	@echo "gen-world - seed world via gen CLI"
	@echo "gen-traffic - send traffic via gen CLI"
	@echo "eval      - generate suite + run runners"
	@echo "load      - run k6 load script (requires k6)"
	@echo "demo      - run scripts/demo.sh"

up:
	docker compose up --build -d
	@echo "waiting for api..."
	@powershell -NoProfile -Command "for ($$i=0; $$i -lt 30; $$i++) { try { Invoke-RestMethod -Uri http://127.0.0.1:8080/healthz -TimeoutSec 2 | Out-Null; break } catch { Start-Sleep -Seconds 2 } }"

down:
	docker compose down

stop:
	docker compose stop

build:
	cd $(GO_DIR) && go build ./...

test:
	cd $(GO_DIR) && go test ./...

race:
	cd $(GO_DIR) && go test -race ./...

gen-world:
	cd $(GO_DIR) && go run ./cmd/gen world --config ../$(CONFIG)

gen-traffic:
	cd $(GO_DIR) && go run ./cmd/gen traffic --config ../$(CONFIG) --duration 60s

gen-burst:
	cd $(GO_DIR) && go run ./cmd/gen burst --config ../$(CONFIG) --count 200 --concurrency 20

eval:
	$(PY) evals/generate_suite.py --config configs/eval.yaml
	$(PY) evals/runners/go_scenarios.py --config configs/eval.yaml

load:
	k6 run --env BASE_URL=http://127.0.0.1:8080 load/assign_latency.js

demo:
	bash scripts/demo.sh
