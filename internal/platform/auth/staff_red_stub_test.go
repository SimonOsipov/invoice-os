package auth

import (
	"context"
	"net/http"
)

// RED STUB, delete with this file when staff.go adds the real RequireRulesRole and StaffFromContext (ENGI-11-05).
// Both stubs fail open or report nothing, so every staff_test.go assertion fails on its own line.
func RequireRulesRole(next http.Handler) http.Handler { return next }

func StaffFromContext(context.Context) (Identity, bool) { return Identity{}, false }
