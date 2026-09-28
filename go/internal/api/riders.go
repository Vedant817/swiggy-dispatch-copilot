package api

import (
	"net/http"

	"github.com/google/uuid"
)

func (s *Server) handleListRiders(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "available"
	}
	if status != "available" && status != "offered" && status != "busy" && status != "offline" {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid status")
		return
	}
	riders, err := s.Store.ListRidersByStatus(r.Context(), status, 200)
	if err != nil {
		writeError(w, r, 500, "INTERNAL", "list riders failed")
		return
	}
	writeJSON(w, 200, map[string]any{"riders": riders})
}

func (s *Server) handleRiderLocation(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	var req struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid JSON")
		return
	}
	rd, err := s.Store.UpdateRiderLocation(r.Context(), id, req.Lat, req.Lng)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 404, "RIDER_NOT_FOUND", "rider not found")
		}
		return
	}
	writeJSON(w, 200, map[string]any{"rider": rd})
}
