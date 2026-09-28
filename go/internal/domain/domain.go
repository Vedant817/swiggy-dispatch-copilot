// Package domain owns order lifecycle, scoring, and stable error codes.
package domain

import (
	"math"
	"time"
)

const (
	OrderCreated       = "created"
	OrderPreparing     = "preparing"
	OrderReady         = "ready_for_assign"
	OrderOffering      = "offering"
	OrderAssigned      = "assigned"
	OrderPickedUp      = "picked_up"
	OrderDelivered     = "delivered"
	OrderCancelled     = "cancelled"

	RiderAvailable = "available"
	RiderOffered   = "offered"
	RiderBusy      = "busy"
	RiderOffline   = "offline"

	AssignOffered    = "offered"
	AssignAccepted   = "accepted"
	AssignExpired    = "expired"
	AssignCancelled  = "cancelled"
	AssignSuperseded = "superseded"

	ProposalReassign     = "reassign"
	ProposalBatchHint    = "batch_hint"
	ProposalDelayExplain = "delay_explain"

	ProposalPending  = "pending"
	ProposalAccepted = "accepted"
	ProposalRejected = "rejected"
	ProposalExpired  = "expired"
)

// OrderTransitions maps current -> allowed next states (customer + worker + webhook paths).
var OrderTransitions = map[string][]string{
	OrderCreated:   {OrderPreparing, OrderCancelled},
	OrderPreparing: {OrderReady, OrderCancelled},
	OrderReady:     {OrderOffering, OrderCancelled},
	OrderOffering:  {OrderAssigned, OrderReady, OrderCancelled},
	OrderAssigned:  {OrderPickedUp, OrderReady, OrderCancelled},
	OrderPickedUp:  {OrderDelivered, OrderCancelled},
	OrderDelivered: {},
	OrderCancelled: {},
}

func CanTransitionOrder(from, to string) bool {
	for _, n := range OrderTransitions[from] {
		if n == to {
			return true
		}
	}
	return false
}

// FromForTransition returns the allowed `from` set for a target (for SQL conditional update).
func FromForTransition(to string) []string {
	var out []string
	for from, nexts := range OrderTransitions {
		for _, n := range nexts {
			if n == to {
				out = append(out, from)
			}
		}
	}
	return out
}

type Weights struct {
	DistanceKm float64
	RiderLoad  float64
	Rating     float64
	VipBonus   float64
}

type ScoreBreakdown struct {
	DistanceKm float64 `json:"distance_km"`
	Load       float64 `json:"load"`
	Rating     float64 `json:"rating"`
	Vip        float64 `json:"vip"`
	Score      float64 `json:"score"`
}

// HaversineKm is pure and seed-independent.
func HaversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371.0088
	dLat := (lat2 - lat1) * math.Pi / 180
	dLng := (lng2 - lng1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

func Score(distanceKm, load, rating float64, isVip bool, w Weights) ScoreBreakdown {
	vip := 0.0
	if isVip {
		vip = 1.0
	}
	s := w.DistanceKm*distanceKm + w.RiderLoad*load + w.Rating*rating + w.VipBonus*vip
	return ScoreBreakdown{DistanceKm: distanceKm, Load: load, Rating: rating, Vip: vip, Score: s}
}

// Clock allows deterministic expiry tests.
type Clock interface{ Now() time.Time }
type SystemClock struct{}
func (SystemClock) Now() time.Time { return time.Now() }
type FixedClock struct{ T time.Time }
func (c FixedClock) Now() time.Time { return c.T }
