package blog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeFields is the one 422 envelope (D-38).
func writeFields(w http.ResponseWriter, fe *FieldErrors) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": fe.Summary, "fields": fe.Fields})
}

// mapError answers a store error; an unmapped one is logged and becomes a 500.
func mapError(w http.ResponseWriter, r *http.Request, log *slog.Logger, op string, err error) {
	var fe *FieldErrors
	switch {
	case errors.As(err, &fe):
		writeFields(w, fe)
	case errors.Is(err, db.ErrNotStaff):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, ErrPostNotFound), errors.Is(err, ErrCategoryNotFound), errors.Is(err, ErrCoverNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrCategoryNameTaken), errors.Is(err, ErrOnlyCategory):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrDirection):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		log.ErrorContext(r.Context(), "blog: "+op, slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// decodeBody reads one JSON value of at most limit bytes into v; it answers 413 or 400 itself.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if err = dec.Decode(&struct{}{}); errors.Is(err, io.EOF) {
			err = nil
		} else if err == nil {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func orDefault(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.Default()
	}
	return log
}

// postBody decodes and checks a PostInput; it answers 400, 413 or 422 itself.
func postBody(w http.ResponseWriter, r *http.Request) (PostInput, bool) {
	var in PostInput
	if !decodeBody(w, r, maxPostBodyBytes, &in) {
		return in, false
	}
	if in.Status != "draft" && in.Status != "published" {
		writeError(w, http.StatusBadRequest, "status must be draft or published")
		return in, false
	}
	if !blank(in.PublishDate) {
		if _, err := time.Parse(time.DateOnly, strings.TrimSpace(*in.PublishDate)); err != nil {
			writeError(w, http.StatusBadRequest, "publish_date must be YYYY-MM-DD")
			return in, false
		}
	}
	if err := ValidateBlocks(in.Body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return in, false
	}
	in, fe := CheckPost(in)
	if fe != nil {
		writeFields(w, fe)
		return in, false
	}
	return in, true
}

// categoryBody decodes and checks a CategoryInput; it answers 400, 413 or 422 itself.
func categoryBody(w http.ResponseWriter, r *http.Request) (CategoryInput, bool) {
	var in CategoryInput
	if !decodeBody(w, r, maxSmallBodyBytes, &in) {
		return in, false
	}
	in, fe := CheckCategory(in)
	if fe != nil {
		writeFields(w, fe)
		return in, false
	}
	return in, true
}

// ListCategoriesHandler serves GET /v1/staff/blog/categories.
func ListCategoriesHandler(list func(ctx context.Context) ([]Category, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		cats, err := list(r.Context())
		if err != nil {
			mapError(w, r, log, "list categories", err)
			return
		}
		if cats == nil {
			cats = []Category{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"categories": cats})
	}
}

// AddCategoryHandler serves POST /v1/staff/blog/categories.
func AddCategoryHandler(add func(ctx context.Context, in CategoryInput) (Category, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		in, ok := categoryBody(w, r)
		if !ok {
			return
		}
		c, err := add(r.Context(), in)
		if err != nil {
			mapError(w, r, log, "add category", err)
			return
		}
		writeJSON(w, http.StatusCreated, c)
	}
}

// UpdateCategoryHandler serves PUT /v1/staff/blog/categories/{id}.
func UpdateCategoryHandler(update func(ctx context.Context, id string, in CategoryInput) (Category, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		in, ok := categoryBody(w, r)
		if !ok {
			return
		}
		c, err := update(r.Context(), r.PathValue("id"), in)
		if err != nil {
			mapError(w, r, log, "update category", err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
}

// MoveCategoryHandler serves POST /v1/staff/blog/categories/{id}/move.
func MoveCategoryHandler(move func(ctx context.Context, id, direction string) (bool, []Category, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Direction string `json:"direction"`
		}
		if !decodeBody(w, r, maxSmallBodyBytes, &in) {
			return
		}
		if in.Direction != "up" && in.Direction != "down" {
			writeError(w, http.StatusBadRequest, ErrDirection.Error())
			return
		}
		moved, cats, err := move(r.Context(), r.PathValue("id"), in.Direction)
		if err != nil {
			mapError(w, r, log, "move category", err)
			return
		}
		if cats == nil {
			cats = []Category{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"moved": moved, "categories": cats})
	}
}

// DeleteCategoryHandler serves DELETE /v1/staff/blog/categories/{id}?move_to=<id>.
func DeleteCategoryHandler(del func(ctx context.Context, id string, moveTo *string) (DeleteResult, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		var moveTo *string
		if v := r.URL.Query().Get("move_to"); v != "" {
			moveTo = &v
		}
		res, err := del(r.Context(), r.PathValue("id"), moveTo)
		if err != nil {
			mapError(w, r, log, "delete category", err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// AuthorsHandler serves GET /v1/staff/blog/authors.
func AuthorsHandler(list func(ctx context.Context) ([]Author, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		authors, err := list(r.Context())
		if err != nil {
			mapError(w, r, log, "list authors", err)
			return
		}
		if authors == nil {
			authors = []Author{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"authors": authors})
	}
}

// ListPostsHandler serves GET /v1/staff/blog/posts.
func ListPostsHandler(list func(ctx context.Context) ([]Post, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		posts, err := list(r.Context())
		if err != nil {
			mapError(w, r, log, "list posts", err)
			return
		}
		if posts == nil {
			posts = []Post{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"posts": posts})
	}
}

// CreatePostHandler serves POST /v1/staff/blog/posts.
func CreatePostHandler(create func(ctx context.Context, in PostInput) (Post, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		in, ok := postBody(w, r)
		if !ok {
			return
		}
		p, err := create(r.Context(), in)
		if err != nil {
			mapError(w, r, log, "create post", err)
			return
		}
		writeJSON(w, http.StatusCreated, p)
	}
}

// UpdatePostHandler serves PUT /v1/staff/blog/posts/{id}.
func UpdatePostHandler(update func(ctx context.Context, id string, in PostInput) (Post, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		in, ok := postBody(w, r)
		if !ok {
			return
		}
		p, err := update(r.Context(), r.PathValue("id"), in)
		if err != nil {
			mapError(w, r, log, "update post", err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

// DeletePostHandler serves DELETE /v1/staff/blog/posts/{id}.
func DeletePostHandler(del func(ctx context.Context, id string) error, log *slog.Logger) http.HandlerFunc {
	return noContentHandler(del, "delete post", log)
}

// RemoveCoverHandler serves DELETE /v1/staff/blog/posts/{id}/cover.
func RemoveCoverHandler(remove func(ctx context.Context, id string) error, log *slog.Logger) http.HandlerFunc {
	return noContentHandler(remove, "remove cover", log)
}

func noContentHandler(do func(ctx context.Context, id string) error, op string, log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		if err := do(r.Context(), r.PathValue("id")); err != nil {
			mapError(w, r, log, op, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// SetFeaturedHandler serves PUT /v1/staff/blog/posts/{id}/featured.
func SetFeaturedHandler(set func(ctx context.Context, id string, featured bool) (FeaturedResult, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Featured *bool `json:"featured"`
		}
		if !decodeBody(w, r, maxSmallBodyBytes, &in) {
			return
		}
		if in.Featured == nil {
			writeError(w, http.StatusBadRequest, "featured is required")
			return
		}
		res, err := set(r.Context(), r.PathValue("id"), *in.Featured)
		if err != nil {
			mapError(w, r, log, "set featured", err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// SetCoverHandler serves PUT /v1/staff/blog/posts/{id}/cover; the body is the raw image.
func SetCoverHandler(set func(ctx context.Context, id, contentType string, bytes []byte) (Cover, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		// One byte past the cap tells "too large" from "exactly at the cap".
		data, err := io.ReadAll(io.LimitReader(r.Body, MaxCoverBytes+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if len(data) > MaxCoverBytes {
			writeError(w, http.StatusRequestEntityTooLarge, MsgCoverTooLarge)
			return
		}
		ct := http.DetectContentType(data)
		if ct != "image/jpeg" && ct != "image/png" {
			writeError(w, http.StatusUnsupportedMediaType, MsgCoverType)
			return
		}
		c, err := set(r.Context(), r.PathValue("id"), ct, data)
		if err != nil {
			mapError(w, r, log, "set cover", err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
}

// GetCoverHandler serves GET /v1/staff/blog/posts/{id}/cover: the stored bytes, never cached.
func GetCoverHandler(get func(ctx context.Context, id string) (string, []byte, error), log *slog.Logger) http.HandlerFunc {
	log = orDefault(log)
	return func(w http.ResponseWriter, r *http.Request) {
		ct, b, err := get(r.Context(), r.PathValue("id"))
		if err != nil {
			mapError(w, r, log, "get cover", err)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}
}
