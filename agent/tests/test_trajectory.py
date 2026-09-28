"""Deterministic trajectory test with fake HTTP client (no LLM, no DB)."""
from app.graph import plan_reassign


class FakeClient:
    def __init__(self):
        self.calls = []
        self.proposed = False

    def get_ops_snapshot(self):
        self.calls.append("get_ops_snapshot")
        return {"open_orders": 1}

    def get_order(self, oid):
        self.calls.append("get_order")
        return {"order": {"status": "offering"}, "active_assignment": {"rider_id": "r-old"}}

    def get_order_trace(self, oid):
        self.calls.append("get_order_trace")
        return {"events": [{"event": "offer_created"}]}

    def list_available_riders(self):
        self.calls.append("list_available_riders")
        return {"riders": [{"id": "r-old"}, {"id": "r-new"}]}

    def propose_reassign(self, order_id, rider_id, reason, idempotency_key=None):
        self.calls.append("propose_reassign")
        assert order_id == "o-1" and rider_id == "r-new"
        assert idempotency_key
        self.proposed = True
        return {"proposal": {"id": "p-1"}}

    def commit_proposal(self, pid, ops_token=None):
        self.calls.append("commit_proposal")
        return {"ok": True}

    def tool_names(self):
        return list(self.calls)


def test_trajectory():
    c = FakeClient()
    res = plan_reassign(c, "reassign stalled VIP", order_id="o-1", auto_confirm=True)
    assert res.proposal_id == "p-1" and res.committed
    assert c.calls[:4] == ["get_ops_snapshot", "get_order", "get_order_trace", "list_available_riders"]
    assert "propose_reassign" in c.calls and "commit_proposal" in c.calls
