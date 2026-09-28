package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/store"
	"github.com/jackc/pgx/v5"
)

func requestID(r *http.Request) string { return r.Header.Get("X-Request-Id") }

func writeError(w http.ResponseWriter, r *http.Request, httpCode int, code, msg string) {
	writeJSON(w, httpCode, map[string]string{"code": code, "message": msg, "request_id": requestID(r)})
}

func mapStoreError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrIdempotencyReused) {
		writeError(w, r, 409, "IDEMPOTENCY_KEY_REUSED", "idempotency key reused with different payload")
		return true
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, r, 404, "NOT_FOUND", "not found")
		return true
	}
	if errors.Is(err, store.ErrStateConflict) {
		writeError(w, r, 409, "ORDER_STATE_CONFLICT", "illegal state transition")
		return true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, 404, "NOT_FOUND", "not found")
		return true
	}
	msg := err.Error()
	// Postgres deadlock (40P01) from concurrent commit/accept: retryable conflict.
	if strings.Contains(msg, "40P01") || strings.Contains(strings.ToLower(msg), "deadlock") {
		writeError(w, r, 409, "ASSIGNMENT_STATE_CONFLICT", "concurrent modification, retry")
		return true
	}
	switch {
	case strings.HasPrefix(msg, "ORDER_NOT_FOUND"):
		writeError(w, r, 404, "ORDER_NOT_FOUND", msg)
		return true
	case strings.HasPrefix(msg, "RIDER_NOT_FOUND"):
		writeError(w, r, 404, "RIDER_NOT_FOUND", msg)
		return true
	case strings.HasPrefix(msg, "ASSIGNMENT_NOT_FOUND"):
		writeError(w, r, 404, "ASSIGNMENT_NOT_FOUND", msg)
		return true
	case strings.HasPrefix(msg, "PROPOSAL_NOT_FOUND"):
		writeError(w, r, 404, "PROPOSAL_NOT_FOUND", msg)
		return true
	case strings.Contains(msg, "ORDER_STATE_CONFLICT"):
		writeError(w, r, 409, "ORDER_STATE_CONFLICT", msg)
		return true
	case strings.Contains(msg, "RIDER_STATE_CONFLICT"):
		writeError(w, r, 409, "RIDER_STATE_CONFLICT", msg)
		return true
	case strings.Contains(msg, "ASSIGNMENT_STATE_CONFLICT"):
		writeError(w, r, 409, "ASSIGNMENT_STATE_CONFLICT", msg)
		return true
	case strings.Contains(msg, "PROPOSAL_STATE_CONFLICT"):
		writeError(w, r, 409, "PROPOSAL_STATE_CONFLICT", msg)
		return true
	case strings.Contains(msg, "PROPOSAL_NOT_COMMITTABLE"):
		writeError(w, r, 422, "PROPOSAL_NOT_COMMITTABLE", msg)
		return true
	case strings.Contains(msg, "NO_RIDERS_AVAILABLE"):
		writeError(w, r, 409, "NO_RIDERS_AVAILABLE", "no available riders")
		return true
	case strings.Contains(msg, "VALIDATION_ERROR"):
		writeError(w, r, 400, "VALIDATION_ERROR", msg)
		return true
	case strings.Contains(msg, "UNAUTHORIZED"):
		writeError(w, r, 401, "UNAUTHORIZED", msg)
		return true
	case strings.Contains(msg, "FORBIDDEN"):
		writeError(w, r, 403, "FORBIDDEN", msg)
		return true
	}
	return false
}
