package api

import (
	"encoding/json"
	"net/http"
	"time"

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
	abort := func() { s.abortIdem(r, "POST /v1/proposals/{id}/commit", id.String()) }
	p, err := s.Store.GetProposal(r.Context(), id)
	if err != nil {
		abort()
		if err == pgx.ErrNoRows {
			writeError(w, r, 404, "PROPOSAL_NOT_FOUND", "proposal not found")
			return
		}
		writeError(w, r, 500, "INTERNAL", "lookup failed")
		return
	}
	if p.Status != domain.ProposalPending {
		abort()
		writeError(w, r, 409, "PROPOSAL_STATE_CONFLICT", "proposal "+p.Status)
		return
	}
	if p.Type != domain.ProposalReassign {
		abort()
		writeError(w, r, 422, "PROPOSAL_NOT_COMMITTABLE", "only reassign commits")
		return
	}
	oidStr, _ := p.Payload["order_id"].(string)
	ridStr, _ := p.Payload["rider_id"].(string)
	oid, err1 := uuid.Parse(oidStr)
	rid, err2 := uuid.Parse(ridStr)
	if err1 != nil || err2 != nil {
		abort()
		writeError(w, r, 422, "PROPOSAL_NOT_COMMITTABLE", "bad payload IDs")
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		abort()
		writeError(w, r, 500, "INTERNAL", "tx failed")
		return
	}
	defer tx.Rollback(r.Context())
	fail := func(code int, c, m string) {
		abort()
		writeError(w, r, code, c, m)
	}
	// Lock proposal with expiry recheck (TOCTOU-safe).
	var pstatus string
	var pexp time.Time
	if err := tx.QueryRow(r.Context(), `SELECT status,expires_at FROM proposals WHERE id=$1 FOR UPDATE`, id).Scan(&pstatus, &pexp); err != nil {
		fail(404, "PROPOSAL_NOT_FOUND", "proposal not found")
		return
	}
	if pstatus != domain.ProposalPending {
		fail(409, "PROPOSAL_STATE_CONFLICT", "proposal "+pstatus)
		return
	}
	if time.Now().After(pexp) {
		_, _ = tx.Exec(r.Context(), `UPDATE proposals SET status='expired',updated_at=now() WHERE id=$1`, id)
		_ = tx.Commit(r.Context())
		fail(409, "PROPOSAL_STATE_CONFLICT", "proposal expired")
		return
	}
	var ostatus string
	var opriority string
	var restID uuid.UUID
	if err := tx.QueryRow(r.Context(), `SELECT status,priority,restaurant_id FROM orders WHERE id=$1 FOR UPDATE`, oid).Scan(&ostatus, &opriority, &restID); err != nil {
		if err == pgx.ErrNoRows {
			fail(404, "ORDER_NOT_FOUND", "order not found")
			return
		}
		fail(500, "INTERNAL", "order lookup failed")
		return
	}
	if ostatus != domain.OrderOffering && ostatus != domain.OrderReady {
		fail(409, "ORDER_STATE_CONFLICT", "order "+ostatus)
		return
	}
	var rstatus string
	var rlat, rlng, rrating float64
	if err := tx.QueryRow(r.Context(), `SELECT status,lat,lng,rating FROM riders WHERE id=$1 FOR UPDATE`, rid).Scan(&rstatus, &rlat, &rlng, &rrating); err != nil {
		if err == pgx.ErrNoRows {
			fail(404, "RIDER_NOT_FOUND", "rider not found")
			return
		}
		fail(500, "INTERNAL", "rider lookup failed")
		return
	}
	if rstatus != domain.RiderAvailable {
		fail(409, "RIDER_STATE_CONFLICT", "rider "+rstatus)
		return
	}
	// Supersede current active offer if any.
	var oldID *uuid.UUID
	var oldRider *uuid.UUID
	var oldAid, oldRid uuid.UUID
	err = tx.QueryRow(r.Context(),
		`SELECT id,rider_id FROM assignments WHERE order_id=$1 AND status IN ('offered','accepted') FOR UPDATE`, oid).
		Scan(&oldAid, &oldRid)
	if err == nil {
		if _, err := tx.Exec(r.Context(), `UPDATE assignments SET status=$1 WHERE id=$2 AND status IN ('offered','accepted')`, domain.AssignSuperseded, oldAid); err != nil {
			fail(500, "INTERNAL", "supersede failed")
			return
		}
		if _, err := tx.Exec(r.Context(), `UPDATE riders SET status=$1,updated_at=now() WHERE id=$2 AND status IN ('offered','busy')`, domain.RiderAvailable, oldRid); err != nil {
			fail(500, "INTERNAL", "release failed")
			return
		}
		oldID, oldRider = &oldAid, &oldRid
		_ = s.Store.AppendEvent(r.Context(), tx, oid, &oldAid, &oldRid, "offer_superseded", map[string]any{"by_proposal": id.String()})
	} else if err != pgx.ErrNoRows {
		fail(500, "INTERNAL", "active lookup failed")
		return
	}
	// Score the commit for trace evidence (distance + rating + vip, load 0 at commit time).
	var restLat, restLng float64
	_ = tx.QueryRow(r.Context(), `SELECT lat,lng FROM restaurants WHERE id=$1`, restID).Scan(&restLat, &restLng)
	score := domain.Score(domain.HaversineKm(restLat, restLng, rlat, rlng), 0, rrating, opriority == "vip", domain.Weights{
		DistanceKm: s.Cfg.Scoring.Weights.DistanceKm,
		RiderLoad:  s.Cfg.Scoring.Weights.RiderLoad,
		Rating:     s.Cfg.Scoring.Weights.Rating,
		VipBonus:   s.Cfg.Scoring.Weights.VipBonus,
	})
	scoreRaw, _ := json.Marshal(score)
	// Commit new accepted assignment directly (ops-confirmed).
	tag, err := tx.Exec(r.Context(), `UPDATE riders SET status=$1,updated_at=now() WHERE id=$2 AND status=$3`, domain.RiderBusy, rid, domain.RiderAvailable)
	if err != nil {
		if !mapStoreError(w, r, err) {
			fail(500, "INTERNAL", "rider update failed")
		} else {
			abort()
		}
		return
	}
	if tag.RowsAffected() == 0 {
		fail(409, "RIDER_STATE_CONFLICT", "rider taken")
		return
	}
	var naID uuid.UUID
	err = tx.QueryRow(r.Context(),
		`INSERT INTO assignments(order_id,rider_id,status,expires_at,accepted_at,score,score_breakdown)
		 VALUES($1,$2,'accepted',now()+($3 * interval '1 second'),now(),$4,$5)
		 RETURNING id`,
		oid, rid, s.Cfg.Assign.OfferTTL.Seconds(), score.Score, scoreRaw).Scan(&naID)
	if err != nil {
		if !mapStoreError(w, r, err) {
			fail(409, "ASSIGNMENT_STATE_CONFLICT", "commit conflict")
		} else {
			abort()
		}
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE orders SET status=$1,updated_at=now() WHERE id=$2 AND status IN ('offering','ready_for_assign')`, domain.OrderAssigned, oid); err != nil {
		fail(500, "INTERNAL", "order update failed")
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE proposals SET status=$1,updated_at=now() WHERE id=$2 AND status='pending'`, domain.ProposalAccepted, id); err != nil {
		fail(500, "INTERNAL", "proposal update failed")
		return
	}
	_ = s.Store.AppendEvent(r.Context(), tx, oid, &naID, &rid, "proposal_committed", map[string]any{"proposal_id": id.String(), "score": score})
	if err := tx.Commit(r.Context()); err != nil {
		if !mapStoreError(w, r, err) {
			fail(500, "INTERNAL", "commit failed")
		} else {
			abort()
		}
		return
	}
	s.Counters.Inc("proposals_committed")
	s.Counters.Inc("assigns_committed")
	resp := map[string]any{"proposal_id": id.String(), "assignment_id": naID.String(), "order_id": oid.String(), "score": score.Score}
	if oldID != nil {
		resp["superseded_assignment_id"] = oldID.String()
	}
	if oldRider != nil {
		resp["released_rider_id"] = oldRider.String()
	}
	s.endIdem(r, "POST /v1/proposals/{id}/commit", id.String(), hash, 200, resp)
	writeJSON(w, 200, resp)
}
