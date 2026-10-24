package customer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ErrBackfillPrivilegedRole refuses a SUPERUSER/BYPASSRLS connection: RLS would be inert.
var ErrBackfillPrivilegedRole = errors.New("customer: refusing to backfill over a SUPERUSER/BYPASSRLS role; run as invoice_app")

// SkippedInvoice is an invoice whose buyer TIN is not fileable; TIN is the raw buyer_tin.
type SkippedInvoice struct{ InvoiceNumber, TIN string }

// Collision is a TIN whose invoices carry more than one distinct name.
type Collision struct {
	TIN      string
	Names    []string // every distinct trimmed non-blank name, sorted
	Chosen   string   // newest name; for an existing record, its current name ("" when it has none)
	Existing bool     // the TIN already had a record, left as it was
}

// BackfillResult is what one Backfill run found; a dry run reports what a real run would do.
type BackfillResult struct {
	InvoicesRead int
	TINs         int // distinct fileable buyer TINs
	Created      int
	Existing     int // TINs - Created
	NoTIN        int // invoices with a NULL or blank buyer_tin
	Skipped      []SkippedInvoice
	Collisions   []Collision
}

// fileableTIN must match the pattern in fillSQL.
const fileableTIN = `^([0-9]{8}-[0-9]{4}|RN-[0-9]+)$`

// newest picks a column's newest non-blank value, as repeated fills would leave it.
func newest(col string) string {
	return "(array_agg(f." + col + " ORDER BY f.created_at DESC, f.id DESC) FILTER (WHERE f." + col + " IS NOT NULL))[1]"
}

// Column expressions mirror fillSQL.
var backfillAggSQL = `
WITH f AS (
	SELECT btrim(buyer_tin) AS tin, created_at, id,
		nullif(btrim(buyer_name), '') AS name,
		CASE WHEN btrim(invoice_kind) IN ('B2B','B2G','B2C') THEN btrim(invoice_kind) END AS buyer_type,
		nullif(btrim(buyer_email), '') AS email,
		nullif(btrim(buyer_telephone), '') AS telephone,
		nullif(btrim(buyer_street), '') AS street,
		nullif(btrim(buyer_city), '') AS city,
		nullif(btrim(buyer_postal_zone), '') AS postal_zone,
		nullif(btrim(buyer_country), '') AS country,
		nullif(btrim(buyer_state), '') AS state,
		nullif(btrim(buyer_lga), '') AS lga
	FROM invoices WHERE btrim(buyer_tin) ~ $1
)
SELECT f.tin, ` + newest("name") + `, ` + newest("buyer_type") + `, ` + newest("email") + `, ` +
	newest("telephone") + `, ` + newest("street") + `, ` + newest("city") + `, ` + newest("postal_zone") + `, ` +
	newest("country") + `, ` + newest("state") + `, ` + newest("lga") + `,
	coalesce(array_agg(DISTINCT f.name) FILTER (WHERE f.name IS NOT NULL), '{}'),
	c.id IS NOT NULL, c.name
FROM f LEFT JOIN customers c ON c.tin = f.tin
GROUP BY f.tin, c.id, c.name
ORDER BY f.tin`

const backfillInsertSQL = `
INSERT INTO customers (tenant_id, tin, name, buyer_type, email, telephone, street, city, postal_zone, country, state, lga)
VALUES (nullif(current_setting('app.current_tenant', true), '')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (tenant_id, tin) DO NOTHING`

type backfillRow struct {
	tin      string
	vals     [10]*string // name, buyer_type, email, telephone, street, city, postal_zone, country, state, lga
	names    []string
	exists   bool
	existing *string
}

// Backfill files a record for every fileable buyer TIN on the tenant's invoices. A TIN that
// already has a record is left untouched. It writes no audit row.
func Backfill(ctx context.Context, pool *pgxpool.Pool, tenantID string, dryRun bool) (BackfillResult, error) {
	var res BackfillResult
	err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var privileged bool
		if err := tx.QueryRow(ctx, `SELECT rolbypassrls OR rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&privileged); err != nil {
			return fmt.Errorf("customer: backfill role check: %w", err)
		}
		if privileged {
			return ErrBackfillPrivilegedRole
		}
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE nullif(btrim(buyer_tin), '') IS NULL) FROM invoices`).
			Scan(&res.InvoicesRead, &res.NoTIN); err != nil {
			return fmt.Errorf("customer: backfill count: %w", err)
		}

		srows, err := tx.Query(ctx, `SELECT invoice_number, buyer_tin FROM invoices
			WHERE nullif(btrim(buyer_tin), '') IS NOT NULL AND btrim(buyer_tin) !~ $1 ORDER BY invoice_number, id`, fileableTIN)
		if err != nil {
			return fmt.Errorf("customer: backfill skipped: %w", err)
		}
		for srows.Next() {
			var s SkippedInvoice
			if err := srows.Scan(&s.InvoiceNumber, &s.TIN); err != nil {
				srows.Close()
				return fmt.Errorf("customer: backfill skipped: %w", err)
			}
			res.Skipped = append(res.Skipped, s)
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return fmt.Errorf("customer: backfill skipped: %w", err)
		}

		rows, err := tx.Query(ctx, backfillAggSQL, fileableTIN)
		if err != nil {
			return fmt.Errorf("customer: backfill aggregate: %w", err)
		}
		var found []backfillRow
		for rows.Next() {
			var r backfillRow
			dest := []any{&r.tin}
			for i := range r.vals {
				dest = append(dest, &r.vals[i])
			}
			dest = append(dest, &r.names, &r.exists, &r.existing)
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return fmt.Errorf("customer: backfill aggregate: %w", err)
			}
			found = append(found, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("customer: backfill aggregate: %w", err)
		}

		res.TINs = len(found)
		for _, r := range found {
			if len(r.names) > 1 {
				sort.Strings(r.names)
				c := Collision{TIN: r.tin, Names: r.names, Existing: r.exists}
				switch {
				case r.exists && r.existing != nil:
					c.Chosen = *r.existing
				case !r.exists && r.vals[0] != nil:
					c.Chosen = *r.vals[0]
				}
				res.Collisions = append(res.Collisions, c)
			}
			if r.exists {
				continue
			}
			if dryRun {
				res.Created++
				continue
			}
			// ceiling: one INSERT per TIN; batch it if one tenant holds more than ~10k buyer TINs
			args := []any{r.tin}
			for _, v := range r.vals {
				args = append(args, v)
			}
			tag, err := tx.Exec(ctx, backfillInsertSQL, args...)
			if err != nil {
				return fmt.Errorf("customer: backfill insert %s: %w", r.tin, err)
			}
			res.Created += int(tag.RowsAffected())
		}
		res.Existing = res.TINs - res.Created
		return nil
	})
	if err != nil {
		return BackfillResult{}, err
	}
	return res, nil
}

// SeedDemo backfills every db.DemoTenants tenant at boot, except on the hosted posture,
// where it returns before touching pool. A failed tenant does not stop the others.
func SeedDemo(ctx context.Context, pool *pgxpool.Pool, deployEnv string, logger *slog.Logger) error {
	if platform.Posture(deployEnv) == platform.PostureHosted {
		return nil
	}
	var failed []string
	for _, tenantID := range db.DemoTenants {
		res, err := Backfill(ctx, pool, tenantID, false)
		if logger != nil {
			logger.Info("customer: demo backfill", "tenant", tenantID,
				"created", res.Created, "existing", res.Existing, "error", err)
		}
		if err != nil {
			failed = append(failed, tenantID)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("customer: demo backfill failed for %d of %d tenants: %s",
			len(failed), len(db.DemoTenants), strings.Join(failed, ", "))
	}
	return nil
}
