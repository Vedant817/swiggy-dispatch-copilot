package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	return dsn
}

func TestMigrateAndConstraints(t *testing.T) {
	ctx := context.Background()
	st, err := Connect(ctx, testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	r, err := st.CreateRestaurant(ctx, "test-kitchen", 12.95, 77.6, 12, 10)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := st.CreateRider(ctx, 12.95, 77.6, 4.8)
	if err != nil {
		t.Fatal(err)
	}
	o, err := st.CreateOrder(ctx, r.ID, "normal", time.Now().Add(30*time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Insert two active assignments for same order: second must fail.
	exp := time.Now().Add(time.Minute)
	if _, err := st.Pool.Exec(ctx, `INSERT INTO assignments(order_id,rider_id,expires_at) VALUES($1,$2,$3)`, o.ID, rd.ID, exp); err != nil {
		t.Fatal(err)
	}
	rd2, err := st.CreateRider(ctx, 12.96, 77.61, 4.9)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO assignments(order_id,rider_id,expires_at) VALUES($1,$2,$3)`, o.ID, rd2.ID, exp); err == nil {
		t.Fatal("expected duplicate active assignment per order to fail")
	}
	// Same rider cannot hold two active offers.
	o2, err := st.CreateOrder(ctx, r.ID, "vip", time.Now().Add(30*time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO assignments(order_id,rider_id,expires_at) VALUES($1,$2,$3)`, o2.ID, rd.ID, exp); err == nil {
		t.Fatal("expected duplicate active assignment per rider to fail")
	}
	_ = uuid.New()
}
