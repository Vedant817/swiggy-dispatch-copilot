package api

import (
	"net/http"

	"github.com/google/uuid"
)

func (s *Server) handleEnqueueAssign(w http.ResponseWriter, r *http.Request, orderID uuid.UUID) {
	body := map[string]any{"order_id": orderID.String()}
	replayed, hash := s.checkIdem(w, r, "POST /v1/orders/{id}/assign", orderID.String(), body)
	if replayed {
		return
	}
	a, err := s.Assign.TryAssign(r.Context(), orderID)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 500, "INTERNAL", "assign failed")
		}
		return
	}
	resp := map[string]any{"assignment": a}
	s.saveIdem(r, "POST /v1/orders/{id}/assign", orderID.String(), hash, 201, resp)
	writeJSON(w, 201, resp)
}

func (s *Server) handleGetAssignment(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	a, err := s.Store.GetAssignment(r.Context(), id)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 404, "ASSIGNMENT_NOT_FOUND", "assignment not found")
		}
		return
	}
	writeJSON(w, 200, map[string]any{"assignment": a})
}

func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	body := map[string]any{"action": "accept"}
	replayed, hash := s.checkIdem(w, r, "POST /v1/assignments/{id}/accept", id.String(), body)
	if replayed {
		return
	}
	a, err := s.Assign.Accept(r.Context(), id)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 500, "INTERNAL", "accept failed")
		}
		return
	}
	resp := map[string]any{"assignment": a}
	s.saveIdem(r, "POST /v1/assignments/{id}/accept", id.String(), hash, 200, resp)
	writeJSON(w, 200, resp)
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	body := map[string]any{"action": "reject"}
	replayed, hash := s.checkIdem(w, r, "POST /v1/assignments/{id}/reject", id.String(), body)
	if replayed {
		return
	}
	if err := s.Assign.Reject(r.Context(), id); err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 500, "INTERNAL", "reject failed")
		}
		return
	}
	resp := map[string]any{"ok": true}
	s.saveIdem(r, "POST /v1/assignments/{id}/reject", id.String(), hash, 200, resp)
	writeJSON(w, 200, resp)
}
