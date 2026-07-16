// Map-step logic, ported 1:1 from the prototype's `recognize` / `initMapping` /
// `groupInvoices` methods (Platform.dc.html ~L1369-1441).

import { CANON, FIELD_LABEL, FILE_DATA, HEADER_FIELDS } from '../data'
import type { HeaderConflict, InvoiceGroup, Mapping } from '../types'

// Header aliases that auto-place a column. `invoice_number` is deliberately
// absent: the fiscal identifier is never guessed — a plausible wrong default
// invites rubber-stamping, and this data is submitted under the firm's TIN.
const ALIAS: Record<string, string[]> = {
  issue_date: ['issuedate', 'date'],
  buyer_tin: ['buyertin'],
  currency: ['currency', 'ccy'],
  vat: ['vat'],
  total: ['total'],
  line_quantity: ['qty', 'quantity'],
  line_unit_price: ['unitprice'],
}

const norm = (s: string) => String(s).toLowerCase().replace(/[^a-z0-9]/g, '')

export function recognize(headers: string[]): Mapping {
  const res: Mapping = {}
  const used: Record<string, boolean> = {}
  CANON.forEach((c) => {
    res[c.key] = null
  })
  CANON.forEach((c) => {
    const al = ALIAS[c.key]
    if (!al) return
    const hit = headers.find((h) => al.indexOf(norm(h)) >= 0 && !used[h])
    if (hit) {
      res[c.key] = hit
      used[hit] = true
    }
  })
  return res
}

export function initMapping(fileId: string): Mapping {
  const rec = recognize(FILE_DATA[fileId].headers)
  const map: Mapping = {}
  CANON.forEach((c) => {
    map[c.key] = rec[c.key] || null
  })
  return map
}

// Group line-item rows into invoices by the column mapped to invoice_number.
// sheetRow is the row number the user sees in their spreadsheet (1-based, +1
// for the header row), so a conflict can cite where to look in the source file.
export function groupInvoices(fileId: string | null, mapping: Mapping | null): InvoiceGroup[] {
  const data = fileId ? FILE_DATA[fileId] : undefined
  if (!data || !mapping || !mapping.invoice_number) return []
  const col = mapping.invoice_number
  const order: string[] = []
  const groups: Record<string, { row: Record<string, string>; sheetRow: number }[]> = {}
  data.rows.forEach((r, i) => {
    const k = r[col]
    if (!groups[k]) {
      groups[k] = []
      order.push(k)
    }
    groups[k].push({ row: r, sheetRow: i + 2 })
  })
  return order.map((k) => {
    const lines = groups[k]
    const conflicts: HeaderConflict[] = []
    HEADER_FIELDS.forEach((f) => {
      const hc = mapping[f]
      if (!hc) return
      const vals = lines.map((l) => l.row[hc])
      if (new Set(vals).size > 1) {
        conflicts.push({ field: f, label: FIELD_LABEL[f], rows: lines.map((l) => l.sheetRow), values: Array.from(new Set(vals)) })
      }
    })
    const first = lines[0].row
    return {
      number: k,
      issueDate: mapping.issue_date ? first[mapping.issue_date] : null,
      buyer: mapping.buyer_name ? first[mapping.buyer_name] : null,
      total: mapping.total ? first[mapping.total] : null,
      lineCount: lines.length,
      sheetRows: lines.map((l) => l.sheetRow),
      conflicts,
      quarantined: conflicts.length > 0,
    }
  })
}
