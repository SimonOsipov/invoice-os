// Package main implements revalidate-invoices: it re-evaluates every
// status='validated' invoice against the active rule set and demotes any
// that now carry a blocking violation back to draft
// (internal/invoice.RevalidateActive) -- for an invoice validated before a
// stricter rule shipped and that now carries the violation the rule would
// catch.
//
// Run with:
//
//	DATABASE_URL=<invoice_app DSN> DATABASE_READER_URL=<invoice_tenant_reader DSN> \
//	VALIDATION_URL=… S2S_TOKEN=… \
//	  go run ./tools/revalidate-invoices [--all-tenants | --tenant <uuid> …] [--dry-run=false] [--verify]
//
// --all-tenants is the default when no --tenant is given; DATABASE_READER_URL
// is only required in that mode. --dry-run defaults to true -- pass
// --dry-run=false WITH the '=' (the space-separated form silently leaves it
// true, since flag.Bool consumes no following argument). --verify re-scans
// the selected tenants with no writes and exits non-zero if any validated
// invoice still carries a blocking violation; run it bare, so it covers every
// tenant.
//
// Against a deployed environment, run it under `railway run`. Not a service:
// no railway.json, no Dockerfile target.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// tenantFlag collects a repeatable --tenant. flag.String cannot repeat, so
// this is a flag.Value; each value is parsed as a uuid at flag-parse time, so
// a typo is fatal before any connection opens.
type tenantFlag []string

func (t *tenantFlag) String() string { return strings.Join(*t, ",") }

func (t *tenantFlag) Set(v string) error {
	if _, err := uuid.Parse(v); err != nil {
		return fmt.Errorf("not a uuid: %q", v)
	}
	*t = append(*t, v)
	return nil
}

func main() {
	var tenants tenantFlag
	flag.Var(&tenants, "tenant", "tenant uuid to revalidate; repeat the flag for several")
	allTenantsFlag := flag.Bool("all-tenants", false, "revalidate every tenant via the reader pool (the default when no --tenant is given)")
	dryRun := flag.Bool("dry-run", true, "report without writing; pass --dry-run=false WITH the '=' (the space-separated form silently leaves it true)")
	verify := flag.Bool("verify", false, "re-scan the selected tenants with no writes and exit non-zero if any validated invoice still carries a blocking violation")
	flag.Parse()

	allTenants := *allTenantsFlag || len(tenants) == 0

	if err := run(context.Background(), tenants, allTenants, *dryRun, *verify); err != nil {
		fmt.Fprintf(os.Stderr, "revalidate-invoices: %v\n", err)
		os.Exit(1)
	}
}

// run checks config in fail-fast order -- DATABASE_URL, then (only for
// --all-tenants) DATABASE_READER_URL, then the validation-service config --
// all BEFORE opening any pool or issuing any query.
func run(ctx context.Context, tenants []string, allTenants, dryRun, verify bool) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required (the invoice_app DSN)")
	}

	readerDSN := os.Getenv("DATABASE_READER_URL")
	if allTenants && readerDSN == "" {
		return errors.New("DATABASE_READER_URL is required for --all-tenants (the invoice_tenant_reader DSN)")
	}

	validationURL, s2sToken := os.Getenv("VALIDATION_URL"), os.Getenv("S2S_TOKEN")
	if validationURL == "" || s2sToken == "" {
		return errors.New("VALIDATION_URL and S2S_TOKEN are required")
	}

	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	tenantIDs := tenants
	if allTenants {
		reader, err := db.NewPool(ctx, readerDSN)
		if err != nil {
			return err
		}
		defer reader.Close()
		if tenantIDs, err = enumerateTenants(ctx, reader); err != nil {
			return err
		}
	}

	store := invoice.NewStore(pool)
	gate := invoice.NewGate(store, invoice.NewValidator(validationURL, s2sToken, nil))

	// --verify is a read-only re-scan whatever --dry-run says.
	writing := !dryRun && !verify
	mode := "DRY RUN (nothing will be written)"
	switch {
	case verify:
		mode = "VERIFY (read-only)"
	case writing:
		mode = "WRITING"
	}
	fmt.Printf("revalidate-invoices: %s, %d tenant(s)\n", mode, len(tenantIDs))

	var examined, demoted, clean, skipped int
	var failed, remaining []string
	for _, tenantID := range tenantIDs {
		res, err := invoice.RevalidateActive(ctx, pool, store, gate, tenantID, !writing)
		if err != nil {
			// One bad tenant never starves the rest; the aggregate error comes
			// after every tenant has been attempted.
			fmt.Fprintf(os.Stderr, "tenant %s: %v\n", tenantID, err)
			failed = append(failed, tenantID)
			continue
		}
		report(tenantID, res)
		examined += res.Examined
		demoted += res.Demoted
		clean += res.Clean
		skipped += res.Skipped
		if verify {
			remaining = append(remaining, res.Notes...)
		}
	}
	fmt.Printf("\ntotal: %d examined, %d demoted, %d clean, %d skipped\n", examined, demoted, clean, skipped)

	if len(failed) > 0 {
		return fmt.Errorf("%d of %d tenant(s) failed: %s", len(failed), len(tenantIDs), strings.Join(failed, ", "))
	}
	if verify && demoted > 0 {
		return fmt.Errorf("verify failed: %d validated invoice(s) still carry a blocking violation:\n  %s",
			demoted, strings.Join(remaining, "\n  "))
	}
	return nil
}

// enumerateTenants lists every tenant id via the reader pool
// (invoice_tenant_reader, no app.current_tenant GUC set) -- the
// tenant_enumerate policy ORs in every row for that role alone
// (reconciliation.enumerateTenants).
func enumerateTenants(ctx context.Context, reader *pgxpool.Pool) ([]string, error) {
	rows, err := reader.Query(ctx, `SELECT id FROM tenants ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("enumerate tenants: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan tenant id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("enumerate tenants rows: %w", err)
	}
	return ids, nil
}

func report(tenantID string, res invoice.RevalidateResult) {
	fmt.Printf("\ntenant %s\n", tenantID)
	fmt.Printf("  examined: %d, demoted: %d, clean: %d, skipped: %d\n",
		res.Examined, res.Demoted, res.Clean, res.Skipped)
	for _, n := range res.Notes {
		fmt.Printf("  - %s\n", n)
	}
}
