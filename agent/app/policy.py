"""Local belt-and-suspenders checks before commit. Server is authoritative."""
from __future__ import annotations


def check_commit(order: dict, rider_id: str, available_rider_ids: set[str]) -> tuple[bool, str]:
    status = (order.get("order") or order).get("status") if isinstance(order, dict) else ""
    if status not in ("offering", "ready_for_assign"):
        return False, f"order status {status} not committable"
    if rider_id not in available_rider_ids:
        return False, "target rider not available at read time"
    return True, "ok"


def validate_ids_exist(order_id: str, rider_id: str, seen_order_ids: set[str], seen_rider_ids: set[str]) -> tuple[bool, str]:
    if order_id not in seen_order_ids:
        return False, "invented order_id (not returned by tools)"
    if rider_id not in seen_rider_ids:
        return False, "invented rider_id (not returned by tools)"
    return True, "ok"
