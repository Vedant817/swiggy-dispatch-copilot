package store

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("APP_ENV") == "prod" {
		t.Skip("isolated TEST_DATABASE_URL required; destructive tests never run in prod")
	}
	return dsn
}

func TestConcurrentMigrationsRecordChecksums(t *testing.T) {
	ctx := context.Background()
	dsn := testDSN(t)
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, err := Connect(ctx, dsn)
			if err != nil {
				errs[i] = err
				return
			}
			defer st.Close()
			errs[i] = st.Migrate(ctx)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent migration: %v", err)
		}
	}
	st, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var applied int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM _migrations WHERE checksum IS NOT NULL`).Scan(&applied); err != nil || applied != 4 {
		t.Fatalf("applied migrations: %d %v", applied, err)
	}
}

func TestLegacyMigrationWithoutChecksumIsUpgraded(t *testing.T) {
	ctx := context.Background()
	st, err := Connect(ctx, testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE _migrations SET checksum=NULL WHERE id='001_init.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("upgrade legacy row: %v", err)
	}
	var checksum *string
	if err := st.Pool.QueryRow(ctx, `SELECT checksum FROM _migrations WHERE id='001_init.sql'`).Scan(&checksum); err != nil || checksum == nil || *checksum == "" {
		t.Fatalf("checksum missing after upgrade: %v %v", checksum, err)
	}
}

func TestLegacyMigrationTableWithoutChecksumColumn(t *testing.T) {
	ctx := context.Background()
	admin, err := Connect(ctx, testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	database := "dispatch_legacy_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err := admin.Pool.Exec(ctx, `CREATE DATABASE `+database); err != nil {
		t.Skipf("database creation unavailable for isolated legacy drill: %v", err)
	}
	defer func() {
		if _, err := admin.Pool.Exec(ctx, `DROP DATABASE `+database); err != nil {
			t.Errorf("legacy drill cleanup: %v", err)
		}
	}()
	config, err := pgxpool.ParseConfig(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = database
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Store{Pool: pool}
	if _, err := pool.Exec(ctx, `CREATE TABLE _migrations(id TEXT PRIMARY KEY); INSERT INTO _migrations(id) VALUES('001_init.sql')`); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if err := legacy.Migrate(ctx); err != nil {
		pool.Close()
		t.Fatalf("legacy table migration: %v", err)
	}
	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM _migrations WHERE checksum IS NOT NULL`).Scan(&applied); err != nil || applied != 4 {
		pool.Close()
		t.Fatalf("upgraded checksums: %d %v", applied, err)
	}
	pool.Close()
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
