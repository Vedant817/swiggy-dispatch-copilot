package store

import "context"

func (s *Store) ExpireProposals(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	tag, err := s.Pool.Exec(ctx,
		`UPDATE proposals SET status='expired',updated_at=now() WHERE id IN (
			SELECT id FROM proposals WHERE status='pending' AND expires_at < now() ORDER BY expires_at LIMIT $1 FOR UPDATE SKIP LOCKED
		)`, limit)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) AbortIdempotency(ctx context.Context, key, method, tmpl, target string) error {
	if key == "" {
		return nil
	}
	_, err := s.Pool.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE key=$1 AND method=$2 AND path_template=$3 AND target_id=$4 AND status=-1`,
		key, method, tmpl, target)
	return err
}
