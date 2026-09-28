package platform

import (
	"bytes"
	"encoding/json"
	"net/textproto"
	"regexp"
	"strings"
	"unicode"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
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

// scrubEvent removes request data, queries, user identity and customer text
// from an error event or transaction before it leaves for Sentry. It never
// drops the event.
func scrubEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	event.Transaction = stripQuery(event.Transaction)
	event.User = sentry.User{}
	event.Message = ScrubText(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = ScrubText(event.Exception[i].Value)
	}

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
			event.Tags[k] = ScrubText(v)
		}
	}

	for k, c := range event.Contexts {
		c = scrubData(c)
		if d, ok := c["data"].(map[string]interface{}); ok && k == "trace" {
			c["data"] = scrubData(d)
		}
		event.Contexts[k] = c
	}

	for i, s := range event.Spans {
		event.Spans[i] = scrubSpan(s)
	}

	// Breadcrumbs are shared with the scope, so each is replaced, not edited.
	for i, b := range event.Breadcrumbs {
		if b == nil {
			continue
		}
		c := *b
		c.Message = ScrubText(c.Message)
		c.Data = scrubData(c.Data)
		event.Breadcrumbs[i] = &c
	}
	return event
}

// scrubLog removes queries and customer text from a log record before it
// leaves for Sentry. It never drops the record.
func scrubLog(log *sentry.Log) *sentry.Log {
	log.Body = ScrubText(log.Body)
	for k, v := range log.Attributes {
		// Emitf writes each argument here raw and unquoted, so ScrubText cannot see it.
		if k == "http.query" || k == "http.fragment" || strings.HasPrefix(k, "sentry.message.parameters.") || strings.HasPrefix(k, "user.") {
			delete(log.Attributes, k)
			continue
		}
		switch v.Type() {
		case attribute.STRING:
			log.Attributes[k] = attribute.StringValue(ScrubText(v.AsString()))
		case attribute.STRINGSLICE:
			ss := v.AsStringSlice()
			for i := range ss {
				ss[i] = ScrubText(ss[i])
			}
			log.Attributes[k] = attribute.StringSliceValue(ss)
		}
	}
	return log
}

// scrubSpan returns a scrubbed copy of s. A caller may still write to a
// finished child span, so Tags and Data are read under the span's own lock.
func scrubSpan(s *sentry.Span) *sentry.Span {
	// MakeSerializationSafe is the only exported path that takes the span's lock.
	(&sentry.Event{Spans: []*sentry.Span{s}}).MakeSerializationSafe()
	var snap struct {
		Tags map[string]string      `json:"tags"`
		Data map[string]interface{} `json:"data"`
	}
	raw, err := json.Marshal(s)
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		err = dec.Decode(&snap)
	}
	if err != nil {
		snap.Tags, snap.Data = nil, nil
	}
	for k, v := range snap.Tags {
		snap.Tags[k] = ScrubText(v)
	}
	return &sentry.Span{
		TraceID:      s.TraceID,
		SpanID:       s.SpanID,
		ParentSpanID: s.ParentSpanID,
		Name:         ScrubText(s.Name),
		Op:           s.Op,
		Description:  ScrubText(s.Description),
		Status:       s.Status,
		Tags:         snap.Tags,
		StartTime:    s.StartTime,
		EndTime:      s.EndTime,
		Data:         scrubData(snap.Data),
		Sampled:      s.Sampled,
		Source:       s.Source,
		Origin:       s.Origin,
	}
}

// scrubData returns a copy of d without top-level query or fragment keys,
// with strings at any depth passed through ScrubText.
func scrubData(d map[string]interface{}) map[string]interface{} {
	if d == nil {
		return nil
	}
	out := make(map[string]interface{}, len(d))
	for k, v := range d {
		if k == "http.query" || k == "http.fragment" {
			continue
		}
		out[k] = scrubValue(v)
	}
	return out
}

// scrubValue returns a copy of v with every nested string passed through ScrubText.
func scrubValue(v interface{}) interface{} {
	switch x := v.(type) {
	case string:
		return ScrubText(x)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, e := range x {
			out[k] = scrubValue(e)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(x))
		for k, e := range x {
			out[k] = ScrubText(e)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, e := range x {
			out[i] = scrubValue(e)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, e := range x {
			out[i] = ScrubText(e)
		}
		return out
	}
	return v
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
// Convention: an error message %q-quotes every customer-derived value.
// ceiling: covers quoted text and upstream reasons only; re-triage every Errorf/errors.New site when a Sentry event shows customer text
func ScrubText(s string) string {
	return stripQuery(redactUpstreamReason(redactQuoted(s)))
}

// redactQuoted replaces each Go double-quoted segment with "[redacted]".
// Rule: '"' and '\"' are rune literals, not delimiters; an odd delimiter count redacts from the first delimiter to the end.
func redactQuoted(s string) string {
	if !strings.Contains(s, `"`) {
		return s
	}
	first, open := -1, false
	for i := 0; i < len(s); i++ {
		switch {
		case !open && runeLiteralLen(s[i:]) > 0:
			i += runeLiteralLen(s[i:]) - 1
		case open && s[i] == '\\':
			i++
		case s[i] == '"':
			if first < 0 {
				first = i
			}
			open = !open
		}
	}
	if first < 0 {
		return s
	}
	if open {
		return s[:first] + `"[redacted]`
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if n := runeLiteralLen(s[i:]); n > 0 {
			b.WriteString(s[i : i+n])
			i += n - 1
			continue
		}
		if s[i] != '"' {
			b.WriteByte(s[i])
			continue
		}
		b.WriteString(`"[redacted]"`)
		for i++; s[i] != '"'; i++ {
			if s[i] == '\\' {
				i++
			}
		}
	}
	return b.String()
}

// runeLiteralLen returns the length of a '"' or '\"' prefix of s, else 0.
func runeLiteralLen(s string) int {
	switch {
	case strings.HasPrefix(s, `'"'`):
		return 3
	case strings.HasPrefix(s, `'\"'`):
		return 4
	}
	return 0
}

var upstreamStatus = regexp.MustCompile(`returned [0-9]{3}: `)

// redactUpstreamReason replaces the text after "returned <3 digits>: " with [redacted].
func redactUpstreamReason(s string) string {
	loc := upstreamStatus.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[1]] + "[redacted]"
}
