"""HTTP tools: Go APIs only. No SQL, no admin routes."""
from __future__ import annotations

import time
import uuid

import httpx
from pydantic import BaseModel


class ToolCall(BaseModel):
    name: str
    latency_ms: float
    ok: bool


class DispatchClient:
    def __init__(self, base_url: str, timeout: float = 10.0, max_calls: int = 12):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.max_calls = max_calls
        self.calls: list[ToolCall] = []
        self._client = httpx.Client(timeout=timeout)

    def _guard(self, name: str):
        if len(self.calls) >= self.max_calls:
            raise RuntimeError(f"tool budget exhausted at {name}")
        return time.perf_counter()

    def _record(self, name: str, start: float, ok: bool):
        self.calls.append(ToolCall(name=name, latency_ms=(time.perf_counter() - start) * 1000, ok=ok))

    def _get(self, name: str, path: str, headers: dict | None = None):
        start = self._guard(name)
        try:
            r = self._client.get(self.base_url + path, headers=headers)
            r.raise_for_status()
            self._record(name, start, True)
            return r.json()
        except Exception:
            self._record(name, start, False)
            raise

    def _post(self, name: str, path: str, body: dict | None = None, headers: dict | None = None):
        start = self._guard(name)
        try:
            hdrs = {"Content-Type": "application/json"}
            if headers:
                hdrs.update(headers)
            # Stable idempotency per tool call chain: caller may override.
            if "Idempotency-Key" not in hdrs:
                hdrs["Idempotency-Key"] = str(uuid.uuid4())
            r = self._client.post(self.base_url + path, json=body or {}, headers=hdrs)
            if r.status_code >= 400:
                self._record(name, start, False)
                raise RuntimeError(f"{name} {r.status_code}: {r.text[:500]}")
            self._record(name, start, True)
            return r.json()
        except RuntimeError:
            raise
        except Exception as e:
            self._record(name, start, False)
            raise RuntimeError(f"{name} failed: {e}")

    # Read tools.
    def get_ops_snapshot(self):
        return self._get("get_ops_snapshot", "/v1/ops/snapshot")

    def get_order(self, order_id: str):
        return self._get("get_order", f"/v1/orders/{order_id}")

    def get_order_trace(self, order_id: str):
        return self._get("get_order_trace", f"/v1/orders/{order_id}/trace")

    def list_available_riders(self):
        return self._get("list_available_riders", "/v1/riders?status=available")

    # Propose tools.
    def propose_reassign(self, order_id: str, rider_id: str, reason: str, idempotency_key: str | None = None):
        hdrs = {}
        if idempotency_key:
            hdrs["Idempotency-Key"] = idempotency_key
        return self._post("propose_reassign", "/v1/proposals",
                          {"type": "reassign", "payload": {"order_id": order_id, "rider_id": rider_id}, "reason": reason}, hdrs)

    def propose_batch_hint(self, order_ids: list[str], reason: str, idempotency_key: str | None = None):
        hdrs = {}
        if idempotency_key:
            hdrs["Idempotency-Key"] = idempotency_key
        return self._post("propose_batch_hint", "/v1/proposals",
                          {"type": "batch_hint", "payload": {"order_ids": order_ids}, "reason": reason}, hdrs)

    def commit_proposal(self, proposal_id: str, ops_token: str | None = None):
        hdrs = {}
        if ops_token:
            hdrs["X-Ops-Token"] = ops_token
        return self._post("commit_proposal", f"/v1/proposals/{proposal_id}/commit", {}, hdrs)

    def tool_names(self) -> list[str]:
        return [c.name for c in self.calls]
