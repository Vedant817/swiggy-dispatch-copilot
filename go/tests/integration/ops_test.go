package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSnapshotAndTrace(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	rest, _ := st.CreateRestaurant(ctx, "ops-kitchen", 12.95, 77.6, 12, 10)
	_, _ = st.CreateRider(ctx, 12.951, 77.601, 4.9)
	o, _ := st.CreateOrder(ctx, rest.ID, "vip", time.Now().Add(30*time.Minute), nil)
	for _, s := range []string{"prepare", "ready"} {
		_, _ = doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/"+s, nil, nil)
	}
	_, _ = doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/assign", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	c, out := doReq(t, srv, "GET", "/v1/ops/snapshot", nil, nil)
	if c != 200 || out["open_orders"] == nil || out["available_riders"] == nil {
		t.Fatalf("snapshot %d %v", c, out)
	}
	c, out = doReq(t, srv, "GET", "/v1/orders/"+o.ID.String()+"/trace", nil, nil)
	if c != 200 {
		t.Fatalf("trace %d %v", c, out)
	}
	if _, ok := out["offers"]; !ok {
		t.Fatalf("trace missing offers %v", out)
	}
	if _, ok := out["events"]; !ok {
		t.Fatalf("trace missing events %v", out)
	}
}
