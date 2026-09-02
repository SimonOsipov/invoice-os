// Shared violation wire types plus the severity -> StatusStyle pill mapper, consumed by
// ViolationsTable, ReviewRow, lib/reviewBatch and lib/invoices.
import type { StatusStyle } from '../types'

export type Severity = 'error' | 'warning' | 'info'

// `expected`/`actual` (INVCR-01-08 AC-3) carry the arithmetic a math rule disagreed with,
// so the inline fix editor (subtask 14) can show "expected 1,150.00, got 1,050.00" instead
// of only the rule's prose. OPTIONAL and `string`, mirroring the `*string`+`omitempty` the
// server side puts on the wire in subtask 12 ([expected-is-decimal-string]): a rule with no
// arithmetic to report omits the keys entirely, so ABSENT means absent -- never `''`, which
// would render as an empty expectation rather than as no expectation. Decimal STRINGS, not
// numbers, for the same reason every money field on the wire is ([D13]).
export interface Violation {
  rule_key: string
  severity: Severity
  message: string
  path?: string
  expected?: string
  actual?: string
}

const MUTED_STYLE: StatusStyle = { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'Info' }

const SEVERITY_STYLE: Partial<Record<Severity, StatusStyle>> = {
  error: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'Error' },
  warning: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'Warning' },
  info: MUTED_STYLE,
}

// Total mapping: `sev` is typed `Severity`, but the wire value comes from JSON.parse'd
// server data with no runtime enum validation, so an out-of-enum value must still
// resolve to a well-formed StatusStyle rather than `undefined` (Architect Decision —
// unknown -> muted fallback, so M4/future rule-sets render correctly instead of
// crashing a pill component that destructures the result).
export function severityStyle(sev: Severity): StatusStyle {
  return SEVERITY_STYLE[sev] ?? MUTED_STYLE
}
