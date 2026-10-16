// Package main implements backfill-customers: it files a customers record for
// every fileable buyer TIN already on a tenant's invoices (customer.Backfill).
// A TIN that already has a record is left as it is.
//
// Run with:
//
//	DATABASE_URL=<invoice_app DSN> \
//	  go run ./tools/backfill-customers --tenant <uuid> [--tenant <uuid> …] [--dry-run=false]
//
// --dry-run is true by default; pass --dry-run=false WITH the '='.
// The role must be invoice_app: a SUPERUSER or BYPASSRLS role is refused.
//
// Production Postgres has no public address. Two routes reach it:
//
//	A. SSH tunnel: ssh -N -L 15432:127.0.0.1:5432 <postgres-instance-id>@ssh.railway.com,
//	   then run the CLI on this machine with DATABASE_URL on 127.0.0.1:15432.
//	B. In-network: build a static linux binary, scp it into the Postgres
//	   container, and run it with `railway ssh --service Postgres -- …` as
//	   invoice_app over 127.0.0.1:5432.
//
// Not a service: no railway.json, no Dockerfile target.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/customer"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// tenantFlag collects a repeatable --tenant; each value is parsed as a uuid at
// flag-parse time, so a typo is fatal before any connection opens.
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
	flag.Var(&tenants, "tenant", "tenant uuid to backfill; repeat the flag for several")
	dryRun := flag.Bool("dry-run", true, "report without writing; pass --dry-run=false WITH the '=' (the space-separated form silently leaves it true)")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "backfill-customers: unexpected argument %q (flags after it were ignored; write --dry-run=false with '=')\n", flag.Arg(0))
		os.Exit(1)
	}

	if err := run(context.Background(), tenants, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "backfill-customers: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, tenants []string, dryRun bool) error {
	if len(tenants) == 0 {
		return errors.New("at least one --tenant is required")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required (the invoice_app DSN)")
	}
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	return runTenants(ctx, os.Stdout, os.Stderr, tenants, dryRun,
		func(ctx context.Context, t string, d bool) (customer.BackfillResult, error) {
			return customer.Backfill(ctx, pool, t, d)
		})
}

// runTenants backfills each tenant in turn; a tenant error does not stop the loop.
func runTenants(ctx context.Context, out, errOut io.Writer, tenants []string, dryRun bool,
	backfill func(context.Context, string, bool) (customer.BackfillResult, error)) error {
	mode := "DRY RUN (nothing will be written)"
	if !dryRun {
		mode = "WRITING"
	}
	fmt.Fprintf(out, "backfill-customers: %s, %d tenant(s)\n", mode, len(tenants))

	failed := false
	for _, tenantID := range tenants {
		res, err := backfill(ctx, tenantID, dryRun)
		if err != nil {
			fmt.Fprintf(errOut, "tenant %s: %v\n", tenantID, err)
			failed = true
			continue
		}
		report(out, tenantID, dryRun, res)
	}
	if failed {
		return errors.New("one or more tenants failed")
	}
	return nil
}

func report(w io.Writer, tenantID string, dryRun bool, res customer.BackfillResult) {
	created := "records created:    "
	if dryRun {
		created = "records to create:  "
	}
	fmt.Fprintf(w, "\ntenant %s\n", tenantID)
	fmt.Fprintf(w, "  invoices read:       %d\n", res.InvoicesRead)
	fmt.Fprintf(w, "  distinct TINs:       %d\n", res.TINs)
	fmt.Fprintf(w, "  %s %d\n", created, res.Created)
	fmt.Fprintf(w, "  records unchanged:   %d\n", res.Existing)
	fmt.Fprintf(w, "  invoices with no buyer TIN: %d\n", res.NoTIN)
	fmt.Fprintf(w, "  invoices skipped (TIN not fileable): %d\n", len(res.Skipped))
	for _, s := range res.Skipped {
		fmt.Fprintf(w, "    - %s  %q\n", s.InvoiceNumber, s.TIN)
	}
	fmt.Fprintf(w, "  TINs with more than one name: %d\n", len(res.Collisions))
	for _, c := range res.Collisions {
		names := make([]string, len(c.Names))
		for i, n := range c.Names {
			names[i] = fmt.Sprintf("%q", n)
		}
		verb := "->"
		if c.Existing {
			verb = "-> kept"
		}
		chosen := fmt.Sprintf("%q", c.Chosen)
		if c.Existing && c.Chosen == "" {
			chosen = "(record has no name)"
		}
		fmt.Fprintf(w, "    - %s: %s %s %s\n", c.TIN, strings.Join(names, " | "), verb, chosen)
	}
}
