package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/config"
)

func TestProductionRoleBoundaries(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("SERVICE_TOKEN", strings.Repeat("s", 24))
	t.Setenv("AGENT_TOKEN", strings.Repeat("a", 24))
	t.Setenv("OPS_TOKEN", strings.Repeat("o", 24))
	t.Setenv("WEBHOOK_TOKEN", strings.Repeat("w", 24))
	s := &Server{Cfg: config.Config{}}
	cases := []struct {
		method, path, header, value string
		want bool
	}{
		{http.MethodPost, "/v1/orders", "X-Agent-Token", strings.Repeat("a", 24), false},
		{http.MethodPost, "/v1/orders/x/assign", "X-Agent-Token", strings.Repeat("a", 24), false},
		{http.MethodPost, "/v1/proposals/x/commit", "X-Agent-Token", strings.Repeat("a", 24), false},
		{http.MethodPost, "/v1/webhooks/rider", "X-Agent-Token", strings.Repeat("a", 24), false},
		{http.MethodGet, "/v1/ops/snapshot", "", "", false},
		{http.MethodGet, "/v1/ops/snapshot", "X-Agent-Token", strings.Repeat("a", 24), true},
		{http.MethodPost, "/v1/proposals", "X-Agent-Token", strings.Repeat("a", 24), true},
		{http.MethodPost, "/v1/proposals/x/commit", "X-Ops-Token", strings.Repeat("o", 24), true},
		{http.MethodPost, "/v1/webhooks/rider", "X-Webhook-Token", strings.Repeat("w", 24), true},
		{http.MethodPost, "/v1/orders", "X-Service-Token", strings.Repeat("s", 24), true},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.method == http.MethodPost {
			r.Header.Set("Idempotency-Key", "unique-test-key")
		}
		if tc.header != "" {
			r.Header.Set(tc.header, tc.value)
		}
		w := httptest.NewRecorder()
		if got := s.authorizeV1(w, r); got != tc.want {
			t.Errorf("%s %s role %s: allowed=%v, want %v (%s)", tc.method, tc.path, tc.header, got, tc.want, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/orders", nil)
	r.Header.Set("X-Service-Token", strings.Repeat("s", 24))
	w := httptest.NewRecorder()
	if s.authorizeV1(w, r) || w.Code != http.StatusBadRequest {
		t.Fatalf("missing production idempotency key: status=%d", w.Code)
	}
}
