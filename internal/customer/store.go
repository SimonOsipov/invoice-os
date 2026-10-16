package customer

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const columns = `id, tin, name, contact_person, buyer_type, email, telephone, street, city, postal_zone, country, state, lga, status, created_at`

// Store runs as the invoice_app role; RLS scopes every query.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func scan(row pgx.Row) (Customer, error) {
	var c Customer
	err := row.Scan(&c.ID, &c.TIN, &c.Name, &c.ContactPerson, &c.BuyerType, &c.Email, &c.Telephone,
		&c.Street, &c.City, &c.PostalZone, &c.Country, &c.State, &c.LGA, &c.Status, &c.CreatedAt)
	return c, err
}

// List returns one page in name (NULLs last), tin order plus the total before paging.
func (s *Store) List(ctx context.Context, f ListFilter) ([]Customer, int, error) {
	items := []Customer{}
	var total int
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var conds []string
		var args []any
		if tin := strings.TrimSpace(f.TIN); tin != "" {
			args = append(args, tin)
			conds = append(conds, "tin = $"+itoa(len(args)))
		}
		if f.Status != "" {
			args = append(args, f.Status)
			conds = append(conds, "status = $"+itoa(len(args)))
		}
		where := ""
		if len(conds) > 0 {
			where = " WHERE " + strings.Join(conds, " AND ")
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM customers"+where, args...).Scan(&total); err != nil {
			return err
		}
		n := len(args)
		rows, err := tx.Query(ctx,
			"SELECT "+columns+" FROM customers"+where+
				" ORDER BY name ASC NULLS LAST, tin ASC LIMIT $"+itoa(n+1)+" OFFSET $"+itoa(n+2),
			append(args, f.Limit, f.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scan(rows)
			if err != nil {
				return err
			}
			items = append(items, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Update trims each sent field ("" stores NULL), records the sent names in edited_fields and
// writes the audit row in the same transaction.
func (s *Store) Update(ctx context.Context, id string, in UpdateInput) (Customer, error) {
	cols := []struct {
		name string
		val  *string
	}{
		{"name", in.Name}, {"contact_person", in.ContactPerson}, {"buyer_type", in.BuyerType},
		{"email", in.Email}, {"telephone", in.Telephone}, {"street", in.Street}, {"city", in.City},
		{"postal_zone", in.PostalZone}, {"country", in.Country}, {"state", in.State}, {"lga", in.LGA},
	}
	var sets []string
	var args []any
	sent := []string{}
	for _, c := range cols {
		if c.val == nil {
			continue
		}
		var v any
		if t := strings.TrimSpace(*c.val); t != "" {
			v = t
		}
		args = append(args, v)
		sets = append(sets, c.name+" = $"+itoa(len(args)))
		sent = append(sent, c.name)
	}
	if len(sent) == 0 {
		return Customer{}, errNoFields
	}
	args = append(args, sent, id)
	sets = append(sets, "edited_fields = ARRAY(SELECT DISTINCT unnest(edited_fields || $"+itoa(len(args)-1)+"::text[]) ORDER BY 1)")

	var out Customer
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		c, err := scan(tx.QueryRow(ctx,
			"UPDATE customers SET "+strings.Join(sets, ", ")+" WHERE id = $"+itoa(len(args))+"::text::uuid RETURNING "+columns,
			args...))
		if err != nil {
			var pgErr *pgconn.PgError
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				return ErrNotFound
			case errors.As(err, &pgErr) && pgErr.Code == "22P02":
				return ErrNotFound
			case errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.TableName == "customers":
				return errBuyerType
			}
			return err
		}
		out = c
		return audit.Record(ctx, tx, caller.Subject, "customers.customer.updated", map[string]any{
			"id": c.ID, "tin": c.TIN, "fields": sent,
		})
	})
	if err != nil {
		return Customer{}, err
	}
	return out, nil
}

// Create inserts an active record. Only non-blank fields are stored and marked in edited_fields,
// so the invoice fill can still fill the rest. The TIN format is left to the CHECK.
func (s *Store) Create(ctx context.Context, in CreateInput) (Customer, error) {
	cols := []struct {
		name string
		val  *string
	}{
		{"name", in.Fields.Name}, {"contact_person", in.Fields.ContactPerson}, {"buyer_type", in.Fields.BuyerType},
		{"email", in.Fields.Email}, {"telephone", in.Fields.Telephone}, {"street", in.Fields.Street},
		{"city", in.Fields.City}, {"postal_zone", in.Fields.PostalZone}, {"country", in.Fields.Country},
		{"state", in.Fields.State}, {"lga", in.Fields.LGA},
	}
	var out Customer
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		if in.TIN == nil || strings.TrimSpace(*in.TIN) == "" {
			return errTINRequired
		}
		names := []string{"tenant_id", "tin"}
		holders := []string{"$1::uuid", "$2"}
		args := []any{caller.TenantID, strings.TrimSpace(*in.TIN)}
		set := []string{}
		buyerType := ""
		for _, c := range cols {
			if c.val == nil {
				continue
			}
			t := strings.TrimSpace(*c.val)
			if t == "" {
				continue
			}
			set = append(set, c.name)
			if c.name == "buyer_type" {
				buyerType = t
				continue
			}
			args = append(args, t)
			names = append(names, c.name)
			holders = append(holders, "$"+itoa(len(args)))
		}
		sort.Strings(set)
		args = append(args, set)
		names = append(names, "edited_fields")
		holders = append(holders, "$"+itoa(len(args))+"::text[]")

		c, err := scan(tx.QueryRow(ctx,
			"INSERT INTO customers ("+strings.Join(names, ", ")+") VALUES ("+strings.Join(holders, ", ")+") RETURNING "+columns,
			args...))
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				switch {
				case pgErr.Code == "23505" && pgErr.ConstraintName == "customers_tenant_tin_uq":
					return ErrDuplicateTIN
				case pgErr.Code == "23514" && pgErr.TableName == "customers" && pgErr.ConstraintName == "customers_tin_check":
					return errTINFormat
				case pgErr.Code == "54000": // TIN past the unique index's row limit; no other column is indexed
					return errTINFormat
				}
			}
			return err
		}
		if buyerType != "" {
			// Set after the insert so a bad TIN is reported before a bad buyer_type (CHECKs fire in name order).
			c, err = scan(tx.QueryRow(ctx, "UPDATE customers SET buyer_type = $1 WHERE id = $2::uuid RETURNING "+columns, buyerType, c.ID))
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "customers_buyer_type_check" {
				return errBuyerType
			}
			if err != nil {
				return err
			}
		}
		out = c
		return audit.Record(ctx, tx, caller.Subject, "customers.customer.created", map[string]any{
			"id": c.ID, "tin": c.TIN, "fields": set,
		})
	})
	if err != nil {
		return Customer{}, err
	}
	return out, nil
}

// SetStatus moves a record to target. A move to the current status is ErrRedundantTransition,
// decided under the row lock before any write, so it changes nothing and writes no audit row.
func (s *Store) SetStatus(ctx context.Context, id, target string) (Customer, error) {
	var out Customer
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		var current string
		if err := tx.QueryRow(ctx, "SELECT status FROM customers WHERE id = $1::text::uuid FOR UPDATE", id).Scan(&current); err != nil {
			var pgErr *pgconn.PgError
			if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && pgErr.Code == "22P02") {
				return ErrNotFound
			}
			return err
		}
		if current == target {
			return ErrRedundantTransition
		}
		c, err := scan(tx.QueryRow(ctx, "UPDATE customers SET status = $1 WHERE id = $2::text::uuid RETURNING "+columns, target, id))
		if err != nil {
			return err
		}
		out = c
		if target == StatusArchived {
			return audit.Record(ctx, tx, caller.Subject, "customers.customer.archived", map[string]any{
				"id": c.ID, "from": current, "to": target,
			})
		}
		return audit.Record(ctx, tx, caller.Subject, "customers.customer.restored", map[string]any{
			"id": c.ID, "from": current, "to": target,
		})
	})
	if err != nil {
		return Customer{}, err
	}
	return out, nil
}

// LookupByTIN runs in the caller's tenant tx; nil, nil when no active record matches.
func LookupByTIN(ctx context.Context, tx pgx.Tx, tin string) (*Customer, error) {
	c, err := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM customers WHERE tin = $1 AND status = 'active'", strings.TrimSpace(tin)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func itoa(n int) string { return strconv.Itoa(n) }
