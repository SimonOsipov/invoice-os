// UBL 2.1 XML generator — was ported exactly from the prototype's `ublXml(inv)` method
// (Platform.dc.html ~L1033-1038); persona-handoff-fix step 3 rebuilds it over the REAL
// invoice record (InvoiceRecord, lib/invoices.ts) instead of the fabricated
// `active.invoices` overlay (Invoice, types.ts) — XmlModal.tsx is the only caller, and
// it now resolves a real invoice via getInvoice() rather than a mock cycled onto the
// selected company.
//
// This version ADDS an AccountingSupplierParty the mock generator never rendered at
// all (it only ever printed the buyer) — supplier_tin/supplier_name are real,
// per-invoice columns (migrations/20260714103137_invoices.sql:54-55), already rendered
// live elsewhere (InvoiceDetail.tsx:320-321), so this is reusing an established,
// already-trusted source, not inventing one. Closes the wrong-TIN hazard a document
// preview with no supplier identity at all would otherwise risk once a caller is wired
// back up: a fabricated supplier TIN printed on a fiscal document preview is exactly
// the class of bug this whole plan set out to fix.
//
// No `docType`/B2C branching: the real schema has no doc-type column at all
// (invoices.ts's InvoiceRecord), so every document renders as the one UBL invoice type
// code (380, "Commercial invoice") rather than guessing at a distinction the wire
// doesn't carry. `<cbc:Percent>` is now DERIVED from the invoice's own real vat/subtotal
// (never a hardcoded 7.5 assumed to apply universally) — Nigeria's standard VAT rate is
// 7.5%, but an exempt or partially-taxed invoice's real ratio can differ, and this is
// exactly the number that would tell a reader so.

import type { InvoiceRecord } from './invoices'

const CURRENCY_FALLBACK = 'NGN'

export function ublXml(inv: InvoiceRecord): string {
  const currency = inv.currency ?? CURRENCY_FALLBACK
  const sub = inv.subtotal != null ? Number(inv.subtotal) : 0
  const vat = inv.vat != null ? Number(inv.vat) : 0
  const total = inv.total != null ? Number(inv.total) : sub + vat
  const vatPercent = sub > 0 ? ((vat / sub) * 100).toFixed(1) : '0.0'
  const items = inv.line_items ?? []
  const lines = items
    .map(
      (it, i) =>
        '    <cac:InvoiceLine>\n      <cbc:ID>' +
        (i + 1) +
        '</cbc:ID>\n      <cbc:InvoicedQuantity unitCode="EA">' +
        (it.quantity ?? '0') +
        '</cbc:InvoicedQuantity>\n      <cbc:LineExtensionAmount currencyID="' +
        currency +
        '">' +
        (it.line_total ?? '0.00') +
        '</cbc:LineExtensionAmount>\n      <cac:Item><cbc:Name>' +
        (it.description ?? '') +
        '</cbc:Name></cac:Item>\n      <cac:Price><cbc:PriceAmount currencyID="' +
        currency +
        '">' +
        (it.unit_price ?? '0.00') +
        '</cbc:PriceAmount></cac:Price>\n    </cac:InvoiceLine>',
    )
    .join('\n')
  return (
    '<?xml version="1.0" encoding="UTF-8"?>\n<Invoice xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"\n         xmlns:cac="urn:oasis:names:...:CommonAggregateComponents-2"\n         xmlns:cbc="urn:oasis:names:...:CommonBasicComponents-2">\n  <cbc:CustomizationID>urn:peppol:bis:billing:3.0</cbc:CustomizationID>\n  <cbc:ProfileID>urn:fdc:firs.gov.ng:mbs:1.0</cbc:ProfileID>\n  <cbc:ID>' +
    inv.invoice_number +
    '</cbc:ID>\n  <cbc:IssueDate>' +
    (inv.issue_date ?? '') +
    '</cbc:IssueDate>\n  <cbc:InvoiceTypeCode>380</cbc:InvoiceTypeCode>\n  <cbc:DocumentCurrencyCode>' +
    currency +
    '</cbc:DocumentCurrencyCode>\n  <cac:AccountingSupplierParty>\n    <cac:Party>\n      <cac:PartyName><cbc:Name>' +
    (inv.supplier_name ?? '') +
    '</cbc:Name></cac:PartyName>\n      <cac:PartyTaxScheme><cbc:CompanyID>' +
    (inv.supplier_tin ?? '') +
    '</cbc:CompanyID></cac:PartyTaxScheme>\n    </cac:Party>\n  </cac:AccountingSupplierParty>\n  <cac:AccountingCustomerParty>\n    <cac:Party>\n      <cac:PartyName><cbc:Name>' +
    (inv.buyer_name ?? '') +
    '</cbc:Name></cac:PartyName>\n      <cac:PartyTaxScheme><cbc:CompanyID>' +
    (inv.buyer_tin ?? '') +
    '</cbc:CompanyID></cac:PartyTaxScheme>\n    </cac:Party>\n  </cac:AccountingCustomerParty>\n  <cac:TaxTotal>\n    <cbc:TaxAmount currencyID="' +
    currency +
    '">' +
    vat.toFixed(2) +
    '</cbc:TaxAmount>\n    <cac:TaxSubtotal>\n      <cbc:TaxableAmount currencyID="' +
    currency +
    '">' +
    sub.toFixed(2) +
    '</cbc:TaxableAmount>\n      <cbc:Percent>' +
    vatPercent +
    '</cbc:Percent>\n    </cac:TaxSubtotal>\n  </cac:TaxTotal>\n  <cac:LegalMonetaryTotal>\n    <cbc:LineExtensionAmount currencyID="' +
    currency +
    '">' +
    sub.toFixed(2) +
    '</cbc:LineExtensionAmount>\n    <cbc:TaxInclusiveAmount currencyID="' +
    currency +
    '">' +
    total.toFixed(2) +
    '</cbc:TaxInclusiveAmount>\n    <cbc:PayableAmount currencyID="' +
    currency +
    '">' +
    total.toFixed(2) +
    '</cbc:PayableAmount>\n  </cac:LegalMonetaryTotal>\n' +
    lines +
    '\n</Invoice>'
  )
}
