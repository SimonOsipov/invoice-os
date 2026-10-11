import type { InvoiceEditInput, InvoiceLineItem, LineItemEditInput } from './invoices'
import type { AuthedFetch } from './portfolio'
import { EXPLAIN_LABEL, type Violation } from './validationApi'

// Mirrors ExplainFix / ExplainResult in internal/invoice/explain.go; `line` null = header field.
export interface ExplainFix {
  field: string
  label: string
  line: number | null
  current: string | null
  value: string
}

export interface ExplainResult {
  status: 'ok' | 'unavailable'
  explanation: string | null
  fix: ExplainFix | null
}

export function explainViolation(
  authedFetch: AuthedFetch,
  base: string,
  id: string,
  v: Pick<Violation, 'rule_key' | 'path'>,
): Promise<ExplainResult> {
  return authedFetch<ExplainResult>(`${base}/api/invoice/v1/invoices/${id}/explain`, {
    method: 'POST',
    body: { rule_key: v.rule_key, path: v.path ?? '' },
  })
}

// A line fix resends every line (with its id) so the PATCH replaces nothing it did not mean to.
export function fixPatch(lines: readonly InvoiceLineItem[], fix: ExplainFix): InvoiceEditInput | null {
  if (fix.line === null) return { [fix.field]: fix.value } as InvoiceEditInput
  if (!lines.some((l) => l.line_no === fix.line)) return null
  const line_items = [...lines]
    .sort((a, b) => a.line_no - b.line_no)
    .map((l): LineItemEditInput => {
      const row: LineItemEditInput = {
        id: l.id,
        description: l.description,
        quantity: l.quantity,
        unit_price: l.unit_price,
        line_total: l.line_total,
        line_tax: l.line_tax,
      }
      return l.line_no === fix.line ? { ...row, [fix.field]: fix.value } : row
    })
  return { line_items }
}

export const EXPLAIN_COPY = {
  button: EXPLAIN_LABEL,
  loading: 'Explaining…',
  unavailable: 'No explanation is available right now. Try again later.',
  proposed: 'Suggested fix',
  accept: 'Accept fix',
  accepting: 'Saving…',
  editorOpen: 'Close the editor first — accepting a fix saves the invoice.',
  unsaved: 'Save your changes first — accepting a fix saves this invoice.',
  stale: 'Re-validate first — this result is out of date.',
  acceptFailed: 'The fix was not saved, and this invoice has been reloaded. Click Explain to try again.',
} as const
