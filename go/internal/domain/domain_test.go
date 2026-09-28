package domain

import "testing"

func TestOrderTransitions(t *testing.T) {
	cases := []struct{ from, to string; ok bool }{
		{"created", "preparing", true},
		{"created", "ready_for_assign", false},
		{"preparing", "ready_for_assign", true},
		{"ready_for_assign", "offering", true},
		{"offering", "assigned", true},
		{"offering", "ready_for_assign", true},
		{"assigned", "picked_up", true},
		{"assigned", "ready_for_assign", true},
		{"picked_up", "delivered", true},
		{"delivered", "cancelled", false},
		{"offering", "picked_up", false},
	}
	for _, c := range cases {
		if got := CanTransitionOrder(c.from, c.to); got != c.ok {
			t.Fatalf("%s->%s = %v want %v", c.from, c.to, got, c.ok)
		}
	}
}

func TestScoringOrdersNearestFirst(t *testing.T) {
	w := Weights{DistanceKm: -1.0, RiderLoad: -0.5, Rating: 0.2, VipBonus: 0.8}
	near := Score(0.5, 0, 4.5, false, w)
	far := Score(5.0, 0, 5.0, false, w)
	if !(near.Score > far.Score) {
		t.Fatalf("near %v should beat far %v", near.Score, far.Score)
	}
	vipFar := Score(5.0, 0, 4.5, true, w)
	normalNear := Score(0.5, 0, 4.5, false, w)
	// VIP bonus 0.8 does not overcome 4.5km at -1.0/km; assert exact math instead of outcome.
	if vipFar.Score != -5.0+0.9+0.8 {
		t.Fatalf("vip score math = %v", vipFar.Score)
	}
	_ = normalNear
}

func TestHaversine(t *testing.T) {
	d := HaversineKm(12.95, 77.6, 12.95, 77.6)
	if d != 0 {
		t.Fatalf("zero distance = %v", d)
	}
	d = HaversineKm(12.90, 77.50, 13.00, 77.70)
	if d < 10 || d > 35 {
		t.Fatalf("bbox diagonal suspicious: %v", d)
	}
}
