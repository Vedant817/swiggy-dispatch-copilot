package store

import (
	"context"
	"os"
	"testing"
)

func TestStaleIdempotencyCleanup(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("APP_ENV") == "prod" {
		t.Skip("isolated TEST_DATABASE_URL required; destructive tests never run in prod")
	}
	ctx := context.Background()
	st, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Claim then simulate crash (never complete), backdate, cleanup.
	claimed, _, _, err := st.ClaimIdempotency(ctx, "crash-key", "POST", "POST /v1/orders", "", map[string]any{"a": 1})
	if err != nil || !claimed {
		t.Fatalf("claim = %v %v", claimed, err)
	}
	if _, err := st.Pool.Exec(ctx,
		`UPDATE idempotency_keys SET created_at = now() - interval '10 minutes'
		 WHERE key='crash-key' AND method='POST'`); err != nil {
		t.Fatal(err)
	}
	n, err := st.CleanupStaleIdempotency(ctx, "5 minutes")
	if err != nil || n != 1 {
		t.Fatalf("cleanup = %d %v", n, err)
	}
	// Fresh claim must not be deleted.
	claimed, _, _, err = st.ClaimIdempotency(ctx, "fresh-key", "POST", "POST /v1/orders", "", map[string]any{"a": 1})
	if err != nil || !claimed {
		t.Fatalf("claim2 = %v %v", claimed, err)
	}
	n, err = st.CleanupStaleIdempotency(ctx, "5 minutes")
	if err != nil || n != 0 {
		t.Fatalf("cleanup fresh = %d %v", n, err)
	}
	// Same key+payload can be claimed again after abort path.
	if err := st.AbortIdempotency(ctx, "fresh-key", "POST", "POST /v1/orders", ""); err != nil {
		t.Fatal(err)
	}
	claimed, _, _, err = st.ClaimIdempotency(ctx, "fresh-key", "POST", "POST /v1/orders", "", map[string]any{"a": 1})
	if err != nil || !claimed {
		t.Fatalf("reclaim = %v %v", claimed, err)
	}
	_ = st.AbortIdempotency(ctx, "fresh-key", "POST", "POST /v1/orders", "")
	_ = st.AbortIdempotency(ctx, "crash-key", "POST", "POST /v1/orders", "")
}
