package blog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ErrDirection is a move direction other than "up" or "down".
var ErrDirection = errors.New("direction must be up or down")

// DeleteResult is the DeleteCategory answer; MoveTo is nil when no post moved.
type DeleteResult struct {
	MovedPosts int     `json:"moved_posts"`
	MoveTo     *string `json:"move_to"`
}

const categoryColumns = `c.id, c.name, c.headline, c.intro, c.icon, c.tone::int, count(p.id)::int`

const categoryList = `SELECT ` + categoryColumns + `
	FROM public.blog_categories c LEFT JOIN public.blog_posts p ON p.category_id = c.id
	GROUP BY c.id ORDER BY c.position`

func scanCategory(row pgx.Row) (Category, error) {
	var c Category
	err := row.Scan(&c.ID, &c.Name, &c.Headline, &c.Intro, &c.Icon, &c.Tone, &c.PostCount)
	return c, err
}

func listCategories(ctx context.Context, tx pgx.Tx) ([]Category, error) {
	rows, err := tx.Query(ctx, categoryList)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func getCategory(ctx context.Context, tx pgx.Tx, id string) (Category, error) {
	return scanCategory(tx.QueryRow(ctx, `SELECT `+categoryColumns+`
		FROM public.blog_categories c LEFT JOIN public.blog_posts p ON p.category_id = c.id
		WHERE c.id = $1 GROUP BY c.id`, id))
}

// lockCategory reads the row FOR UPDATE; an unknown id is ErrCategoryNotFound before any write.
func lockCategory(ctx context.Context, tx pgx.Tx, id string) (name string, position int, err error) {
	err = tx.QueryRow(ctx, `SELECT name, position FROM public.blog_categories WHERE id = $1 FOR UPDATE`, id).Scan(&name, &position)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrCategoryNotFound
	}
	return name, position, err
}

func (s *Store) ListCategories(ctx context.Context) ([]Category, error) {
	if _, err := db.StaffActor(ctx); err != nil {
		return nil, err
	}
	var out []Category
	err := db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = listCategories(ctx, tx)
		return err
	})
	return out, err
}

// AddCategory appends a category with an id slugged from its name.
func (s *Store) AddCategory(ctx context.Context, in CategoryInput) (Category, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return Category{}, err
	}
	in, fe := CheckCategory(in)
	if fe != nil {
		return Category{}, fe
	}
	var out Category
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockBlog(ctx, tx); err != nil {
			return err
		}
		var ids []string
		rows, err := tx.Query(ctx, `SELECT id FROM public.blog_categories`)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		taken := map[string]bool{}
		for _, id := range ids {
			taken[id] = true
		}
		id := UniqueID(Slug(in.Name), func(s string) bool { return taken[s] })
		if _, err := tx.Exec(ctx, `INSERT INTO public.blog_categories (id, name, headline, intro, icon, tone, position)
			VALUES ($1, $2, $3, $4, $5, $6, (SELECT coalesce(max(position), 0) + 1 FROM public.blog_categories))`,
			id, in.Name, in.Headline, in.Intro, in.Icon, in.Tone); err != nil {
			return mapSQLState(err)
		}
		if out, err = getCategory(ctx, tx, id); err != nil {
			return err
		}
		return recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: EventCategoryAdded, Payload: out})
	})
	if err != nil {
		return Category{}, err
	}
	return out, nil
}

// UpdateCategory rewrites a category's fields; its id never changes.
func (s *Store) UpdateCategory(ctx context.Context, id string, in CategoryInput) (Category, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return Category{}, err
	}
	in, fe := CheckCategory(in)
	if fe != nil {
		return Category{}, fe
	}
	var out Category
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockBlog(ctx, tx); err != nil {
			return err
		}
		if _, _, err := lockCategory(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE public.blog_categories SET name = $2, headline = $3, intro = $4, icon = $5, tone = $6 WHERE id = $1`,
			id, in.Name, in.Headline, in.Intro, in.Icon, in.Tone); err != nil {
			return mapSQLState(err)
		}
		if out, err = getCategory(ctx, tx, id); err != nil {
			return err
		}
		return recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: EventCategoryUpdated, Payload: out})
	})
	if err != nil {
		return Category{}, err
	}
	return out, nil
}

// MoveCategory swaps a category with its neighbour; at an edge it reports moved false and writes nothing (D-31).
func (s *Store) MoveCategory(ctx context.Context, id, direction string) (bool, []Category, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return false, nil, err
	}
	if direction != "up" && direction != "down" {
		return false, nil, ErrDirection
	}
	var moved bool
	var out []Category
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockBlog(ctx, tx); err != nil {
			return err
		}
		_, pos, err := lockCategory(ctx, tx, id)
		if err != nil {
			return err
		}
		neighbour := `SELECT id, position FROM public.blog_categories WHERE position < $1 ORDER BY position DESC LIMIT 1`
		if direction == "down" {
			neighbour = `SELECT id, position FROM public.blog_categories WHERE position > $1 ORDER BY position LIMIT 1`
		}
		var other string
		var otherPos int
		err = tx.QueryRow(ctx, neighbour, pos).Scan(&other, &otherPos)
		if err == nil {
			if _, err = tx.Exec(ctx, `UPDATE public.blog_categories SET position = CASE id WHEN $1 THEN $4::int ELSE $3::int END WHERE id IN ($1, $2)`,
				id, other, pos, otherPos); err != nil {
				return err
			}
			moved = true
			if err = recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: EventCategoryMoved,
				Payload: map[string]any{"id": id, "direction": direction, "swapped_with": other}}); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = listCategories(ctx, tx)
		return err
	})
	if err != nil {
		return false, nil, err
	}
	return moved, out, nil
}

// DeleteCategory moves the category's posts to moveTo and deletes it in one transaction.
func (s *Store) DeleteCategory(ctx context.Context, id string, moveTo *string) (DeleteResult, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return DeleteResult{}, err
	}
	var res DeleteResult
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockBlog(ctx, tx); err != nil {
			return err
		}
		name, _, err := lockCategory(ctx, tx, id)
		if err != nil {
			return err
		}
		var categories, posts int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.blog_categories)::int,
			(SELECT count(*) FROM public.blog_posts WHERE category_id = $1)::int`, id).Scan(&categories, &posts); err != nil {
			return err
		}
		exists := false
		if moveTo != nil && *moveTo != id {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.blog_categories WHERE id = $1)`, *moveTo).Scan(&exists); err != nil {
				return err
			}
		}
		if err := CheckCategoryDelete(categories, posts, moveTo, exists, id); err != nil {
			return err
		}
		res = DeleteResult{}
		if posts > 0 {
			if _, err := tx.Exec(ctx, `UPDATE public.blog_posts SET category_id = $2 WHERE category_id = $1`, id, *moveTo); err != nil {
				return err
			}
			res = DeleteResult{MovedPosts: posts, MoveTo: moveTo}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM public.blog_categories WHERE id = $1`, id); err != nil {
			return fmt.Errorf("blog: delete category: %w", err)
		}
		return recordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: EventCategoryDeleted,
			Payload: map[string]any{"id": id, "name": name, "move_to": res.MoveTo, "moved_posts": res.MovedPosts}})
	})
	if err != nil {
		return DeleteResult{}, err
	}
	return res, nil
}
