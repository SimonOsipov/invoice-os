package platform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"github.com/getsentry/sentry-go"
)

// requestOutcome collects what a request's handlers say about a 5xx. The mutex
// covers handlers that log from other goroutines through the request context.
type requestOutcome struct {
	mu        sync.Mutex
	cause     error
	elsewhere bool
}

func outcomeFromContext(ctx context.Context) *requestOutcome {
	out, _ := ctx.Value(ctxKeyOutcome).(*requestOutcome)
	return out
}

func (o *requestOutcome) setCause(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cause = err
}

func (o *requestOutcome) read() (error, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.cause, o.elsewhere
}

// ReportedElsewhere marks the request's 5xx.
func ReportedElsewhere(ctx context.Context) {
	if out := outcomeFromContext(ctx); out != nil {
		out.mu.Lock()
		out.elsewhere = true
		out.mu.Unlock()
	}
}

// CapturePanic reports a panic recovered off the request path; call it inside the deferred recover.
// ceiling: a non-error, non-string panic value ships as %#v; stringify and scrub it if one ever carries customer data
func CapturePanic(ctx context.Context, rec any) {
	if hub := taggedHub(ctx); hub != nil {
		hub.RecoverWithContext(ctx, rec)
	}
}

// serverErrorMiddleware reports a 5xx response once. It has no defer, so a
// panic unwinds past it and only recoveryMiddleware reports it.
// ceiling: one event per 5xx; sample or rate-cap if an outage ever burns the quota
func serverErrorMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := &requestOutcome{}
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyOutcome, out))
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		// The mux writes the matched pattern onto this request pointer.
		next.ServeHTTP(rec, r)

		// A cancelled request context means the client went away.
		if rec.status < http.StatusInternalServerError || errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
		cause, elsewhere := out.read()
		if elsewhere {
			return
		}
		captureServerError(r, rec.status, cause)
	})
}

// captureServerError fingerprints by route pattern and status only: scrubEvent
// does not scrub Fingerprint, so it must never hold request text.
func captureServerError(r *http.Request, status int, cause error) {
	hub := taggedHub(r.Context())
	if hub == nil {
		return
	}
	route := r.Pattern
	if route == "" {
		route = "unmatched"
	}
	code := strconv.Itoa(status)
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetRequest(r)
		scope.SetLevel(sentry.LevelError)
		scope.SetFingerprint([]string{"http-5xx", route, code})
		scope.SetTag("http.route", route)
		scope.SetTag("http.status_code", code)
	})
	if cause != nil {
		hub.CaptureException(cause)
		return
	}
	hub.CaptureMessage(fmt.Sprintf("HTTP %s from %s", code, route))
}
