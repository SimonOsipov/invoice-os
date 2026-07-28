// Customer aggregation from a LIVE, entity-scoped invoice list — shared by the
// Customers and Reports views. Both callers now feed this the SAME already-fetched,
// entity-scoped InvoiceRecord[] (lib/invoices.ts's listInvoices, entity-scoped
// server-side since [entity-id-restored]; gateByActiveEntity's own doc comment covers
// what's still client-side) they use for everything else on their page
// (persona-handoff-fix step 3) — this used to aggregate the fabricated
// `active.invoices` overlay (Invoice[], types.ts), attributing invented buyers to real
// company names. buyer_tin/buyer_name are real invoice columns
// (migrations/20260714103137_invoices.sql:54-55); there is no separate customers
// table anywhere in the schema, so this aggregation IS the buyer master data, not an
// approximation of it.

import type { InvoiceRecord } from './invoices'

export type CustomerAgg = {
  name: string
  tin: string
  totalNum: number
  count: number
  last: string
  valid: boolean
}

// Matches the REAL backend's buyer-tin-format/supplier-tin-format rule verbatim
// (internal/importer/service_tin_test.go: `^[0-9]{8}-[0-9]{4}$`) — carried over
// unchanged from the mock version, since it turns out to be the identical shape.
const validTin = (t: string) => /^\d{8}-\d{4}$/.test(t)

export function aggregateCustomers(invoices: InvoiceRecord[]): CustomerAgg[] {
  const cm: Record<string, CustomerAgg> = {}
  invoices.forEach((i) => {
    // Group by TIN when the row has one — it's the domain's real identifier, and
    // buyer_name is free-text CSV input two DIFFERENT buyers can share (imported
    // spreadsheets aren't deduped against each other). Falls back to a name-keyed
    // bucket only when there's no TIN to key on at all; a null name additionally
    // merges every such row into one '—' bucket, a deliberate, minor simplification
    // (an import with genuinely no buyer identity has nothing more specific to key on).
    const tin = i.buyer_tin ?? ''
    const name = i.buyer_name ?? '—'
    const k = tin !== '' ? tin : `name:${name.toLowerCase()}`
    const amt = i.total != null ? Number(i.total) : 0
    // issue_date ?? created_at mirrors fmtDate's own fallback elsewhere (InvoicesList.tsx,
    // InvoiceDetail.tsx) — created_at is NOT NULL, so this is always a real string.
    const rowDate = i.issue_date ?? i.created_at
    if (!cm[k]) cm[k] = { name, tin, totalNum: 0, count: 0, last: rowDate, valid: true }
    const o = cm[k]
    o.count++
    o.totalNum += amt
    if (rowDate > o.last) o.last = rowDate
    if (!validTin(tin)) {
      o.valid = false
      o.tin = tin
    }
  })
  return Object.keys(cm)
    .map((k) => cm[k])
    .sort((a, b) => b.totalNum - a.totalNum)
}

export function initials(name: string): string {
  return name
    .replace(/[^A-Za-z ]/g, '')
    .split(' ')
    .filter(Boolean)
    .map((w) => w[0])
    .join('')
    .slice(0, 2)
    .toUpperCase()
}
