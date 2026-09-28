package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
)

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var openOrders, offering, availableRiders int
	_ = s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE status IN ('created','preparing','ready_for_assign','offering')`).Scan(&openOrders)
	_ = s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE status='offering'`).Scan(&offering)
	_ = s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM riders WHERE status='available'`).Scan(&availableRiders)
	rows, _ := s.Store.Pool.Query(ctx,
		`SELECT id,order_id,rider_id,expires_at FROM assignments WHERE status='offered' ORDER BY expires_at ASC LIMIT 20`)
	defer func() {
		if rows != nil {
			rows.Close()
		}
	}()
	type TO struct {
		AssignmentID string `json:"assignment_id"`
		OrderID      string `json:"order_id"`
		ExpiresAt    string `json:"expires_at"`
	}
	timeouts := []TO{}
	if rows != nil {
		for rows.Next() {
			var aid, oid, rid uuid.UUID
			var exp string
			_ = rows.Scan(&aid, &oid, &rid, &exp)
			timeouts = append(timeouts, TO{AssignmentID: aid.String(), OrderID: oid.String(), ExpiresAt: exp})
		}
	}
	writeJSON(w, 200, map[string]any{
		"open_orders":      openOrders,
		"offering":         offering,
		"available_riders": availableRiders,
		"offering_timeouts": timeouts,
		"counters":         s.Counters.Snapshot(),
	})
}

func (s *Server) handleOrderTrace(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	o, err := s.Store.GetOrder(r.Context(), id)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 404, "ORDER_NOT_FOUND", "order not found")
		}
		return
	}
	events, err := s.Store.ListEvents(r.Context(), id)
	if err != nil {
		writeError(w, r, 500, "INTERNAL", "trace failed")
		return
	}
	rows, _ := s.Store.Pool.Query(r.Context(),
		`SELECT id,rider_id,status,score,score_breakdown,offered_at,expires_at FROM assignments WHERE order_id=$1 ORDER BY offered_at ASC`, id)
	offers := []map[string]any{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var aid, rid uuid.UUID
			var st string
			var score float64
			var raw []byte
			var offered, expires string
			_ = rows.Scan(&aid, &rid, &st, &score, &raw, &offered, &expires)
			var bd map[string]any
			_ = json.Unmarshal(raw, &bd)
			offers = append(offers, map[string]any{
				"assignment_id": aid.String(), "rider_id": rid.String(), "status": st,
				"score": score, "score_breakdown": bd, "offered_at": offered, "expires_at": expires,
			})
		}
	}
	writeJSON(w, 200, map[string]any{"order": o, "offers": offers, "events": events})
}
