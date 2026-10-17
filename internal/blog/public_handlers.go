package blog

import (
	"context"
	"log/slog"
	"net/http"
)

func notImplemented(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }

// PublicCategoriesHandler serves GET /internal/blog/categories.
func PublicCategoriesHandler(_ func(ctx context.Context, today string) ([]Category, error), _ *slog.Logger) http.HandlerFunc {
	return notImplemented
}

// PublicPostsHandler serves GET /internal/blog/posts.
func PublicPostsHandler(_ func(ctx context.Context, today string) ([]PublicPost, error), _ *slog.Logger) http.HandlerFunc {
	return notImplemented
}

// PublicPostHandler serves GET /internal/blog/posts/{id}.
func PublicPostHandler(_ func(ctx context.Context, id, today string) (PublicPost, error), _ *slog.Logger) http.HandlerFunc {
	return notImplemented
}

// PublicCoverHandler serves GET /internal/blog/covers/{id}/{version}.
func PublicCoverHandler(_ func(ctx context.Context, id string, version int64, today string) (string, []byte, error), _ *slog.Logger) http.HandlerFunc {
	return notImplemented
}
