package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"
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

// registerTxOptions: read-write, because the audit row commits with the read.
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

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// xlsxRows adapts a stream writer to csvWriter. Every cell is a string.
type xlsxRows struct {
	sw  *excelize.StreamWriter
	row int
}

func (x *xlsxRows) Write(record []string) error {
	cells := make([]any, len(record))
	for i, v := range record {
		if len(v) > excelize.TotalCellChars && utf16Len(v) > excelize.TotalCellChars {
			e := &CellTooLongError{Column: invoicesCSVHeader[i]}
			switch {
			case i == 1 && len(record) > 0:
				e.InvoiceNumber = record[0]
			case len(record) > 1:
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
// audit row committed before onStart fires and before the first byte.
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

const xlsxContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// registerFn is Store.Register as RegisterHandler consumes it.
type registerFn func(ctx context.Context, r Request, w io.Writer, onStart func(filename string)) error

// parseRegisterRequest reads entity_id and two YYYY-MM-DD dates; a year below 1 is
// refused because the bundle's RFC3339 parser accepts year 0 and Postgres does not.
func parseRegisterRequest(query url.Values) (Request, string) {
	entityID := query.Get("entity_id")
	if entityID == "" {
		return Request{}, "entity_id is required"
	}
	if _, err := uuid.Parse(entityID); err != nil {
		return Request{}, "entity_id must be a well-formed uuid"
	}
	var dates [2]time.Time
	for i, name := range [2]string{"from", "to"} {
		raw := query.Get(name)
		if raw == "" {
			return Request{}, name + " is required"
		}
		d, err := time.Parse(time.DateOnly, raw)
		if err != nil || d.Year() < 1 {
			return Request{}, name + " must be a YYYY-MM-DD date"
		}
		dates[i] = d
	}
	if dates[0].After(dates[1]) {
		return Request{}, "from must not be after to"
	}
	return Request{EntityID: entityID, From: dates[0], To: dates[1]}, ""
}

// RegisterHandler returns GET /v1/invoice-register: identity, then parameters, then the store.
func RegisterHandler(register registerFn, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		req, msg := parseRegisterRequest(r.URL.Query())
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}

		sink := &bundleSink{w: w, contentType: xlsxContentType}
		err := register(r.Context(), req, sink, sink.setFilename)
		if err == nil {
			return
		}
		if sink.wrote {
			log.ErrorContext(r.Context(), "archive: register abandoned mid-stream", slog.Any("err", err))
			return
		}
		status, body := registerStatusForErr(err)
		if status == http.StatusInternalServerError {
			log.ErrorContext(r.Context(), "archive: register", slog.Any("err", err))
		}
		writeError(w, status, body)
	}
}

// registerStatusForErr words the cap and cell refusals for the register, else statusForErr.
func registerStatusForErr(err error) (int, string) {
	var tooMany *TooManyInvoicesError
	var tooLong *CellTooLongError
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	case errors.Is(err, db.ErrWorkspaceLocked):
		return http.StatusForbidden, db.WorkspaceLockedMessage
	case errors.As(err, &tooMany):
		return http.StatusBadRequest,
			fmt.Sprintf("%d invoices exceeds the register limit of %d", tooMany.Count, tooMany.Limit)
	case errors.As(err, &tooLong):
		return http.StatusBadRequest,
			fmt.Sprintf("invoice %s: %s is longer than the %d characters an XLSX cell holds",
				tooLong.InvoiceNumber, tooLong.Column, excelize.TotalCellChars)
	}
	return statusForErr(err)
}
