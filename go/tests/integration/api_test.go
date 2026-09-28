package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/api"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/config"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/redisx"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/store"
	"github.com/google/uuid"
)

func testServer(t *testing.T) (*api.Server, *store.Store) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	os.Setenv("DATABASE_URL", dsn)
	if os.Getenv("REDIS_ADDR") == "" {
		os.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	}
	cfg, err := config.Load("../../../configs/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	rdb, _ := redisx.Dial(os.Getenv("REDIS_ADDR"))
	srv := api.New(cfg, st, rdb)
	return srv, st
}

func doReq(t *testing.T, srv *api.Server, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("X-Request-Id", uuid.NewString())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("missing X-Request-Id")
	}
	return rec.Code, out
}

func TestOrderLifecycleAndIdempotency(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	rest, err := st.CreateRestaurant(ctx, "lifecycle-kitchen", 12.95, 77.6, 12, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRider(ctx, 12.951, 77.601, 4.8); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	code, out := doReq(t, srv, "POST", "/v1/orders", map[string]any{"restaurant_id": rest.ID.String(), "priority": "vip"}, map[string]string{"Idempotency-Key": key})
	if code != 201 {
		t.Fatalf("create %d %v", code, out)
	}
	orderID := out["order"].(map[string]any)["id"].(string)
	// Replay identical must return 201 same body.
	code2, out2 := doReq(t, srv, "POST", "/v1/orders", map[string]any{"restaurant_id": rest.ID.String(), "priority": "vip"}, map[string]string{"Idempotency-Key": key})
	if code2 != 201 || out2["order"].(map[string]any)["id"] != orderID {
		t.Fatalf("replay failed %d %v", code2, out2)
	}
	// Same key different payload must 409.
	code3, _ := doReq(t, srv, "POST", "/v1/orders", map[string]any{"restaurant_id": rest.ID.String(), "priority": "normal"}, map[string]string{"Idempotency-Key": key})
	if code3 != 409 {
		t.Fatalf("hash mismatch should be 409, got %d", code3)
	}
	oid, _ := uuid.Parse(orderID)
	for _, step := range []string{"prepare", "ready"} {
		c, o := doReq(t, srv, "POST", "/v1/orders/"+oid.String()+"/"+step, nil, nil)
		if c != 200 {
			t.Fatalf("%s %d %v", step, c, o)
		}
	}
	// Assign should create offer.
	c, o := doReq(t, srv, "POST", fmt.Sprintf("/v1/orders/%s/assign", oid), nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 201 {
		t.Fatalf("assign %d %v", c, o)
	}
	assignID := o["assignment"].(map[string]any)["id"].(string)
	// Accept.
	c, o = doReq(t, srv, "POST", "/v1/assignments/"+assignID+"/accept", nil, map[string]string{"Idempotency-Key": uuid.NewString()})
	if c != 200 {
		t.Fatalf("accept %d %v", c, o)
	}
	// Pickup/deliver.
	for _, step := range []string{"pickup", "deliver"} {
		c, o := doReq(t, srv, "POST", "/v1/orders/"+oid.String()+"/"+step, nil, nil)
		if c != 200 {
			t.Fatalf("%s %d %v", step, c, o)
		}
	}
	// Illegal transition: delivered -> ready should 409.
	c, _ = doReq(t, srv, "POST", "/v1/orders/"+oid.String()+"/ready", nil, nil)
	if c != 409 {
		t.Fatalf("illegal transition should be 409, got %d", c)
	}
	_ = time.Now
}
