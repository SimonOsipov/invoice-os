package blogweb

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/klauspost/compress/gzhttp"
)

type Config struct {
	Root    string   // directory holding the built SPA
	Gateway *url.URL // public gateway origin
	Client  *http.Client
	Now     func() time.Time
	Log     *slog.Logger
}

// Base is the path the blog is served under; a twin of BLOG_BASE in frontend/landing/src/sitePaths.ts.
const Base = "/blog"

var idRe = regexp.MustCompile(`^[a-z0-9-]{1,80}$`)

type server struct {
	root    fs.FS
	index   []byte
	shell   Shell
	gateway string
	src     *Source
}

// New reads Root/index.html and returns the gzip-wrapped page server.
func New(cfg Config) (http.Handler, error) {
	index, err := os.ReadFile(filepath.Join(cfg.Root, "index.html"))
	if err != nil {
		return nil, err
	}
	shell, err := ParseShell(string(index))
	if err != nil {
		return nil, err
	}
	if cfg.Gateway == nil {
		return nil, errors.New("blogweb: no gateway URL")
	}
	s := &server{
		root:    os.DirFS(cfg.Root),
		index:   index,
		shell:   shell,
		gateway: cfg.Gateway.String(),
		src:     NewSource(cfg.Gateway, cfg.Client, cfg.Now, cfg.Log),
	}
	return gzhttp.GzipHandler(s), nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := stripBase(r.URL.Path)
	site := "https://" + r.Host + Base
	switch {
	case p == "/health":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("OK"))
		return
	case strings.HasSuffix(p, ".map"):
		// Cloud Run labels a typeless response text/html.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		return
	case p == "/robots.txt":
		s.write(w, r, http.StatusOK, "text/plain; charset=utf-8", Robots(site))
		return
	case p == "/sitemap.xml":
		snap, err := s.src.Get(r.Context())
		if err != nil {
			unavailable(w, r, nil)
			return
		}
		s.write(w, r, http.StatusOK, "application/xml; charset=utf-8", Sitemap(site, snap))
		return
	}
	if s.serveFile(w, r, p) {
		return
	}
	snap, err := s.src.Get(r.Context())
	if err != nil {
		unavailable(w, r, s.index)
		return
	}
	status, page := s.document(site, p, snap)
	s.write(w, r, status, "text/html; charset=utf-8", page)
}

// stripBase drops the base so the old hosts, which serve unprefixed paths, keep working. "/blogger" is a slug, not stripped.
func stripBase(p string) string {
	if p == Base {
		return "/"
	}
	if rest, ok := strings.CutPrefix(p, Base+"/"); ok {
		return "/" + rest
	}
	return p
}

// unavailable answers 503; a document gets the unmodified shell so the SPA still boots.
func unavailable(w http.ResponseWriter, r *http.Request, shell []byte) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Retry-After", "60")
	if shell != nil {
		h.Set("Content-Type", "text/html; charset=utf-8")
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	if r.Method == http.MethodGet {
		_, _ = w.Write(shell)
	}
}

func (s *server) write(w http.ResponseWriter, r *http.Request, status int, ctype string, body []byte) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

// serveFile serves a regular file under Root; it reports whether it answered.
func (s *server) serveFile(w http.ResponseWriter, r *http.Request, p string) bool {
	if p == "/" || strings.HasSuffix(p, "/") {
		return false
	}
	name := strings.TrimPrefix(path.Clean(p), "/")
	if !fs.ValidPath(name) {
		return false
	}
	if fi, err := fs.Stat(s.root, name); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	http.ServeFileFS(w, r, s.root, name)
	return true
}

// document picks the page for a path; the not-found page is the same for every miss.
func (s *server) document(origin, p string, snap *Snapshot) (int, []byte) {
	switch p {
	case "/":
		return http.StatusOK, s.shell.Home(origin, snap)
	case "/subscribe", "/subscribe/":
		return http.StatusOK, s.shell.Subscribe(origin)
	case "/subscribe/confirm", "/subscribe/confirm/":
		return http.StatusOK, s.shell.SubscribeConfirm(origin)
	}
	trimmed := strings.TrimSuffix(p, "/")
	if id, ok := strings.CutPrefix(trimmed, "/category/"); ok && idRe.MatchString(id) {
		for _, c := range snap.Categories {
			if c.ID == id {
				return http.StatusOK, s.shell.Category(origin, snap, c)
			}
		}
	} else if slug := strings.TrimPrefix(trimmed, "/"); !ok && idRe.MatchString(slug) {
		for _, post := range snap.Posts {
			if post.ID == slug {
				return http.StatusOK, s.shell.Post(origin, s.gateway, snap, post)
			}
		}
	}
	return http.StatusNotFound, s.shell.NotFound(origin, "/")
}
