package blog

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Red-phase stub: the signatures are final, the bodies return zero values.

var lagos = time.FixedZone("WAT", 3600)

var errNotImplemented = errors.New("not implemented")

const PublicCoverPrefix = "/blog/covers/"

type PublicPost struct {
	ID          string  `json:"id"`
	CategoryID  string  `json:"category_id"`
	PublishDate string  `json:"publish_date"`
	Title       string  `json:"title"`
	Summary     string  `json:"summary"`
	AuthorName  string  `json:"author_name"`
	Featured    bool    `json:"featured"`
	Cover       *string `json:"cover"`
	Body        []Block `json:"body"`
}

func LagosDate(now time.Time) string { return "" }

func CoverPath(id string, version int64) string { return "" }

func (s *Store) readPublic(ctx context.Context, fn func(pgx.Tx) error) error {
	return errNotImplemented
}

func (s *Store) PublicCategories(ctx context.Context, today string) ([]Category, error) {
	return nil, errNotImplemented
}

func (s *Store) PublicPosts(ctx context.Context, today string) ([]PublicPost, error) {
	return nil, errNotImplemented
}

func (s *Store) PublicPost(ctx context.Context, id, today string) (PublicPost, error) {
	return PublicPost{}, errNotImplemented
}

func (s *Store) PublicCover(ctx context.Context, id string, version int64, today string) (contentType string, b []byte, err error) {
	return "", nil, errNotImplemented
}
