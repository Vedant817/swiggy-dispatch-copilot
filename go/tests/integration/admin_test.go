package integration

import (
	"testing"
)

func TestSeedWorldAndReset(t *testing.T) {
	srv, st := testServer(t)
	_ = st
	code, out := doReq(t, srv, "POST", "/admin/seed/world", map[string]any{"seed": 42, "restaurants": 5, "riders": 10}, nil)
	if code != 201 {
		t.Fatalf("seed %d %v", code, out)
	}
	if int(out["restaurants"].(float64)) != 5 || int(out["riders"].(float64)) != 10 {
		t.Fatalf("counts %v", out)
	}
	code, _ = doReq(t, srv, "DELETE", "/admin/reset", nil, nil)
	if code != 200 {
		t.Fatalf("reset %d", code)
	}
	code, out = doReq(t, srv, "GET", "/v1/riders?status=available", nil, nil)
	if code != 200 {
		t.Fatalf("list %d %v", code, out)
	}
	if len(out["riders"].([]any)) != 0 {
		t.Fatalf("reset should clear riders")
	}
}
