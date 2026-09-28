package assign

import (
	"context"
	"time"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/domain"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/obs"
	"github.com/google/uuid"
)

// ReofferReady assigns ready orders with no active offer, oldest first.
func (s *Service) ReofferReady(ctx context.Context, limit int) int {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.Store.Pool.Query(ctx,
		`SELECT id FROM orders WHERE status=$1 ORDER BY updated_at ASC LIMIT $2`, domain.OrderReady, limit)
	if err != nil {
		return 0
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
		if _, found, _ := s.Store.ActiveAssignmentForOrder(ctx, nil, id); found {
			continue
		}
		if _, err := s.TryAssign(ctx, id); err == nil {
			n++
		}
	}
	return n
}

// Start launches expiry + reoffer loop until ctx done. Used by api in-process pool and worker binary.
func (s *Service) Start(ctx context.Context) {
	backoff := s.Cfg.Assign.ReofferBackoff
	if backoff <= 0 {
		backoff = time.Second
	}
	t := time.NewTicker(backoff)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			expired, _ := s.ExpireDue(cctx, 100)
			reoffered := s.ReofferReady(cctx, s.Cfg.Assign.WorkerCount*2)
			if expired > 0 || reoffered > 0 {
				obs.Log("worker_tick", map[string]any{"expired": expired, "reoffered": reoffered})
			}
			cancel()
		}
	}
}
