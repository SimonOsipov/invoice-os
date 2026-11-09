// The one seam to app source (only these three modules); the check never imports app components.
import { ACCEPTED_PICKED_TYPES, classifyPickedFile, MAX_UPLOAD_BYTES, previewColumns } from '../../app/src/lib/importFlow'
import { canSubmitMapping, toImportMapping } from '../../app/src/lib/mapping'
import { IMPORT_KEYS, INVOICE_FIELDS, labelOf } from '../../app/src/lib/invoiceFields'
import type { Mapping } from '../../app/src/types'

export { ACCEPTED_PICKED_TYPES, classifyPickedFile, MAX_UPLOAD_BYTES, previewColumns, canSubmitMapping, toImportMapping, IMPORT_KEYS, labelOf }
export type { Mapping }

export const ACCEPT = ACCEPTED_PICKED_TYPES.map((r) => r.ext).join(',')

// Order of Go importer.CheckMappingKeys.
export const CHECK_FIELD_KEYS: readonly string[] = [...IMPORT_KEYS, 'supplier_name', 'supplier_tin']

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
  if (!f) return key
  return f.line ? `Line ${f.label.toLowerCase()}` : f.label
}
