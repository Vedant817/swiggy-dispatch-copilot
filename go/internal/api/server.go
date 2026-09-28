// Package api implements HTTP handlers. Full routes land in B2/C phases.
package api

import (
	"context"
	"encoding/json"
	"net/http"

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
	Mux      *http.ServeMux
}

func New(cfg config.Config, st *store.Store, rdb *redisx.Client) *Server {
	s := &Server{Cfg: cfg, Store: st, Redis: rdb, Counters: obs.NewCounters(), Mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.Mux.HandleFunc("/healthz", s.handleLiveness)
	s.Mux.HandleFunc("/readyz", s.handleReadiness)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-Id")
	if rid == "" {
		rid = uuid.NewString()
	}
	w.Header().Set("X-Request-Id", rid)
	s.Mux.ServeHTTP(w, r)
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
		ctx, cancel := context.WithTimeout(ctx, 2_000_000_000)
		defer cancel()
		if err := s.Redis.Ping(ctx); err != nil {
			writeJSON(w, 503, map[string]string{"status": "redis_unavailable"})
			return
		}
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
