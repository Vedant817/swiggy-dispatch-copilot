package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateProposal(ctx context.Context, typ string, payload map[string]any, reason string, ttl time.Duration) (Proposal, error) {
	var p Proposal
	var rawPayload, rawOut []byte
	rawPayload, _ = json.Marshal(payload)
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO proposals(type,payload,reason,expires_at) VALUES($1,$2,$3,now() + ($4 * interval '1 second'))
		 RETURNING id,type,payload,status,reason,created_by,expires_at,created_at,updated_at`,
		typ, rawPayload, reason, ttl.Seconds()).Scan(
		&p.ID, &p.Type, &rawOut, &p.Status, &p.Reason, &p.CreatedBy, &p.ExpiresAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	_ = json.Unmarshal(rawOut, &p.Payload)
	return p, nil
}

func (s *Store) GetProposal(ctx context.Context, id uuid.UUID) (Proposal, error) {
	var p Proposal
	var raw []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT id,type,payload,status,reason,created_by,expires_at,created_at,updated_at FROM proposals WHERE id=$1`, id).
		Scan(&p.ID, &p.Type, &raw, &p.Status, &p.Reason, &p.CreatedBy, &p.ExpiresAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	_ = json.Unmarshal(raw, &p.Payload)
	// Lazy expiry.
	if p.Status == "pending" && time.Now().After(p.ExpiresAt) {
		_, _ = s.Pool.Exec(ctx, `UPDATE proposals SET status='expired',updated_at=now() WHERE id=$1`, id)
		p.Status = "expired"
	}
	return p, nil
}

func (s *Store) SetProposalStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, from []string, to, reason string) (Proposal, error) {
	var p Proposal
	var raw []byte
	q := `UPDATE proposals SET status=$2,reason=COALESCE(NULLIF($3,''),reason),updated_at=now()
		WHERE id=$1 AND status = ANY($4)
		RETURNING id,type,payload,status,reason,created_by,expires_at,created_at,updated_at`
	var row pgx.Row
	if tx != nil {
		row = tx.QueryRow(ctx, q, id, to, reason, from)
	} else {
		row = s.Pool.QueryRow(ctx, q, id, to, reason, from)
	}
	err := row.Scan(&p.ID, &p.Type, &raw, &p.Status, &p.Reason, &p.CreatedBy, &p.ExpiresAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			var exists bool
			_ = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM proposals WHERE id=$1)`, id).Scan(&exists)
			if !exists {
				return p, ErrNotFound
			}
			return p, ErrStateConflict
		}
		return p, err
	}
	_ = json.Unmarshal(raw, &p.Payload)
	return p, nil
}
