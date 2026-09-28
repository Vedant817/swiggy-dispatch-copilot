package api

import (
	"net/http"
	"strings"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/gen"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/obs"
)

type seedWorldReq struct {
	Seed        *int64 `json:"seed"`
	Restaurants *int   `json:"restaurants"`
	Riders      *int   `json:"riders"`
}

func (s *Server) dispatchAdmin(w http.ResponseWriter, r *http.Request) {
	if strings.ToLower(s.Cfg.AppEnv()) == "prod" {
		writeError(w, r, 403, "FORBIDDEN", "admin disabled in prod")
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	switch {
	case r.URL.Path == "/admin/seed/world" && r.Method == http.MethodPost:
		s.handleSeedWorld(w, r)
	case r.URL.Path == "/admin/reset" && r.Method == http.MethodDelete:
		if err := s.Store.Reset(r.Context()); err != nil {
			writeError(w, r, 500, "INTERNAL", "reset failed")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	default:
		writeError(w, r, 404, "NOT_FOUND", "unknown admin route")
	}
}

func (s *Server) handleSeedWorld(w http.ResponseWriter, r *http.Request) {
	var req seedWorldReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid JSON")
		return
	}
	seed := s.Cfg.Generator.Seed
	if req.Seed != nil {
		seed = *req.Seed
	}
	nRest := s.Cfg.Generator.Restaurants
	if req.Restaurants != nil {
		nRest = *req.Restaurants
	}
	nRiders := s.Cfg.Generator.Riders
	if req.Riders != nil {
		nRiders = *req.Riders
	}
	if nRest < 0 || nRiders < 0 || nRest > 5000 || nRiders > 20000 {
		writeError(w, r, 400, "VALIDATION_ERROR", "counts out of range")
		return
	}
	bbox := s.Cfg.Generator.CityBBox
	if len(bbox) != 4 {
		bbox = []float64{12.9, 77.5, 13.0, 77.7}
	}
	spec := gen.GenerateWorld(seed, bbox, nRest, nRiders)
	createdRest := 0
	for _, rs := range spec.Restaurants {
		if _, err := s.Store.CreateRestaurant(r.Context(), rs.Name, rs.Lat, rs.Lng, rs.PrepMinutesP50, rs.Capacity); err != nil {
			obs.Log("seed_failed", map[string]any{"error": err.Error()})
			writeError(w, r, 500, "INTERNAL", "seed restaurant failed")
			return
		}
		createdRest++
	}
	createdRiders := 0
	for _, rs := range spec.Riders {
		if _, err := s.Store.CreateRider(r.Context(), rs.Lat, rs.Lng, rs.Rating); err != nil {
			writeError(w, r, 500, "INTERNAL", "seed rider failed")
			return
		}
		createdRiders++
	}
	writeJSON(w, 201, map[string]any{"seed": seed, "restaurants": createdRest, "riders": createdRiders})
}
