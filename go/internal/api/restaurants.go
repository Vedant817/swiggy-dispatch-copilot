package api

import (
	"net/http"
)

func (s *Server) handleListRestaurants(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.Pool.Query(r.Context(),
		`SELECT id,name,lat,lng,prep_minutes_p50,capacity,created_at FROM restaurants ORDER BY created_at LIMIT 200`)
	if err != nil {
		writeError(w, r, 500, "INTERNAL", "list failed")
		return
	}
	defer rows.Close()
	type R struct {
		ID any `json:"id"`
		Name string `json:"name"`
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	}
	out := []R{}
	for rows.Next() {
		var id, created any
		var name string
		var lat, lng float64
		var prep, cap int
		_ = rows.Scan(&id, &name, &lat, &lng, &prep, &cap, &created)
		out = append(out, R{ID: id, Name: name, Lat: lat, Lng: lng})
	}
	writeJSON(w, 200, map[string]any{"restaurants": out})
}
