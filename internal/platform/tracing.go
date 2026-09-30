package platform

import (
	"net/http"
	"strings"

	"github.com/getsentry/sentry-go"
)

// tracingMiddleware opens one transaction per request, named by the mux's route
// pattern before the handler runs. It has no defer: an http.ErrAbortHandler
// panic unwinds past it and sends no transaction.
// ceiling: sampled flag reset on every inbound hop; reset only at the gateway before TracesSampleRate drops below 1.0
func tracingMiddleware(mux *http.ServeMux) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions || isProbePath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			hub := taggedHub(r.Context())
			if hub == nil {
				next.ServeHTTP(w, r)
				return
			}

			method := methodLabel(r.Method)
			name, source := method+" unmatched", sentry.SourceCustom
			// The mux returns a raw path, not a pattern, for CONNECT.
			if r.Method != http.MethodConnect {
				if _, pattern := mux.Handler(r); pattern != "" {
					name, source = pattern, sentry.SourceRoute
					if !strings.Contains(pattern, " ") {
						name = method + " " + pattern
					}
				}
			}

			ctx := sentry.SetHubOnContext(r.Context(), hub)
			// Baggage is never read; the inbound sampled flag is reset so the sample rate decides.
			span := sentry.StartTransaction(ctx, name,
				sentry.ContinueTrace(hub, r.Header.Get("sentry-trace"), ""),
				func(s *sentry.Span) { s.Sampled = sentry.SampledUndefined },
				sentry.WithOpName("http.server"),
				sentry.WithTransactionSource(source),
			)
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r.WithContext(span.Context()))

			span.Status = sentry.HTTPtoSpanStatus(rec.status)
			span.SetData("http.request.method", method)
			span.SetData("http.response.status_code", rec.status)
			span.Finish()
		})
	}
}

// isProbePath reports the health endpoints, including the gateway's /healthz/fleet.
func isProbePath(p string) bool {
	return p == "/healthz" || p == "/readyz" || strings.HasPrefix(p, "/healthz/")
}

// methodLabel maps a request method into a closed set so a client cannot choose name text.
func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodTrace, http.MethodConnect:
		return m
	}
	return "OTHER"
}
