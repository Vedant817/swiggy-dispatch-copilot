// Package assign owns candidate scoring and transactional offer lifecycle.
package assign

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/config"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/domain"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/obs"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/redisx"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	Cfg      config.Config
	Store    *store.Store
	Redis    *redisx.Client
	Counters *obs.Counters
}

func New(cfg config.Config, st *store.Store, rdb *redisx.Client, c *obs.Counters) *Service {
	if c == nil {
		c = obs.NewCounters()
	}
	return &Service{Cfg: cfg, Store: st, Redis: rdb, Counters: c}
}

func weights(cfg config.Config) domain.Weights {
	return domain.Weights{
		DistanceKm: cfg.Scoring.Weights.DistanceKm,
		RiderLoad:  cfg.Scoring.Weights.RiderLoad,
		Rating:     cfg.Scoring.Weights.Rating,
		VipBonus:   cfg.Scoring.Weights.VipBonus,
	}
}

// TryAssign attempts one offer for a ready order. It is idempotent via active-assignment check.
func (s *Service) TryAssign(ctx context.Context, orderID uuid.UUID) (store.Assignment, error) {
	lockKey := "assign:" + orderID.String()
	token := uuid.NewString()
	if s.Redis != nil {
		ok, err := s.Redis.Acquire(ctx, lockKey, token, s.Cfg.Assign.LockTTL)
		if err == nil && !ok {
			return store.Assignment{}, fmt.Errorf("ASSIGNMENT_STATE_CONFLICT: assign in progress")
		}
		if err == nil {
			defer s.Redis.Release(ctx, lockKey, token)
		}
	}
	order, err := s.Store.GetOrder(ctx, orderID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return store.Assignment{}, fmt.Errorf("ORDER_NOT_FOUND")
		}
		return store.Assignment{}, err
	}
	if order.Status != domain.OrderReady {
		if active, found, _ := s.Store.ActiveAssignmentForOrder(ctx, nil, orderID); found {
			return active, nil
		}
		return store.Assignment{}, fmt.Errorf("ORDER_STATE_CONFLICT: order %s not ready", order.Status)
	}
	// Load restaurant for distance.
	var restLat, restLng float64
	if err := s.Store.Pool.QueryRow(ctx, `SELECT lat,lng FROM restaurants WHERE id=$1`, order.RestaurantID).Scan(&restLat, &restLng); err != nil {
		return store.Assignment{}, err
	}
	cands, err := s.Store.Pool.Query(ctx,
		`SELECT id,lat,lng,rating FROM riders WHERE status='available' ORDER BY rating DESC LIMIT $1`, s.Cfg.Assign.MaxCandidates*5)
	if err != nil {
		return store.Assignment{}, err
	}
	type cand struct {
		id     uuid.UUID
		lat    float64
		lng    float64
		rating float64
		score  domain.ScoreBreakdown
	}
	var scored []cand
	for cands.Next() {
		var c cand
		if err := cands.Scan(&c.id, &c.lat, &c.lng, &c.rating); err != nil {
			cands.Close()
			return store.Assignment{}, err
		}
		d := domain.HaversineKm(restLat, restLng, c.lat, c.lng)
		c.score = domain.Score(d, 0, c.rating, order.Priority == "vip", weights(s.Cfg))
		scored = append(scored, c)
	}
	cands.Close()
	if cands.Err() != nil {
		return store.Assignment{}, cands.Err()
	}
	if len(scored) == 0 {
		return store.Assignment{}, fmt.Errorf("NO_RIDERS_AVAILABLE")
	}
	// Pick best score.
	best := scored[0]
	for _, c := range scored[1:] {
		if c.score.Score > best.score.Score {
			best = c
		}
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return store.Assignment{}, err
	}
	defer tx.Rollback(ctx)
	// Revalidate under lock.
	var ost string
	if err := tx.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1 FOR UPDATE`, orderID).Scan(&ost); err != nil {
		return store.Assignment{}, err
	}
	if ost != domain.OrderReady {
		if active, found, _ := s.Store.ActiveAssignmentForOrder(ctx, tx, orderID); found {
			_ = tx.Rollback(ctx)
			return active, nil
		}
		return store.Assignment{}, fmt.Errorf("ORDER_STATE_CONFLICT: order %s", ost)
	}
	// CAS rider.
	tag, err := tx.Exec(ctx, `UPDATE riders SET status='offered',updated_at=now() WHERE id=$1 AND status='available'`, best.id)
	if err != nil {
		return store.Assignment{}, err
	}
	if tag.RowsAffected() == 0 {
		return store.Assignment{}, fmt.Errorf("RIDER_STATE_CONFLICT")
	}
	breakRaw, _ := json.Marshal(best.score)
	var a store.Assignment
	var breakdown []byte
	exp := time.Now().Add(s.Cfg.Assign.OfferTTL)
	err = tx.QueryRow(ctx,
		`INSERT INTO assignments(order_id,rider_id,expires_at,score,score_breakdown) VALUES($1,$2,$3,$4,$5)
		 RETURNING id,order_id,rider_id,status,offered_at,expires_at,accepted_at,score,score_breakdown,created_at`,
		orderID, best.id, exp, best.score.Score, breakRaw).Scan(
		&a.ID, &a.OrderID, &a.RiderID, &a.Status, &a.OfferedAt, &a.ExpiresAt, &a.AcceptedAt, &a.Score, &breakdown, &a.CreatedAt)
	if err != nil {
		return store.Assignment{}, err
	}
	_ = json.Unmarshal(breakdown, &a.ScoreBreakdown)
	if _, err := tx.Exec(ctx, `UPDATE orders SET status='offering',updated_at=now() WHERE id=$1`, orderID); err != nil {
		return store.Assignment{}, err
	}
	_ = s.Store.AppendEvent(ctx, tx, orderID, &a.ID, &best.id, "offer_created", map[string]any{"score": best.score})
	if err := tx.Commit(ctx); err != nil {
		return store.Assignment{}, err
	}
	s.Counters.Inc("offers_created")
	obs.Log("offer_created", map[string]any{"order_id": orderID.String(), "assignment_id": a.ID.String(), "rider_id": best.id.String()})
	return a, nil
}

// Accept commits an offered assignment.
func (s *Service) Accept(ctx context.Context, assignmentID uuid.UUID) (store.Assignment, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return store.Assignment{}, err
	}
	defer tx.Rollback(ctx)
	var a store.Assignment
	var breakdown []byte
	err = tx.QueryRow(ctx,
		`SELECT id,order_id,rider_id,status,offered_at,expires_at,accepted_at,score,score_breakdown,created_at FROM assignments WHERE id=$1 FOR UPDATE`,
		assignmentID).Scan(&a.ID, &a.OrderID, &a.RiderID, &a.Status, &a.OfferedAt, &a.ExpiresAt, &a.AcceptedAt, &a.Score, &breakdown, &a.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return a, fmt.Errorf("ASSIGNMENT_NOT_FOUND")
		}
		return a, err
	}
	if a.Status != domain.AssignOffered {
		return a, fmt.Errorf("ASSIGNMENT_STATE_CONFLICT: %s", a.Status)
	}
	if time.Now().After(a.ExpiresAt) {
		_, _ = tx.Exec(ctx, `UPDATE assignments SET status='expired' WHERE id=$1`, assignmentID)
		_, _ = tx.Exec(ctx, `UPDATE riders SET status='available',updated_at=now() WHERE id=$1`, a.RiderID)
		_, _ = tx.Exec(ctx, `UPDATE orders SET status='ready_for_assign',updated_at=now() WHERE id=$1`, a.OrderID)
		_ = s.Store.AppendEvent(ctx, tx, a.OrderID, &a.ID, &a.RiderID, "offer_expired", nil)
		_ = tx.Commit(ctx)
		s.Counters.Inc("offers_expired")
		return a, fmt.Errorf("ASSIGNMENT_STATE_CONFLICT: expired")
	}
	if _, err := tx.Exec(ctx, `UPDATE assignments SET status='accepted',accepted_at=now() WHERE id=$1`, assignmentID); err != nil {
		return a, err
	}
	if _, err := tx.Exec(ctx, `UPDATE riders SET status='busy',updated_at=now() WHERE id=$1`, a.RiderID); err != nil {
		return a, err
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET status='assigned',updated_at=now() WHERE id=$1`, a.OrderID); err != nil {
		return a, err
	}
	_ = s.Store.AppendEvent(ctx, tx, a.OrderID, &a.ID, &a.RiderID, "offer_accepted", nil)
	if err := tx.Commit(ctx); err != nil {
		return a, err
	}
	a.Status = domain.AssignAccepted
	s.Counters.Inc("assigns_committed")
	return a, nil
}

// Reject releases an offer back to the queue.
func (s *Service) Reject(ctx context.Context, assignmentID uuid.UUID) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var orderID, riderID uuid.UUID
	var status string
	err = tx.QueryRow(ctx, `SELECT order_id,rider_id,status FROM assignments WHERE id=$1 FOR UPDATE`, assignmentID).Scan(&orderID, &riderID, &status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("ASSIGNMENT_NOT_FOUND")
		}
		return err
	}
	if status != domain.AssignOffered {
		return fmt.Errorf("ASSIGNMENT_STATE_CONFLICT: %s", status)
	}
	_, _ = tx.Exec(ctx, `UPDATE assignments SET status='cancelled' WHERE id=$1`, assignmentID)
	_, _ = tx.Exec(ctx, `UPDATE riders SET status='available',updated_at=now() WHERE id=$1`, riderID)
	_, _ = tx.Exec(ctx, `UPDATE orders SET status='ready_for_assign',updated_at=now() WHERE id=$1 AND status='offering'`, orderID)
	_ = s.Store.AppendEvent(ctx, tx, orderID, &assignmentID, &riderID, "offer_rejected", nil)
	return tx.Commit(ctx)
}

// ExpireDue sweeps timed-out offers. Returns count expired.
func (s *Service) ExpireDue(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.Store.Pool.Query(ctx,
		`SELECT id FROM assignments WHERE status='offered' AND expires_at < now() ORDER BY expires_at LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		tx, err := s.Store.Pool.Begin(ctx)
		if err != nil {
			continue
		}
		var orderID, riderID uuid.UUID
		var status string
		if err := tx.QueryRow(ctx, `SELECT order_id,rider_id,status FROM assignments WHERE id=$1 FOR UPDATE`, id).Scan(&orderID, &riderID, &status); err != nil {
			_ = tx.Rollback(ctx)
			continue
		}
		if status != domain.AssignOffered {
			_ = tx.Rollback(ctx)
			continue
		}
		_, _ = tx.Exec(ctx, `UPDATE assignments SET status='expired' WHERE id=$1`, id)
		_, _ = tx.Exec(ctx, `UPDATE riders SET status='available',updated_at=now() WHERE id=$1 AND status='offered'`, riderID)
		_, _ = tx.Exec(ctx, `UPDATE orders SET status='ready_for_assign',updated_at=now() WHERE id=$1 AND status='offering'`, orderID)
		_ = s.Store.AppendEvent(ctx, tx, orderID, &id, &riderID, "offer_expired", nil)
		if err := tx.Commit(ctx); err == nil {
			n++
			s.Counters.Inc("offers_expired")
		}
	}
	return n, nil
}
