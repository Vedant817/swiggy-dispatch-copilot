package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LoadOrderReplay checks a completed claim before doing nondeterministic
// picker work; CreateOrderIdempotent rechecks under its transactional lock.
func (s *Store) LoadOrderReplay(ctx context.Context, key string, request any) (Order, bool, error) {
	var order Order
	if key == "" {
		return order, false, nil
	}
	result, _, err := s.CheckIdempotency(ctx, nil, key, "POST", "POST /v1/orders", "", request)
	if err != nil || !result.Replay {
		return order, false, err
	}
	if result.Status != 201 {
		return order, false, ErrStateConflict
	}
	var response struct{ Order Order `json:"order"` }
	if err := json.Unmarshal(result.Body, &response); err != nil {
		return order, false, err
	}
	return response.Order, true, nil
}

// CreateOrderIdempotent serializes a key's retries and commits its claim,
// order, trace event and cached response in the *same* transaction. A crash
// before commit leaves neither an abandoned claim nor a duplicate order.
func (s *Store) CreateOrderIdempotent(ctx context.Context, restaurantID uuid.UUID, priority string, sla time.Time, key string, request any) (Order, bool, error) {
	var order Order
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return order, false, err
	}
	defer tx.Rollback(ctx)
	var hash string
	if key != "" {
		hash, err = payloadHash(request)
		if err != nil {
			return order, false, err
		}
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "order:create:"+key); err != nil {
			return order, false, err
		}
		var oldHash string
		var status int
		var raw []byte
		err = tx.QueryRow(ctx, `SELECT request_hash,status,body FROM idempotency_keys WHERE key=$1 AND method='POST' AND path_template='POST /v1/orders' AND target_id=''`, key).Scan(&oldHash, &status, &raw)
		if err == nil {
			if oldHash != hash {
				return order, false, ErrIdempotencyReused
			}
			if status != 201 {
				return order, false, ErrStateConflict
			}
			var response struct{ Order Order `json:"order"` }
			if err = json.Unmarshal(raw, &response); err != nil {
				return order, false, err
			}
			return response.Order, true, nil
		}
		if err != pgx.ErrNoRows {
			return order, false, err
		}
	}
	var optionalKey *string
	if key != "" {
		optionalKey = &key
	}
	err = tx.QueryRow(ctx, `INSERT INTO orders(restaurant_id,priority,sla_deliver_by,idempotency_key) VALUES($1,$2,$3,$4) RETURNING id,restaurant_id,status,priority,sla_deliver_by,idempotency_key,created_at,updated_at`, restaurantID, priority, sla, optionalKey).Scan(
		&order.ID, &order.RestaurantID, &order.Status, &order.Priority, &order.SLADeliverBy, &order.IdempotencyKey, &order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		return order, false, err
	}
	if err = s.AppendEvent(ctx, tx, order.ID, nil, nil, "order_created", map[string]any{"priority": priority}); err != nil {
		return order, false, err
	}
	order.IdempotencyKey = nil
	if key != "" {
		response, marshalErr := json.Marshal(map[string]any{"order": order})
		if marshalErr != nil {
			return order, false, marshalErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO idempotency_keys(key,method,path_template,target_id,request_hash,status,body) VALUES($1,'POST','POST /v1/orders','',$2,201,$3)`, key, hash, response); err != nil {
			return order, false, fmt.Errorf("cache order response: %w", err)
		}
	}
	return order, false, tx.Commit(ctx)
}
