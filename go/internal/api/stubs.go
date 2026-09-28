package api

import (
	"net/http"

	"github.com/google/uuid"
)

// Stubs wired fully in phases B3/C. They keep B2 compiling.

func (s *Server) handleCommitProposal(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	writeError(w, r, 501, "VALIDATION_ERROR", "proposals wired in C3")
}
