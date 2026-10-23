package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"time"
)

const (
	blogShortCache   = "public, max-age=60"
	blogCoverCache   = "public, max-age=31536000, immutable"
	blogReadTimeout  = 5 * time.Second
	maxBlogBodyBytes = 8 << 20
)

var (
	blogSlugRE    = regexp.MustCompile(`^[a-z0-9-]{1,80}$`)
	blogVersionRE = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
)

// BlogRoutes holds the public blog read handlers; NotFound answers every other path under /blog/posts/ and /blog/covers/.
type BlogRoutes struct{ Categories, Posts, Post, Cover, NotFound http.Handler }

// BlogHandlers serves the anonymous blog reads by forwarding to notifications at base.
func BlogHandlers(base *url.URL, client *http.Client, gatewayToken string, log *slog.Logger) BlogRoutes {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	b := &blogReader{base: base, client: &c, token: gatewayToken, log: log}
	return BlogRoutes{
		Categories: b.handler(true, true, func(*http.Request) ([]string, bool) { return []string{"categories"}, true }),
		Posts:      b.handler(true, true, func(*http.Request) ([]string, bool) { return []string{"posts"}, true }),
		Post: b.handler(true, false, func(r *http.Request) ([]string, bool) {
			slug := r.PathValue("slug")
			return []string{"posts", slug}, blogSlugRE.MatchString(slug)
		}),
		Cover: b.handler(false, false, func(r *http.Request) ([]string, bool) {
			id, version := r.PathValue("id"), r.PathValue("version")
			return []string{"covers", id, version}, blogSlugRE.MatchString(id) && blogVersionRE.MatchString(version)
		}),
		NotFound: b.handler(true, false, func(*http.Request) ([]string, bool) { return nil, false }),
	}
}

type blogReader struct {
	base   *url.URL
	client *http.Client
	token  string
	log    *slog.Logger
}

func (b *blogReader) notFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", blogShortCache)
	writeError(w, http.StatusNotFound, "not found")
}

func (b *blogReader) unavailable(w http.ResponseWriter, r *http.Request, status int, err error) {
	attrs := []any{slog.Int("status", status)}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	b.log.WarnContext(r.Context(), "blog-read: upstream failed", attrs...)
	w.Header().Set("Cache-Control", "no-store")
	writeError(w, http.StatusBadGateway, "blog is unavailable")
}

// handler builds one read route. listRoute marks the routes whose upstream 404 is deploy skew.
func (b *blogReader) handler(json, listRoute bool, target func(*http.Request) ([]string, bool)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.Header().Set("Cache-Control", "no-store")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		// The uniform 404 must match across routes, so the cover route carries Vary on its 404 only.
		addVary := func() {
			if !slices.Contains(h.Values("Vary"), "Origin") {
				h.Add("Vary", "Origin")
			}
		}
		if json {
			addVary()
		}

		segs, ok := target(r)
		if !ok {
			addVary()
			b.notFound(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), blogReadTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base.JoinPath(append([]string{"internal", "blog"}, segs...)...).String(), nil)
		if err != nil {
			b.unavailable(w, r, 0, err)
			return
		}
		req.Header.Set("X-Gateway-Token", b.token)
		resp, err := b.client.Do(req)
		if err != nil {
			b.unavailable(w, r, 0, err)
			return
		}
		defer resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusNotFound && !listRoute:
			addVary()
			b.notFound(w)
		case resp.StatusCode != http.StatusOK:
			b.unavailable(w, r, resp.StatusCode, nil)
		default:
			body, err := io.ReadAll(io.LimitReader(resp.Body, maxBlogBodyBytes+1))
			ct := resp.Header.Get("Content-Type")
			if err != nil || len(body) > maxBlogBodyBytes || ct == "" {
				b.unavailable(w, r, resp.StatusCode, nil)
				return
			}
			h.Set("Content-Type", ct)
			if json {
				h.Set("Cache-Control", blogShortCache)
			} else {
				h.Set("Cache-Control", blogCoverCache)
			}
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(body)
			}
		}
	})
}
