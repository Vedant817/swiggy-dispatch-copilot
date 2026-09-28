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
		obs.Log("reoffer_query_failed", map[string]any{"error": err.Error()})
		return 0
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		obs.Log("reoffer_scan_failed", map[string]any{"error": err.Error()})
	}
	n := 0
	for _, id := range ids {
		_, found, aerr := s.Store.ActiveAssignmentForOrder(ctx, nil, id)
		if aerr != nil {
			obs.Log("reoffer_check_failed", map[string]any{"order_id": id.String(), "error": aerr.Error()})
			continue
		}
		if found {
			continue
		}
		if _, err := s.TryAssign(ctx, id); err == nil {
			n++
		} else if err.Error() == "NO_RIDERS_AVAILABLE" {
			s.Counters.Inc("reoffer_no_riders")
			break
		} else {
			obs.Log("reoffer_failed", map[string]any{"order_id": id.String(), "error": err.Error()})
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
			expired, eerr := s.ExpireDue(cctx, 100)
			if eerr != nil {
				obs.Log("expire_failed", map[string]any{"error": eerr.Error()})
			}
			pexp, _ := s.Store.ExpireProposals(cctx, 100)
			stale, _ := s.Store.CleanupStaleIdempotency(cctx, "5 minutes")
			reoffered := s.ReofferReady(cctx, s.Cfg.Assign.WorkerCount*2)
			if expired > 0 || reoffered > 0 || pexp > 0 || stale > 0 {
				obs.Log("worker_tick", map[string]any{"expired": expired, "reoffered": reoffered, "proposals_expired": pexp, "stale_idem": stale})
			}
			cancel()
		}
	}
}
