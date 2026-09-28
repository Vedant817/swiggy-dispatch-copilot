package integration

import (
	"context"
	"testing"
	"time"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/api"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/assign"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/redisx"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/store"
	"github.com/google/uuid"
)

func mustOrder(t *testing.T, srv *api.Server, st *store.Store, priority string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	rest, err := st.CreateRestaurant(ctx, "lifecycle-kitchen", 12.95, 77.6, 12, 10)
	if err != nil {
		t.Fatal(err)
	}
	o, err := st.CreateOrder(ctx, rest.ID, priority, time.Now().Add(30*time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	return o.ID
}

func TestCustomerCancelAndTerminalPath(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	_, _ = st.CreateRider(ctx, 12.951, 77.601, 4.9)

	// Cancel from created.
	oid := mustOrder(t, srv, st, "normal")
	if code, _ := doReq(t, srv, "POST", "/v1/orders/"+oid.String()+"/cancel", nil, nil); code != 200 {
		t.Fatalf("cancel from created = %d, want 200", code)
	}
	// Cancel again -> 409 (already cancelled).
	if code, _ := doReq(t, srv, "POST", "/v1/orders/"+oid.String()+"/cancel", nil, nil); code != 409 {
		t.Fatalf("cancel cancelled = %d, want 409", code)
	}
	// Illegal: deliver straight from created-state order.
	oid2 := mustOrder(t, srv, st, "normal")
	if code, _ := doReq(t, srv, "POST", "/v1/orders/"+oid2.String()+"/deliver", nil, nil); code != 409 {
		t.Fatalf("deliver from created = %d, want 409", code)
	}
	// Illegal: pickup from ready.
	for _, s := range []string{"prepare", "ready"} {
		if code, _ := doReq(t, srv, "POST", "/v1/orders/"+oid2.String()+"/"+s, nil, nil); code != 200 {
			t.Fatalf("%s = %d", s, code)
		}
	}
	if code, _ := doReq(t, srv, "POST", "/v1/orders/"+oid2.String()+"/pickup", nil, nil); code != 409 {
		t.Fatalf("pickup from ready = %d, want 409", code)
	}
	// Full terminal path via assign+accept.
	if code, _ := doReq(t, srv, "POST", "/v1/orders/"+oid2.String()+"/assign", nil, map[string]string{"Idempotency-Key": uuid.NewString()}); code != 201 {
		t.Fatalf("assign = %d", code)
	}
	st2, out := doReq(t, srv, "GET", "/v1/orders/"+oid2.String(), nil, nil)
	_ = st2
	aid := out["active_assignment"].(map[string]any)["id"].(string)
	if code, _ := doReq(t, srv, "POST", "/v1/assignments/"+aid+"/accept", nil, map[string]string{"Idempotency-Key": uuid.NewString()}); code != 200 {
		t.Fatalf("accept = %d", code)
	}
	for _, s := range []string{"pickup", "deliver"} {
		if code, _ := doReq(t, srv, "POST", "/v1/orders/"+oid2.String()+"/"+s, nil, nil); code != 200 {
			t.Fatalf("%s = %d", s, code)
		}
	}
	// Delivered is terminal: cancel -> 409.
	if code, _ := doReq(t, srv, "POST", "/v1/orders/"+oid2.String()+"/cancel", nil, nil); code != 409 {
		t.Fatalf("cancel delivered = %d, want 409", code)
	}
}

func TestProposalExpiryCommitRejected(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	rest, _ := st.CreateRestaurant(ctx, "exp-kitchen", 12.95, 77.6, 12, 10)
	rd, _ := st.CreateRider(ctx, 12.951, 77.601, 4.9)
	o, _ := st.CreateOrder(ctx, rest.ID, "normal", time.Now().Add(30*time.Minute), nil)
	c, out := doReq(t, srv, "POST", "/v1/proposals",
		map[string]any{"type": "reassign", "payload": map[string]any{"order_id": o.ID.String(), "rider_id": rd.ID.String()}},
		map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("propose %d", c)
	}
	pid := out["proposal"].(map[string]any)["id"].(string)
	puid, _ := uuid.Parse(pid)
	if _, err := st.Pool.Exec(ctx, `UPDATE proposals SET expires_at = now() - interval '1 minute' WHERE id=$1`, puid); err != nil {
		t.Fatal(err)
	}
	if code, _ := doReq(t, srv, "POST", "/v1/proposals/"+pid+"/commit", nil, map[string]string{"Idempotency-Key": uuid.NewString()}); code != 409 {
		t.Fatalf("commit expired = %d, want 409", code)
	}
}

func TestAssignSurvivesRedisOutage(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	bad, _ := redisx.Dial("127.0.0.1:6399")
	srv.Redis = bad
	srv.Assign = assign.New(srv.Cfg, st, bad, srv.Counters)
	rest, _ := st.CreateRestaurant(ctx, "outage-kitchen", 12.95, 77.6, 12, 10)
	_, _ = st.CreateRider(ctx, 12.951, 77.601, 4.9)
	o, _ := st.CreateOrder(ctx, rest.ID, "normal", time.Now().Add(30*time.Minute), nil)
	for _, s := range []string{"prepare", "ready"} {
		if code, _ := doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/"+s, nil, nil); code != 200 {
			t.Fatalf("%s = %d", s, code)
		}
	}
	// DB constraints (not Redis) enforce correctness, so this must still offer.
	if code, _ := doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/assign", nil, map[string]string{"Idempotency-Key": uuid.NewString()}); code != 201 {
		t.Fatalf("assign during redis outage = %d, want 201", code)
	}
}

func TestMigrateIsRerunnable(t *testing.T) {
	_, st := testServer(t)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("third migrate: %v", err)
	}
}
