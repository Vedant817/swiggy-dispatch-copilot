package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var openOrders, offering, availableRiders int
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE status IN ('created','preparing','ready_for_assign','offering')`).Scan(&openOrders); err != nil {
		writeError(w, r, 500, "INTERNAL", "snapshot failed")
		return
	}
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE status='offering'`).Scan(&offering); err != nil {
		writeError(w, r, 500, "INTERNAL", "snapshot failed")
		return
	}
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM riders WHERE status='available'`).Scan(&availableRiders); err != nil {
		writeError(w, r, 500, "INTERNAL", "snapshot failed")
		return
	}
	rows, err := s.Store.Pool.Query(ctx,
		`SELECT id,order_id,rider_id,expires_at FROM assignments WHERE status='offered' ORDER BY expires_at ASC, id ASC LIMIT 20`)
	if err != nil {
		writeError(w, r, 500, "INTERNAL", "snapshot failed")
		return
	}
	defer rows.Close()
	type TO struct {
		AssignmentID string    `json:"assignment_id"`
		OrderID      string    `json:"order_id"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	timeouts := []TO{}
	for rows.Next() {
		var aid, oid, rid uuid.UUID
		var exp time.Time
		if err := rows.Scan(&aid, &oid, &rid, &exp); err != nil {
			writeError(w, r, 500, "INTERNAL", "snapshot failed")
			return
		}
		timeouts = append(timeouts, TO{AssignmentID: aid.String(), OrderID: oid.String(), ExpiresAt: exp})
	}
	if err := rows.Err(); err != nil {
		writeError(w, r, 500, "INTERNAL", "snapshot failed")
		return
	}
	writeJSON(w, 200, map[string]any{
		"open_orders":       openOrders,
		"offering":          offering,
		"available_riders":  availableRiders,
		"offering_timeouts": timeouts,
		"counters":          s.Counters.Snapshot(),
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for name, val := range s.Counters.Snapshot() {
		_, _ = w.Write([]byte("dispatch_" + name + " " + itoa(val) + "\n"))
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func (s *Server) handleOrderTrace(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	o, err := s.Store.GetOrder(r.Context(), id)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, 404, "ORDER_NOT_FOUND", "order not found")
			return
		}
		if !mapStoreError(w, r, err) {
			writeError(w, r, 404, "ORDER_NOT_FOUND", "order not found")
		}
		return
	}
	o.IdempotencyKey = nil
	events, err := s.Store.ListEvents(r.Context(), id)
	if err != nil {
		writeError(w, r, 500, "INTERNAL", "trace failed")
		return
	}
	rows, err := s.Store.Pool.Query(r.Context(),
		`SELECT id,rider_id,status,score,score_breakdown,offered_at,expires_at FROM assignments WHERE order_id=$1 ORDER BY offered_at ASC, id ASC LIMIT 200`, id)
	if err != nil {
		writeError(w, r, 500, "INTERNAL", "trace failed")
		return
	}
	defer rows.Close()
	offers := []map[string]any{}
	for rows.Next() {
		var aid, rid uuid.UUID
		var st string
		var score float64
		var raw []byte
		var offered, expires time.Time
		if err := rows.Scan(&aid, &rid, &st, &score, &raw, &offered, &expires); err != nil {
			writeError(w, r, 500, "INTERNAL", "trace failed")
			return
		}
		var bd map[string]any
		_ = json.Unmarshal(raw, &bd)
		offers = append(offers, map[string]any{
			"assignment_id": aid.String(), "rider_id": rid.String(), "status": st,
			"score": score, "score_breakdown": bd, "offered_at": offered, "expires_at": expires,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, r, 500, "INTERNAL", "trace failed")
		return
	}
	writeJSON(w, 200, map[string]any{"order": o, "offers": offers, "events": events})
}
