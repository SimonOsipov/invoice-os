// Package publiccheck reads at most MaxInvoices invoices from one file and runs the global
// MBS rules on them with no tenant, no stored file and no job.
package publiccheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/airecord"
	"github.com/SimonOsipov/invoice-os/internal/platform/filesig"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

const (
	MaxFileBytes = 15 << 20 // the app's upload cap
	MaxInvoices  = 2
)

var (
	ErrTooLarge        = errors.New("publiccheck: file is over 15 MiB")
	ErrFormat          = errors.New("publiccheck: file is not csv, xlsx, pdf or docx")
	ErrBadFile         = errors.New("publiccheck: the file is empty or cannot be read")
	ErrReadUnavailable = errors.New("publiccheck: the document cannot be read now")
	ErrDocumentTooLong = errors.New("publiccheck: the document has too much text")
)

type Request struct {
	File      []byte
	Format    string            // csv | xlsx | pdf | docx
	HeaderRow int               // spreadsheet only; 0 means 1
	Mapping   map[string]string // spreadsheet only: field key -> header text
}

// Result carries each evaluated payload (amounts, buyer, supplier): callers must not log it.
type Result struct {
	Invoices      []Invoice
	InvoicesTotal int
	RowsTotal     int      // spreadsheet data rows; 0 for a document
	DocumentType  string   // a document's non-invoice verdict; "" otherwise
	NotChecked    []string // rule keys dropped because only the company profile fills them
	// BlankNumberRows lists the spreadsheet rows with no invoice number, as Service.Import reports them.
	BlankNumberRows []importer.RowError
}

type Invoice struct {
	Number     string
	Failures   []validation.Violation
	Payload    map[string]any // nil for a problem invoice
	NotChecked []string       // sorted rule keys dropped from this invoice's rule set; nil for a problem invoice
}

// Checker holds no logger on purpose: it handles the visitor's payload.
type Checker struct {
	LoadRules func(ctx context.Context, dates []string, custom []validation.Rule) (map[string]validation.RuleSet, error)
	Engine    *validation.Engine
	Mapper    importer.MappingSuggester
	Readers   extraction.CheckReaders
	Now       func() time.Time // nil means time.Now
}

// companyTargets are filled from the company profile in the app, never from a file.
var companyTargets = []string{"supplier.email", "supplier.telephone", "supplier.street", "supplier.city",
	"supplier.postal_zone", "supplier.country", "supplier.state", "supplier.lga"}

func spreadsheet(format string) bool { return format == "csv" || format == "xlsx" }

// prelude is the size and empty gate both entry points share.
func prelude(file []byte) error {
	if len(file) > MaxFileBytes {
		return ErrTooLarge
	}
	if len(file) == 0 {
		return ErrBadFile
	}
	return nil
}

func (c *Checker) Check(ctx context.Context, req Request) (Result, error) {
	if err := prelude(req.File); err != nil {
		return Result{}, err
	}
	doc := req.Format == "pdf" || req.Format == "docx"
	if !doc && !spreadsheet(req.Format) {
		return Result{}, ErrFormat
	}
	if !filesig.Matches(req.Format, req.File) {
		return Result{}, ErrFormat
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	actx := airecord.WithPublicCheck(ctx)

	var (
		sheet importer.CheckSheet
		res   Result
	)
	if doc {
		fields, docType, err := extraction.ReadForCheck(actx, c.Readers, req.File, req.Format)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			if errors.Is(err, extraction.ErrDocumentUnreadable) {
				return Result{}, fmt.Errorf("%w: %w", ErrBadFile, err)
			}
			if errors.Is(err, extraction.ErrCheckDocumentTooLong) {
				return Result{}, fmt.Errorf("%w: %w", ErrDocumentTooLong, err)
			}
			return Result{}, fmt.Errorf("%w: %w", ErrReadUnavailable, err)
		}
		inv, problem := importer.CheckDocumentInvoice(checkFields(fields))
		sheet = importer.CheckSheet{
			Invoices:      []importer.CheckSheetInvoice{{Invoice: inv, Problem: problem}},
			InvoicesTotal: 1,
		}
		if inv != nil {
			sheet.Invoices[0].Number = inv.InvoiceNumber
		}
		res.DocumentType = docType
	} else {
		var err error
		sheet, err = importer.CheckSpreadsheet(actx, req.File, req.Format, req.HeaderRow, req.Mapping, MaxInvoices)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			if errors.Is(err, importer.ErrValidation) {
				return Result{}, err
			}
			return Result{}, fmt.Errorf("%w: %w", ErrBadFile, err)
		}
	}
	res.InvoicesTotal, res.RowsTotal, res.BlankNumberRows = sheet.InvoicesTotal, sheet.RowsTotal, sheet.BlankNumberRows

	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	today := now().UTC().Format(time.DateOnly)

	res.Invoices = make([]Invoice, len(sheet.Invoices))
	dateOf := make([]string, len(sheet.Invoices))
	var distinct []string
	for i, it := range sheet.Invoices {
		if it.Problem != nil {
			res.Invoices[i] = Invoice{Number: it.Number, Failures: []validation.Violation{{
				Severity: "error", Path: it.Problem.Field, Message: it.Problem.Message,
			}}}
			continue
		}
		p, err := jsonShaped(invoice.MBSPayload(*it.Invoice))
		if err != nil {
			return Result{}, err
		}
		res.Invoices[i] = Invoice{Number: it.Number, Payload: p}
		dateOf[i] = validation.RuleSetDate(p, today)
		if !slices.Contains(distinct, dateOf[i]) {
			distinct = append(distinct, dateOf[i])
		}
	}
	if len(distinct) == 0 {
		return res, nil
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	loaded, err := c.LoadRules(ctx, distinct, nil)
	if err != nil {
		return Result{}, err
	}
	sets := make(map[string]validation.RuleSet, len(distinct))
	droppedBy := make(map[string][]string, len(distinct))
	for _, d := range distinct {
		rs, ok := loaded[d]
		if !ok {
			return Result{}, fmt.Errorf("publiccheck: no rule set loaded for %s", d)
		}
		rs, dropped := dropCompanyRules(rs)
		slices.Sort(dropped)
		droppedBy[d] = slices.Compact(dropped)
		res.NotChecked = append(res.NotChecked, droppedBy[d]...)
		sets[d] = rs
	}
	slices.Sort(res.NotChecked)
	res.NotChecked = slices.Compact(res.NotChecked)

	for i := range res.Invoices {
		if res.Invoices[i].Payload == nil {
			continue
		}
		out, err := c.Engine.Evaluate(validation.Payload{"invoice": res.Invoices[i].Payload}, sets[dateOf[i]])
		if err != nil {
			return Result{}, err
		}
		res.Invoices[i].NotChecked = droppedBy[dateOf[i]]
		res.Invoices[i].Failures = out.Violations
	}
	return res, nil
}

// jsonShaped round-trips p so the engine sees float64 numbers, as BatchValidateHandler's decoded body gives it.
func jsonShaped(p map[string]any) (map[string]any, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// dropCompanyRules returns a copy of rs without the rules on companyTargets, and the keys of the enabled ones.
func dropCompanyRules(rs validation.RuleSet) (validation.RuleSet, []string) {
	var dropped []string
	kept := make([]validation.Rule, 0, len(rs.Rules))
	for _, r := range rs.Rules {
		if slices.Contains(companyTargets, r.Target) {
			if r.Enabled { // a disabled rule would never have run
				dropped = append(dropped, r.Key)
			}
			continue
		}
		kept = append(kept, r)
	}
	rs.Rules = kept
	return rs, dropped
}

func (c *Checker) SuggestMapping(ctx context.Context, file []byte, format string) (importer.CheckSuggestion, error) {
	if err := prelude(file); err != nil {
		return importer.CheckSuggestion{}, err
	}
	if !spreadsheet(format) {
		return importer.CheckSuggestion{}, ErrFormat
	}
	if !filesig.Matches(format, file) {
		return importer.CheckSuggestion{}, ErrFormat
	}
	if err := ctx.Err(); err != nil {
		return importer.CheckSuggestion{}, err
	}
	s, err := importer.SuggestCheckMapping(airecord.WithPublicCheck(ctx), c.Mapper, file, format)
	if err != nil {
		if ctx.Err() != nil {
			return importer.CheckSuggestion{}, ctx.Err()
		}
		return importer.CheckSuggestion{}, fmt.Errorf("%w: %w", ErrBadFile, err)
	}
	return s, nil
}

// checkFields bridges extraction to importer, which may not import each other.
func checkFields(in []extraction.FieldResult) []importer.CheckField {
	out := make([]importer.CheckField, len(in))
	for i, f := range in {
		out[i] = importer.CheckField{Name: f.Name, Value: f.Value}
		if f.Reason != extraction.ReasonNone {
			r := string(f.Reason)
			out[i].Reason = &r
		}
	}
	return out
}
