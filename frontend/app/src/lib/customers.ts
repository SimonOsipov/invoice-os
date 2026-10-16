// Per-buyer invoice counts and totals, keyed by trimmed TIN (the fill files btrim(buyer_tin)).
// Shared by Customers (joined to the record) and Reports.

import type { InvoiceRecord } from './invoices'

export type CustomerAgg = {
  name: string
  tin: string
  totalNum: number
  count: number
  last: string
}

export function aggregateCustomers(invoices: InvoiceRecord[]): CustomerAgg[] {
  const cm: Record<string, CustomerAgg> = {}
  invoices.forEach((i) => {
    // Group by TIN; buyer_name is free text two different buyers can share. No TIN: bucket by name.
    const tin = (i.buyer_tin ?? '').trim()
    const name = i.buyer_name ?? '—'
    const k = tin !== '' ? tin : `name:${name.toLowerCase()}`
    const amt = i.total != null ? Number(i.total) : 0
    // issue_date ?? created_at mirrors fmtDate's own fallback elsewhere (InvoicesList.tsx,
    // InvoiceDetail.tsx) — created_at is NOT NULL, so this is always a real string.
    const rowDate = i.issue_date ?? i.created_at
    if (!cm[k]) cm[k] = { name, tin, totalNum: 0, count: 0, last: rowDate }
    const o = cm[k]
    o.count++
    o.totalNum += amt
    if (rowDate > o.last) o.last = rowDate
  })
  return Object.keys(cm)
    .map((k) => cm[k])
    .sort((a, b) => b.totalNum - a.totalNum)
}

// The record's name wins; else the invoice's; else a dash.
export function customerName(recordName: string | null | undefined, invoiceName: string | undefined): string {
  return recordName ?? invoiceName ?? '—'
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
