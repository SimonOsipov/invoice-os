// Contract spec for GET /v1/invoices/{id}/ubl (BUG-04) -- the fleet's only non-JSON
// response, and until now the only invoice route with no deployed-gateway coverage at all.
// Nothing had proven the reverse proxy passes an XML body and its hardening headers through
// untouched (internal/gateway/gateway.go builds a bare httputil.ReverseProxy with no
// ModifyResponse), and the highest-value assertion here -- the detail payload's
// `ubl_blocked_reason` being byte-identical to this route's own 409 body -- spans two routes
// and could not live in either alone.
//
// A new contract-<family>.spec.ts rather than a describe in contract-invoice.spec.ts,
// matching this package's naming and contract-source-document.spec.ts's precedent for a
// per-feature file over an invoice-scoped route.
//
// rawFetch (client.ts:36-53) always res.json()s, so it can see neither an XML body nor any
// response header -- hence the local ublFetch seam below, exactly the case
// contract-source-document.spec.ts:67-69 documents for multipart.
import { test, expect } from '@playwright/test'
import { login, createEntity, createInvoice, getInvoice, rawFetch, apiBase, PERSONAS } from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope, type RawResult } from './contract-helpers'

// Own copy (repo convention -- no cross-suite imports between spec files), mirroring
// e2e/topology/invoice-surfaces.spec.ts's cleanInvoiceFields. UBL-complete: an issue date, a
// currency, a buyer name and one line item, with supplier_name entity-derived
// ([supplier-from-entity]) -- so ubl.Missing (internal/ubl/ubl.go) is empty.
function cleanInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

// Everything above except line_items, so ubl.Missing returns EXACTLY one entry and the
// refusal is a single ASCII clause. The other two omissions this seam might reach are
// unreachable in practice: Store.Create rejects a blank invoice number pre-tx and overwrites
// supplier_name from the entity.
function oneGapInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
  }
}

type UblResult = { status: number; headers: Headers; text: string; body: unknown }

// token=null omits Authorization (the 401 case).
function authHeaders(token: string | null): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {}
}

async function ublFetch(token: string | null, invoiceId: string): Promise<UblResult> {
  const res = await fetch(`${apiBase()}/api/invoice/v1/invoices/${invoiceId}/ubl`, {
    headers: authHeaders(token),
  })
  const text = await res.text()
  let body: unknown
  try {
    body = JSON.parse(text)
  } catch {
    body = undefined
  }
  return { status: res.status, headers: res.headers, text, body }
}

// The refusal bodies ARE plain JSON envelopes, so they go through the shared assertion; only
// the success body needs the raw text.
function asRaw(res: UblResult): RawResult {
  return { status: res.status, body: res.body }
}

// Every block of `tag` in xml, attribute-tolerant; no XML dependency in this package.
function sections(xml: string, tag: string): string[] {
  return [...xml.matchAll(new RegExp(`<${tag}(?:\\s[^>]*)?>([\\s\\S]*?)</${tag}>`, 'g'))].map((m) => m[1])
}

function section(xml: string, tag: string): string {
  const found = sections(xml, tag)
  expect(found.length, `exactly one <${tag}> block`).toBe(1)
  return found[0]
}

// Text of the single `tag` element in xml, whatever its attributes (currencyID).
function elementText(xml: string, tag: string): string {
  const found = [...xml.matchAll(new RegExp(`<${tag}(?:\\s[^>]*)?>([^<]*)</${tag}>`, 'g'))]
  expect(found.length, `exactly one <${tag}> element`).toBe(1)
  return found[0][1]
}

function detailFetch(token: string, invoiceId: string): Promise<RawResult> {
  return rawFetch(`/api/invoice/v1/invoices/${invoiceId}`, { headers: authHeaders(token) })
}

test.describe('invoice UBL contract (API E2E, over the deployed gateway)', () => {
  let tokenA: string
  let tokenB: string

  test.beforeAll(async () => {
    tokenA = await login(PERSONAS.A)
    tokenB = await login(PERSONAS.B)
  })

  test('ubl contract: a complete invoice serves the XML document, and the payload agrees', async () => {
    const entity = await createEntity(tokenA, { name: `BUG-04 ubl ok ${freshTin()}`, tin: freshTin() })
    const invoiceNumber = `INV-BUG04-API-${freshTin()}`
    const invoice = await createInvoice(tokenA, { entity_id: entity.id, ...cleanInvoiceFields(invoiceNumber) })

    const res = await ublFetch(tokenA, invoice.id)
    expect(res.status, 'a UBL-complete invoice must render').toBe(200)
    // Exact, not a prefix match. The charset is what stops a browser guessing an encoding,
    // and this is the one place the gateway's header passthrough is observable at all.
    expect(res.headers.get('content-type')).toBe('application/xml; charset=utf-8')
    expect(res.headers.get('x-content-type-options')).toBe('nosniff')
    // mime.FormatMediaType quotes the filename only when it has to; INV-... needs no quoting,
    // so a substring match is correct either way.
    expect(res.headers.get('content-disposition')).toContain(`${invoiceNumber}.xml`)

    expect(res.text.startsWith('<?xml version="1.0" encoding="UTF-8"?>'), 'the XML declaration must survive the proxy').toBe(
      true,
    )
    expect(res.text, 'the document must carry THIS invoice, not a template').toContain(
      `<cbc:ID>${invoiceNumber}</cbc:ID>`,
    )

    const detail = await detailFetch(tokenA, invoice.id)
    expect(detail.status).toBe(200)
    const body = detail.body as Record<string, unknown>
    expect(body.can_view_ubl, 'a UBL-complete invoice advertises the gate open').toBe(true)
    // Go's nil pointer with no `omitempty` marshals to an EXPLICIT null. A consumer must be
    // able to tell that apart from an absent key ([gates-on-the-wire]), so presence is
    // asserted alongside the value, not just falsiness.
    expect(
      'ubl_blocked_reason' in body && body.ubl_blocked_reason === null,
      'ubl_blocked_reason must be an explicit null, not omitted',
    ).toBe(true)
  })

  test("ubl contract: the served document carries this invoice's totals, tax lines, seller and buyer", async () => {
    const entity = await createEntity(tokenA, { name: `Zz TEST-04 ${freshTin()}`, tin: freshTin() })
    const created = await createInvoice(tokenA, {
      entity_id: entity.id,
      invoice_number: `INV-TEST04-UBL-${freshTin()}`,
      issue_date: '2026-01-01T00:00:00Z',
      supplier_tin: freshTin(),
      buyer_tin: freshTin(),
      buyer_name: `TEST-04 Buyer ${freshTin()}`,
      currency: 'NGN',
      subtotal: '1250.50',
      vat: '93.79',
      total: '1344.29',
      line_items: [
        { description: 'TEST-04 alpha', quantity: '10', unit_price: '100', line_total: '1000', line_tax: '75' },
        { description: 'TEST-04 beta', quantity: '1', unit_price: '250.50', line_total: '250.50', line_tax: '18.79' },
      ],
    })

    // The oracle: the live read, never the typed literals (money may come back normalised).
    const inv = await getInvoice(tokenA, created.id)
    const lines = [...(inv.line_items ?? [])].sort((a, b) => a.line_no - b.line_no)
    expect(lines.length, 'the GET hydrates both line items').toBe(2)

    const res = await ublFetch(tokenA, created.id)
    expect(res.status, 'a UBL-complete invoice must render').toBe(200)
    const xml = res.text

    const supplier = section(xml, 'cac:AccountingSupplierParty')
    expect(elementText(supplier, 'cbc:Name'), 'supplier name').toBe(inv.supplier_name)
    expect(elementText(supplier, 'cbc:CompanyID'), 'supplier TIN').toBe(inv.supplier_tin)
    const buyer = section(xml, 'cac:AccountingCustomerParty')
    expect(elementText(buyer, 'cbc:Name'), 'buyer name').toBe(inv.buyer_name)
    expect(elementText(buyer, 'cbc:CompanyID'), 'buyer TIN').toBe(inv.buyer_tin)

    const totals = section(xml, 'cac:LegalMonetaryTotal')
    expect(elementText(totals, 'cbc:TaxExclusiveAmount'), 'subtotal').toBe(inv.subtotal)
    expect(elementText(totals, 'cbc:PayableAmount'), 'total').toBe(inv.total)
    // Each line carries its own TaxTotal, so the document-level one is found before the first line.
    const head = xml.slice(0, xml.indexOf('<cac:InvoiceLine'))
    expect(elementText(section(head, 'cac:TaxTotal'), 'cbc:TaxAmount'), 'document VAT').toBe(inv.vat)

    const xmlLines = sections(xml, 'cac:InvoiceLine')
    expect(xmlLines.length, 'exactly two InvoiceLine blocks').toBe(2)
    for (const [i, li] of lines.entries()) {
      const block = xmlLines[i]
      expect(elementText(block, 'cbc:ID'), `line ${i + 1} is line_no ${li.line_no}`).toBe(String(li.line_no))
      expect(elementText(block, 'cbc:LineExtensionAmount'), `line_no ${li.line_no} line_total`).toBe(li.line_total)
      expect(elementText(section(block, 'cac:TaxTotal'), 'cbc:TaxAmount'), `line_no ${li.line_no} line_tax`).toBe(
        li.line_tax,
      )
    }
  })

  test('ubl contract: an incomplete invoice is refused with the SAME sentence the payload carries', async () => {
    const entity = await createEntity(tokenA, { name: `BUG-04 ubl gap ${freshTin()}`, tin: freshTin() })
    const invoiceNumber = `INV-BUG04-GAP-${freshTin()}`
    const invoice = await createInvoice(tokenA, { entity_id: entity.id, ...oneGapInvoiceFields(invoiceNumber) })

    const res = await ublFetch(tokenA, invoice.id)
    assertErrorEnvelope(asRaw(res), 409, 'a UBL-incomplete invoice')
    const reason = (res.body as { error: string }).error
    expect(reason, 'the one gap reachable through this seam').toContain('at least one line item')

    const detail = await detailFetch(tokenA, invoice.id)
    expect(detail.status).toBe(200)
    const body = detail.body as Record<string, unknown>
    expect(body.can_view_ubl, 'a UBL-incomplete invoice advertises the gate closed').toBe(false)
    // ublGate (internal/invoice/ubl.go) is the SINGLE derivation behind both, so these are
    // one string by construction and drift is the defect this pins. Both sides are read off
    // the wire, so the sentence's em dash is never typed here.
    expect(body.ubl_blocked_reason, 'the payload reason and the 409 body must be one string').toBe(reason)
  })

  test('ubl contract: 401, 400, and 404 for both unknown and cross-tenant ids', async () => {
    const entity = await createEntity(tokenA, { name: `BUG-04 ubl errs ${freshTin()}`, tin: freshTin() })
    const invoiceNumber = `INV-BUG04-ERR-${freshTin()}`
    const invoice = await createInvoice(tokenA, { entity_id: entity.id, ...cleanInvoiceFields(invoiceNumber) })

    // Positive control FIRST: this exact id 200s for its owner, so none of the refusals below
    // can come from a route that simply refuses everyone.
    const control = await ublFetch(tokenA, invoice.id)
    expect(control.status, "the owner's own read is still 200").toBe(200)

    assertErrorEnvelope(asRaw(await ublFetch(null, invoice.id)), 401, 'no auth')
    // A syntactically valid but RLS-invisible UUID, never a non-UUID string -- that would
    // raise Postgres 22P02 and mask the intended 404 (contract-portfolio.spec.ts's note).
    const unknown = await ublFetch(tokenA, crypto.randomUUID())
    const crossTenant = await ublFetch(tokenB, invoice.id)
    assertErrorEnvelope(asRaw(unknown), 404, 'an unknown invoice id')
    assertErrorEnvelope(asRaw(crossTenant), 404, "B reads A's invoice UBL")
    // Byte-identical envelopes: a cross-tenant id must not be distinguishable from an unknown
    // one, or the route becomes an existence oracle.
    expect(crossTenant.body).toEqual(unknown.body)

    assertErrorEnvelope(asRaw(await ublFetch(tokenA, 'not-a-uuid')), 400, 'malformed invoice id')
  })
})
