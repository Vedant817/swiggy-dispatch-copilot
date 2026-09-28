package api

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/obs"
)

// beginIdem atomically claims an idempotency key. owned=false means response already written.
func (s *Server) beginIdem(w http.ResponseWriter, r *http.Request, tmpl, target string, body any) (owned bool, hash string) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return true, ""
	}
	claimed, res, h, err := s.Store.ClaimIdempotency(r.Context(), key, r.Method, tmpl, target, body)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 500, "INTERNAL", "idempotency check failed")
		}
		return false, ""
	}
	if !claimed {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
		s.Counters.Inc("idempotency_replays")
		obs.Log("idempotent_replay", map[string]any{"key": key, "template": tmpl, "target": target})
		return false, ""
	}
	return true, h
}

func (s *Server) endIdem(r *http.Request, tmpl, target, hash string, status int, body any) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return
	}
	_ = s.Store.CompleteIdempotency(r.Context(), key, r.Method, tmpl, target, status, body)
}

func (s *Server) abortIdem(r *http.Request, tmpl, target string) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return
	}
	_ = s.Store.AbortIdempotency(r.Context(), key, r.Method, tmpl, target)
}

// checkIdem kept for read-only callers that only need replay without claiming.
func (s *Server) checkIdem(w http.ResponseWriter, r *http.Request, tmpl, target string, body any) (bool, string) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return false, ""
	}
	res, hash, err := s.Store.CheckIdempotency(r.Context(), nil, key, r.Method, tmpl, target, body)
	if err != nil {
		if mapStoreError(w, r, err) {
			return true, ""
		}
		writeError(w, r, 500, "INTERNAL", "idempotency check failed")
		return true, ""
	}
	if res.Replay {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
		s.Counters.Inc("idempotency_replays")
		obs.Log("idempotent_replay", map[string]any{"key": key, "template": tmpl, "target": target})
		return true, ""
	}
	return false, hash
}

func (s *Server) saveIdem(r *http.Request, tmpl, target, hash string, status int, body any) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || hash == "" {
		return
	}
	_ = s.Store.SaveIdempotency(r.Context(), nil, key, r.Method, tmpl, target, hash, status, body)
}

func decodeJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// Empty tokens mean open local mode (dev/test). Set ADMIN_TOKEN/OPS_TOKEN to enforce.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	want := s.Cfg.AdminToken()
	if want == "" && !strings.EqualFold(s.Cfg.AppEnv(), "prod") {
		return true
	}
	if !matchesToken(r.Header.Get("X-Admin-Token"), want) {
		writeError(w, r, 401, "UNAUTHORIZED", "admin token required")
		return false
	}
	return true
}

func (s *Server) requireOps(w http.ResponseWriter, r *http.Request) bool {
	want := s.Cfg.OpsToken()
	if want == "" && !strings.EqualFold(s.Cfg.AppEnv(), "prod") {
		return true
	}
	if !matchesToken(r.Header.Get("X-Ops-Token"), want) {
		writeError(w, r, 403, "FORBIDDEN", "ops token required")
		return false
	}
	return true
}

func matchesToken(got, want string) bool {
	return want != "" && len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// authorizeV1 permits only the capabilities assigned to each production role.
// Local mode remains token-optional for the generated-data demo.
func (s *Server) authorizeV1(w http.ResponseWriter, r *http.Request) bool {
	if !strings.EqualFold(s.Cfg.AppEnv(), "prod") {
		if r.URL.Path == "/v1/webhooks/rider" {
			if token := os.Getenv("WEBHOOK_TOKEN"); token != "" && !matchesToken(r.Header.Get("X-Webhook-Token"), token) {
				writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "webhook token required")
				return false
			}
		}
		return true
	}
	path := r.URL.Path
	if strings.HasSuffix(path, "/commit") || (strings.HasPrefix(path, "/v1/proposals/") && strings.HasSuffix(path, "/reject")) {
		return s.requireOps(w, r)
	}
	if path == "/v1/webhooks/rider" {
		if matchesToken(r.Header.Get("X-Webhook-Token"), os.Getenv("WEBHOOK_TOKEN")) {
			return true
		}
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "webhook token required")
		return false
	}
	if r.Method == http.MethodGet || (path == "/v1/proposals" && r.Method == http.MethodPost) {
		if matchesToken(r.Header.Get("X-Agent-Token"), os.Getenv("AGENT_TOKEN")) ||
			matchesToken(r.Header.Get("X-Service-Token"), os.Getenv("SERVICE_TOKEN")) ||
			matchesToken(r.Header.Get("X-Ops-Token"), os.Getenv("OPS_TOKEN")) {
			return true
		}
	} else if matchesToken(r.Header.Get("X-Service-Token"), os.Getenv("SERVICE_TOKEN")) {
		return true
	}
	writeError(w, r, http.StatusForbidden, "FORBIDDEN", "insufficient role for API operation")
	return false
}
