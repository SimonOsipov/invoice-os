package blog

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// lagos is Africa/Lagos, UTC+1 all year.
var lagos = time.FixedZone("WAT", 3600)

const PublicCoverPrefix = "/blog/covers/"

// PublicPost is the anonymous wire shape: author_name only, never the author id.
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

// LagosDate is the Africa/Lagos calendar date of now.
func LagosDate(now time.Time) string { return now.In(lagos).Format(time.DateOnly) }

// CoverPath is the public URL of a cover; version changes when the cover is replaced.
func CoverPath(id string, version int64) string {
	return PublicCoverPrefix + id + "/" + strconv.FormatInt(version, 10)
}

// visible is the one rule of which posts anyone may see; $1 is today's Lagos date.
const visible = `p.status = 'published' AND p.publish_date <= $1::date`

const coverVersion = `(extract(epoch FROM c.updated_at) * 1000000)::bigint`

const publicPostSelect = `SELECT p.id, p.category_id, to_char(p.publish_date, 'YYYY-MM-DD'), p.title, p.summary,
		p.author_name, p.featured, p.body, ` + coverVersion + `
	FROM public.blog_posts p LEFT JOIN public.blog_post_covers c ON c.post_id = p.id
	WHERE ` + visible

func scanPublicPost(row pgx.Row) (PublicPost, error) {
	var p PublicPost
	var version *int64
	err := row.Scan(&p.ID, &p.CategoryID, &p.PublishDate, &p.Title, &p.Summary, &p.AuthorName, &p.Featured, &p.Body, &version)
	if err != nil {
		return PublicPost{}, err
	}
	if version != nil {
		path := CoverPath(p.ID, *version)
		p.Cover = &path
	}
	return p, nil
}

// readPublic opens a READ ONLY transaction on the pool: an anonymous read has no caller to gate.
func (s *Store) readPublic(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) PublicCategories(ctx context.Context, today string) ([]Category, error) {
	out := []Category{}
	err := s.readPublic(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+categoryColumns+`
			FROM public.blog_categories c LEFT JOIN public.blog_posts p ON p.category_id = c.id AND `+visible+`
			GROUP BY c.id ORDER BY c.position`, today)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCategory(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) PublicPosts(ctx context.Context, today string) ([]PublicPost, error) {
	out := []PublicPost{}
	err := s.readPublic(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, publicPostSelect+` ORDER BY p.publish_date DESC, p.id`, today)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPublicPost(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) PublicPost(ctx context.Context, id, today string) (PublicPost, error) {
	var out PublicPost
	err := s.readPublic(ctx, func(tx pgx.Tx) (err error) {
		out, err = scanPublicPost(tx.QueryRow(ctx, publicPostSelect+` AND p.id = $2`, today, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicPost{}, ErrPostNotFound
	}
	return out, err
}

// PublicCover serves bytes only for the current version of a visible post's cover.
func (s *Store) PublicCover(ctx context.Context, id string, version int64, today string) (contentType string, b []byte, err error) {
	err = s.readPublic(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT c.content_type, c.bytes
			FROM public.blog_posts p JOIN public.blog_post_covers c ON c.post_id = p.id
			WHERE `+visible+` AND p.id = $2 AND `+coverVersion+` = $3`, today, id, version).Scan(&contentType, &b)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrPostNotFound
	}
	return contentType, b, err
}
