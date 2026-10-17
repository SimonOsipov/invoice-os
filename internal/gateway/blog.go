package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const (
	BlogReadPerIP   = 600
	BlogReadWindow  = 10 * time.Minute
	BlogReadMaxKeys = 100_000
)

const (
	blogShortCache   = "public, max-age=60"
	blogCoverCache   = "public, max-age=31536000, immutable"
	blogReadTimeout  = 5 * time.Second
	maxBlogBodyBytes = 8 << 20
)

// BlogRoutes holds the four public blog read handlers.
type BlogRoutes struct{ Categories, Posts, Post, Cover http.Handler }

// BlogHandlers is a stub: every route answers 501 until the implementation lands.
func BlogHandlers(base *url.URL, client *http.Client, gatewayToken string, perIP *SignInThrottle, enforce bool, log *slog.Logger) BlogRoutes {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	})
	return BlogRoutes{Categories: h, Posts: h, Post: h, Cover: h}
}
