// Package blogweb serves crawler-readable HTML for the blog SPA. It is a client of the
// gateway's public blog JSON and imports no other internal package.
package blogweb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	upstreamTimeout   = 5 * time.Second
	maxUpstreamBytes  = 8 << 20 // the gateway's maxBlogBodyBytes
	retryAfterFailure = 5 * time.Second
)

var ErrUnavailable = errors.New("blog data unavailable")

type Block struct {
	Type  string   `json:"type"`
	Text  string   `json:"text"`
	Items []string `json:"items"`
	Kind  string   `json:"kind"`
	Title string   `json:"title"`
	Who   string   `json:"who"`
	Demo  string   `json:"demo"`
}

type Post struct {
	ID          string  `json:"id"`
	CategoryID  string  `json:"category_id"`
	PublishDate string  `json:"publish_date"` // YYYY-MM-DD
	Title       string  `json:"title"`
	Summary     string  `json:"summary"`
	AuthorName  string  `json:"author_name"`
	Featured    bool    `json:"featured"`
	Cover       *string `json:"cover"` // "/blog/covers/<id>/<version>" on the gateway origin
	Body        []Block `json:"body"`
}

type Category struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Headline  string `json:"headline"`
	Intro     string `json:"intro"`
	PostCount int    `json:"post_count"`
}

type Snapshot struct {
	Categories []Category // gateway order = position order
	Posts      []Post     // gateway order = publish_date DESC, id
}

type categoriesEnvelope struct {
	Categories []Category `json:"categories"`
}

type postsEnvelope struct {
	Posts []Post `json:"posts"`
}

// Source holds one snapshot of the gateway's public blog JSON for the upstream max-age.
type Source struct {
	gateway *url.URL
	client  *http.Client
	now     func() time.Time
	log     *slog.Logger

	mu      sync.Mutex
	snap    *Snapshot
	expires time.Time
	retryAt time.Time
}

func NewSource(gateway *url.URL, client *http.Client, now func() time.Time, log *slog.Logger) *Source {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Source{gateway: gateway, client: &c, now: now, log: log}
}

// Get returns the held snapshot while it is fresh, else refetches. A failure drops the snapshot.
func (s *Source) Get(ctx context.Context) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.snap != nil && now.Before(s.expires) {
		return s.snap, nil
	}
	if now.Before(s.retryAt) {
		return nil, ErrUnavailable
	}
	var cats categoriesEnvelope
	var posts postsEnvelope
	catAge, err := s.fetch(ctx, "/blog/categories", &cats)
	var postAge time.Duration
	if err == nil {
		postAge, err = s.fetch(ctx, "/blog/posts", &posts)
	}
	if err != nil {
		s.snap, s.retryAt = nil, now.Add(retryAfterFailure)
		return nil, ErrUnavailable
	}
	s.snap = &Snapshot{Categories: cats.Categories, Posts: posts.Posts}
	s.expires = now.Add(min(catAge, postAge))
	return s.snap, nil
}

func (s *Source) fetch(ctx context.Context, path string, into any) (time.Duration, error) {
	status, age, err := s.fetchOnce(ctx, path, into)
	if err != nil {
		s.log.Warn("blogweb: upstream failed", "path", path, "status", status, "err", err)
	}
	return age, err
}

func (s *Source) fetchOnce(ctx context.Context, path string, into any) (int, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	u := *s.gateway
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, 0, errors.New("upstream status " + resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBytes+1))
	if err != nil {
		return resp.StatusCode, 0, err
	}
	if len(body) > maxUpstreamBytes {
		return resp.StatusCode, 0, errors.New("upstream body over the cap")
	}
	if err := json.Unmarshal(body, into); err != nil {
		return resp.StatusCode, 0, err
	}
	return resp.StatusCode, maxAge(resp.Header.Get("Cache-Control")), nil
}

// maxAge returns the max-age directive; a missing or unparseable one counts as 0.
func maxAge(cc string) time.Duration {
	for _, d := range strings.Split(cc, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(d), "max-age="); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return time.Duration(n) * time.Second
			}
		}
	}
	return 0
}
