// Package gen produces deterministic world specs from seed + config. No fixed IDs.
package gen

import (
	"fmt"
	"math/rand"
)

type RestaurantSpec struct {
	Name          string
	Lat, Lng      float64
	PrepMinutesP50 int
	Capacity      int
}

type RiderSpec struct {
	Lat, Lng float64
	Rating   float64
}

type WorldSpec struct {
	Seed        int64
	Restaurants []RestaurantSpec
	Riders      []RiderSpec
}

var nameAdjectives = []string{"Spicy", "Tandoori", "Cloud", "Midnight", "Green", "Royal", "Urban", "Saffron"}
var nameNouns = []string{"Kitchen", "Bowl", "Byte", "Pot", "Thali", "Wok", "Oven", "Dhaba"}

func GenerateWorld(seed int64, bbox []float64, nRest, nRiders int) WorldSpec {
	if len(bbox) != 4 {
		bbox = []float64{12.9, 77.5, 13.0, 77.7}
	}
	rng := rand.New(rand.NewSource(seed))
	minLat, minLng, maxLat, maxLng := bbox[0], bbox[1], bbox[2], bbox[3]
	ws := WorldSpec{Seed: seed}
	for i := 0; i < nRest; i++ {
		ws.Restaurants = append(ws.Restaurants, RestaurantSpec{
			Name:           fmt.Sprintf("%s %s %d", nameAdjectives[rng.Intn(len(nameAdjectives))], nameNouns[rng.Intn(len(nameNouns))], i),
			Lat:            minLat + rng.Float64()*(maxLat-minLat),
			Lng:            minLng + rng.Float64()*(maxLng-minLng),
			PrepMinutesP50: 8 + rng.Intn(20),
			Capacity:       5 + rng.Intn(15),
		})
	}
	for i := 0; i < nRiders; i++ {
		ws.Riders = append(ws.Riders, RiderSpec{
			Lat:    minLat + rng.Float64()*(maxLat-minLat),
			Lng:    minLng + rng.Float64()*(maxLng-minLng),
			Rating: 3.5 + rng.Float64()*1.5,
		})
	}
	return ws
}

// PickRestaurant selects via picker: random|hotspot|nearest_to_rider.
// hotspot picks the highest-capacity restaurant (deterministic); unknown pickers return -1.
func PickRestaurant(ws WorldSpec, picker string, riderLat, riderLng float64, rng *rand.Rand) int {
	if len(ws.Restaurants) == 0 {
		return -1
	}
	switch picker {
	case "hotspot":
		best, bestCap := 0, -1
		for i, r := range ws.Restaurants {
			if r.Capacity > bestCap {
				bestCap, best = r.Capacity, i
			}
		}
		return best
	case "nearest_to_rider":
		best, bestD := 0, 1e18
		for i, r := range ws.Restaurants {
			d := (r.Lat-riderLat)*(r.Lat-riderLat) + (r.Lng-riderLng)*(r.Lng-riderLng)
			if d < bestD {
				bestD, best = d, i
			}
		}
		return best
	case "random":
		if rng == nil {
			return -1
		}
		return rng.Intn(len(ws.Restaurants))
	default:
		return -1
	}
}

func IsVip(rng *rand.Rand, ratio float64) bool { return rng.Float64() < ratio }
