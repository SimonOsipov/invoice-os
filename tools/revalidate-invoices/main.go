// Package main implements revalidate-invoices: it re-evaluates every
// status='validated' invoice against the active rule set and demotes any
// that now carry a blocking violation back to draft
// (internal/invoice.RevalidateActive) -- the fix for BUG-05, where an
// invoice validated before the buyer-tin-required rule shipped can still
// carry a missing buyer TIN.
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
// every tenant with no writes and exits non-zero if any validated invoice
// still carries a blocking violation.
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
	verify := flag.Bool("verify", false, "re-scan every tenant with no writes and exit non-zero if any validated invoice still carries a blocking violation")
	flag.Parse()

	allTenants := *allTenantsFlag || len(tenants) == 0

	if err := run(context.Background(), tenants, allTenants, *dryRun, *verify); err != nil {
		fmt.Fprintf(os.Stderr, "revalidate-invoices: %v\n", err)
		os.Exit(1)
	}
}

// run checks config in fail-fast order -- DATABASE_URL, then (only for
// --all-tenants) DATABASE_READER_URL, then the validation-service config --
// all BEFORE opening any pool or issuing any query. The per-tenant loop
// (RevalidateActive over --tenant or the reader-pool enumeration) and
// --verify's aggregate exit are task-412's Stage 2/3 GREEN work; this Stage
// 2.5 stub stops right after the checks.
func run(ctx context.Context, tenants []string, allTenants, dryRun, verify bool) error {
	if os.Getenv("DATABASE_URL") == "" {
		return errors.New("DATABASE_URL is required (the invoice_app DSN)")
	}

	if allTenants && os.Getenv("DATABASE_READER_URL") == "" {
		return errors.New("DATABASE_READER_URL is required for --all-tenants (the invoice_tenant_reader DSN)")
	}

	if os.Getenv("VALIDATION_URL") == "" || os.Getenv("S2S_TOKEN") == "" {
		return errors.New("VALIDATION_URL and S2S_TOKEN are required")
	}

	return errors.New("revalidate-invoices: not implemented")
}
