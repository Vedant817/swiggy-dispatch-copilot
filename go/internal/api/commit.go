package api

import (
	"encoding/json"
	"net/http"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Server) handleCommitProposal(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if !s.requireOps(w, r) {
		return
	}
	body := map[string]any{"action": "commit"}
	owned, hash := s.beginIdem(w, r, "POST /v1/proposals/{id}/commit", id.String(), body)
	if !owned {
		return
	}
	p, err := s.Store.GetProposal(r.Context(), id)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, 404, "PROPOSAL_NOT_FOUND", "proposal not found")
			return
		}
		writeError(w, r, 500, "INTERNAL", "lookup failed")
		return
	}
	if p.Status != domain.ProposalPending {
		writeError(w, r, 409, "PROPOSAL_STATE_CONFLICT", "proposal "+p.Status)
		return
	}
	if p.Type != domain.ProposalReassign {
		writeError(w, r, 422, "PROPOSAL_NOT_COMMITTABLE", "only reassign commits")
		return
	}
	oidStr, _ := p.Payload["order_id"].(string)
	ridStr, _ := p.Payload["rider_id"].(string)
	oid, err1 := uuid.Parse(oidStr)
	rid, err2 := uuid.Parse(ridStr)
	if err1 != nil || err2 != nil {
		writeError(w, r, 422, "PROPOSAL_NOT_COMMITTABLE", "bad payload IDs")
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		writeError(w, r, 500, "INTERNAL", "tx failed")
		return
	}
	defer tx.Rollback(r.Context())
	// Lock proposal, order, rider in consistent order: proposal, order, rider.
	var pstatus string
	if err := tx.QueryRow(r.Context(), `SELECT status FROM proposals WHERE id=$1 FOR UPDATE`, id).Scan(&pstatus); err != nil {
		writeError(w, r, 404, "PROPOSAL_NOT_FOUND", "proposal not found")
		return
	}
	if pstatus != domain.ProposalPending {
		writeError(w, r, 409, "PROPOSAL_STATE_CONFLICT", "proposal "+pstatus)
		return
	}
	var ostatus string
	if err := tx.QueryRow(r.Context(), `SELECT status FROM orders WHERE id=$1 FOR UPDATE`, oid).Scan(&ostatus); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, 404, "ORDER_NOT_FOUND", "order not found")
			return
		}
		writeError(w, r, 500, "INTERNAL", "order lookup failed")
		return
	}
	if ostatus != domain.OrderOffering && ostatus != domain.OrderReady {
		writeError(w, r, 409, "ORDER_STATE_CONFLICT", "order "+ostatus)
		return
	}
	var rstatus string
	if err := tx.QueryRow(r.Context(), `SELECT status FROM riders WHERE id=$1 FOR UPDATE`, rid).Scan(&rstatus); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, 404, "RIDER_NOT_FOUND", "rider not found")
			return
		}
		writeError(w, r, 500, "INTERNAL", "rider lookup failed")
		return
	}
	if rstatus != domain.RiderAvailable {
		writeError(w, r, 409, "RIDER_STATE_CONFLICT", "rider "+rstatus)
		return
	}
	// Supersede current active offer if any.
	var oldID *uuid.UUID
	var oldRider *uuid.UUID
	var oldStatus string
	var oldAid, oldRid uuid.UUID
	err = tx.QueryRow(r.Context(),
		`SELECT id,rider_id,status FROM assignments WHERE order_id=$1 AND status IN ('offered','accepted') FOR UPDATE`, oid).
		Scan(&oldAid, &oldRid, &oldStatus)
	if err == nil {
		if _, err := tx.Exec(r.Context(), `UPDATE assignments SET status=$1 WHERE id=$2`, domain.AssignSuperseded, oldAid); err != nil {
			writeError(w, r, 500, "INTERNAL", "supersede failed")
			return
		}
		if _, err := tx.Exec(r.Context(), `UPDATE riders SET status=$1,updated_at=now() WHERE id=$2`, domain.RiderAvailable, oldRid); err != nil {
			writeError(w, r, 500, "INTERNAL", "release failed")
			return
		}
		oldID, oldRider = &oldAid, &oldRid
		_ = oldStatus
		_ = s.Store.AppendEvent(r.Context(), tx, oid, &oldAid, &oldRid, "offer_superseded", map[string]any{"by_proposal": id.String()})
	} else if err != pgx.ErrNoRows {
		writeError(w, r, 500, "INTERNAL", "active lookup failed")
		return
	}
	// Commit new accepted assignment directly (ops-confirmed).
	tag, err := tx.Exec(r.Context(), `UPDATE riders SET status=$1,updated_at=now() WHERE id=$2 AND status=$3`, domain.RiderBusy, rid, domain.RiderAvailable)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, r, 409, "RIDER_STATE_CONFLICT", "rider taken")
		return
	}
	var na struct {
		ID uuid.UUID `json:"id"`
	}
	var raw []byte
	ttlSecs := s.Cfg.Assign.OfferTTL.Seconds()
	err = tx.QueryRow(r.Context(),
		`INSERT INTO assignments(order_id,rider_id,status,expires_at,accepted_at,score,score_breakdown)
		 VALUES($1,$2,'accepted',now()+($3 * interval '1 second'),now(),0,'{}')
		 RETURNING id,score_breakdown`,
		oid, rid, ttlSecs).Scan(&na.ID, &raw)
	if err != nil {
		writeError(w, r, 409, "ASSIGNMENT_STATE_CONFLICT", "commit conflict")
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE orders SET status=$1,updated_at=now() WHERE id=$2`, domain.OrderAssigned, oid); err != nil {
		writeError(w, r, 500, "INTERNAL", "order update failed")
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE proposals SET status=$1,updated_at=now() WHERE id=$2`, domain.ProposalAccepted, id); err != nil {
		writeError(w, r, 500, "INTERNAL", "proposal update failed")
		return
	}
	_ = s.Store.AppendEvent(r.Context(), tx, oid, &na.ID, &rid, "proposal_committed", map[string]any{"proposal_id": id.String()})
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, r, 500, "INTERNAL", "commit failed")
		return
	}
	s.Counters.Inc("proposals_committed")
	s.Counters.Inc("assigns_committed")
	resp := map[string]any{"proposal_id": id.String(), "assignment_id": na.ID.String(), "order_id": oid.String()}
	if oldID != nil {
		resp["superseded_assignment_id"] = oldID.String()
	}
	if oldRider != nil {
		resp["released_rider_id"] = oldRider.String()
	}
	_ = json.RawMessage{}
	s.endIdem(r, "POST /v1/proposals/{id}/commit", id.String(), hash, 200, resp)
	writeJSON(w, 200, resp)
}
