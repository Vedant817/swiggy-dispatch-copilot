package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CancelOrder closes an outstanding offer and releases its rider atomically
// with the order transition, event, and idempotent response.
func (s *Store) CancelOrder(ctx context.Context, id uuid.UUID, key, template string) (Order, error) {
	var order Order
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return order, err
	}
	defer tx.Rollback(ctx)
	var current string
	err = tx.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1 FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return order, ErrNotFound
	}
	if err != nil {
		return order, err
	}
	switch current {
	case "created", "preparing", "ready_for_assign", "offering", "assigned":
	default:
		return order, ErrStateConflict
	}
	var assignmentID, riderID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id,rider_id FROM assignments WHERE order_id=$1 AND status IN ('offered','accepted') FOR UPDATE`, id).Scan(&assignmentID, &riderID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return order, err
	}
	if err == nil {
		if _, err = tx.Exec(ctx, `UPDATE assignments SET status='cancelled' WHERE id=$1 AND status IN ('offered','accepted')`, assignmentID); err != nil {
			return order, err
		}
		if _, err = tx.Exec(ctx, `UPDATE riders SET status='available',updated_at=now() WHERE id=$1 AND status IN ('offered','busy')`, riderID); err != nil {
			return order, err
		}
	}
	err = tx.QueryRow(ctx, `UPDATE orders SET status='cancelled',updated_at=now() WHERE id=$1 AND status=$2 RETURNING id,restaurant_id,status,priority,sla_deliver_by,idempotency_key,created_at,updated_at`, id, current).Scan(
		&order.ID, &order.RestaurantID, &order.Status, &order.Priority, &order.SLADeliverBy, &order.IdempotencyKey, &order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		return order, err
	}
	var assignmentRef, riderRef *uuid.UUID
	if assignmentID != uuid.Nil {
		assignmentRef, riderRef = &assignmentID, &riderID
	}
	if err = s.AppendEvent(ctx, tx, id, assignmentRef, riderRef, "order_cancelled", map[string]any{"from": current}); err != nil {
		return order, err
	}
	order.IdempotencyKey = nil
	if err = s.CompleteIdempotencyTx(ctx, tx, key, "POST", template, id.String(), 200, map[string]any{"order": order}); err != nil {
		return order, err
	}
	return order, tx.Commit(ctx)
}
