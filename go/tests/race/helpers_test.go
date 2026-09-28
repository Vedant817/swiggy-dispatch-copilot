package race

import (
	"strings"

	"github.com/google/uuid"
)

func mustParse(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}

func isBenignAssignErr(err error) bool {
	if err == nil {
		return true
	}
	m := err.Error()
	return strings.Contains(m, "ASSIGNMENT_STATE_CONFLICT") ||
		strings.Contains(m, "ORDER_STATE_CONFLICT") ||
		strings.Contains(m, "RIDER_STATE_CONFLICT") ||
		strings.Contains(m, "NO_RIDERS_AVAILABLE")
}
