package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type riderWebhookReq struct {
	EventType    string  `json:"event_type"`
	OrderID      *uuid.UUID `json:"order_id,omitempty"`
	AssignmentID *uuid.UUID `json:"assignment_id,omitempty"`
	RiderID      *uuid.UUID `json:"rider_id,omitempty"`
}

func canonicalHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *Server) handleRiderWebhook(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, r, 400, "VALIDATION_ERROR", "Idempotency-Key required for webhooks")
		return
	}
	var req riderWebhookReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid JSON")
		return
	}
	etype := req.EventType
	if etype == "cancelled" || etype == "rider.cancelled" {
		etype = "rider.cancelled"
	} else if etype == "offline" || etype == "rider.offline" {
		etype = "rider.offline"
	} else {
		writeError(w, r, 400, "VALIDATION_ERROR", "event_type must be cancelled|offline")
		return
	}
	hash := canonicalHash(req)
	// Ledger replay.
	var storedHash string
	var storedCode int
	var storedBody []byte
	err := s.Store.Pool.QueryRow(r.Context(),
		`SELECT payload_hash,result_code,response_body FROM webhooks_ledger WHERE idempotency_key=$1`, key).
		Scan(&storedHash, &storedCode, &storedBody)
	if err == nil {
		if storedHash != hash {
			writeError(w, r, 409, "IDEMPOTENCY_KEY_REUSED", "webhook key reused")
			return
		}
		s.Counters.Inc("webhook_duplicates")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(storedCode)
		_, _ = w.Write(storedBody)
		return
	}
	if err != pgx.ErrNoRows {
		writeError(w, r, 500, "INTERNAL", "ledger check failed")
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		writeError(w, r, 500, "INTERNAL", "tx failed")
		return
	}
	defer tx.Rollback(r.Context())
	var orderID *uuid.UUID
	var riderID *uuid.UUID
	var assignID *uuid.UUID
	if req.AssignmentID != nil {
		var oid, rid uuid.UUID
		var st string
		if err := tx.QueryRow(r.Context(), `SELECT order_id,rider_id,status FROM assignments WHERE id=$1 FOR UPDATE`, *req.AssignmentID).Scan(&oid, &rid, &st); err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, r, 404, "ASSIGNMENT_NOT_FOUND", "assignment not found")
				return
			}
			writeError(w, r, 500, "INTERNAL", "lookup failed")
			return
		}
		if st == domain.AssignOffered || st == domain.AssignAccepted {
			_, _ = tx.Exec(r.Context(), `UPDATE assignments SET status=$1 WHERE id=$2`, domain.AssignCancelled, *req.AssignmentID)
			_, _ = tx.Exec(r.Context(), `UPDATE riders SET status=$1,updated_at=now() WHERE id=$2`, domain.RiderAvailable, rid)
			_, _ = tx.Exec(r.Context(), `UPDATE orders SET status=$1,updated_at=now() WHERE id=$2 AND status IN ('offering','assigned')`, domain.OrderReady, oid)
			_ = s.Store.AppendEvent(r.Context(), tx, oid, req.AssignmentID, &rid, "rider_cancel", map[string]any{"event": etype})
		}
		orderID, riderID, assignID = &oid, &rid, req.AssignmentID
		if etype == "rider.offline" {
			_, _ = tx.Exec(r.Context(), `UPDATE riders SET status=$1,updated_at=now() WHERE id=$2`, domain.RiderOffline, rid)
		}
	} else if req.OrderID != nil {
		oid := *req.OrderID
		active, found, _ := s.Store.ActiveAssignmentForOrder(r.Context(), tx, oid)
		if found {
			_, _ = tx.Exec(r.Context(), `UPDATE assignments SET status=$1 WHERE id=$2`, domain.AssignCancelled, active.ID)
			_, _ = tx.Exec(r.Context(), `UPDATE riders SET status=$1,updated_at=now() WHERE id=$2`, domain.RiderAvailable, active.RiderID)
			_ = s.Store.AppendEvent(r.Context(), tx, oid, &active.ID, &active.RiderID, "rider_cancel", map[string]any{"event": etype})
			riderID, assignID = &active.RiderID, &active.ID
			if etype == "rider.offline" {
				_, _ = tx.Exec(r.Context(), `UPDATE riders SET status=$1,updated_at=now() WHERE id=$2`, domain.RiderOffline, active.RiderID)
			}
		} else if req.RiderID != nil && etype == "rider.offline" {
			_, _ = tx.Exec(r.Context(), `UPDATE riders SET status=$1,updated_at=now() WHERE id=$2`, domain.RiderOffline, *req.RiderID)
			riderID = req.RiderID
		}
		_, _ = tx.Exec(r.Context(), `UPDATE orders SET status=$1,updated_at=now() WHERE id=$2 AND status IN ('offering','assigned')`, domain.OrderReady, oid)
		orderID = &oid
	} else if req.RiderID != nil && etype == "rider.offline" {
		_, _ = tx.Exec(r.Context(), `UPDATE riders SET status=$1,updated_at=now() WHERE id=$2`, domain.RiderOffline, *req.RiderID)
		riderID = req.RiderID
	} else {
		writeError(w, r, 400, "VALIDATION_ERROR", "order_id or assignment_id required")
		return
	}
	respBody := map[string]any{"ok": true, "event": etype}
	if orderID != nil {
		respBody["order_id"] = orderID.String()
	}
	if riderID != nil {
		respBody["rider_id"] = riderID.String()
	}
	raw, _ := json.Marshal(respBody)
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO webhooks_ledger(idempotency_key,event_type,payload_hash,result_code,response_body) VALUES($1,$2,$3,200,$4)
		 ON CONFLICT (idempotency_key) DO NOTHING`, key, etype, hash, raw); err != nil {
		writeError(w, r, 500, "INTERNAL", "ledger write failed")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, r, 500, "INTERNAL", "commit failed")
		return
	}
	_ = orderID
	_ = assignID
	writeJSON(w, 200, respBody)
}
