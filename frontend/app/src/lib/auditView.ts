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

export const AUDIT_PAGE_SIZES = [25, 50, 100] as const

// The reader is forward-only: it mints a cursor for the NEXT page and never one for the
// previous. Prev therefore has to be a client-held stack of the cursors already used --
// `null` is a legitimate entry, standing for the first page's unset cursor.
export interface AuditPageState {
  limit: number
  cursor: string | null
  stack: (string | null)[]
}

export const AUDIT_PAGE_INITIAL: AuditPageState = { limit: 25, cursor: null, stack: [] }

export function auditPageNext(s: AuditPageState, nextCursor: string | null): AuditPageState {
  if (nextCursor == null) return s
  return { ...s, cursor: nextCursor, stack: [...s.stack, s.cursor] }
}

export function auditPagePrev(s: AuditPageState): AuditPageState {
  if (s.stack.length === 0) return s
  return { ...s, cursor: s.stack[s.stack.length - 1], stack: s.stack.slice(0, -1) }
}

// A cursor addresses a boundary at the limit it was minted under, so carrying one into a
// request at a different limit would skip or repeat rows. Resizing restarts the run.
export function auditPageResize(limit: number): AuditPageState {
  return { limit, cursor: null, stack: [] }
}

// Keyset gives no offset, so the first index comes from how many pages have been passed.
// Resizing resets the stack, so `limit` is constant for every page counted here.
export function auditRangeLabel(s: AuditPageState, rowsOnPage: number, total: number): string {
  if (rowsOnPage === 0) return `0 of ${total.toLocaleString('en-NG')}`
  const first = s.stack.length * s.limit + 1
  return `${first.toLocaleString('en-NG')}–${(first + rowsOnPage - 1).toLocaleString('en-NG')} of ${total.toLocaleString('en-NG')}`
}

// The append-only guarantee, stated as fact because it IS one: audit_log carries GRANT
// SELECT, INSERT only, plus triggers raising restrict_violation on UPDATE, DELETE and
// TRUNCATE (pinned by TestAudit_NoTruncate). Hedging it would understate the database.
export const AUDIT_IMMUTABILITY_CLAIM =
  'This log is append-only. Entries cannot be edited or deleted, by anyone, including us — the database accepts inserts and reads and rejects every update, delete and truncate.'

// Only an unfiltered `total` is a lifetime figure (Option A, user decision 2026-08-23).
// The reader exposes no first-row date, so the strip states none. If a later story gives
// this screen a default filter, this sentence stops being true and must change with it.
export function auditStripCount(lifetimeTotal: number | null): string | null {
  if (lifetimeTotal == null || lifetimeTotal <= 0) return null
  const n = lifetimeTotal.toLocaleString('en-NG')
  return lifetimeTotal === 1 ? '1 event recorded' : `${n} events recorded`
}
