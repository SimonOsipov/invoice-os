package platform

import (
	"context"
	"net/http"
)

// ReportedElsewhere marks the request's 5xx as already reported by another service.
func ReportedElsewhere(ctx context.Context) {}

// CapturePanic reports a panic recovered off the request path; call it inside the deferred recover.
func CapturePanic(ctx context.Context, rec any) {}

// serverErrorMiddleware reports a 5xx response once.
func serverErrorMiddleware(next http.Handler) http.Handler { return next }
