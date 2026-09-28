package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestProposalLifecycle(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	rest, _ := st.CreateRestaurant(ctx, "prop-kitchen", 12.95, 77.6, 12, 10)
	rd, _ := st.CreateRider(ctx, 12.951, 77.601, 4.9)
	o, _ := st.CreateOrder(ctx, rest.ID, "vip", time.Now().Add(30*time.Minute), nil)
	// Valid reassign creates pending, no assignment side effect.
	c, out := doReq(t, srv, "POST", "/v1/proposals",
		map[string]any{"type": "reassign", "payload": map[string]any{"order_id": o.ID.String(), "rider_id": rd.ID.String()}, "reason": "test"},
		map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("create %d %v", c, out)
	}
	pid := out["proposal"].(map[string]any)["id"].(string)
	if n, _ := st.CountActiveAssignments(ctx, o.ID); n != 0 {
		t.Fatalf("proposal must not assign")
	}
	// Unknown IDs rejected.
	c, _ = doReq(t, srv, "POST", "/v1/proposals",
		map[string]any{"type": "reassign", "payload": map[string]any{"order_id": uuid.NewString(), "rider_id": rd.ID.String()}},
		nil)
	if c != 404 {
		t.Fatalf("unknown order should be 404, got %d", c)
	}
	// Malformed rejected.
	c, _ = doReq(t, srv, "POST", "/v1/proposals", map[string]any{"type": "reassign", "payload": map[string]any{}}, nil)
	if c != 400 {
		t.Fatalf("malformed should be 400, got %d", c)
	}
	// Get.
	c, _ = doReq(t, srv, "GET", "/v1/proposals/"+pid, nil, nil)
	if c != 200 {
		t.Fatalf("get %d", c)
	}
	// Reject requires ops token only when configured; open local passes.
	c, _ = doReq(t, srv, "POST", "/v1/proposals/"+pid+"/reject", map[string]any{"reason": "no"}, nil)
	if c != 200 {
		t.Fatalf("reject %d", c)
	}
}
