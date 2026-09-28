#!/usr/bin/env python
"""Seeded eval suite compiler: configs/eval.yaml -> evals/suites/*.json (24 default)."""
from __future__ import annotations

import argparse
import json
import os
import random
import sys

ARCHETYPES = [
    ("reassign_after_cancel", "vip", [
        {"action": "create_order", "priority": "vip"},
        {"action": "mark_ready"},
        {"action": "assign"},
        {"action": "webhook_rider_cancel", "when": "after_offer"},
        {"action": "propose_reassign"},
        {"action": "commit_proposal"},
    ], "Propose a valid reassignment after rider cancel.",
     {"order_eventually": "assigned", "proposal_committed": True, "no_double_assign": True,
      "tools_used_subset": ["get_order_trace", "propose_reassign", "commit_proposal"]}),
    ("timeout_reoffer", "normal", [
        {"action": "create_order", "priority": "normal"},
        {"action": "mark_ready"},
        {"action": "assign"},
        {"action": "accept"},
    ], "Offer converts to assignment without double-assign.",
     {"order_eventually": "assigned", "proposal_committed": False, "no_double_assign": True,
      "tools_used_subset": ["get_order_trace"]}),
    ("reject_recovery", "normal", [
        {"action": "create_order", "priority": "normal"},
        {"action": "mark_ready"},
        {"action": "assign"},
        {"action": "reject"},
    ], "Rejected offer returns order to the queue without double-assign.",
     {"order_eventually": "ready_for_assign", "proposal_committed": False, "no_double_assign": True,
      "tools_used_subset": ["get_order_trace"]}),
]


def build_suite(seed: int, count: int, world: dict) -> list[dict]:
    rng = random.Random(seed)
    out = []
    for i in range(count):
        name, prio, script, goal, asserts = ARCHETYPES[i % len(ARCHETYPES)]
        scn_seed = seed * 100000 + i
        # Deterministic priority flip for variety (keeps archetype intent).
        if rng.random() < 0.25:
            script = [dict(s, priority=("vip" if s.get("priority") == "normal" else "normal")) if "priority" in s else dict(s) for s in script]
        out.append({
            "id": f"scn_{name}_{i + 1:03d}",
            "seed": scn_seed,
            "setup": {"world": dict(world), "script": script},
            "agent_goal": goal,
            "assert": asserts,
        })
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--config", default="configs/eval.yaml")
    args = ap.parse_args()
    import yaml
    with open(args.config) as f:
        cfg = yaml.safe_load(f)
    seed = int(cfg.get("seed", 99))
    count = int(cfg.get("scenario_count", 24))
    world = cfg.get("world", {"restaurants": 10, "riders": 25})
    out_dir = cfg.get("output_dir", "evals/suites")
    os.makedirs(out_dir, exist_ok=True)
    suite = build_suite(seed, count, world)
    for scn in suite:
        with open(os.path.join(out_dir, scn["id"] + ".json"), "w") as f:
            json.dump(scn, f, indent=2, sort_keys=True)
    print(f"wrote {len(suite)} scenarios to {out_dir} (seed={seed})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
