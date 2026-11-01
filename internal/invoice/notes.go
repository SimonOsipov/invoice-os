package invoice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

type NoteLineInput struct{ LineID, Quantity, UnitPrice string }

type NoteInput struct {
	DocumentType  submission.DocumentType
	InvoiceNumber string
	IssueDate     *time.Time
	Lines         []NoteLineInput
}

// noteRefusal answers why inv cannot start a note; nil when it can. The note clause comes first:
// an accepted note has an IRN.
func noteRefusal(inv Invoice) error {
	if inv.DocumentType != "" && inv.DocumentType != submission.CommercialInvoice {
		return ErrOriginalIsANote
	}
	if inv.Status != StatusAccepted || inv.IRN == nil {
		return ErrOriginalNotIssued
	}
	return nil
}

// IssueNote creates a draft credit or debit note from the chosen lines of an accepted invoice.
// The server computes every amount; it applies no cap against the original's quantities.
func (s *Store) IssueNote(ctx context.Context, originalID string, in NoteInput) (Invoice, error) {
	if in.DocumentType != submission.CreditNote && in.DocumentType != submission.DebitNote {
		return Invoice{}, fmt.Errorf("%w: document_type must be credit_note or debit_note", ErrValidation)
	}
	in.InvoiceNumber = strings.TrimSpace(in.InvoiceNumber)
	if in.InvoiceNumber == "" || in.IssueDate == nil || len(in.Lines) == 0 {
		return Invoice{}, fmt.Errorf("%w: a note needs a number, an issue date and lines", ErrValidation)
	}
	seen := make(map[string]bool, len(in.Lines))
	for _, l := range in.Lines {
		id := strings.ToLower(l.LineID)
		if seen[id] {
			return Invoice{}, fmt.Errorf("%w: a line is repeated", ErrValidation)
		}
		seen[id] = true
		if _, err := noteQuantity(l.Quantity); err != nil {
			return Invoice{}, err
		}
		if _, err := noteUnitPrice(l.UnitPrice); err != nil {
			return Invoice{}, err
		}
	}

	orig, err := s.NoteOriginal(ctx, originalID)
	if err != nil {
		return Invoice{}, err
	}

	byID := make(map[string]LineItem, len(orig.LineItems))
	for _, li := range orig.LineItems {
		byID[strings.ToLower(li.ID)] = li
	}
	type chosen struct {
		line LineItem
		in   NoteLineInput
	}
	picks := make([]chosen, 0, len(in.Lines))
	for _, l := range in.Lines {
		li, ok := byID[strings.ToLower(l.LineID)]
		if !ok {
			return Invoice{}, ErrUnknownLineID
		}
		picks = append(picks, chosen{li, l})
	}
	sort.SliceStable(picks, func(a, b int) bool { return picks[a].line.LineNo < picks[b].line.LineNo })

	money := make([]noteMoneyLine, len(picks))
	for i, p := range picks {
		money[i] = noteMoneyLine{p.in.Quantity, p.in.UnitPrice, p.line.TaxCategory, p.line.TaxPercent}
	}
	amounts, err := noteAmounts(money)
	if err != nil {
		return Invoice{}, err
	}

	items := make([]LineItemInput, len(picks))
	for i, p := range picks {
		qty, price, total := p.in.Quantity, p.in.UnitPrice, amounts.LineTotals[i]
		items[i] = LineItemInput{
			Description: p.line.Description, Quantity: &qty, UnitPrice: &price, LineTotal: &total, LineTax: amounts.LineTaxes[i],
			TaxCategory: p.line.TaxCategory, HSNCode: p.line.HSNCode, ISICCode: p.line.ISICCode,
			ProductCategory: p.line.ProductCategory, ServiceCategory: p.line.ServiceCategory,
			SellersItemIdentification: p.line.SellersItemIdentification, PriceUnit: p.line.PriceUnit,
			TaxPercent: p.line.TaxPercent, BaseQuantity: p.line.BaseQuantity,
		}
	}

	return s.Create(ctx, noteHeader(orig, CreateInput{
		InvoiceNumber: in.InvoiceNumber, IssueDate: in.IssueDate,
		DocumentType: in.DocumentType, OriginalInvoiceID: &orig.ID,
		Subtotal: &amounts.Subtotal, VAT: &amounts.VAT, Total: &amounts.Total,
		LineItems: items,
	}))
}

// noteHeader copies what a note takes from its original; IssueNote and the document import share it.
// The caller sets DocumentType and OriginalInvoiceID, and Currency when it read one.
func noteHeader(orig Invoice, in CreateInput) CreateInput {
	in.EntityID = orig.EntityID
	if in.Currency == nil || strings.TrimSpace(*in.Currency) == "" {
		in.Currency = orig.Currency
	}
	in.TaxCurrencyCode, in.InvoiceKind = orig.TaxCurrencyCode, orig.InvoiceKind
	in.BuyerTIN, in.BuyerName = orig.BuyerTIN, orig.BuyerName
	in.SupplierEmail, in.SupplierTelephone, in.SupplierStreet = orig.SupplierEmail, orig.SupplierTelephone, orig.SupplierStreet
	in.SupplierCity, in.SupplierPostalZone, in.SupplierCountry = orig.SupplierCity, orig.SupplierPostalZone, orig.SupplierCountry
	in.SupplierState, in.SupplierLGA = orig.SupplierState, orig.SupplierLGA
	in.BuyerEmail, in.BuyerTelephone, in.BuyerStreet = orig.BuyerEmail, orig.BuyerTelephone, orig.BuyerStreet
	in.BuyerCity, in.BuyerPostalZone, in.BuyerCountry = orig.BuyerCity, orig.BuyerPostalZone, orig.BuyerCountry
	in.BuyerState, in.BuyerLGA = orig.BuyerState, orig.BuyerLGA
	return in
}

// NoteOriginal reads originalID and applies noteRefusal.
func (s *Store) NoteOriginal(ctx context.Context, originalID string) (Invoice, error) {
	orig, err := s.Get(ctx, originalID)
	if err != nil {
		return Invoice{}, err
	}
	if err := noteRefusal(orig); err != nil {
		return Invoice{}, err
	}
	return orig, nil
}

// CreateDocumentNote files in as a note of originalID. in.EntityID must be the original's entity.
func (s *Store) CreateDocumentNote(ctx context.Context, originalID string, in CreateInput) (Invoice, error) {
	orig, err := s.NoteOriginal(ctx, originalID)
	if err != nil {
		return Invoice{}, err
	}
	if !strings.EqualFold(orig.EntityID, in.EntityID) {
		return Invoice{}, ErrOriginalOtherEntity
	}
	in.OriginalInvoiceID = &orig.ID
	return s.Create(ctx, noteHeader(orig, in))
}

// NoteRefusalMessage is the one owner of the three 409 sentences.
func NoteRefusalMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrOriginalIsANote):
		return "a note cannot be the original of a note", true
	case errors.Is(err, ErrOriginalOtherEntity):
		return "the original belongs to another client", true
	case errors.Is(err, ErrOriginalNotIssued):
		return "a note needs an accepted invoice with an IRN", true
	}
	return "", false
}

type NoteSummary struct {
	ID            string                  `json:"id"`
	DocumentType  submission.DocumentType `json:"document_type"`
	InvoiceNumber string                  `json:"invoice_number"`
	Status        Status                  `json:"status"`
	Total         *string                 `json:"total"`
}

type NotesOnInvoice struct {
	Notes           []NoteSummary `json:"notes"`
	CreditedSoFar   string        `json:"credited_so_far"`
	StillCreditable *string       `json:"still_creditable"`
}

// Notes lists the notes on originalID and what is still creditable. A missing or hidden original is ErrNotFound.
func (s *Store) Notes(ctx context.Context, originalID string) (NotesOnInvoice, error) {
	out := NotesOnInvoice{Notes: []NoteSummary{}}
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var total *string
		if err := tx.QueryRow(ctx, `SELECT total::text FROM invoices WHERE id = $1`, originalID).Scan(&total); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if pgCode(err) == "22P02" {
				return ErrValidation
			}
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT id::text, document_type, invoice_number, status, total::text
			   FROM invoices WHERE original_invoice_id = $1 ORDER BY created_at, id`, originalID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n NoteSummary
			if err := rows.Scan(&n.ID, &n.DocumentType, &n.InvoiceNumber, &n.Status, &n.Total); err != nil {
				return err
			}
			out.Notes = append(out.Notes, n)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		if out.CreditedSoFar, err = CreditedSoFarTx(ctx, tx, originalID, ""); err != nil {
			return err
		}
		if total != nil {
			t, err := decimal.NewFromString(*total)
			if err != nil {
				return err
			}
			c, err := decimal.NewFromString(out.CreditedSoFar)
			if err != nil {
				return err
			}
			left := t.Sub(c).StringFixed(2)
			out.StillCreditable = &left
		}
		return nil
	})
	if err != nil {
		return NotesOnInvoice{}, err
	}
	return out, nil
}
