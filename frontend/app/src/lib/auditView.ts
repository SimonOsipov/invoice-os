// Every user-visible string on the Audit screen, plus the decisions the screen makes.
//
// Copy and derivation live here, not in AuditView.tsx, per the shipped
// [bulk-copy-lives-in-the-lib] convention (lib/approvals.ts): vitest is `environment:
// node` for this project by default, so logic written into a component is logic a plain
// unit test cannot reach.

import type { AsyncStatus } from '@invoice-os/api-client'

import type { AuditResponse } from './audit'

export const AUDIT_COPY = {
  eyebrow: 'COMPLIANCE',
  h1: 'Audit log',
  subtitle: 'Every recorded action, newest first',
  tenantFallback: 'This workspace',
  emptyTitle: 'Nothing recorded yet',
  emptyMessage: 'Actions appear here as soon as anyone creates, validates, approves or transmits an invoice.',
  // No count to state: the only unfiltered response this screen saw reported none.
  emptyByFilterBare: 'The log is not empty. These filters exclude every event in it.',
  clearFilter: 'Clear filter',
} as const

export type AuditScreenState = 'loading' | 'error' | 'new-workspace' | 'empty-by-filter' | 'filtered' | 'loaded'

// The empty/new-workspace split is taken from `log_is_empty` and nothing else. The server
// computes that flag with an unfiltered probe when total is 0 (audit/store.go:29-49), so a
// screen that guessed from the response's shape would be re-deriving what it was handed.
export function auditScreenState(status: AsyncStatus, data: AuditResponse | null, filtered: boolean): AuditScreenState {
  if (status === 'error') return 'error'
  if (data == null) return 'loading'
  if (data.total === 0) return data.log_is_empty ? 'new-workspace' : 'empty-by-filter'
  return filtered ? 'filtered' : 'loaded'
}

// N is a LIFETIME figure, so it may only come from an unfiltered response (Option A, user
// decision 2026-08-23). Absent one, the sentence drops the number rather than passing off
// a filtered `total` as the size of the log.
export function emptyByFilterCopy(lifetimeTotal: number | null): string {
  if (lifetimeTotal == null || lifetimeTotal <= 0) return AUDIT_COPY.emptyByFilterBare
  const n = lifetimeTotal === 1 ? '1 event' : `${lifetimeTotal.toLocaleString('en-NG')} events`
  return `The log is not empty. It holds ${n} — these filters exclude every one of them.`
}

export function invoiceFilterPillLabel(invoiceNumber: string | null): string {
  return invoiceNumber != null ? `Invoice ${invoiceNumber}` : 'One invoice'
}
