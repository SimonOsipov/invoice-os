package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	readyPrefix  = "GoTrue API started on:"
	memoSize     = 256
	resendWindow = time.Hour
	maxLabel     = 80
	maxText      = 500
)

var (
	jwtRE   = regexp.MustCompile(`eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	uuidRE  = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	digitRE = regexp.MustCompile(`[0-9]{7,}`)
	tokenRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

// reqMemo is what an earlier line of one request left behind.
type reqMemo struct {
	panicked bool
	email    string
}

// classifier turns GoTrue stderr lines into Sentry events.
type classifier struct {
	now func() time.Time

	mu        sync.Mutex
	ready     bool
	jsonLabel string
	rawLabel  string
	memo      map[string]*reqMemo
	order     []string
	sent      map[string]time.Time
}

func newClassifier() *classifier {
	return &classifier{now: time.Now, memo: map[string]*reqMemo{}, sent: map[string]time.Time{}}
}

// line returns the event for one stderr line, or nil.
func (c *classifier) line(b []byte) *sentry.Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		if c.rawLabel == "" {
			c.rawLabel = label(string(b))
		}
		return nil
	}
	msg, _ := m["msg"].(string)
	level, _ := m["level"].(string)

	if strings.HasPrefix(msg, readyPrefix) {
		c.ready = true
		return nil
	}
	if level == "fatal" || level == "panic" {
		c.jsonLabel = label(msg)
	}

	rid := requestID(m)
	email := actorEmail(m)
	if rid != "" && email != "" {
		c.remember(rid).email = email
	}

	switch msg {
	case "request panicked":
		if rid != "" {
			c.remember(rid).panicked = true
		}
		return c.request(m, rid, email, "GoTrue panic", text(m["panic"]))
	case "request completed":
		status, _ := m["status"].(float64)
		if status < 500 || (rid != "" && c.memo[rid] != nil && c.memo[rid].panicked) {
			return nil
		}
		return c.request(m, rid, email, fmt.Sprintf("GoTrue %d", int(status)), text(m["error"]))
	}
	return nil
}

func (c *classifier) request(m map[string]any, rid, email, typ, value string) *sentry.Event {
	if value == "" {
		value = "request failed"
	}
	if email == "" && rid != "" && c.memo[rid] != nil {
		email = c.memo[rid].email
	}
	path := normPath(str(m["path"]))
	fp := []string{"gotrue", typ, path, value}
	if !c.allow(fp) {
		return nil
	}
	tags := map[string]string{"path": path}
	for _, k := range []string{"method", "error_code", "grant_type"} {
		if v := str(m[k]); tokenRE.MatchString(v) {
			tags[k] = v
		}
	}
	if rid != "" {
		tags["request_id"] = rid
	}
	if s, ok := m["status"].(float64); ok {
		tags["status"] = fmt.Sprint(int(s))
	}
	return &sentry.Event{
		Level:       sentry.LevelError,
		Exception:   []sentry.Exception{{Type: typ, Value: value}},
		Tags:        tags,
		User:        sentry.User{Email: email},
		Fingerprint: fp,
	}
}

// exit returns the event for GoTrue ending with a non-zero code, or not starting.
func (c *classifier) exit(code int, startErr error) *sentry.Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	phase := "run"
	if !c.ready {
		phase = "boot"
	}
	value, lbl := "", c.jsonLabel
	switch {
	case startErr != nil:
		value, lbl = "GoTrue did not start", "did not start"
	case code == 0:
		return nil
	default:
		if lbl == "" {
			lbl = c.rawLabel
		}
		value = fmt.Sprintf("GoTrue exited with code %d", code)
		if lbl != "" {
			value += ": " + lbl
		}
	}
	return &sentry.Event{
		Level:       sentry.LevelFatal,
		Exception:   []sentry.Exception{{Type: "GoTrue exit", Value: value}},
		Tags:        map[string]string{"auth.phase": phase},
		Fingerprint: []string{"gotrue", "exit", phase, lbl},
	}
}

// allow admits one event per fingerprint per hour.
// ceiling: no daily cap, so many distinct failures can spend the shared quota; revisit if auth spends more than 500 errors in a month
func (c *classifier) allow(fp []string) bool {
	key, now := strings.Join(fp, "\x00"), c.now()
	if at, ok := c.sent[key]; ok && now.Sub(at) < resendWindow {
		return false
	}
	if len(c.sent) >= memoSize*4 {
		for k, at := range c.sent {
			if now.Sub(at) >= resendWindow {
				delete(c.sent, k)
			}
		}
	}
	c.sent[key] = now
	return true
}

func (c *classifier) remember(rid string) *reqMemo {
	if r, ok := c.memo[rid]; ok {
		return r
	}
	if len(c.order) >= memoSize {
		delete(c.memo, c.order[0])
		c.order = c.order[1:]
	}
	r := &reqMemo{}
	c.memo[rid], c.order = r, append(c.order, rid)
	return r
}

func requestID(m map[string]any) string {
	if id := str(m["request_id"]); tokenRE.MatchString(id) {
		return id
	}
	if a, ok := m["auth_audit_event"].(map[string]any); ok {
		if id := str(a["request_id"]); tokenRE.MatchString(id) {
			return id
		}
	}
	return ""
}

// actorEmail returns actor_username when it is an address; the platform filter makes the final check.
func actorEmail(m map[string]any) string {
	for _, k := range []string{"auth_event", "auth_audit_event"} {
		if a, ok := m[k].(map[string]any); ok {
			if u := str(a["actor_username"]); strings.Count(u, "@") == 1 {
				return u
			}
		}
	}
	return ""
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func text(v any) string {
	if s, ok := v.(string); ok {
		return normText(s)
	}
	if v == nil {
		return ""
	}
	return normText(fmt.Sprint(v))
}

// normText drops tokens, ids and phone numbers from GoTrue's error text.
func normText(s string) string {
	s = jwtRE.ReplaceAllString(s, "[redacted]")
	s = uuidRE.ReplaceAllString(s, "{id}")
	s = digitRE.ReplaceAllString(s, "{n}")
	s = platform.ScrubText(s)
	if len(s) > maxText {
		s = s[:maxText]
	}
	return s
}

// normPath strips the query and replaces each UUID with {id}.
func normPath(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = uuidRE.ReplaceAllString(p, "{id}")
	if len(p) > 200 {
		p = p[:200]
	}
	return p
}

// label is the text before the first ": ". D-7: nothing after it can leave.
func label(s string) string {
	s, _, _ = strings.Cut(s, ": ")
	if len(s) > maxLabel {
		s = s[:maxLabel]
	}
	return platform.ScrubText(s)
}
