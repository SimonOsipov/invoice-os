package archive

import (
	"context"
	"fmt"
	"io"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
	"github.com/xuri/excelize/v2"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const registerSheet = "Invoice register"

// registerScope filters on the issue date, not created_at; a NULL issue_date never
// matches, and drafts are not part of a register.
const registerScope = "\n  FROM invoices WHERE entity_id = $1 AND issue_date >= $2::date AND issue_date <= $3::date AND status <> 'draft'"

const selectRegisterSQL = "SELECT " + invoicesColumns + registerScope + " ORDER BY issue_date, invoice_number, id"

const countRegisterSQL = "SELECT count(*)" + registerScope

// registerTxOptions: read-write, because the audit row commits with the read (DJ1).
var registerTxOptions = pgx.TxOptions{IsoLevel: pgx.RepeatableRead}

// CellTooLongError refuses a register holding a cell that XLSX would silently cut.
type CellTooLongError struct {
	InvoiceNumber string
	Column        string
}

func (e *CellTooLongError) Error() string {
	return fmt.Sprintf("archive: invoice %s: %s is longer than the %d characters an XLSX cell holds",
		e.InvoiceNumber, e.Column, excelize.TotalCellChars)
}

// xlsxRows adapts a stream writer to csvWriter. Every cell is a string (D3).
type xlsxRows struct {
	sw  *excelize.StreamWriter
	row int
}

func (x *xlsxRows) Write(record []string) error {
	cells := make([]any, len(record))
	for i, v := range record {
		if len(v) > excelize.TotalCellChars && len(utf16.Encode([]rune(v))) > excelize.TotalCellChars {
			e := &CellTooLongError{Column: invoicesCSVHeader[i]}
			if len(record) > 1 {
				e.InvoiceNumber = record[1]
			}
			return e
		}
		cells[i] = v
	}
	x.row++
	cell, err := excelize.CoordinatesToCellName(1, x.row)
	if err != nil {
		return err
	}
	return x.sw.SetRow(cell, cells)
}

type registerOpts struct {
	subject     string
	maxInvoices int
}

type registerAudit struct {
	EntityID string `json:"entity_id"`
	From     string `json:"from"`
	To       string `json:"to"`
	RowCount int    `json:"row_count"`
}

// buildRegister reads the client's register into a workbook and records the export on
// tx. The caller owns the returned file and closes it.
func buildRegister(ctx context.Context, tx pgx.Tx, r Request, o registerOpts) (*excelize.File, string, error) {
	if o.maxInvoices <= 0 {
		return nil, "", fmt.Errorf("archive: maxInvoices must be positive, got %d", o.maxInvoices)
	}

	entity, err := selectEntity(ctx, tx, r.EntityID)
	if err != nil {
		return nil, "", err
	}

	from, to := r.From.Format(time.DateOnly), r.To.Format(time.DateOnly)
	var count int
	if err := tx.QueryRow(ctx, countRegisterSQL, entity.ID, from, to).Scan(&count); err != nil {
		return nil, "", fmt.Errorf("archive: count register: %w", err)
	}
	if count > o.maxInvoices {
		return nil, "", &TooManyInvoicesError{Count: count, Limit: o.maxInvoices}
	}

	f := excelize.NewFile()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
		}
	}()
	if err := f.SetSheetName("Sheet1", registerSheet); err != nil {
		return nil, "", fmt.Errorf("archive: name register sheet: %w", err)
	}
	sw, err := f.NewStreamWriter(registerSheet)
	if err != nil {
		return nil, "", fmt.Errorf("archive: register stream writer: %w", err)
	}
	xw := &xlsxRows{sw: sw}
	if err := xw.Write(invoicesCSVHeader); err != nil {
		return nil, "", err
	}
	ids, err := writeInvoiceRows(ctx, tx, selectRegisterSQL, []any{entity.ID, from, to}, xw)
	if err != nil {
		return nil, "", err
	}
	if err := sw.Flush(); err != nil {
		return nil, "", fmt.Errorf("archive: flush register: %w", err)
	}

	if err := audit.Record(ctx, tx, o.subject, "invoice_register.exported", registerAudit{EntityID: entity.ID, From: from, To: to, RowCount: len(ids)}); err != nil {
		return nil, "", err
	}
	ok = true
	return f, registerFilename(entity.Name, r), nil
}

// registerFilename mirrors bundleFilename's slug and date form.
func registerFilename(entityName string, r Request) string {
	return "ASComply_invoice-register_" + entitySlug(entityName, r.EntityID) + "_" +
		r.From.UTC().Format("20060102") + "_" + r.To.UTC().Format("20060102") + ".xlsx"
}

// Register writes the client's invoice register to w. The workbook is built and the
// audit row committed before onStart fires and before the first byte (D7).
func (s *Store) Register(ctx context.Context, r Request, w io.Writer, onStart func(filename string)) error {
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return db.ErrNoTenant
	}
	var f *excelize.File
	var name string
	err := db.WithinRequestTenantTxOpts(ctx, s.pool, registerTxOptions, func(tx pgx.Tx) error {
		var err error
		f, name, err = buildRegister(ctx, tx, r, registerOpts{subject: id.Subject, maxInvoices: s.maxInvoices})
		return err
	})
	if f != nil {
		defer f.Close()
	}
	if err != nil {
		return err
	}
	if onStart != nil {
		onStart(name)
	}
	return f.Write(w)
}
