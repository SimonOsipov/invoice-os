// Command strandedruns lists the open approval runs nobody can decide, read-only across tenants.
// DATABASE_URL is the invoice_app DSN, read one tenant transaction at a time.
// DATABASE_READER_URL is the invoice_tenant_reader DSN, used to list the tenants.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/reconciliation"
)

func main() {
	defer platform.ReportBootPanic()
	execute(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
}

func execute(log *slog.Logger) {
	if err := run(context.Background(), os.Stdout); err != nil {
		platform.Fatal(log, "strandedruns: %v", err)
	}
}

func run(ctx context.Context, w io.Writer) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required (the invoice_app DSN)")
	}
	readerDSN := os.Getenv("DATABASE_READER_URL")
	if readerDSN == "" {
		return errors.New("DATABASE_READER_URL is required (the invoice_tenant_reader DSN)")
	}

	app, err := db.NewPool(ctx, dsn)
	if err != nil {
		return err
	}
	defer app.Close()
	reader, err := db.NewPool(ctx, readerDSN)
	if err != nil {
		return err
	}
	defer reader.Close()

	ids, err := reconciliation.EnumerateTenants(ctx, reader)
	if err != nil {
		return err
	}
	rows, err := approval.ListStranded(ctx, app, ids)
	if err != nil {
		return err
	}
	return report(w, rows)
}

// flat keeps customer text on one tab-separated line.
var flat = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ")

func cell(s string) string {
	if s == "" {
		return "-"
	}
	return flat.Replace(s)
}

func report(w io.Writer, rows []approval.StrandedRun) error {
	if len(rows) == 0 {
		_, err := io.WriteString(w, "no stranded runs\n")
		return err
	}
	var b strings.Builder
	b.WriteString("workspace_id\tworkspace\tinvoice_id\tinvoice\trole_key\trole_title\treason\tarmed_at\tblocks\n")
	workspaces := map[string]struct{}{}
	for _, r := range rows {
		workspaces[r.WorkspaceID] = struct{}{}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.WorkspaceID, flat.Replace(r.Workspace), r.InvoiceID, flat.Replace(r.InvoiceNumber),
			cell(r.RoleKey), cell(r.RoleTitle), r.Reason, r.ArmedAt.UTC().Format(time.RFC3339), r.Blocks)
	}
	fmt.Fprintf(&b, "%d stranded run(s) in %d workspace(s)\n", len(rows), len(workspaces))
	_, err := io.WriteString(w, b.String())
	return err
}
