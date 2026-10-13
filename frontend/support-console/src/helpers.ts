// Pure presentation helpers. Everything here returns design-system token NAMES, never literals.

import type { Severity } from './types'

export interface StatusStyle {
  bg: string
  border: string
  text: string
  label: string
}

// Rule severity: warn is amber, info is muted, there is no green.
const SEVERITY: Record<Severity, StatusStyle> = {
  error: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'ERROR' },
  warn: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'WARN' },
  info: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'INFO' },
}

export function severityStyle(severity: Severity): StatusStyle {
  return SEVERITY[severity]
}
