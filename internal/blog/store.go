package blog

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

// recordStaff is a var so a test can force an audit failure.
var recordStaff = audit.RecordStaff

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// lockBlog serialises every blog write: slug, position, pin and delete-move choices (D-32).
func lockBlog(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('blog'))`)
	return err
}

// mapSQLState turns the category-name unique violation into ErrCategoryNameTaken.
func mapSQLState(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "blog_categories_name_key" {
		return ErrCategoryNameTaken
	}
	return err
}
