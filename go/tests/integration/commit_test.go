package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestProposalCommitReassign(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	rest, _ := st.CreateRestaurant(ctx, "commit-kitchen", 12.95, 77.6, 12, 10)
	r1, _ := st.CreateRider(ctx, 12.951, 77.601, 4.5)
	r2, _ := st.CreateRider(ctx, 12.952, 77.602, 4.9)
	o, _ := st.CreateOrder(ctx, rest.ID, "vip", time.Now().Add(30*time.Minute), nil)
	for _, s := range []string{"prepare", "ready"} {
		_, _ = doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/"+s, nil, nil)
	}
	c, out := doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/assign", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("assign %d %v", c, out)
	}
	oldAssign := out["assignment"].(map[string]any)["id"].(string)
	// Propose reassign to r2 (ensure r2 is available; if r1 got offer, r2 is free; if r2 got offer, use r1).
	_ = r1
	target := r2.ID.String()
	if out["assignment"].(map[string]any)["rider_id"] == target {
		// create third rider to guarantee available target
		r3, _ := st.CreateRider(ctx, 12.953, 77.603, 5.0)
		target = r3.ID.String()
	}
	c, out = doReq(t, srv, "POST", "/v1/proposals",
		map[string]any{"type": "reassign", "payload": map[string]any{"order_id": o.ID.String(), "rider_id": target}, "reason": "stalled"},
		map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("propose %d %v", c, out)
	}
	pid := out["proposal"].(map[string]any)["id"].(string)
	// Concurrent commit + accept old offer: at most one active.
	var wg sync.WaitGroup
	var commitCode, acceptCode int
	wg.Add(2)
	go func() {
		defer wg.Done()
		cc, _ := doReq(t, srv, "POST", "/v1/proposals/"+pid+"/commit", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
		commitCode = cc
	}()
	go func() {
		defer wg.Done()
		ac, _ := doReq(t, srv, "POST", "/v1/assignments/"+oldAssign+"/accept", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
		acceptCode = ac
	}()
	wg.Wait()
	t.Logf("commit=%d accept=%d", commitCode, acceptCode)
	n, _ := st.CountActiveAssignments(ctx, o.ID)
	if n != 1 {
		t.Fatalf("active = %d, want 1", n)
	}
	// Old offer accept after supersession must fail if commit won.
	if commitCode == 200 {
		cc, _ := doReq(t, srv, "POST", "/v1/assignments/"+oldAssign+"/accept", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
		if cc == 200 {
			t.Fatalf("stale accept should fail after supersede")
		}
		oo, _ := st.GetOrder(ctx, o.ID)
		if oo.Status != "assigned" {
			t.Fatalf("order status = %s", oo.Status)
		}
	}
	// Deterministic supersede: fresh order, offer, propose, commit alone must assign.
	o2, _ := st.CreateOrder(ctx, rest.ID, "vip", time.Now().Add(30*time.Minute), nil)
	for _, s := range []string{"prepare", "ready"} {
		_, _ = doReq(t, srv, "POST", "/v1/orders/"+o2.ID.String()+"/"+s, nil, nil)
	}
	c, out = doReq(t, srv, "POST", "/v1/orders/"+o2.ID.String()+"/assign", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("assign2 %d %v", c, out)
	}
	old2 := out["assignment"].(map[string]any)["id"].(string)
	oldRider2 := out["assignment"].(map[string]any)["rider_id"].(string)
	// Pick a different available rider as target.
	c, out = doReq(t, srv, "GET", "/v1/riders?status=available", nil, nil)
	if c != 200 {
		t.Fatalf("riders %d", c)
	}
	target2 := ""
	for _, r := range out["riders"].([]any) {
		id := r.(map[string]any)["id"].(string)
		if id != oldRider2 {
			target2 = id
			break
		}
	}
	if target2 == "" {
		r3, _ := st.CreateRider(ctx, 12.96, 77.62, 5.0)
		target2 = r3.ID.String()
	}
	c, out = doReq(t, srv, "POST", "/v1/proposals",
		map[string]any{"type": "reassign", "payload": map[string]any{"order_id": o2.ID.String(), "rider_id": target2}},
		map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("propose2 %d %v", c, out)
	}
	pid2 := out["proposal"].(map[string]any)["id"].(string)
	c, _ = doReq(t, srv, "POST", "/v1/proposals/"+pid2+"/commit", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 200 {
		t.Fatalf("commit2 %d", c)
	}
	c, _ = doReq(t, srv, "POST", "/v1/assignments/"+old2+"/accept", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c == 200 {
		t.Fatalf("stale accept after supersede should fail")
	}
	// Double commit must 409.
	c, _ = doReq(t, srv, "POST", "/v1/proposals/"+pid2+"/commit", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 409 {
		t.Fatalf("double commit should be 409, got %d", c)
	}
	// batch_hint commit must 422.
	c, out = doReq(t, srv, "POST", "/v1/proposals",
		map[string]any{"type": "batch_hint", "payload": map[string]any{"order_ids": []string{o.ID.String()}}},
		map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("batch propose %d", c)
	}
	bpid := out["proposal"].(map[string]any)["id"].(string)
	c, _ = doReq(t, srv, "POST", "/v1/proposals/"+bpid+"/commit", nil, nil)
	if c != 422 {
		t.Fatalf("batch commit should be 422, got %d", c)
	}
	_ = uuid.New()
}
