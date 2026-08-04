// Package main implements backfill-source-rows: it recovers
// invoices.source_rows for invoices imported before the column existed, by
// reparsing each invoice's stored source document through the same decode the
// import path uses.
//
// Run with:
//
//	DATABASE_URL=<invoice_app DSN> \
//	DOCUMENT_BUCKET=… DOCUMENT_ENDPOINT=… DOCUMENT_REGION=… \
//	DOCUMENT_ACCESS_KEY_ID=… DOCUMENT_SECRET_ACCESS_KEY=… \
//	  go run ./tools/backfill-source-rows --tenant <uuid> [--tenant <uuid> …] [--dry-run=false]
//
// Against a deployed environment, run it under `railway run`. Not a service:
// no railway.json, no Dockerfile target.
//
// Anything ambiguous writes nothing and is reported instead: a tied or
// partially-covering invoice-number column, an invoice number shared within
// one document, or a row count that disagrees with the stored line-item
// count. A document that will not open or decode is reported and skipped.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/importer"
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
	flag.Var(&tenants, "tenant", "tenant uuid to backfill; repeat the flag for several")
	dryRun := flag.Bool("dry-run", true, "report without writing; pass --dry-run=false WITH the '=' (the space-separated form silently leaves it true)")
	flag.Parse()

	if len(tenants) == 0 {
		fmt.Fprintln(os.Stderr, "backfill-source-rows: at least one --tenant is required")
		os.Exit(1)
	}

	if err := run(context.Background(), tenants, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "backfill-source-rows: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, tenants []string, dryRun bool) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required (the invoice_app DSN)")
	}
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	cfg, err := document.ConfigFromEnv()
	if err != nil {
		return err
	}
	objects, err := document.NewS3Store(cfg, nil)
	if err != nil {
		return err
	}
	docSvc := document.NewService(document.NewStore(pool), objects)

	mode := "DRY RUN (nothing will be written)"
	if !dryRun {
		mode = "WRITING"
	}
	fmt.Printf("backfill-source-rows: %s, %d tenant(s)\n", mode, len(tenants))

	failed := false
	for _, tenantID := range tenants {
		res, err := importer.BackfillSourceRows(ctx, pool, docSvc.Open, tenantID, dryRun)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tenant %s: %v\n", tenantID, err)
			failed = true
			continue
		}
		report(tenantID, res)
	}
	// Skipped and ambiguous documents are an outcome, not a failure; only a
	// tenant-level error is.
	if failed {
		return errors.New("one or more tenants failed")
	}
	return nil
}

func report(tenantID string, res importer.BackfillResult) {
	fmt.Printf("\ntenant %s\n", tenantID)
	fmt.Printf("  documents: %d scanned, %d skipped, %d ambiguous\n",
		res.DocumentsScanned, res.DocumentsSkipped, res.DocumentsAmbiguous)
	fmt.Printf("  invoices:  %d recoverable, %d written, %d ambiguous\n",
		res.InvoicesRecoverable, res.InvoicesWritten, res.InvoicesAmbiguous)
	for _, n := range res.Notes {
		fmt.Printf("  - %s\n", n)
	}
}
