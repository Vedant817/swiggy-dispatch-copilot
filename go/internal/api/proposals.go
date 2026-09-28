package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type createProposalReq struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
	Reason  string         `json:"reason"`
}

func (s *Server) handleCreateProposal(w http.ResponseWriter, r *http.Request) {
	var req createProposalReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid JSON")
		return
	}
	if req.Type != "reassign" && req.Type != "batch_hint" && req.Type != "delay_explain" {
		writeError(w, r, 400, "VALIDATION_ERROR", "type must be reassign|batch_hint|delay_explain")
		return
	}
	if req.Payload == nil {
		writeError(w, r, 400, "VALIDATION_ERROR", "payload required")
		return
	}
	// Validate referenced IDs exist (no invented IDs).
	if err := s.validateProposalPayload(r, req.Type, req.Payload); err != nil {
		if ae, ok := err.(*apiErr); ok {
			if ae.code == "NOT_FOUND" {
				writeError(w, r, 404, ae.code, ae.msg)
			} else {
				writeError(w, r, 400, ae.code, ae.msg)
			}
			return
		}
		if !mapStoreError(w, r, err) {
			writeError(w, r, 400, "VALIDATION_ERROR", err.Error())
		}
		return
	}
	body := map[string]any{"type": req.Type, "payload": req.Payload, "reason": req.Reason}
	owned, hash := s.beginIdem(w, r, "POST /v1/proposals", "", body)
	if !owned {
		return
	}
	p, err := s.Store.CreateProposal(r.Context(), req.Type, req.Payload, req.Reason, s.Cfg.Proposals.TTL)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 500, "INTERNAL", "create proposal failed")
		}
		return
	}
	s.Counters.Inc("proposals_created")
	resp := map[string]any{"proposal": p}
	s.endIdem(r, "POST /v1/proposals", "", hash, 201, resp)
	writeJSON(w, 201, resp)
}

func (s *Server) validateProposalPayload(r *http.Request, typ string, payload map[string]any) error {
	ctx := r.Context()
	checkUUID := func(key string) (*uuid.UUID, bool) {
		v, ok := payload[key]
		if !ok {
			return nil, false
		}
		str, ok := v.(string)
		if !ok {
			return nil, false
		}
		id, err := uuid.Parse(str)
		if err != nil {
			return nil, false
		}
		return &id, true
	}
	switch typ {
	case "reassign":
		oid, ok1 := checkUUID("order_id")
		rid, ok2 := checkUUID("rider_id")
		if !ok1 || !ok2 {
			return err400("reassign payload requires order_id and rider_id UUIDs")
		}
		var exists bool
		if err := s.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE id=$1)`, *oid).Scan(&exists); err != nil || !exists {
			return err404("ORDER_NOT_FOUND: proposal order unknown")
		}
		if err := s.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM riders WHERE id=$1)`, *rid).Scan(&exists); err != nil || !exists {
			return err404("RIDER_NOT_FOUND: proposal rider unknown")
		}
	case "batch_hint":
		raw, ok := payload["order_ids"]
		if !ok {
			return err400("batch_hint requires order_ids")
		}
		arr, ok := raw.([]any)
		if !ok || len(arr) == 0 {
			return err400("batch_hint requires non-empty order_ids")
		}
		for _, v := range arr {
			str, ok := v.(string)
			if !ok {
				return err400("order_ids must be UUID strings")
			}
			id, err := uuid.Parse(str)
			if err != nil {
				return err400("order_ids must be UUID strings")
			}
			var exists bool
			if err := s.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE id=$1)`, id).Scan(&exists); err != nil || !exists {
				return err404("ORDER_NOT_FOUND: batch order unknown")
			}
		}
	case "delay_explain":
		oid, ok := checkUUID("order_id")
		if !ok {
			return err400("delay_explain requires order_id UUID")
		}
		var exists bool
		if err := s.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE id=$1)`, *oid).Scan(&exists); err != nil || !exists {
			return err404("ORDER_NOT_FOUND: proposal order unknown")
		}
	}
	return nil
}

func err400(m string) error { return &apiErr{code: "VALIDATION_ERROR", msg: m} }
func err404(m string) error { return &apiErr{code: "NOT_FOUND", msg: m} }

type apiErr struct {
	code, msg string
}

func (e *apiErr) Error() string { return e.code + ": " + e.msg }

func (s *Server) handleGetProposal(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, err := s.Store.GetProposal(r.Context(), id)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, 404, "PROPOSAL_NOT_FOUND", "proposal not found")
			return
		}
		if !mapStoreError(w, r, err) {
			writeError(w, r, 500, "INTERNAL", "get failed")
		}
		return
	}
	writeJSON(w, 200, map[string]any{"proposal": p})
}

func (s *Server) handleRejectProposal(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if !s.requireOps(w, r) {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = decodeJSON(r, &req)
	p, err := s.Store.SetProposalStatus(r.Context(), nil, id, []string{"pending"}, "rejected", req.Reason)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 409, "PROPOSAL_STATE_CONFLICT", "cannot reject")
		}
		return
	}
	s.Counters.Inc("proposals_rejected")
	writeJSON(w, 200, map[string]any{"proposal": p})
}
