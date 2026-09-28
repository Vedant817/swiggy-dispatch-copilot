// Package api implements HTTP handlers.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/assign"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/config"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/obs"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/redisx"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/store"
	"github.com/google/uuid"
)

type Server struct {
	Cfg      config.Config
	Store    *store.Store
	Redis    *redisx.Client
	Counters *obs.Counters
	Assign   *assign.Service
	Mux      *http.ServeMux
}

func New(cfg config.Config, st *store.Store, rdb *redisx.Client) *Server {
	c := obs.NewCounters()
	s := &Server{Cfg: cfg, Store: st, Redis: rdb, Counters: c, Mux: http.NewServeMux()}
	s.Assign = assign.New(cfg, st, rdb, c)
	s.routes()
	return s
}

func (s *Server) routes() {
	s.Mux.HandleFunc("/healthz", s.handleLiveness)
	s.Mux.HandleFunc("/readyz", s.handleReadiness)
	s.Mux.HandleFunc("/v1/", s.dispatchV1)
	s.Mux.HandleFunc("/admin/", s.dispatchAdmin)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-Id")
	if rid == "" {
		rid = uuid.NewString()
		r.Header.Set("X-Request-Id", rid)
	}
	w.Header().Set("X-Request-Id", rid)
	obs.Log("http_request", map[string]any{"method": r.Method, "path": r.URL.Path, "request_id": rid})
	s.Mux.ServeHTTP(w, r)
}

func parseUUID(s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) dispatchV1(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/v1")
	// Orders collection.
	if p == "/orders" {
		if r.Method == http.MethodPost {
			s.handleCreateOrder(w, r)
			return
		}
		writeError(w, r, 405, "VALIDATION_ERROR", "method not allowed")
		return
	}
	if strings.HasPrefix(p, "/orders/") {
		rest := strings.TrimPrefix(p, "/orders/")
		parts := strings.Split(rest, "/")
		id, ok := parseUUID(parts[0])
		if !ok {
			writeError(w, r, 400, "VALIDATION_ERROR", "invalid order id")
			return
		}
		if len(parts) == 1 {
			if r.Method == http.MethodGet {
				s.handleGetOrder(w, r, id)
				return
			}
			writeError(w, r, 405, "VALIDATION_ERROR", "method not allowed")
			return
		}
		if len(parts) == 2 && parts[1] == "trace" && r.Method == http.MethodGet {
			s.handleOrderTrace(w, r, id)
			return
		}
		if len(parts) == 2 && r.Method == http.MethodPost {
			switch parts[1] {
			case "assign":
				s.handleEnqueueAssign(w, r, id)
				return
			default:
				if t, ok := orderTargets()[parts[1]]; ok {
					s.handleOrderTransition(w, r, id, t.to, t.from, t.event)
					return
				}
			}
		}
		writeError(w, r, 404, "NOT_FOUND", "unknown order route")
		return
	}
	if strings.HasPrefix(p, "/assignments/") {
		rest := strings.TrimPrefix(p, "/assignments/")
		parts := strings.Split(rest, "/")
		id, ok := parseUUID(parts[0])
		if !ok {
			writeError(w, r, 400, "VALIDATION_ERROR", "invalid assignment id")
			return
		}
		if len(parts) == 1 && r.Method == http.MethodGet {
			s.handleGetAssignment(w, r, id)
			return
		}
		if len(parts) == 2 && r.Method == http.MethodPost {
			switch parts[1] {
			case "accept":
				s.handleAccept(w, r, id)
				return
			case "reject":
				s.handleReject(w, r, id)
				return
			}
		}
		writeError(w, r, 404, "NOT_FOUND", "unknown assignment route")
		return
	}
	if p == "/riders" && r.Method == http.MethodGet {
		s.handleListRiders(w, r)
		return
	}
	if p == "/restaurants" && r.Method == http.MethodGet {
		s.handleListRestaurants(w, r)
		return
	}
	if strings.HasPrefix(p, "/riders/") && strings.HasSuffix(p, "/location") && r.Method == http.MethodPost {
		trim := strings.TrimPrefix(p, "/riders/")
		idStr := strings.TrimSuffix(trim, "/location")
		id, ok := parseUUID(idStr)
		if !ok {
			writeError(w, r, 400, "VALIDATION_ERROR", "invalid rider id")
			return
		}
		s.handleRiderLocation(w, r, id)
		return
	}
	if p == "/webhooks/rider" && r.Method == http.MethodPost {
		s.handleRiderWebhook(w, r)
		return
	}
	if p == "/proposals" && r.Method == http.MethodPost {
		s.handleCreateProposal(w, r)
		return
	}
	if strings.HasPrefix(p, "/proposals/") {
		rest := strings.TrimPrefix(p, "/proposals/")
		parts := strings.Split(rest, "/")
		id, ok := parseUUID(parts[0])
		if !ok {
			writeError(w, r, 400, "VALIDATION_ERROR", "invalid proposal id")
			return
		}
		if len(parts) == 1 && r.Method == http.MethodGet {
			s.handleGetProposal(w, r, id)
			return
		}
		if len(parts) == 2 && r.Method == http.MethodPost {
			switch parts[1] {
			case "commit":
				s.handleCommitProposal(w, r, id)
				return
			case "reject":
				s.handleRejectProposal(w, r, id)
				return
			}
		}
		writeError(w, r, 404, "NOT_FOUND", "unknown proposal route")
		return
	}
	if p == "/ops/snapshot" && r.Method == http.MethodGet {
		s.handleSnapshot(w, r)
		return
	}
	writeError(w, r, 404, "NOT_FOUND", "unknown route")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleLiveness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.Store != nil {
		if err := s.Store.Ping(ctx); err != nil {
			writeJSON(w, 503, map[string]string{"status": "postgres_unavailable"})
			return
		}
	}
	if s.Redis != nil {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := s.Redis.Ping(ctx); err != nil {
			writeJSON(w, 503, map[string]string{"status": "redis_unavailable"})
			return
		}
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
