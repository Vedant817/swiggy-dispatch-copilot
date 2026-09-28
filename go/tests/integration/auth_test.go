package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTokenEnforcement(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	t.Setenv("ADMIN_TOKEN", "test-admin-secret")
	t.Setenv("OPS_TOKEN", "test-ops-secret")

	// Admin without token -> 401.
	if code, _ := doReq(t, srv, "DELETE", "/admin/reset", nil, nil); code != 401 {
		t.Fatalf("reset without token = %d, want 401", code)
	}
	// Admin with wrong token -> 401.
	if code, _ := doReq(t, srv, "DELETE", "/admin/reset", nil, map[string]string{"X-Admin-Token": "wrong"}); code != 401 {
		t.Fatalf("reset wrong token = %d, want 401", code)
	}
	// Admin with right token -> 200.
	if code, _ := doReq(t, srv, "DELETE", "/admin/reset", nil, map[string]string{"X-Admin-Token": "test-admin-secret"}); code != 200 {
		t.Fatalf("reset with token = %d, want 200", code)
	}

	rest, _ := st.CreateRestaurant(ctx, "auth-kitchen", 12.95, 77.6, 12, 10)
	rd, _ := st.CreateRider(ctx, 12.951, 77.601, 4.9)
	o, _ := st.CreateOrder(ctx, rest.ID, "normal", time.Now().Add(30*time.Minute), nil)
	c, out := doReq(t, srv, "POST", "/v1/proposals",
		map[string]any{"type": "reassign", "payload": map[string]any{"order_id": o.ID.String(), "rider_id": rd.ID.String()}},
		map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("propose %d %v", c, out)
	}
	pid := out["proposal"].(map[string]any)["id"].(string)

	// Commit without ops token -> 403.
	if code, _ := doReq(t, srv, "POST", "/v1/proposals/"+pid+"/commit", nil, nil); code != 403 {
		t.Fatalf("commit without token = %d, want 403", code)
	}
	// Reject without ops token -> 403.
	if code, _ := doReq(t, srv, "POST", "/v1/proposals/"+pid+"/reject", nil, nil); code != 403 {
		t.Fatalf("reject without token = %d, want 403", code)
	}
	// Agent-style read/propose stays open: the 201 above was created without any ops token.
}
