package api

import (
	"net/http"

	"github.com/google/uuid"
)

// Stubs wired fully in phases B3/C. They keep B2 compiling.

func (s *Server) handleRiderWebhook(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, 501, "VALIDATION_ERROR", "webhook wired in B5")
}

func (s *Server) handleCreateProposal(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, 501, "VALIDATION_ERROR", "proposals wired in C2")
}

func (s *Server) handleGetProposal(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	writeError(w, r, 501, "VALIDATION_ERROR", "proposals wired in C2")
}

func (s *Server) handleCommitProposal(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	writeError(w, r, 501, "VALIDATION_ERROR", "proposals wired in C3")
}

func (s *Server) handleRejectProposal(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	writeError(w, r, 501, "VALIDATION_ERROR", "proposals wired in C2")
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, 501, "VALIDATION_ERROR", "snapshot wired in C1")
}

func (s *Server) handleOrderTrace(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	writeError(w, r, 501, "VALIDATION_ERROR", "trace wired in C1")
}

func (s *Server) dispatchAdmin(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, 501, "VALIDATION_ERROR", "admin wired in B3")
}
