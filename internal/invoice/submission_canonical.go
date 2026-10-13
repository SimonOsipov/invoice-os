// This file is 03 (the Invoice context)'s own knowledge of how a
// Store.Get-hydrated invoice projects onto 05 (internal/submission)'s
// Canonical -- the type M6's real adapter is written against
// ([canonical-is-05-owned]). 03 imports 05; 05 imports NOTHING from 03
// ([mapper-lives-in-03]) -- reversed, M5-04's job-args import from 03 into
// 05 would close a cycle.
//
// Everything here is PURE: no DB, no HTTP, no clock -- mirrors payload.go's
// MBSPayload discipline exactly.
//
// inv MUST be Store.Get-hydrated. Store.Get orders LineItems by line_no
// (store.go:210-232); Store.List returns headers with LineItems left nil
// (store.go:331) -- a List-sourced invoice silently maps to zero lines, the
// same hazard MBSPayload's header already documents, and this mapper is
// equally unable to detect it (it is pure and has no way to tell "empty
// because List" from "empty because zero line items").
package invoice

import "github.com/SimonOsipov/invoice-os/internal/submission"

// SubmissionCanonical projects inv onto 05's Canonical
// ([canonical-is-invoice-content]). Carries invoice CONTENT only: no tenant
// id, no status, no violations -- Canonical has no such fields, so this is
// enforced at compile time, not by this function. Nil pointers pass through
// as nil, never coerced to "".
func SubmissionCanonical(inv Invoice) submission.Canonical {
	var lines []submission.CanonicalLine
	for _, li := range inv.LineItems {
		lines = append(lines, submission.CanonicalLine{
			LineID:      li.ID,
			LineNo:      li.LineNo,
			Description: li.Description,
			Quantity:    li.Quantity,
			UnitPrice:   li.UnitPrice,
			LineTotal:   li.LineTotal,
			LineTax:     li.LineTax,

			TaxCategory:               li.TaxCategory,
			HSNCode:                   li.HSNCode,
			ISICCode:                  li.ISICCode,
			ProductCategory:           li.ProductCategory,
			ServiceCategory:           li.ServiceCategory,
			SellersItemIdentification: li.SellersItemIdentification,
			PriceUnit:                 li.PriceUnit,
			TaxPercent:                li.TaxPercent,
			BaseQuantity:              li.BaseQuantity,
		})
	}

	return submission.Canonical{
		InvoiceID:     inv.ID,
		InvoiceNumber: inv.InvoiceNumber,
		IssueDate:     inv.IssueDate,
		Supplier: submission.Party{
			TIN:  inv.SupplierTIN,
			Name: inv.SupplierName,

			Email:      inv.SupplierEmail,
			Telephone:  inv.SupplierTelephone,
			Street:     inv.SupplierStreet,
			City:       inv.SupplierCity,
			PostalZone: inv.SupplierPostalZone,
			Country:    inv.SupplierCountry,
			State:      inv.SupplierState,
			LGA:        inv.SupplierLGA,
		},
		Buyer: submission.Party{
			TIN:  inv.BuyerTIN,
			Name: inv.BuyerName,

			Email:      inv.BuyerEmail,
			Telephone:  inv.BuyerTelephone,
			Street:     inv.BuyerStreet,
			City:       inv.BuyerCity,
			PostalZone: inv.BuyerPostalZone,
			Country:    inv.BuyerCountry,
			State:      inv.BuyerState,
			LGA:        inv.BuyerLGA,
		},
		Currency: inv.Currency,
		Subtotal: inv.Subtotal,
		VAT:      inv.VAT,
		Total:    inv.Total,
		Lines:    lines,

		InvoiceKind:     inv.InvoiceKind,
		TaxCurrencyCode: inv.TaxCurrencyCode,
		DueDate:         inv.DueDate,
		IssueTime:       inv.IssueTime,
		TaxPointDate:    inv.TaxPointDate,
		PaymentStatus:   inv.PaymentStatus,
		TaxSubtotals:    canonicalTaxSubtotals(taxSubtotals(inv.LineItems)),
	}
}

func canonicalTaxSubtotals(in []TaxSubtotal) []submission.TaxSubtotal {
	var out []submission.TaxSubtotal
	for _, t := range in {
		out = append(out, submission.TaxSubtotal{
			Category:      t.TaxCategory,
			Percent:       t.TaxPercent,
			TaxableAmount: t.TaxableAmount,
			TaxAmount:     t.TaxAmount,
		})
	}
	return out
}
