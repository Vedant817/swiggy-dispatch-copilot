package gen

import (
	"encoding/json"
	"math/rand"
	"testing"
)

func TestDeterministic(t *testing.T) {
	bbox := []float64{12.9, 77.5, 13.0, 77.7}
	a := GenerateWorld(42, bbox, 5, 10)
	b := GenerateWorld(42, bbox, 5, 10)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("same seed must produce same world")
	}
	c := GenerateWorld(43, bbox, 5, 10)
	jc, _ := json.Marshal(c)
	if string(ja) == string(jc) {
		t.Fatal("different seeds should differ")
	}
	rng := rand.New(rand.NewSource(1))
	if PickRestaurant(a, "hotspot", 0, 0, rng) != 0 {
		t.Fatal("hotspot should pick 0")
	}
}
