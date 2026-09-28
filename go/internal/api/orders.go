package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/domain"
	"github.com/google/uuid"
)

type createOrderReq struct {
	RestaurantID *uuid.UUID `json:"restaurant_id"`
	Priority     string     `json:"priority"`
}

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid JSON")
		return
	}
	if req.RestaurantID == nil || *req.RestaurantID == uuid.Nil {
		writeError(w, r, 400, "VALIDATION_ERROR", "restaurant_id required")
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
	body := map[string]any{"restaurant_id": req.RestaurantID.String(), "priority": priority}
	replayed, hash := s.checkIdem(w, r, "POST /v1/orders", "", body)
	if replayed {
		return
	}
	var key *string
	if k := r.Header.Get("Idempotency-Key"); k != "" {
		key = &k
	}
	o, err := s.Store.CreateOrder(r.Context(), *req.RestaurantID, priority, time.Now().Add(45*time.Minute), key)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 500, "INTERNAL", "create order failed")
		}
		return
	}
	_ = s.Store.AppendEvent(r.Context(), nil, o.ID, nil, nil, "order_created", map[string]any{"priority": priority})
	resp := map[string]any{"order": o}
	s.saveIdem(r, "POST /v1/orders", "", hash, 201, resp)
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
	replayed, hash := s.checkIdem(w, r, tmpl, id.String(), body)
	if replayed {
		return
	}
	o, err := s.Store.SetOrderStatus(r.Context(), nil, id, okFrom, to)
	if err != nil {
		if !mapStoreError(w, r, err) {
			writeError(w, r, 500, "INTERNAL", "transition failed")
		}
		return
	}
	_ = s.Store.AppendEvent(r.Context(), nil, id, nil, nil, event, map[string]any{"to": to})
	resp := map[string]any{"order": o}
	s.saveIdem(r, tmpl, id.String(), hash, 200, resp)
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
			domain.OrderCreated, domain.OrderPreparing, domain.OrderReady, domain.OrderOffering,
		}, "order_cancelled"},
	}
}
