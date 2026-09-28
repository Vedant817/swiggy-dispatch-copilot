package race

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/api"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/config"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/redisx"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/store"
)

func testSetup(t *testing.T) (*api.Server, *store.Store) {
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
	return api.New(cfg, st, rdb), st
}

func seedOrder(t *testing.T, srv *api.Server, st *store.Store, riders int) string {
	t.Helper()
	ctx := context.Background()
	rest, err := st.CreateRestaurant(ctx, "race-kitchen", 12.95, 77.6, 12, 10)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < riders; i++ {
		if _, err := st.CreateRider(ctx, 12.95+float64(i)*0.001, 77.6, 4.5); err != nil {
			t.Fatal(err)
		}
	}
	o, err := st.CreateOrder(ctx, rest.ID, "vip", time.Now().Add(30*time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetOrderStatus(ctx, nil, o.ID, []string{"created"}, "preparing"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetOrderStatus(ctx, nil, o.ID, []string{"preparing"}, "ready_for_assign"); err != nil {
		t.Fatal(err)
	}
	return o.ID.String()
}

func TestParallelAssignSameOrder(t *testing.T) {
	srv, st := testSetup(t)
	ctx := context.Background()
	orderID := seedOrder(t, srv, st, 5)
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := srv.Assign.TryAssign(ctx, mustParse(orderID))
			if err != nil && !isBenignAssignErr(err) {
				errs[idx] = err
			}
		}(i)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatalf("assign error: %v", e)
		}
	}
	n, err := st.CountActiveAssignments(ctx, mustParse(orderID))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("active assignments = %d, want 1", n)
	}
}

func TestParallelAssignAndExpiry(t *testing.T) {
	srv, st := testSetup(t)
	ctx := context.Background()
	orderID := seedOrder(t, srv, st, 5)
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if _, err := srv.Assign.TryAssign(ctx, mustParse(orderID)); err != nil && !isBenignAssignErr(err) {
				errs[idx] = err
			}
		}(i * 2)
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if _, err := srv.Assign.ExpireDue(ctx, 10); err != nil {
				errs[idx] = err
			}
		}(i*2 + 1)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatalf("race error: %v", e)
		}
	}
	n, err := st.CountActiveAssignments(ctx, mustParse(orderID))
	if err != nil {
		t.Fatal(err)
	}
	if n > 1 {
		t.Fatalf("active = %d, want <=1", n)
	}
}

func TestParallelAcceptAndReject(t *testing.T) {
	srv, st := testSetup(t)
	ctx := context.Background()
	orderID := seedOrder(t, srv, st, 3)
	a, err := srv.Assign.TryAssign(ctx, mustParse(orderID))
	if err != nil {
		t.Fatalf("seed assign: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = srv.Assign.Accept(ctx, a.ID)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = srv.Assign.Reject(ctx, a.ID)
		}()
	}
	wg.Wait()
	n, err := st.CountActiveAssignments(ctx, mustParse(orderID))
	if err != nil {
		t.Fatal(err)
	}
	if n > 1 {
		t.Fatalf("active = %d, want <=1", n)
	}
}

func TestCapacityExhaustion(t *testing.T) {
	srv, st := testSetup(t)
	ctx := context.Background()
	rest, err := st.CreateRestaurant(ctx, "cap-kitchen", 12.95, 77.6, 12, 10)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := st.CreateRider(ctx, 12.95, 77.6, 5.0)
	if err != nil {
		t.Fatal(err)
	}
	if rd.Capacity != 1 {
		t.Fatalf("capacity = %d, want 1", rd.Capacity)
	}
	var orderIDs []string
	for i := 0; i < 3; i++ {
		o, err := st.CreateOrder(ctx, rest.ID, "normal", time.Now().Add(30*time.Minute), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetOrderStatus(ctx, nil, o.ID, []string{"created"}, "preparing"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetOrderStatus(ctx, nil, o.ID, []string{"preparing"}, "ready_for_assign"); err != nil {
			t.Fatal(err)
		}
		orderIDs = append(orderIDs, o.ID.String())
	}
	var wg sync.WaitGroup
	errs := make([]error, len(orderIDs))
	for i, oid := range orderIDs {
		wg.Add(1)
		go func(idx int, id string) {
			defer wg.Done()
			if _, err := srv.Assign.TryAssign(ctx, mustParse(id)); err != nil && !isBenignAssignErr(err) {
				errs[idx] = err
			}
		}(i, oid)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatalf("assign error: %v", e)
		}
	}
	// Rider capacity 1: count active for the single rider across all orders <=1.
	var n int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM assignments WHERE rider_id=$1 AND status IN ('offered','accepted')`, rd.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n > 1 {
		t.Fatalf("rider oversubscribed: %d", n)
	}
}
