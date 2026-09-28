#!/usr/bin/env python
"""Agent trajectory runner: reassign scenarios via live planner graph (no LLM key needed)."""
from __future__ import annotations

import argparse
import datetime
import glob
import json
import os
import sys
import urllib.request
import uuid

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "agent"))
from app.graph import plan_reassign  # noqa: E402
from app.tools import DispatchClient  # noqa: E402


def api(base, method, path, body=None, headers=None, retries=4):
    import time
    data = json.dumps(body or {}).encode() if body is not None or method == "POST" else None
    last = None
    for i in range(retries):
        r = urllib.request.Request(base + path, data=data, method=method,
                                   headers={"Content-Type": "application/json", **(headers or {})})
        try:
            with urllib.request.urlopen(r, timeout=15) as resp:
                return resp.status, json.loads(resp.read() or b"{}")
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read() or b"{}")
        except (urllib.error.URLError, ConnectionResetError, ConnectionError, TimeoutError, OSError) as e:
            last = e
            time.sleep(0.5 * (i + 1))
    raise RuntimeError(f"api failed after retries: {last}")


import urllib.error  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--config", default="configs/eval.yaml")
    ap.add_argument("--base", default=None)
    args = ap.parse_args()
    import yaml
    with open(args.config) as f:
        cfg = yaml.safe_load(f)
    base = args.base or cfg.get("base_url", "http://127.0.0.1:8080")
    suites = sorted(glob.glob("evals/suites/scn_reassign_*.json"))
    results = []
    for p in suites:
        with open(p) as f:
            scn = json.load(f)
        sid = scn["id"]
        try:
            world = scn["setup"]["world"]
            api(base, "DELETE", "/admin/reset", {})
            api(base, "POST", "/admin/seed/world",
                {"seed": scn["seed"], "restaurants": world["restaurants"], "riders": world["riders"]}, {})
            st, out = api(base, "POST", "/v1/orders",
                          {"restaurant_picker": "random", "priority": "vip"},
                          {"Idempotency-Key": str(uuid.uuid4())})
            oid = out["order"]["id"]
            for s in ("prepare", "ready"):
                api(base, "POST", f"/v1/orders/{oid}/{s}", {}, {"Idempotency-Key": f"{s}-{oid}-{sid}"})
            api(base, "POST", f"/v1/orders/{oid}/assign", {}, {"Idempotency-Key": f"a-{sid}"})
            api(base, "POST", "/v1/webhooks/rider", {"event_type": "cancelled", "order_id": oid},
                {"Idempotency-Key": f"w-{sid}"})
            # Capture available riders BEFORE planning for invented-ID grounding check.
            st, avail = api(base, "GET", "/v1/riders?status=available")
            available_ids = {r["id"] for r in avail.get("riders", [])}
            client = DispatchClient(base, max_calls=12)
            res = plan_reassign(client, scn["agent_goal"], order_id=oid, auto_confirm=True)
            tools = res.tools_used
            want = scn["assert"].get("tools_used_subset", [])
            ok = res.committed and all(t in tools for t in want)
            st, order = api(base, "GET", f"/v1/orders/{oid}")
            status = (order.get("order") or {}).get("status")
            assigned_rider = (order.get("active_assignment") or {}).get("rider_id")
            # No invented IDs: final rider must be one the tools could have returned.
            if assigned_rider not in available_ids:
                ok = False
            if status != "assigned":
                ok = False
            results.append({"id": sid, "pass": ok, "committed": res.committed, "tools": tools, "status": status})
            print(f"{sid}: {'PASS' if ok else 'FAIL'} {status} {tools}")
        except Exception as e:
            results.append({"id": sid, "pass": False, "error": str(e)[:200]})
            print(f"{sid}: FAIL {e}")
    passed = sum(1 for r in results if r["pass"])
    rate = passed / max(1, len(results))
    ts = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
    os.makedirs("evals/reports", exist_ok=True)
    with open(f"evals/reports/agent-{ts}.json", "w") as f:
        json.dump({"timestamp": ts, "passed": passed, "total": len(results), "pass_rate": rate, "results": results}, f, indent=2)
    with open(f"evals/reports/agent-{ts}.md", "w") as f:
        f.write(f"# Agent eval {ts}\n\npass_rate={rate:.2f} ({passed}/{len(results)})\n")
    print(f"agent pass_rate={rate:.2f} threshold={cfg.get('min_pass_rate')}")
    return 0 if rate >= float(cfg.get("min_pass_rate", 0.85)) else 1


if __name__ == "__main__":
    raise SystemExit(main())
