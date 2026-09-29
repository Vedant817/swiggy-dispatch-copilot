"""Exercise the built API in APP_ENV=prod against a pre-seeded disposable DB.

Requires BASE_URL and SERVICE_TOKEN, AGENT_TOKEN, OPS_TOKEN, WEBHOOK_TOKEN.
The world must be seeded in local mode before the API is restarted in prod.
"""
import json
import os
import urllib.error
import urllib.request
import uuid


BASE = os.environ.get("BASE_URL", "http://127.0.0.1:8080").rstrip("/")


def call(method, path, role=None, payload=None, expected=200):
    headers = {"Content-Type": "application/json"}
    if role:
        headers[f"X-{role}-Token"] = os.environ[f"{role.upper()}_TOKEN"]
    if method == "POST":
        headers["Idempotency-Key"] = str(uuid.uuid4())
    data = json.dumps(payload or {}).encode() if method == "POST" else None
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            status = response.status
            result = json.load(response)
    except urllib.error.HTTPError as error:
        status = error.code
        result = json.load(error)
    if status != expected:
        raise AssertionError(f"{method} {path}: expected {expected}, got {status}: {result}")
    return result


def main():
    for role in ("SERVICE", "AGENT", "OPS", "WEBHOOK"):
        if not os.environ.get(f"{role}_TOKEN"):
            raise RuntimeError(f"{role}_TOKEN required")
    call("GET", "/readyz")
    call("GET", "/v1/ops/snapshot", expected=403)
    call("GET", "/v1/ops/snapshot", role="Agent")
    call("POST", "/v1/orders", role="Agent", payload={"restaurant_picker": "random"}, expected=403)
    created = call("POST", "/v1/orders", role="Service", payload={"restaurant_picker": "random", "priority": "vip"}, expected=201)
    oid = created["order"]["id"]
    for step in ("prepare", "ready"):
        call("POST", f"/v1/orders/{oid}/{step}", role="Service")
    offer = call("POST", f"/v1/orders/{oid}/assign", role="Service", expected=201)["assignment"]
    old_id = offer["id"]
    call("POST", "/v1/webhooks/rider", role="Webhook", payload={"event_type": "cancelled", "order_id": oid}, expected=400)
    call("POST", "/v1/webhooks/rider", role="Webhook", payload={"event_type": "cancelled", "assignment_id": old_id})
    call("POST", f"/v1/assignments/{old_id}/accept", role="Service", expected=409)
    for _ in range(5):
        riders = call("GET", "/v1/riders?status=available", role="Agent")["riders"]
        if not riders:
            raise AssertionError("no available riders for replacement")
        proposal = call("POST", "/v1/proposals", role="Agent", payload={
            "type": "reassign", "payload": {"order_id": oid, "rider_id": riders[0]["id"]}, "reason": "production smoke"
        }, expected=201)["proposal"]
        try:
            call("POST", f"/v1/proposals/{proposal['id']}/commit", role="Ops")
            break
        except AssertionError as error:
            if "got 409" not in str(error):
                raise
    else:
        raise AssertionError("all replacement commit attempts conflicted")
    order = call("GET", f"/v1/orders/{oid}", role="Agent")
    assert order["order"]["status"] == "assigned", order
    assert order["active_assignment"]["id"] != old_id, order
    metrics = urllib.request.Request(BASE + "/metrics", headers={"X-Ops-Token": os.environ["OPS_TOKEN"]})
    with urllib.request.urlopen(metrics, timeout=15) as response:
        assert response.status == 200 and b"dispatch_" in response.read()
    print("production container smoke passed: role boundaries, offer, webhook, stale accept, proposal commit")


if __name__ == "__main__":
    main()
