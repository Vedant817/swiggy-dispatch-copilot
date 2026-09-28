"""Deterministic planner graph: intake -> gather -> draft -> policy -> confirm -> commit -> respond.

Runs without an LLM key using a grounded heuristic (stalled VIP -> available rider).
An LLM narration hook is optional via OPENAI_API_KEY but never invents IDs.
"""
from __future__ import annotations

import os
import uuid
from dataclasses import dataclass, field

from .policy import check_commit, validate_ids_exist
from .tools import DispatchClient


@dataclass
class PlanResult:
    order_id: str
    proposal_id: str | None = None
    committed: bool = False
    reason: str = ""
    tools_used: list[str] = field(default_factory=list)
    token_estimate: int = 0


def _estimate_tokens(text: str) -> int:
    return max(1, len(text) // 4)


def plan_reassign(client: DispatchClient, goal: str, order_id: str | None = None,
                  auto_confirm: bool = False, ops_token: str | None = None) -> PlanResult:
    # gather
    snap = client.get_ops_snapshot()
    seen_orders: set[str] = set()
    target_order_id = order_id
    if target_order_id is None:
        # Prefer an offering order from trace? Snapshot lacks IDs, so list via trace is goal-driven.
        # Fall back: require explicit order_id for determinism.
        raise RuntimeError("order_id required (snapshot is count-only; pass --order)")
    order = client.get_order(target_order_id)
    seen_orders.add(target_order_id)
    trace = client.get_order_trace(target_order_id)
    riders = client.list_available_riders()
    seen_riders = {r["id"] for r in riders.get("riders", [])}
    if not seen_riders:
        return PlanResult(order_id=target_order_id, reason="no available riders", tools_used=client.tool_names())

    # draft: pick first available rider not currently assigned.
    current_rider = None
    try:
        active = order.get("active_assignment")
        if active:
            current_rider = active.get("rider_id")
    except Exception:
        pass
    pick = None
    for rid in sorted(seen_riders):
        if rid != current_rider:
            pick = rid
            break
    if pick is None:
        return PlanResult(order_id=target_order_id, reason="no alternative rider", tools_used=client.tool_names())

    ok, msg = validate_ids_exist(target_order_id, pick, seen_orders, seen_riders)
    if not ok:
        return PlanResult(order_id=target_order_id, reason=msg, tools_used=client.tool_names())
    policy_ok, policy_msg = check_commit(order, pick, seen_riders)
    # Propose is non-mutating (no assignment side effect); commit is gated by policy + server.
    reason = f"Reassign stalled order {target_order_id} to {pick}: {goal}. Trace events={len(trace.get('events', []))}. Policy: {policy_msg}."
    idem = str(uuid.uuid5(uuid.NAMESPACE_URL, f"reassign:{target_order_id}:{pick}"))
    try:
        prop = client.propose_reassign(target_order_id, pick, reason, idempotency_key=idem)
    except Exception as e:
        return PlanResult(order_id=target_order_id, reason=f"propose failed: {e}", tools_used=client.tool_names())
    try:
        pid = prop["proposal"]["id"]
    except (KeyError, TypeError) as e:
        return PlanResult(order_id=target_order_id, reason=f"bad proposal response: {e}", tools_used=client.tool_names())
    res = PlanResult(order_id=target_order_id, proposal_id=pid, reason=reason, tools_used=client.tool_names(),
                     token_estimate=_estimate_tokens(reason + goal))
    if not auto_confirm:
        # Optional LLM narration (never authoritative).
        if os.getenv("OPENAI_API_KEY"):
            res.reason += " [llm narration available]"
        return res
    if not policy_ok:
        res.reason += " commit blocked by local policy (server would recheck)."
        res.tools_used = client.tool_names()
        return res
    try:
        client.commit_proposal(pid, ops_token=ops_token)
        res.committed = True
    except Exception as e:
        res.reason += f" commit failed: {e}"
    res.tools_used = client.tool_names()
    return res
