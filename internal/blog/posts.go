package blog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// FeaturedResult is the SetFeatured answer; Unpinned is the post it displaced.
type FeaturedResult struct {
	ID       string  `json:"id"`
	Featured bool    `json:"featured"`
	Unpinned *string `json:"unpinned"`
}

const postSelect = `SELECT p.id, p.category_id, to_char(p.publish_date, 'YYYY-MM-DD'), p.title, p.summary,
		p.author_id::text, p.author_name, p.status, p.featured, p.body, p.created_at, p.updated_at,
		c.content_type, octet_length(c.bytes), c.updated_at
	FROM public.blog_posts p LEFT JOIN public.blog_post_covers c ON c.post_id = p.id`

func scanPost(row pgx.Row) (Post, error) {
	var p Post
	var ct *string
	var size *int
	var cu *time.Time
	err := row.Scan(&p.ID, &p.CategoryID, &p.PublishDate, &p.Title, &p.Summary, &p.AuthorID, &p.AuthorName,
		&p.Status, &p.Featured, &p.Body, &p.CreatedAt, &p.UpdatedAt, &ct, &size, &cu)
	if err != nil {
		return Post{}, err
	}
	if ct != nil {
		p.Cover = &Cover{ContentType: *ct, SizeBytes: *size, UpdatedAt: *cu}
	}
	return p, nil
}

func getPost(ctx context.Context, tx pgx.Tx, id string) (Post, error) {
	return scanPost(tx.QueryRow(ctx, postSelect+` WHERE p.id = $1`, id))
}

// lockPost reads the row FOR UPDATE; an unknown id is ErrPostNotFound before any write.
func lockPost(ctx context.Context, tx pgx.Tx, id string) (status string, featured bool, err error) {
	err = tx.QueryRow(ctx, `SELECT status, featured FROM public.blog_posts WHERE id = $1 FOR UPDATE`, id).Scan(&status, &featured)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrPostNotFound
	}
	return status, featured, err
}

// unpinOthers clears the pin of every post but id and returns the displaced id, if any.
func unpinOthers(ctx context.Context, tx pgx.Tx, id string) (*string, error) {
	var displaced *string
	err := tx.QueryRow(ctx, `UPDATE public.blog_posts SET featured = false WHERE featured AND id <> $1 RETURNING id`, id).Scan(&displaced)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return displaced, err
}

func recordPin(ctx context.Context, tx pgx.Tx, actor uuid.UUID, id string, displaced *string) error {
	if displaced != nil {
		if err := recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: EventPostUnpinned,
			Payload: map[string]any{"id": *displaced, "displaced_by": id}}); err != nil {
			return err
		}
	}
	return recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: EventPostPinned, Payload: map[string]any{"id": id}})
}

func (s *Store) ListPosts(ctx context.Context) ([]Post, error) {
	if _, err := db.StaffActor(ctx); err != nil {
		return nil, err
	}
	out := []Post{}
	err := db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, postSelect+` ORDER BY p.publish_date DESC NULLS LAST, p.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPost(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// Authors lists every active house member for the console picker; a blank name is nil.
func (s *Store) Authors(ctx context.Context) ([]Author, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return nil, err
	}
	out := []Author{}
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT user_id::text, display_name, email FROM public.staff_read_blog_authors($1)`, actor)
		if err != nil {
			return db.StaffReadError("blog: list authors", err)
		}
		defer rows.Close()
		for rows.Next() {
			var a Author
			if err := rows.Scan(&a.UserID, &a.DisplayName, &a.Email); err != nil {
				return err
			}
			out = append(out, a)
		}
		return db.StaffReadError("blog: list authors", rows.Err())
	})
	return out, err
}

func (s *Store) CreatePost(ctx context.Context, in PostInput) (Post, error) {
	return s.savePost(ctx, "", in)
}

func (s *Store) UpdatePost(ctx context.Context, id string, in PostInput) (Post, error) {
	return s.savePost(ctx, id, in)
}

// savePost creates (id "") or updates a post; an update never changes its id.
func (s *Store) savePost(ctx context.Context, id string, in PostInput) (Post, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return Post{}, err
	}
	in, fe := CheckPost(in)
	if fe != nil {
		return Post{}, fe
	}
	if in.Status == "" {
		in.Status = "draft"
	}
	for _, p := range []**string{&in.CategoryID, &in.PublishDate, &in.AuthorID} {
		if blank(*p) {
			*p = nil
		} else {
			v := strings.TrimSpace(**p)
			*p = &v
		}
	}
	body, err := json.Marshal(in.Body)
	if err != nil {
		return Post{}, err
	}
	var out Post
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockBlog(ctx, tx); err != nil {
			return err
		}
		was, wasFeatured := "", false
		if id != "" {
			if was, wasFeatured, err = lockPost(ctx, tx, id); err != nil {
				return err
			}
		}
		f := map[string]string{}
		summary := ""
		add := func(field, msg string) {
			f[field] = msg
			if summary == "" {
				summary = msg
			}
		}
		if in.CategoryID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.blog_categories WHERE id = $1)`, *in.CategoryID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				add("category_id", MsgCategory)
			}
		}
		var authorName *string
		if in.AuthorID != nil {
			var name *string
			author, perr := uuid.Parse(*in.AuthorID)
			found := false
			if perr == nil {
				err := tx.QueryRow(ctx, `SELECT display_name FROM public.staff_read_blog_authors($1) WHERE user_id = $2`, actor, author).Scan(&name)
				switch {
				case err == nil:
					found = true
				case !errors.Is(err, pgx.ErrNoRows):
					return db.StaffReadError("blog: check author", err)
				}
			}
			if !found {
				add("author_id", MsgAuthorNotStaff)
			} else {
				// uuid.Parse accepts forms ::uuid rejects (urn:uuid:, braces); store the canonical one.
				canon := author.String()
				in.AuthorID = &canon
				if authorName = name; authorName == nil {
					team := TeamAuthorName
					authorName = &team
				}
			}
		}
		if len(f) > 0 {
			return &FieldErrors{Fields: f, Summary: summary}
		}

		var displaced *string
		pin := in.Featured && !wasFeatured
		if id == "" {
			var taken []string
			rows, err := tx.Query(ctx, `SELECT id FROM public.blog_posts WHERE id = $1 OR id LIKE $1 || '-%'`, Slug(in.Title))
			if err != nil {
				return err
			}
			if taken, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
				return err
			}
			id = PostID(in.Title, func(c string) bool {
				for _, t := range taken {
					if t == c {
						return true
					}
				}
				return false
			})
		}
		if pin {
			if displaced, err = unpinOthers(ctx, tx, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO public.blog_posts (id, category_id, publish_date, title, summary, author_id, author_name, status, featured, body)
			VALUES ($1, $2, $3::text::date, $4, $5, $6::text::uuid, $7, $8, $9, $10::jsonb)
			ON CONFLICT (id) DO UPDATE SET category_id = EXCLUDED.category_id, publish_date = EXCLUDED.publish_date,
				title = EXCLUDED.title, summary = EXCLUDED.summary, author_id = EXCLUDED.author_id,
				author_name = EXCLUDED.author_name, status = EXCLUDED.status, featured = EXCLUDED.featured,
				body = EXCLUDED.body, updated_at = now()`,
			id, in.CategoryID, in.PublishDate, in.Title, in.Summary, in.AuthorID, authorName, in.Status, in.Featured, string(body)); err != nil {
			return err
		}
		if out, err = getPost(ctx, tx, id); err != nil {
			return err
		}
		if err := recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: SaveEvent(was, in.Status), Payload: out}); err != nil {
			return err
		}
		switch {
		case pin:
			return recordPin(ctx, tx, actor, id, displaced)
		case !in.Featured && wasFeatured:
			return recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: EventPostUnpinned,
				Payload: map[string]any{"id": id, "displaced_by": nil}})
		}
		return nil
	})
	if err != nil {
		return Post{}, err
	}
	return out, nil
}

func (s *Store) DeletePost(ctx context.Context, id string) error {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return err
	}
	return db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockBlog(ctx, tx); err != nil {
			return err
		}
		status, _, err := lockPost(ctx, tx, id)
		if err != nil {
			return err
		}
		var title string
		if err := tx.QueryRow(ctx, `DELETE FROM public.blog_posts WHERE id = $1 RETURNING title`, id).Scan(&title); err != nil {
			return err
		}
		return recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: EventPostDeleted,
			Payload: map[string]any{"id": id, "title": title, "status": status}})
	})
}

// SetFeatured pins or unpins a post; the same state writes nothing.
func (s *Store) SetFeatured(ctx context.Context, id string, featured bool) (FeaturedResult, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return FeaturedResult{}, err
	}
	res := FeaturedResult{ID: id, Featured: featured}
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockBlog(ctx, tx); err != nil {
			return err
		}
		_, was, err := lockPost(ctx, tx, id)
		if err != nil {
			return err
		}
		if was == featured {
			return nil
		}
		if featured {
			if res.Unpinned, err = unpinOthers(ctx, tx, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE public.blog_posts SET featured = $2 WHERE id = $1`, id, featured); err != nil {
			return err
		}
		if featured {
			return recordPin(ctx, tx, actor, id, res.Unpinned)
		}
		return recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: EventPostUnpinned,
			Payload: map[string]any{"id": id, "displaced_by": nil}})
	})
	if err != nil {
		return FeaturedResult{}, err
	}
	return res, nil
}

// SetCover inserts or replaces the post's cover; the caller has sniffed the type and capped the size.
func (s *Store) SetCover(ctx context.Context, id, contentType string, bytes []byte) (Cover, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return Cover{}, err
	}
	var out Cover
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockBlog(ctx, tx); err != nil {
			return err
		}
		if _, _, err := lockPost(ctx, tx, id); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO public.blog_post_covers (post_id, content_type, bytes) VALUES ($1, $2, $3)
			ON CONFLICT (post_id) DO UPDATE SET content_type = EXCLUDED.content_type, bytes = EXCLUDED.bytes, updated_at = now()
			RETURNING content_type, octet_length(bytes), updated_at`, id, contentType, bytes).
			Scan(&out.ContentType, &out.SizeBytes, &out.UpdatedAt); err != nil {
			return err
		}
		return recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: EventPostCoverSet,
			Payload: map[string]any{"id": id, "content_type": out.ContentType, "size_bytes": out.SizeBytes}})
	})
	if err != nil {
		return Cover{}, err
	}
	return out, nil
}

// RemoveCover deletes the cover; an absent cover writes nothing.
func (s *Store) RemoveCover(ctx context.Context, id string) error {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return err
	}
	return db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockBlog(ctx, tx); err != nil {
			return err
		}
		if _, _, err := lockPost(ctx, tx, id); err != nil {
			return err
		}
		var ct string
		err := tx.QueryRow(ctx, `DELETE FROM public.blog_post_covers WHERE post_id = $1 RETURNING content_type`, id).Scan(&ct)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: EventPostCoverRemoved,
			Payload: map[string]any{"id": id, "content_type": ct}})
	})
}

// GetCover returns the cover's content type and bytes; a read, so no lock and no audit row.
func (s *Store) GetCover(ctx context.Context, id string) (string, []byte, error) {
	if _, err := db.StaffActor(ctx); err != nil {
		return "", nil, err
	}
	var ct *string
	var b []byte
	err := db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT c.content_type, c.bytes FROM public.blog_posts p
			LEFT JOIN public.blog_post_covers c ON c.post_id = p.id WHERE p.id = $1`, id).Scan(&ct, &b)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrPostNotFound
	}
	if err != nil {
		return "", nil, err
	}
	if ct == nil {
		return "", nil, ErrCoverNotFound
	}
	return *ct, b, nil
}
