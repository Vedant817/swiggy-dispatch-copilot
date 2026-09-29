package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/domain"
	"github.com/google/uuid"
)

type createOrderReq struct {
	RestaurantID      *uuid.UUID `json:"restaurant_id"`
	RestaurantPicker  string     `json:"restaurant_picker"`
	Priority          string     `json:"priority"`
}

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid JSON")
		return
	}
	if req.RestaurantID != nil && req.RestaurantPicker != "" {
		writeError(w, r, 400, "VALIDATION_ERROR", "provide either restaurant_id or restaurant_picker")
		return
	}
	priority := req.Priority
	if priority == "" {
		priority = "normal"
	}
	if priority != "normal" && priority != "vip" {
		writeError(w, r, 400, "VALIDATION_ERROR", "priority must be normal|vip")
		return
	}
	// Idempotency hashes the original request, so picker retries replay correctly.
	var body map[string]any
	if req.RestaurantID != nil {
		body = map[string]any{"restaurant_id": req.RestaurantID.String(), "priority": priority}
	} else if req.RestaurantPicker != "" {
		if req.RestaurantPicker != "random" && req.RestaurantPicker != "hotspot" {
			writeError(w, r, 400, "VALIDATION_ERROR", "restaurant_picker must be random|hotspot")
			return
		}
		body = map[string]any{"restaurant_picker": req.RestaurantPicker, "priority": priority}
	} else {
		writeError(w, r, 400, "VALIDATION_ERROR", "restaurant_id required")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	prior, replayed, err := s.Store.LoadOrderReplay(r.Context(), key, body)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 500, "INTERNAL", "order replay failed")
		}
		return
	}
	if replayed {
		s.Counters.Inc("idempotency_replays")
		writeJSON(w, 201, map[string]any{"order": prior})
		return
	}
	restaurantID := req.RestaurantID
	if restaurantID == nil {
		var picked uuid.UUID
		var q string
		if req.RestaurantPicker == "hotspot" {
			q = `SELECT id FROM restaurants ORDER BY capacity DESC LIMIT 1`
		} else {
			q = `SELECT id FROM restaurants ORDER BY random() LIMIT 1`
		}
		if err := s.Store.Pool.QueryRow(r.Context(), q).Scan(&picked); err != nil {
			writeError(w, r, 409, "VALIDATION_ERROR", "no restaurants seeded")
			return
		}
		restaurantID = &picked
	}
	o, replayed, err := s.Store.CreateOrderIdempotent(r.Context(), *restaurantID, priority, time.Now().Add(45*time.Minute), key, body)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 500, "INTERNAL", "create order failed")
		}
		return
	}
	if replayed {
		s.Counters.Inc("idempotency_replays")
	}
	resp := map[string]any{"order": o}
	writeJSON(w, 201, resp)
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	o, err := s.Store.GetOrder(r.Context(), id)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 404, "ORDER_NOT_FOUND", "order not found")
		}
		return
	}
	o.IdempotencyKey = nil
	active, found, _ := s.Store.ActiveAssignmentForOrder(r.Context(), nil, id)
	resp := map[string]any{"order": o}
	if found {
		resp["active_assignment"] = active
	}
	writeJSON(w, 200, resp)
}

func (s *Server) handleOrderTransition(w http.ResponseWriter, r *http.Request, id uuid.UUID, to string, okFrom []string, event string) {
	body := map[string]any{"to": to}
	tmpl := r.Method + " " + r.URL.Path
	// normalize template: replace id with {id}
	tmpl = strings.Replace(tmpl, id.String(), "{id}", 1)
	owned, hash := s.beginIdem(w, r, tmpl, id.String(), body)
	if !owned {
		return
	}
	if to == domain.OrderCancelled {
		o, err := s.Store.CancelOrder(r.Context(), id, r.Header.Get("Idempotency-Key"), tmpl)
		if err != nil {
			s.abortIdem(r, tmpl, id.String())
			if !mapStoreError(w, r, err) {
				writeError(w, r, 500, "INTERNAL", "cancel failed")
			}
			return
		}
		writeJSON(w, 200, map[string]any{"order": o})
		return
	}
	o, err := s.Store.SetOrderStatus(r.Context(), nil, id, okFrom, to)
	if err != nil {
		s.abortIdem(r, tmpl, id.String())
		if !mapStoreError(w, r, err) {
			writeError(w, r, 500, "INTERNAL", "transition failed")
		}
		return
	}
	_ = s.Store.AppendEvent(r.Context(), nil, id, nil, nil, event, map[string]any{"to": to})
	resp := map[string]any{"order": o}
	s.endIdem(r, tmpl, id.String(), hash, 200, resp)
	writeJSON(w, 200, resp)
}

func orderTargets() map[string]struct {
	to     string
	from   []string
	event  string
}{
	return map[string]struct {
		to    string
		from  []string
		event string
	}{
		"prepare": {domain.OrderPreparing, []string{domain.OrderCreated}, "order_prepare"},
		"ready":   {domain.OrderReady, []string{domain.OrderPreparing}, "order_ready"},
		"pickup":  {domain.OrderPickedUp, []string{domain.OrderAssigned}, "order_pickup"},
		"deliver": {domain.OrderDelivered, []string{domain.OrderPickedUp}, "order_deliver"},
		"cancel": {domain.OrderCancelled, []string{
			domain.OrderCreated, domain.OrderPreparing, domain.OrderReady, domain.OrderOffering, domain.OrderAssigned,
		}, "order_cancelled"},
	}
}
