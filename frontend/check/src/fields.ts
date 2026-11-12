// The one seam to app source (only these four modules); the check never imports app components.
import { ACCEPTED_PICKED_TYPES, classifyPickedFile, MAX_UPLOAD_BYTES, previewColumns } from '../../app/src/lib/importFlow'
import { canSubmitMapping, toImportMapping } from '../../app/src/lib/mapping'
import { IMPORT_KEYS, INVOICE_FIELDS, labelOf } from '../../app/src/lib/invoiceFields'
import { formatBytes } from '../../app/src/lib/sourceDocument'
import type { Mapping } from '../../app/src/types'

export { ACCEPTED_PICKED_TYPES, classifyPickedFile, MAX_UPLOAD_BYTES, previewColumns, canSubmitMapping, toImportMapping, IMPORT_KEYS, labelOf, formatBytes }
export type { Mapping }

export const ACCEPT = ACCEPTED_PICKED_TYPES.map((r) => r.ext).join(',')

// Order of Go importer.CheckMappingKeys.
export const CHECK_FIELD_KEYS: readonly string[] = [...IMPORT_KEYS, 'supplier_name', 'supplier_tin']

export function matchedCount(fields: Mapping): number {
  return CHECK_FIELD_KEYS.filter((k) => fields[k] !== null).length
}

// Wire shape of readinesscheck mappingResponse (internal/readinesscheck/handlers.go).
export interface Suggestion {
  columns: string[]
  sample_rows: string[][]
  header_row: number
  rows_total: number
  mapping: Record<string, string>
  source: string
}

export function initialMapping(s: Suggestion): Mapping {
  const out: Mapping = {}
  for (const key of CHECK_FIELD_KEYS) {
    const header = Object.hasOwn(s.mapping, key) ? s.mapping[key] : undefined
    out[key] = header !== undefined && s.columns.includes(header) ? header : null
  }
  return out
}

// labelOf takes a field key; a line import key (line_total) has none, so find it by importKey.
export function fieldLabel(key: string): string {
  const f = INVOICE_FIELDS.find((x) => x.importKey === key) ?? INVOICE_FIELDS.find((x) => x.key === key)
  return f ? f.label : key
}

export const FIELD_GROUPS = [
  ['invoice', 'Invoice'],
  ['amounts', 'Amounts'],
  ['line', 'Line items'],
  ['buyer', 'Buyer'],
  ['supplier', 'Supplier'],
] as const
export type GroupId = (typeof FIELD_GROUPS)[number][0]

const AMOUNTS = ['currency', 'tax_currency_code', 'subtotal', 'vat', 'total']

export function groupOf(key: string): GroupId {
  if (key.startsWith('supplier_')) return 'supplier'
  if (key.startsWith('buyer_')) return 'buyer'
  if (key.startsWith('line_')) return 'line'
  return AMOUNTS.includes(key) ? 'amounts' : 'invoice'
}
