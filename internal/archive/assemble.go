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
	// onStart, when non-nil, fires once with the bundle filename immediately
	// before the first byte reaches w (D-41) -- after selectEntity, the cap
	// check and resolveGeneratedBy, so the caller never arms response headers
	// before those can still fail. Nil is a no-op.
	onStart func(filename string)
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

	if o.onStart != nil {
		o.onStart(bundleFilename(entity.Name, r))
	}

	bw := newBundleWriter(w)

	if err := writeEntityStages(ctx, tx, bw, "", r); err != nil {
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

// prefixedBodies writes body files under dir.
type prefixedBodies struct {
	bw  *bundleWriter
	dir string
}

func (p prefixedBodies) WriteBody(name string, body []byte) error {
	return p.bw.WriteBody(p.dir+name, body)
}

// writeEntityStages writes one company's five CSVs and its body files under dir ("" for the bundle).
func writeEntityStages(ctx context.Context, tx pgx.Tx, bw *bundleWriter, dir string, r Request) error {
	invEntry := bw.newCSVEntry(dir + "invoices.csv")
	ids, err := selectInvoices(ctx, tx, r, invEntry)
	if err != nil {
		return err
	}
	if err := bw.finalizeCSV(invEntry); err != nil {
		return err
	}

	lineEntry := bw.newCSVEntry(dir + "line_items.csv")
	if err := selectLineItems(ctx, tx, ids, lineEntry); err != nil {
		return err
	}
	if err := bw.finalizeCSV(lineEntry); err != nil {
		return err
	}

	histEntry := bw.newCSVEntry(dir + "status_history.csv")
	if err := selectHistory(ctx, tx, ids, histEntry); err != nil {
		return err
	}
	if err := bw.finalizeCSV(histEntry); err != nil {
		return err
	}

	subEntry := bw.newCSVEntry(dir + "submissions.csv")
	if err := selectSubmissions(ctx, tx, ids, subEntry); err != nil {
		return err
	}
	if err := bw.finalizeCSV(subEntry); err != nil {
		return err
	}

	// Bodies stream to their own ZIP entries during this call, so bodies/*
	// physically precede exchange.csv (D-31).
	exEntry := bw.newCSVEntry(dir + "exchange.csv")
	if err := selectExchange(ctx, tx, ids, exEntry, prefixedBodies{bw: bw, dir: dir}); err != nil {
		return err
	}
	if err := bw.finalizeCSV(exEntry); err != nil {
		return err
	}
	return nil
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
