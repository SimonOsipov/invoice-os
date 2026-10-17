package blog

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// now is the clock the public handlers read; tests replace it.
var now = time.Now

// proxied reports a request carrying any X-User-ID header (a user-facing proxy added it).
// The match is case-insensitive so a non-canonical key is caught too.
func proxied(r *http.Request) bool {
	for k := range r.Header {
		if strings.EqualFold(k, "X-User-ID") {
			return true
		}
	}
	return false
}

// publicError answers every missing post or cover with the one 404 body.
func publicError(w http.ResponseWriter, r *http.Request, log *slog.Logger, op string, err error) {
	if errors.Is(err, ErrPostNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	log.ErrorContext(r.Context(), "blog: public "+op, slog.Any("err", err))
	writeError(w, http.StatusInternalServerError, "internal error")
}

// PublicCategoriesHandler serves GET /internal/blog/categories.
func PublicCategoriesHandler(list func(ctx context.Context, today string) ([]Category, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		if proxied(r) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		cats, err := list(r.Context(), LagosDate(now()))
		if err != nil {
			publicError(w, r, log, "categories", err)
			return
		}
		if cats == nil {
			cats = []Category{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"categories": cats})
	}
}

// PublicPostsHandler serves GET /internal/blog/posts.
func PublicPostsHandler(list func(ctx context.Context, today string) ([]PublicPost, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		if proxied(r) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		posts, err := list(r.Context(), LagosDate(now()))
		if err != nil {
			publicError(w, r, log, "posts", err)
			return
		}
		if posts == nil {
			posts = []PublicPost{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"posts": posts})
	}
}

// PublicPostHandler serves GET /internal/blog/posts/{id}.
func PublicPostHandler(get func(ctx context.Context, id, today string) (PublicPost, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		if proxied(r) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		p, err := get(r.Context(), r.PathValue("id"), LagosDate(now()))
		if err != nil {
			publicError(w, r, log, "post", err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

// PublicCoverHandler serves GET /internal/blog/covers/{id}/{version}.
func PublicCoverHandler(get func(ctx context.Context, id string, version int64, today string) (string, []byte, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		version, perr := strconv.ParseInt(r.PathValue("version"), 10, 64)
		if proxied(r) || perr != nil || version < 0 {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		ctype, data, err := get(r.Context(), r.PathValue("id"), version, LagosDate(now()))
		if err != nil {
			publicError(w, r, log, "cover", err)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	}
}
