package invoice

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

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
	if strings.TrimSpace(in.InvoiceNumber) == "" || in.IssueDate == nil || len(in.Lines) == 0 {
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

	orig, err := s.Get(ctx, originalID)
	if err != nil {
		return Invoice{}, err
	}
	if err := noteRefusal(orig); err != nil {
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

	return s.Create(ctx, CreateInput{
		EntityID: orig.EntityID, InvoiceNumber: in.InvoiceNumber, IssueDate: in.IssueDate,
		DocumentType: in.DocumentType, OriginalInvoiceID: &orig.ID,
		BuyerTIN: orig.BuyerTIN, BuyerName: orig.BuyerName, Currency: orig.Currency,
		TaxCurrencyCode: orig.TaxCurrencyCode, InvoiceKind: orig.InvoiceKind,
		Subtotal: &amounts.Subtotal, VAT: &amounts.VAT, Total: &amounts.Total,
		SupplierEmail: orig.SupplierEmail, SupplierTelephone: orig.SupplierTelephone, SupplierStreet: orig.SupplierStreet,
		SupplierCity: orig.SupplierCity, SupplierPostalZone: orig.SupplierPostalZone, SupplierCountry: orig.SupplierCountry,
		SupplierState: orig.SupplierState, SupplierLGA: orig.SupplierLGA,
		BuyerEmail: orig.BuyerEmail, BuyerTelephone: orig.BuyerTelephone, BuyerStreet: orig.BuyerStreet,
		BuyerCity: orig.BuyerCity, BuyerPostalZone: orig.BuyerPostalZone, BuyerCountry: orig.BuyerCountry,
		BuyerState: orig.BuyerState, BuyerLGA: orig.BuyerLGA,
		LineItems: items,
	})
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

// Notes is a compile stub; the executor replaces it.
func (s *Store) Notes(ctx context.Context, originalID string) (NotesOnInvoice, error) {
	return NotesOnInvoice{}, ErrValidation
}
