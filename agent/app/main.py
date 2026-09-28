"""CLI: python -m app.main --goal ... --order ... [--auto-confirm]."""
from __future__ import annotations

import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

from app.graph import plan_reassign  # noqa: E402
from app.tools import DispatchClient  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser(description="Dispatch planner (propose; Go commits)")
    ap.add_argument("--goal", required=True, help="e.g. reassign stalled VIP order")
    ap.add_argument("--order", required=False, default=None, help="order id to reassign")
    ap.add_argument("--base", required=False, default=os.getenv("AGENT_BASE_URL", "http://127.0.0.1:8080"))
    ap.add_argument("--auto-confirm", action="store_true", help="commit after proposing (requires ops token if configured)")
    ap.add_argument("--ops-token", required=False, default=os.getenv("OPS_TOKEN"))
    ap.add_argument("--max-calls", type=int, default=12)
    args = ap.parse_args()

    client = DispatchClient(args.base, max_calls=args.max_calls)
    try:
        res = plan_reassign(client, args.goal, order_id=args.order, auto_confirm=False, ops_token=args.ops_token)
    except Exception as e:
        print(json.dumps({"ok": False, "error": str(e)}))
        return 1
    print(json.dumps({"ok": True, "order_id": res.order_id, "proposal_id": res.proposal_id,
                      "reason": res.reason, "tools": res.tools_used, "tokens": res.token_estimate}))
    if not res.proposal_id:
        return 0
    should_commit = args.auto_confirm
    if not args.auto_confirm:
        ans = input("Commit proposal? [y/N] ").strip().lower()
        should_commit = (ans == "y")
    if not should_commit:
        print("stopped without commit")
        return 0
    try:
        client.commit_proposal(res.proposal_id, ops_token=args.ops_token)
        print(json.dumps({"committed": True, "proposal_id": res.proposal_id}))
    except Exception as e:
        print(json.dumps({"committed": False, "error": str(e)}))
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
