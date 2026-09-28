package store

import (
	"time"

	"github.com/google/uuid"
)

type Restaurant struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Lat           float64   `json:"lat"`
	Lng           float64   `json:"lng"`
	PrepMinutesP50 int      `json:"prep_minutes_p50"`
	Capacity      int       `json:"capacity"`
	CreatedAt     time.Time `json:"created_at"`
}

type Rider struct {
	ID        uuid.UUID `json:"id"`
	Status    string    `json:"status"`
	Lat       float64   `json:"lat"`
	Lng       float64   `json:"lng"`
	Capacity  int       `json:"capacity"`
	Rating    float64   `json:"rating"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Order struct {
	ID            uuid.UUID `json:"id"`
	RestaurantID  uuid.UUID `json:"restaurant_id"`
	Status        string    `json:"status"`
	Priority      string    `json:"priority"`
	SLADeliverBy  time.Time `json:"sla_deliver_by"`
	IdempotencyKey *string  `json:"idempotency_key,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Assignment struct {
	ID             uuid.UUID  `json:"id"`
	OrderID        uuid.UUID  `json:"order_id"`
	RiderID        uuid.UUID  `json:"rider_id"`
	Status         string     `json:"status"`
	OfferedAt      time.Time  `json:"offered_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	AcceptedAt     *time.Time `json:"accepted_at,omitempty"`
	Score          float64    `json:"score"`
	ScoreBreakdown map[string]any `json:"score_breakdown"`
	CreatedAt      time.Time  `json:"created_at"`
}

type Proposal struct {
	ID        uuid.UUID      `json:"id"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	Status    string         `json:"status"`
	Reason    string         `json:"reason"`
	CreatedBy string         `json:"created_by"`
	ExpiresAt time.Time      `json:"expires_at"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type AssignmentEvent struct {
	ID           uuid.UUID      `json:"id"`
	OrderID      uuid.UUID      `json:"order_id"`
	AssignmentID *uuid.UUID     `json:"assignment_id,omitempty"`
	RiderID      *uuid.UUID     `json:"rider_id,omitempty"`
	Event        string         `json:"event"`
	Detail       map[string]any `json:"detail"`
	CreatedAt    time.Time      `json:"created_at"`
}
