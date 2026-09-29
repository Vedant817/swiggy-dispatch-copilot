package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type riderWebhookReq struct {
	EventType    string     `json:"event_type"`
	OrderID      *uuid.UUID `json:"order_id,omitempty"`
	AssignmentID *uuid.UUID `json:"assignment_id,omitempty"`
	RiderID      *uuid.UUID `json:"rider_id,omitempty"`
}

func canonicalHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "unhashable"
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func fail(w http.ResponseWriter, r *http.Request, code int, c, m string) {
	writeError(w, r, code, c, m)
}

func (s *Server) handleRiderWebhook(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		fail(w, r, 400, "VALIDATION_ERROR", "Idempotency-Key required for webhooks")
		return
	}
	var req riderWebhookReq
	if err := decodeJSON(r, &req); err != nil {
		fail(w, r, 400, "VALIDATION_ERROR", "invalid JSON")
		return
	}
	etype := req.EventType
	if etype == "cancelled" || etype == "rider.cancelled" {
		etype = "rider.cancelled"
	} else if etype == "offline" || etype == "rider.offline" {
		etype = "rider.offline"
	} else {
		fail(w, r, 400, "VALIDATION_ERROR", "event_type must be cancelled|rider.cancelled|offline|rider.offline")
		return
	}
	if strings.EqualFold(s.Cfg.AppEnv(), "prod") && req.AssignmentID == nil {
		fail(w, r, 400, "VALIDATION_ERROR", "assignment_id required for production rider events")
		return
	}
	// Hash normalized event so aliases replay.
	norm := riderWebhookReq{EventType: etype, OrderID: req.OrderID, AssignmentID: req.AssignmentID, RiderID: req.RiderID}
	hash := canonicalHash(norm)
	// A rider-only offline event targets the current assignment, if any. The
	// assignment is rechecked under order and assignment locks below.
	if etype == "rider.offline" && req.RiderID != nil && req.OrderID == nil && req.AssignmentID == nil {
		var current uuid.UUID
		err := s.Store.Pool.QueryRow(r.Context(), `SELECT id FROM assignments WHERE rider_id=$1 AND status IN ('offered','accepted')`, *req.RiderID).Scan(&current)
		if err == nil {
			req.AssignmentID = &current
		} else if err != pgx.ErrNoRows {
			fail(w, r, 500, "INTERNAL", "rider assignment lookup failed")
			return
		}
	}

	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		fail(w, r, 500, "INTERNAL", "tx failed")
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize concurrent same-key webhooks.
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, key); err != nil {
		fail(w, r, 500, "INTERNAL", "lock failed")
		return
	}
	var storedHash string
	var storedCode int
	var storedBody []byte
	err = tx.QueryRow(r.Context(),
		`SELECT payload_hash,result_code,response_body FROM webhooks_ledger WHERE idempotency_key=$1`, key).
		Scan(&storedHash, &storedCode, &storedBody)
	if err == nil {
		if storedHash != hash {
			fail(w, r, 409, "IDEMPOTENCY_KEY_REUSED", "webhook key reused")
			return
		}
		s.Counters.Inc("webhook_duplicates")
		_ = tx.Commit(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(storedCode)
		_, _ = w.Write(storedBody)
		return
	}
	if err != pgx.ErrNoRows {
		fail(w, r, 500, "INTERNAL", "ledger check failed")
		return
	}

	exec := func(q string, args ...any) error {
		_, err := tx.Exec(r.Context(), q, args...)
		return err
	}
	var orderID, riderID *uuid.UUID

	if req.AssignmentID != nil {
		var oid, rid uuid.UUID
		var st string
		if err := tx.QueryRow(r.Context(), `SELECT order_id FROM assignments WHERE id=$1`, *req.AssignmentID).Scan(&oid); err != nil {
			if err == pgx.ErrNoRows {
				fail(w, r, 404, "ASSIGNMENT_NOT_FOUND", "assignment not found")
				return
			}
			fail(w, r, 500, "INTERNAL", "lookup failed")
			return
		}
		var orderStatus string
		if err := tx.QueryRow(r.Context(), `SELECT status FROM orders WHERE id=$1 FOR UPDATE`, oid).Scan(&orderStatus); err != nil {
			fail(w, r, 500, "INTERNAL", "order lock failed")
			return
		}
		if err := tx.QueryRow(r.Context(), `SELECT order_id,rider_id,status FROM assignments WHERE id=$1 FOR UPDATE`, *req.AssignmentID).Scan(&oid, &rid, &st); err != nil {
			if err == pgx.ErrNoRows {
				fail(w, r, 404, "ASSIGNMENT_NOT_FOUND", "assignment not found")
				return
			}
			fail(w, r, 500, "INTERNAL", "lookup failed")
			return
		}
		if req.OrderID != nil && *req.OrderID != oid {
			fail(w, r, 400, "VALIDATION_ERROR", "order_id does not match assignment")
			return
		}
		if req.RiderID != nil && *req.RiderID != rid {
			fail(w, r, 400, "VALIDATION_ERROR", "rider_id does not match assignment")
			return
		}
		if etype == "rider.offline" && st != domain.AssignOffered && st != domain.AssignAccepted {
			fail(w, r, 409, "ASSIGNMENT_STATE_CONFLICT", "offline event references a stale assignment")
			return
		}
		if (st == domain.AssignOffered || st == domain.AssignAccepted) && orderStatus != domain.OrderOffering && orderStatus != domain.OrderAssigned {
			fail(w, r, 409, "ORDER_STATE_CONFLICT", "order cannot be requeued")
			return
		}
		if st == domain.AssignOffered || st == domain.AssignAccepted {
			if err := exec(`UPDATE assignments SET status=$1 WHERE id=$2 AND status IN ('offered','accepted')`, domain.AssignCancelled, *req.AssignmentID); err != nil {
				fail(w, r, 500, "INTERNAL", "cancel failed")
				return
			}
			if err := exec(`UPDATE riders SET status=$1,updated_at=now() WHERE id=$2 AND status IN ('offered','busy')`, domain.RiderAvailable, rid); err != nil {
				fail(w, r, 500, "INTERNAL", "rider release failed")
				return
			}
			if err := exec(`UPDATE orders SET status=$1,updated_at=now() WHERE id=$2 AND status IN ('offering','assigned')`, domain.OrderReady, oid); err != nil {
				fail(w, r, 500, "INTERNAL", "requeue failed")
				return
			}
			if err := s.Store.AppendEvent(r.Context(), tx, oid, req.AssignmentID, &rid, "rider_cancel", map[string]any{"event": etype}); err != nil {
				fail(w, r, 500, "INTERNAL", "event failed")
				return
			}
		}
		orderID, riderID = &oid, &rid
		if etype == "rider.offline" {
			if err := exec(`UPDATE riders SET status=$1,updated_at=now() WHERE id=$2`, domain.RiderOffline, rid); err != nil {
				fail(w, r, 500, "INTERNAL", "offline failed")
				return
			}
		}
	} else if req.OrderID != nil {
		oid := *req.OrderID
		var orderStatus string
		if err := tx.QueryRow(r.Context(), `SELECT status FROM orders WHERE id=$1 FOR UPDATE`, oid).Scan(&orderStatus); err != nil {
			if err == pgx.ErrNoRows {
				fail(w, r, 404, "ORDER_NOT_FOUND", "order not found")
				return
			}
			fail(w, r, 500, "INTERNAL", "order lookup failed")
			return
		}
		active, found, err := s.Store.ActiveAssignmentForOrder(r.Context(), tx, oid)
		if err != nil {
			fail(w, r, 500, "INTERNAL", "active lookup failed")
			return
		}
		if found && orderStatus != domain.OrderOffering && orderStatus != domain.OrderAssigned {
			fail(w, r, 409, "ORDER_STATE_CONFLICT", "order cannot be requeued")
			return
		}
		if found {
			if req.RiderID != nil && *req.RiderID != active.RiderID {
				fail(w, r, 400, "VALIDATION_ERROR", "rider_id does not match active assignment")
				return
			}
			// Lock the assignment row before mutating.
			var st string
			if err := tx.QueryRow(r.Context(), `SELECT status FROM assignments WHERE id=$1 FOR UPDATE`, active.ID).Scan(&st); err != nil {
				fail(w, r, 500, "INTERNAL", "lock failed")
				return
			}
			if st == domain.AssignOffered || st == domain.AssignAccepted {
				if err := exec(`UPDATE assignments SET status=$1 WHERE id=$2 AND status IN ('offered','accepted')`, domain.AssignCancelled, active.ID); err != nil {
					fail(w, r, 500, "INTERNAL", "cancel failed")
					return
				}
				if err := exec(`UPDATE riders SET status=$1,updated_at=now() WHERE id=$2 AND status IN ('offered','busy')`, domain.RiderAvailable, active.RiderID); err != nil {
					fail(w, r, 500, "INTERNAL", "rider release failed")
					return
				}
				if err := s.Store.AppendEvent(r.Context(), tx, oid, &active.ID, &active.RiderID, "rider_cancel", map[string]any{"event": etype}); err != nil {
					fail(w, r, 500, "INTERNAL", "event failed")
					return
				}
			}
			riderID = &active.RiderID
			if etype == "rider.offline" {
				if err := exec(`UPDATE riders SET status=$1,updated_at=now() WHERE id=$2`, domain.RiderOffline, active.RiderID); err != nil {
					fail(w, r, 500, "INTERNAL", "offline failed")
					return
				}
			}
		} else if req.RiderID != nil {
			fail(w, r, 409, "RIDER_STATE_CONFLICT", "order has no active assignment for this rider")
			return
		}
		if err := exec(`UPDATE orders SET status=$1,updated_at=now() WHERE id=$2 AND status IN ('offering','assigned')`, domain.OrderReady, oid); err != nil {
			fail(w, r, 500, "INTERNAL", "requeue failed")
			return
		}
		orderID = &oid
	} else if req.RiderID != nil && etype == "rider.offline" {
		tag, err := tx.Exec(r.Context(), `UPDATE riders SET status=$1,updated_at=now() WHERE id=$2 AND NOT EXISTS (SELECT 1 FROM assignments WHERE rider_id=$2 AND status IN ('offered','accepted'))`, domain.RiderOffline, *req.RiderID)
		if err != nil {
			fail(w, r, 500, "INTERNAL", "offline failed")
			return
		}
		if tag.RowsAffected() != 1 {
			fail(w, r, 409, "RIDER_STATE_CONFLICT", "rider has an active assignment or does not exist")
			return
		}
		riderID = req.RiderID
	} else {
		fail(w, r, 400, "VALIDATION_ERROR", "order_id, assignment_id, or rider_id (offline) required")
		return
	}

	respBody := map[string]any{"ok": true, "event": etype}
	if orderID != nil {
		respBody["order_id"] = orderID.String()
	}
	if riderID != nil {
		respBody["rider_id"] = riderID.String()
	}
	raw, err := json.Marshal(respBody)
	if err != nil {
		fail(w, r, 500, "INTERNAL", "encode failed")
		return
	}
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO webhooks_ledger(idempotency_key,event_type,payload_hash,result_code,response_body) VALUES($1,$2,$3,200,$4)`,
		key, etype, hash, raw); err != nil {
		fail(w, r, 500, "INTERNAL", "ledger write failed")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		fail(w, r, 500, "INTERNAL", "commit failed")
		return
	}
	writeJSON(w, 200, respBody)
}
