// UBL 2.1 XML generator ported exactly from the prototype's `ublXml(inv)` method
// (Platform.dc.html ~L1033-1038).

import { amount } from './format'
import type { Invoice } from '../types'

export function ublXml(inv: Invoice): string {
  const sub = amount(inv.items)
  const vat = sub * 0.075
  const total = sub + vat
  const typeCode = inv.docType === 'B2C' ? '384' : '380'
  const lines = inv.items
    .map(
      (it, i) =>
        '    <cac:InvoiceLine>\n      <cbc:ID>' +
        (i + 1) +
        '</cbc:ID>\n      <cbc:InvoicedQuantity unitCode="EA">' +
        it.qty +
        '</cbc:InvoicedQuantity>\n      <cbc:LineExtensionAmount currencyID="NGN">' +
        (it.qty * it.price).toFixed(2) +
        '</cbc:LineExtensionAmount>\n      <cac:Item><cbc:Name>' +
        it.desc +
        '</cbc:Name></cac:Item>\n      <cac:Price><cbc:PriceAmount currencyID="NGN">' +
        Number(it.price).toFixed(2) +
        '</cbc:PriceAmount></cac:Price>\n    </cac:InvoiceLine>',
    )
    .join('\n')
  return (
    '<?xml version="1.0" encoding="UTF-8"?>\n<Invoice xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"\n         xmlns:cac="urn:oasis:names:...:CommonAggregateComponents-2"\n         xmlns:cbc="urn:oasis:names:...:CommonBasicComponents-2">\n  <cbc:CustomizationID>urn:peppol:bis:billing:3.0</cbc:CustomizationID>\n  <cbc:ProfileID>urn:fdc:firs.gov.ng:mbs:1.0</cbc:ProfileID>\n  <cbc:ID>' +
    inv.number +
    '</cbc:ID>\n  <cbc:IssueDate>' +
    inv.date +
    '</cbc:IssueDate>\n  <cbc:InvoiceTypeCode>' +
    typeCode +
    '</cbc:InvoiceTypeCode>\n  <cbc:DocumentCurrencyCode>NGN</cbc:DocumentCurrencyCode>\n  <cac:AccountingCustomerParty>\n    <cac:Party>\n      <cac:PartyName><cbc:Name>' +
    inv.buyer +
    '</cbc:Name></cac:PartyName>\n      <cac:PartyTaxScheme><cbc:CompanyID>' +
    inv.buyerTin +
    '</cbc:CompanyID></cac:PartyTaxScheme>\n    </cac:Party>\n  </cac:AccountingCustomerParty>\n  <cac:TaxTotal>\n    <cbc:TaxAmount currencyID="NGN">' +
    vat.toFixed(2) +
    '</cbc:TaxAmount>\n    <cac:TaxSubtotal>\n      <cbc:TaxableAmount currencyID="NGN">' +
    sub.toFixed(2) +
    '</cbc:TaxableAmount>\n      <cbc:Percent>7.5</cbc:Percent>\n    </cac:TaxSubtotal>\n  </cac:TaxTotal>\n  <cac:LegalMonetaryTotal>\n    <cbc:LineExtensionAmount currencyID="NGN">' +
    sub.toFixed(2) +
    '</cbc:LineExtensionAmount>\n    <cbc:TaxInclusiveAmount currencyID="NGN">' +
    total.toFixed(2) +
    '</cbc:TaxInclusiveAmount>\n    <cbc:PayableAmount currencyID="NGN">' +
    total.toFixed(2) +
    '</cbc:PayableAmount>\n  </cac:LegalMonetaryTotal>\n' +
    lines +
    '\n</Invoice>'
  )
}
