package archive

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/actor"
)

// bundleTxOptions: one snapshot for every CSV (D-33), and the database refuses a
// write the assembler should never attempt.
var bundleTxOptions = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

// assembleOpts carries what Store.Assemble knows about the caller and the tx
// into the tx-scoped body. maxInvoices is a field here (not a package var, D-34)
// so the DB tests can set cap 2 and cap 3 in one test binary.
type assembleOpts struct {
	tenantID    string
	subject     string
	maxInvoices int
	now         time.Time
}

// TooManyInvoicesError refuses a bundle before the first byte (D-35). A struct,
// not a sentinel: subtask 08's 400 body names both numbers.
type TooManyInvoicesError struct {
	Count int
	Limit int
}

func (e *TooManyInvoicesError) Error() string {
	return fmt.Sprintf("archive: %d invoices exceeds the bundle limit of %d", e.Count, e.Limit)
}

// assemble drives subtasks 03-06 in entry order over one already-open tx (D-37).
// Nothing touches w until the entity resolves and the invoice count clears the
// cap (AC-2, AC-3): w is not referenced before newBundleWriter. bw.Close is
// never deferred -- every early return leaves the archive with no central
// directory (AC-7).
func assemble(ctx context.Context, tx pgx.Tx, r Request, w io.Writer, o assembleOpts) error {
	if o.maxInvoices <= 0 {
		return fmt.Errorf("archive: maxInvoices must be positive, got %d", o.maxInvoices)
	}

	entity, err := selectEntity(ctx, tx, r.EntityID)
	if err != nil {
		return err
	}

	count, err := countInvoices(ctx, tx, r)
	if err != nil {
		return err
	}
	if count > o.maxInvoices {
		return &TooManyInvoicesError{Count: count, Limit: o.maxInvoices}
	}

	generatedBy, err := resolveGeneratedBy(ctx, tx, o.subject)
	if err != nil {
		return err
	}

	bw := newBundleWriter(w)

	invEntry := bw.newCSVEntry("invoices.csv")
	ids, err := selectInvoices(ctx, tx, r, invEntry)
	if err != nil {
		return err
	}
	if err := bw.finalizeCSV(invEntry); err != nil {
		return err
	}

	histEntry := bw.newCSVEntry("status_history.csv")
	if err := selectHistory(ctx, tx, ids, histEntry); err != nil {
		return err
	}
	if err := bw.finalizeCSV(histEntry); err != nil {
		return err
	}

	subEntry := bw.newCSVEntry("submissions.csv")
	if err := selectSubmissions(ctx, tx, ids, subEntry); err != nil {
		return err
	}
	if err := bw.finalizeCSV(subEntry); err != nil {
		return err
	}

	// Bodies stream to their own ZIP entries during this call, so bodies/*
	// physically precede exchange.csv (D-31).
	exEntry := bw.newCSVEntry("exchange.csv")
	if err := selectExchange(ctx, tx, ids, exEntry, bw); err != nil {
		return err
	}
	if err := bw.finalizeCSV(exEntry); err != nil {
		return err
	}

	if err := bw.writeManifest(ManifestParams{
		TenantID:    o.tenantID,
		Entity:      entity,
		Request:     r,
		GeneratedBy: generatedBy,
		Now:         o.now,
	}); err != nil {
		return err
	}

	return bw.Close()
}

// resolveGeneratedBy names the acting user for manifest.json through the same
// actor.Resolve status_history.csv uses, inside the same tx (D-39).
func resolveGeneratedBy(ctx context.Context, tx pgx.Tx, subject string) (manifestActor, error) {
	labels, err := actor.Resolve(ctx, tx, []string{subject})
	if err != nil {
		return manifestActor{}, fmt.Errorf("archive: resolve generated_by: %w", err)
	}
	l := labels[subject]
	return manifestActor{Name: l.Text, Kind: string(l.Kind)}, nil
}
