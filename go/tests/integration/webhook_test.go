package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWebhookCancelRecovers(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	rest, _ := st.CreateRestaurant(ctx, "cancel-kitchen", 12.95, 77.6, 12, 10)
	_, _ = st.CreateRider(ctx, 12.951, 77.601, 4.9)
	o, _ := st.CreateOrder(ctx, rest.ID, "vip", time.Now().Add(30*time.Minute), nil)
	for _, s := range []string{"prepare", "ready"} {
		_, _ = doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/"+s, nil, nil)
	}
	c, out := doReq(t, srv, "POST", "/v1/orders/"+o.ID.String()+"/assign", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("assign %d %v", c, out)
	}
	assignID := out["assignment"].(map[string]any)["id"].(string)
	aid, _ := uuid.Parse(assignID)
	_ = aid
	key := uuid.NewString()
	body := map[string]any{"event_type": "cancelled", "order_id": o.ID.String()}
	c, out = doReq(t, srv, "POST", "/v1/webhooks/rider", body, map[string]string{"Idempotency-Key": key})
	if c != 200 {
		t.Fatalf("webhook %d %v", c, out)
	}
	// Duplicate same key+payload must replay 200.
	c2, _ := doReq(t, srv, "POST", "/v1/webhooks/rider", body, map[string]string{"Idempotency-Key": key})
	if c2 != 200 {
		t.Fatalf("duplicate should replay 200, got %d", c2)
	}
	// Same key different payload must 409.
	c3, _ := doReq(t, srv, "POST", "/v1/webhooks/rider", map[string]any{"event_type": "offline", "order_id": o.ID.String()}, map[string]string{"Idempotency-Key": key})
	if c3 != 409 {
		t.Fatalf("hash mismatch should be 409, got %d", c3)
	}
	// Order should be ready again (not stuck offering).
	oo, _ := st.GetOrder(ctx, o.ID)
	if oo.Status != "ready_for_assign" {
		t.Fatalf("after cancel status = %s", oo.Status)
	}
	// Stale accept on old assignment must fail.
	c4, _ := doReq(t, srv, "POST", "/v1/assignments/"+assignID+"/accept", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c4 == 200 {
		t.Fatalf("stale accept should fail")
	}
	// Reoffer should work.
	if n := srv.Assign.ReofferReady(ctx, 5); n != 1 {
		t.Fatalf("reoffer %d", n)
	}
}

func TestStaleOfflineEventCannotDisableNewAssignment(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	rest, err := st.CreateRestaurant(ctx, "offline-kitchen", 12.95, 77.6, 12, 10)
	if err != nil {
		t.Fatal(err)
	}
	rider, err := st.CreateRider(ctx, 12.951, 77.601, 4.9)
	if err != nil {
		t.Fatal(err)
	}
	o, err := st.CreateOrder(ctx, rest.ID, "vip", time.Now().Add(30*time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/orders/" + o.ID.String()
	for _, step := range []string{"prepare", "ready"} {
		if code, _ := doReq(t, srv, "POST", path+"/"+step, nil, nil); code != 200 {
			t.Fatalf("%s: %d", step, code)
		}
	}
	code, offer := doReq(t, srv, "POST", path+"/assign", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if code != 201 {
		t.Fatalf("offer: %d %v", code, offer)
	}
	oldID := offer["assignment"].(map[string]any)["id"].(string)
	code, _ = doReq(t, srv, "POST", "/v1/webhooks/rider", map[string]any{"event_type": "cancelled", "assignment_id": oldID}, map[string]string{"Idempotency-Key": uuid.NewString()})
	if code != 200 {
		t.Fatalf("cancel webhook: %d", code)
	}
	code, next := doReq(t, srv, "POST", path+"/assign", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if code != 201 {
		t.Fatalf("reoffer: %d %v", code, next)
	}
	code, _ = doReq(t, srv, "POST", "/v1/webhooks/rider", map[string]any{"event_type": "offline", "assignment_id": oldID}, map[string]string{"Idempotency-Key": uuid.NewString()})
	if code != 409 {
		t.Fatalf("stale offline: %d; want 409", code)
	}
	got, err := st.GetRider(ctx, rider.ID)
	if err != nil || got.Status != "offered" {
		t.Fatalf("new offer rider status: %s %v", got.Status, err)
	}
	wrong := uuid.NewString()
	code, _ = doReq(t, srv, "POST", "/v1/webhooks/rider", map[string]any{"event_type": "cancelled", "order_id": o.ID.String(), "rider_id": wrong}, map[string]string{"Idempotency-Key": uuid.NewString()})
	if code != 400 {
		t.Fatalf("mismatched rider: %d; want 400", code)
	}
}

func TestProductionWebhookRejectsUnscopedEvents(t *testing.T) {
	srv, _ := testServer(t)
	t.Setenv("APP_ENV", "prod")
	t.Setenv("WEBHOOK_TOKEN", "webhook-test-credential-at-least-24")
	for _, payload := range []map[string]any{
		{"event_type": "offline", "rider_id": uuid.NewString()},
		{"event_type": "offline", "order_id": uuid.NewString()},
		{"event_type": "cancelled", "order_id": uuid.NewString()},
	} {
		code, response := doReq(t, srv, "POST", "/v1/webhooks/rider", payload, map[string]string{
			"Idempotency-Key": uuid.NewString(),
			"X-Webhook-Token":  "webhook-test-credential-at-least-24",
		})
		if code != 400 || response["code"] != "VALIDATION_ERROR" {
			t.Fatalf("unscoped production event: %d %v", code, response)
		}
	}
}
