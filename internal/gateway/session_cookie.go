package gateway

import "net/http"

// SessionHeader selects cookie mode on the three session routes; its only value is "cookie".
const SessionHeader = "X-Auth-Session"

// SessionCookies maps each session origin to its audience.
// STUB (SECAUD-17-09 test-spec): the executor replaces every body below.
type SessionCookies struct{}

// ParseSessionOrigins parses AUTH_SESSION_ORIGINS.
func ParseSessionOrigins(string) (*SessionCookies, error) { return &SessionCookies{}, nil }

// mode: ("", false, nil) is body mode; an error answers 403 before any work.
func (c *SessionCookies) mode(*http.Request) (string, bool, error) { return "", false, nil }

func (c *SessionCookies) set(http.ResponseWriter, *http.Request, string, string) {}

func (c *SessionCookies) clear(http.ResponseWriter, *http.Request, string) {}

func (c *SessionCookies) read(*http.Request, string) string { return "" }

func strictFetch(*http.Request) bool { return false }
