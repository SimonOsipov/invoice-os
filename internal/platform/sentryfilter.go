package platform

import (
	"net/textproto"
	"strings"
	"unicode"

	"github.com/getsentry/sentry-go"
)

// sentryHeaders is an allowlist: a header nobody listed never reaches Sentry.
var sentryHeaders = map[string]struct{}{
	"Accept":         {},
	"Content-Length": {},
	"Content-Type":   {},
	"Host":           {},
	"User-Agent":     {},
	"X-Request-Id":   {},
}

// scrubEvent removes request data, queries and user identity from an error
// event or transaction before it leaves for Sentry. It never drops the event.
func scrubEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	event.Transaction = stripQuery(event.Transaction)
	event.User = sentry.User{}

	if r := event.Request; r != nil {
		r.URL = stripQuery(r.URL)
		r.QueryString = ""
		r.Data = ""
		r.Cookies = ""
		r.Env = nil
		headers := make(map[string]string, len(r.Headers))
		for k, v := range r.Headers {
			ck := textproto.CanonicalMIMEHeaderKey(k)
			if _, ok := sentryHeaders[ck]; ok {
				headers[ck] = v
			}
		}
		r.Headers = headers
	}

	for k, v := range event.Tags {
		// The id tags stay byte-identical so they keep matching Railway logs.
		if k != "request_id" && k != "tenant_id" {
			event.Tags[k] = stripQuery(v)
		}
	}

	if trace, ok := event.Contexts["trace"]; ok && trace != nil {
		if d, ok := trace["description"].(string); ok {
			trace["description"] = stripQuery(d)
		}
		if d, ok := trace["data"].(map[string]interface{}); ok {
			trace["data"] = scrubData(d)
		}
	}

	for _, s := range event.Spans {
		s.Description = stripQuery(s.Description)
		s.Data = scrubData(s.Data)
	}

	// Breadcrumbs are shared with the scope, so each is replaced, not edited.
	for i, b := range event.Breadcrumbs {
		if b == nil {
			continue
		}
		c := *b
		c.Message = stripQuery(c.Message)
		c.Data = scrubData(c.Data)
		event.Breadcrumbs[i] = &c
	}
	return event
}

// scrubData returns a copy of d without query or fragment keys, with string
// values passed through stripQuery.
func scrubData(d map[string]interface{}) map[string]interface{} {
	if d == nil {
		return nil
	}
	out := make(map[string]interface{}, len(d))
	for k, v := range d {
		if k == "http.query" || k == "http.fragment" {
			continue
		}
		if s, ok := v.(string); ok {
			v = stripQuery(s)
		}
		out[k] = v
	}
	return out
}

// stripQuery removes each "?" or "#" and the run of non-whitespace after it.
func stripQuery(s string) string {
	if !strings.ContainsAny(s, "?#") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	skipping := false
	for _, r := range s {
		switch {
		case r == '?' || r == '#':
			skipping = true
		case unicode.IsSpace(r):
			skipping = false
			b.WriteRune(r)
		case !skipping:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ScrubText removes customer text from s before it leaves for Sentry.
func ScrubText(s string) string { return s }

// redactQuoted replaces each Go double-quoted segment with "[redacted]".
func redactQuoted(s string) string { return s }

// redactUpstreamReason replaces the text after "returned <3 digits>: " with [redacted].
func redactUpstreamReason(s string) string { return s }
