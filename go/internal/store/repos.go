package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrIdempotencyReused = errors.New("IDEMPOTENCY_KEY_REUSED")
	ErrNotFound          = errors.New("NOT_FOUND")
	ErrStateConflict     = errors.New("STATE_CONFLICT")
)

// --- helpers ---

func payloadHash(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func mustHash(v any) string {
	h, err := payloadHash(v)
	if err != nil {
		return "unhashable"
	}
	return h
}

// --- restaurants/riders ---

func (s *Store) CreateRestaurant(ctx context.Context, name string, lat, lng float64, prep, capacity int) (Restaurant, error) {
	var r Restaurant
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO restaurants(name,lat,lng,prep_minutes_p50,capacity) VALUES($1,$2,$3,$4,$5)
		 RETURNING id,name,lat,lng,prep_minutes_p50,capacity,created_at`,
		name, lat, lng, prep, capacity).Scan(&r.ID, &r.Name, &r.Lat, &r.Lng, &r.PrepMinutesP50, &r.Capacity, &r.CreatedAt)
	return r, err
}

func (s *Store) CreateRider(ctx context.Context, lat, lng, rating float64) (Rider, error) {
	var r Rider
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO riders(lat,lng,rating) VALUES($1,$2,$3)
		 RETURNING id,status,lat,lng,capacity,rating,created_at,updated_at`,
		lat, lng, rating).Scan(&r.ID, &r.Status, &r.Lat, &r.Lng, &r.Capacity, &r.Rating, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (s *Store) GetRider(ctx context.Context, id uuid.UUID) (Rider, error) {
	var r Rider
	err := s.Pool.QueryRow(ctx,
		`SELECT id,status,lat,lng,capacity,rating,created_at,updated_at FROM riders WHERE id=$1`, id).
		Scan(&r.ID, &r.Status, &r.Lat, &r.Lng, &r.Capacity, &r.Rating, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (s *Store) ListRidersByStatus(ctx context.Context, status string, limit int) ([]Rider, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id,status,lat,lng,capacity,rating,created_at,updated_at FROM riders WHERE status=$1 ORDER BY created_at LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rider{}
	for rows.Next() {
		var r Rider
		if err := rows.Scan(&r.ID, &r.Status, &r.Lat, &r.Lng, &r.Capacity, &r.Rating, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateRiderLocation(ctx context.Context, id uuid.UUID, lat, lng float64) (Rider, error) {
	var r Rider
	err := s.Pool.QueryRow(ctx,
		`UPDATE riders SET lat=$2,lng=$3,updated_at=now() WHERE id=$1
		 RETURNING id,status,lat,lng,capacity,rating,created_at,updated_at`,
		id, lat, lng).Scan(&r.ID, &r.Status, &r.Lat, &r.Lng, &r.Capacity, &r.Rating, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// --- orders ---

func (s *Store) CreateOrder(ctx context.Context, restaurantID uuid.UUID, priority string, sla time.Time, idemKey *string) (Order, error) {
	var o Order
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO orders(restaurant_id,priority,sla_deliver_by,idempotency_key) VALUES($1,$2,$3,$4)
		 RETURNING id,restaurant_id,status,priority,sla_deliver_by,idempotency_key,created_at,updated_at`,
		restaurantID, priority, sla, idemKey).Scan(&o.ID, &o.RestaurantID, &o.Status, &o.Priority, &o.SLADeliverBy, &o.IdempotencyKey, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

func (s *Store) GetOrder(ctx context.Context, id uuid.UUID) (Order, error) {
	var o Order
	err := s.Pool.QueryRow(ctx,
		`SELECT id,restaurant_id,status,priority,sla_deliver_by,idempotency_key,created_at,updated_at FROM orders WHERE id=$1`, id).
		Scan(&o.ID, &o.RestaurantID, &o.Status, &o.Priority, &o.SLADeliverBy, &o.IdempotencyKey, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

// SetOrderStatus performs a checked status transition.
// Returns ErrNotFound when the order does not exist, ErrStateConflict on illegal transition.
func (s *Store) SetOrderStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, from []string, to string) (Order, error) {
	var o Order
	var db pgx.Row
	q := `UPDATE orders SET status=$2,updated_at=now() WHERE id=$1 AND status = ANY($3)
		RETURNING id,restaurant_id,status,priority,sla_deliver_by,idempotency_key,created_at,updated_at`
	if tx != nil {
		db = tx.QueryRow(ctx, q, id, to, from)
	} else {
		db = s.Pool.QueryRow(ctx, q, id, to, from)
	}
	err := db.Scan(&o.ID, &o.RestaurantID, &o.Status, &o.Priority, &o.SLADeliverBy, &o.IdempotencyKey, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			var exists bool
			check := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE id=$1)`, id)
			_ = check.Scan(&exists)
			if !exists {
				return o, ErrNotFound
			}
			return o, ErrStateConflict
		}
		return o, err
	}
	return o, nil
}

// --- assignments ---

func (s *Store) GetAssignment(ctx context.Context, id uuid.UUID) (Assignment, error) {
	var a Assignment
	var breakdown []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT id,order_id,rider_id,status,offered_at,expires_at,accepted_at,score,score_breakdown,created_at FROM assignments WHERE id=$1`, id).
		Scan(&a.ID, &a.OrderID, &a.RiderID, &a.Status, &a.OfferedAt, &a.ExpiresAt, &a.AcceptedAt, &a.Score, &breakdown, &a.CreatedAt)
	if err != nil {
		return a, err
	}
	_ = json.Unmarshal(breakdown, &a.ScoreBreakdown)
	return a, nil
}

func (s *Store) ActiveAssignmentForOrder(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (Assignment, bool, error) {
	var a Assignment
	var breakdown []byte
	var row pgx.Row
	q := `SELECT id,order_id,rider_id,status,offered_at,expires_at,accepted_at,score,score_breakdown,created_at
		FROM assignments WHERE order_id=$1 AND status IN ('offered','accepted') LIMIT 1`
	if tx != nil {
		row = tx.QueryRow(ctx, q, orderID)
	} else {
		row = s.Pool.QueryRow(ctx, q, orderID)
	}
	err := row.Scan(&a.ID, &a.OrderID, &a.RiderID, &a.Status, &a.OfferedAt, &a.ExpiresAt, &a.AcceptedAt, &a.Score, &breakdown, &a.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return a, false, nil
		}
		return a, false, err
	}
	_ = json.Unmarshal(breakdown, &a.ScoreBreakdown)
	return a, true, nil
}

// --- idempotency ---

type IdempotencyResult struct {
	Replay bool
	Status int
	Body   []byte
}

func (s *Store) CheckIdempotency(ctx context.Context, tx pgx.Tx, key, method, tmpl, target string, body any) (IdempotencyResult, string, error) {
	hash, err := payloadHash(body)
	if err != nil {
		return IdempotencyResult{}, "", err
	}
	var status int
	var raw []byte
	var existing string
	q := `SELECT request_hash,status,body FROM idempotency_keys WHERE key=$1 AND method=$2 AND path_template=$3 AND target_id=$4`
	var row pgx.Row
	if tx != nil {
		row = tx.QueryRow(ctx, q, key, method, tmpl, target)
	} else {
		row = s.Pool.QueryRow(ctx, q, key, method, tmpl, target)
	}
	err = row.Scan(&existing, &status, &raw)
	if err == nil {
		if existing != hash {
			return IdempotencyResult{}, "", ErrIdempotencyReused
		}
		return IdempotencyResult{Replay: true, Status: status, Body: raw}, hash, nil
	}
	if err != pgx.ErrNoRows {
		return IdempotencyResult{}, "", err
	}
	return IdempotencyResult{Replay: false}, hash, nil
}

func (s *Store) SaveIdempotency(ctx context.Context, tx pgx.Tx, key, method, tmpl, target, hash string, status int, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	q := `INSERT INTO idempotency_keys(key,method,path_template,target_id,request_hash,status,body) VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (key,method,path_template,target_id) DO NOTHING`
	if tx != nil {
		_, err := tx.Exec(ctx, q, key, method, tmpl, target, hash, status, raw)
		return err
	}
	_, err = s.Pool.Exec(ctx, q, key, method, tmpl, target, hash, status, raw)
	return err
}

// ClaimIdempotency atomically claims a key for execution.
// claimed=true means caller owns execution and must call CompleteIdempotency.
// claimed=false with Replay=true means return stored response.
// In-progress (-1) returns a conflict so callers retry later.
func (s *Store) ClaimIdempotency(ctx context.Context, key, method, tmpl, target string, body any) (bool, IdempotencyResult, string, error) {
	hash, err := payloadHash(body)
	if err != nil {
		return false, IdempotencyResult{}, "", err
	}
	if key == "" {
		return true, IdempotencyResult{}, hash, nil
	}
	tag, err := s.Pool.Exec(ctx,
		`INSERT INTO idempotency_keys(key,method,path_template,target_id,request_hash,status,body)
		 VALUES($1,$2,$3,$4,$5,-1,'{}') ON CONFLICT (key,method,path_template,target_id) DO NOTHING`,
		key, method, tmpl, target, hash)
	if err != nil {
		return false, IdempotencyResult{}, "", err
	}
	if tag.RowsAffected() == 1 {
		return true, IdempotencyResult{}, hash, nil
	}
	var existing string
	var status int
	var raw []byte
	err = s.Pool.QueryRow(ctx,
		`SELECT request_hash,status,body FROM idempotency_keys WHERE key=$1 AND method=$2 AND path_template=$3 AND target_id=$4`,
		key, method, tmpl, target).Scan(&existing, &status, &raw)
	if err != nil {
		return false, IdempotencyResult{}, "", err
	}
	if existing != hash {
		return false, IdempotencyResult{}, "", ErrIdempotencyReused
	}
	if status == -1 {
		return false, IdempotencyResult{}, "", fmt.Errorf("ASSIGNMENT_STATE_CONFLICT: idempotent request in progress")
	}
	return false, IdempotencyResult{Replay: true, Status: status, Body: raw}, hash, nil
}

func (s *Store) CompleteIdempotency(ctx context.Context, key, method, tmpl, target string, status int, body any) error {
	return s.CompleteIdempotencyTx(ctx, nil, key, method, tmpl, target, status, body)
}

// CompleteIdempotencyTx stores the response in the same transaction as the effect.
func (s *Store) CompleteIdempotencyTx(ctx context.Context, tx pgx.Tx, key, method, tmpl, target string, status int, body any) error {
	if key == "" {
		return nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	q := `UPDATE idempotency_keys SET status=$5,body=$6 WHERE key=$1 AND method=$2 AND path_template=$3 AND target_id=$4 AND status=-1`
	var tag pgconn.CommandTag
	if tx != nil {
		tag, err = tx.Exec(ctx, q, key, method, tmpl, target, status, raw)
	} else {
		tag, err = s.Pool.Exec(ctx, q, key, method, tmpl, target, status, raw)
	}
	if err == nil && tag.RowsAffected() != 1 {
		return ErrStateConflict
	}
	return err
}

// --- events ---

func (s *Store) AppendEvent(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, assignmentID, riderID *uuid.UUID, event string, detail any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	q := `INSERT INTO assignment_events(order_id,assignment_id,rider_id,event,detail) VALUES($1,$2,$3,$4,$5)`
	if tx != nil {
		_, err := tx.Exec(ctx, q, orderID, assignmentID, riderID, event, raw)
		return err
	}
	_, err = s.Pool.Exec(ctx, q, orderID, assignmentID, riderID, event, raw)
	return err
}

func (s *Store) ListEvents(ctx context.Context, orderID uuid.UUID) ([]AssignmentEvent, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id,order_id,assignment_id,rider_id,event,detail,created_at FROM assignment_events WHERE order_id=$1 ORDER BY created_at ASC, id ASC LIMIT 500`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssignmentEvent{}
	for rows.Next() {
		var e AssignmentEvent
		var raw []byte
		if err := rows.Scan(&e.ID, &e.OrderID, &e.AssignmentID, &e.RiderID, &e.Event, &raw, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- admin ---

func (s *Store) Reset(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `TRUNCATE assignment_events, idempotency_keys, webhooks_ledger, assignments, proposals, orders, riders, restaurants`)
	return err
}

// CountActiveAssignments is a test helper.
func (s *Store) CountActiveAssignments(ctx context.Context, orderID uuid.UUID) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM assignments WHERE order_id=$1 AND status IN ('offered','accepted')`, orderID).Scan(&n)
	return n, err
}

var _ = time.Now
var _ = uuid.New
