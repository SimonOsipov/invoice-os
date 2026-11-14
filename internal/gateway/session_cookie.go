package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// SessionHeader selects cookie mode on the three session routes; its only value is "cookie".
const SessionHeader = "X-Auth-Session"

const (
	sessionCookiePrefix = "__Host-rt-"
	sessionCookieMaxAge = 400 * 24 * 60 * 60 // seconds; Chrome's cap on a cookie's life
)

var errOriginNotAllowed = errors.New("origin not allowed")

var audienceShape = regexp.MustCompile(`^[a-z]+$`)

// SessionCookies maps each session origin to its audience.
type SessionCookies struct {
	byOrigin map[string]string
}

// ParseSessionOrigins parses AUTH_SESSION_ORIGINS: comma-separated audience=origin pairs.
func ParseSessionOrigins(v string) (*SessionCookies, error) {
	c := &SessionCookies{byOrigin: map[string]string{}}
	auds := map[string]bool{}
	for _, pair := range strings.Split(v, ",") {
		aud, origin, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || !audienceShape.MatchString(aud) {
			return nil, fmt.Errorf("AUTH_SESSION_ORIGINS: %q is not audience=origin with a lower-case audience", pair)
		}
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.HasSuffix(origin, "?") || strings.HasSuffix(origin, "#") {
			return nil, fmt.Errorf("AUTH_SESSION_ORIGINS: %q is not a bare https origin", origin)
		}
		if _, dup := c.byOrigin[origin]; dup || auds[aud] {
			return nil, fmt.Errorf("AUTH_SESSION_ORIGINS: duplicate audience or origin in %q", pair)
		}
		auds[aud] = true
		c.byOrigin[origin] = aud
	}
	return c, nil
}

// Origins lists the session origins; used to check them against the CORS list.
func (c *SessionCookies) Origins() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.byOrigin))
	for o := range c.byOrigin {
		out = append(out, o)
	}
	return out
}

func (c *SessionCookies) isSessionOrigin(origin string) bool {
	if c == nil {
		return false
	}
	_, ok := c.byOrigin[origin]
	return ok
}

// mode: ("", false, nil) is body mode; an error answers 403 before any work.
func (c *SessionCookies) mode(r *http.Request) (string, bool, error) {
	h := r.Header.Get(SessionHeader)
	if h == "" {
		// ceiling: body mode for a browser Origin serves pre-deploy tabs, remove 30 days after the production deploy
		return "", false, nil
	}
	if c == nil || h != "cookie" {
		return "", false, errOriginNotAllowed
	}
	aud, ok := c.byOrigin[r.Header.Get("Origin")]
	if !ok {
		return "", false, errOriginNotAllowed
	}
	return aud, true, nil
}

func (c *SessionCookies) cookie(r *http.Request, aud, value string, maxAge int) *http.Cookie {
	ck := &http.Cookie{Name: sessionCookiePrefix + aud, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: true}
	if strictFetch(r) {
		ck.SameSite = http.SameSiteStrictMode
	} else {
		ck.SameSite = http.SameSiteNoneMode
		ck.Partitioned = true
	}
	return ck
}

func (c *SessionCookies) set(w http.ResponseWriter, r *http.Request, aud, token string) {
	http.SetCookie(w, c.cookie(r, aud, token, sessionCookieMaxAge))
}

// clear sends the set attributes with Max-Age=0 (Go writes Max-Age=0 for a negative MaxAge).
func (c *SessionCookies) clear(w http.ResponseWriter, r *http.Request, aud string) {
	http.SetCookie(w, c.cookie(r, aud, "", -1))
}

// read returns the cookie of aud only.
func (c *SessionCookies) read(r *http.Request, aud string) string {
	ck, err := r.Cookie(sessionCookiePrefix + aud)
	if err != nil {
		return ""
	}
	return ck.Value
}

// Strict only on the browser's own same-site verdict; else None+Partitioned.
func strictFetch(r *http.Request) bool {
	s := r.Header.Get("Sec-Fetch-Site")
	return s == "same-site" || s == "same-origin"
}
