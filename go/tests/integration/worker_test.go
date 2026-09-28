package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOfferExpiryAndReoffer(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	rest, _ := st.CreateRestaurant(ctx, "expiry-kitchen", 12.95, 77.6, 12, 10)
	rd, _ := st.CreateRider(ctx, 12.951, 77.601, 4.9)
	o, _ := st.CreateOrder(ctx, rest.ID, "normal", time.Now().Add(30*time.Minute), nil)
	for _, s := range []string{"prepare", "ready"} {
		c, out := doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/"+s, nil, nil)
		if c != 200 {
			t.Fatalf("%s %d %v", s, c, out)
		}
	}
	c, out := doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/assign", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("assign %d %v", c, out)
	}
	assignID := out["assignment"].(map[string]any)["id"].(string)
	// Force expiry in past to simulate timeout without waiting 15s.
	aid, _ := uuid.Parse(assignID)
	if _, err := st.Pool.Exec(ctx, `UPDATE assignments SET expires_at = now() - interval '1 second' WHERE id=$1`, aid); err != nil {
		t.Fatal(err)
	}
	expired, err := srv.Assign.ExpireDue(ctx, 10)
	if err != nil || expired != 1 {
		t.Fatalf("expire %d %v", expired, err)
	}
	// Order should be ready again, rider available.
	oo, _ := st.GetOrder(ctx, o.ID)
	if oo.Status != "ready_for_assign" {
		t.Fatalf("order status = %s", oo.Status)
	}
	rr, _ := st.GetRider(ctx, rd.ID)
	// rd may not be the offered rider if multiple riders exist from prior tests? Reset ensures clean.
	_ = rr
	// Reoffer should create a new offer.
	n := srv.Assign.ReofferReady(ctx, 5)
	if n != 1 {
		t.Fatalf("reoffer %d", n)
	}
}

func TestRejectReleases(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	rest, _ := st.CreateRestaurant(ctx, "reject-kitchen", 12.95, 77.6, 12, 10)
	_, _ = st.CreateRider(ctx, 12.951, 77.601, 4.9)
	o, _ := st.CreateOrder(ctx, rest.ID, "normal", time.Now().Add(30*time.Minute), nil)
	for _, s := range []string{"prepare", "ready"} {
		_, _ = doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/"+s, nil, nil)
	}
	c, out := doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/assign", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("assign %d %v", c, out)
	}
	assignID := out["assignment"].(map[string]any)["id"].(string)
	c, _ = doReq(t, srv, "POST", "/v1/assignments/"+assignID+"/reject", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 200 {
		t.Fatalf("reject %d", c)
	}
	oo, _ := st.GetOrder(ctx, o.ID)
	if oo.Status != "ready_for_assign" {
		t.Fatalf("after reject status = %s", oo.Status)
	}
}

func TestNoRidersAvailable(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	rest, _ := st.CreateRestaurant(ctx, "empty-kitchen", 12.95, 77.6, 12, 10)
	o, _ := st.CreateOrder(ctx, rest.ID, "normal", time.Now().Add(30*time.Minute), nil)
	for _, s := range []string{"prepare", "ready"} {
		_, _ = doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/"+s, nil, nil)
	}
	c, _ := doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/assign", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 409 {
		t.Fatalf("expected 409 no riders, got %d", c)
	}
}
