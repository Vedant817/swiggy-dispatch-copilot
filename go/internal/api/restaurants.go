package api

import (
	"net/http"

	"github.com/google/uuid"
)

func (s *Server) handleListRestaurants(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.Pool.Query(r.Context(),
		`SELECT id,name,lat,lng,prep_minutes_p50,capacity FROM restaurants ORDER BY created_at, id LIMIT 200`)
	if err != nil {
		writeError(w, r, 500, "INTERNAL", "list failed")
		return
	}
	defer rows.Close()
	type R struct {
		ID   string  `json:"id"`
		Name string  `json:"name"`
		Lat  float64 `json:"lat"`
		Lng  float64 `json:"lng"`
	}
	out := []R{}
	for rows.Next() {
		var id uuid.UUID
		var name string
		var lat, lng float64
		var prep, cap int
		if err := rows.Scan(&id, &name, &lat, &lng, &prep, &cap); err != nil {
			writeError(w, r, 500, "INTERNAL", "list failed")
			return
		}
		out = append(out, R{ID: id.String(), Name: name, Lat: lat, Lng: lng})
	}
	if err := rows.Err(); err != nil {
		writeError(w, r, 500, "INTERNAL", "list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"restaurants": out})
}
