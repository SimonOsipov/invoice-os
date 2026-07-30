// Pure review-batch view-model (INVCR-01-08, task-284). No React, no DOM, no fetch, no
// `window` -- inputs are the batch header + one invoice page + the violation summary +
// filter state; outputs are the header numbers, tab counts, rail, row verdicts, pager
// strings, so 09-11 can stay dumb renderers. Covered by reviewBatch.test.ts.
//
// verdictPill reconciles §7.3 with D2/D3: `status` is invoiceStatusStyle(row.status)
// VERBATIM (label AND colours, never re-authored); badges are separate facts layered on
// top, tones from severityStyle (its `.label` deliberately dropped -- carrying it would
// give a badge a field that contradicts badge.label). `severity === 'error'` is the ONLY
// blocking predicate (§10.12's trap) -- "has a violation" is never the failing-badge
// input, so a warning-only invoice renders VALIDATED + advisory, never rules-failed.
// `kept-invalid` REPLACES `rules-failed`, never stacks. `kept_as_is_at` is read
// structurally here (an optional param), never added to InvoiceRecord -- subtask 15
// owns that wire, and typing it as present now would repeat the shipped
// rule_set_version trap (typed present, reads undefined on list rows).
//
// channelTiles keeps LIVE (pagination.total off two filtered list queries, passed in --
// never counted off the supplied page, never read from invoices_clean/
// invoices_with_violations) and FROZEN (batch.errors via unreadableRows) apart -- §7.1's
// two channels. `atZero` is an explicit output field, not `count === 0` left to the
// component to infer.
//
// unreadableRows reads row numbers off the shipped union reader rowErrorRows
// (importApi.ts) -- never a local re-read. An error carrying neither `row` nor `rows`
// yields ONE entry with `row: null` -- a naive `flatMap(rowErrorRows)` would silently
// drop it, which is exactly the swallow this story forbids.
//
// filterToQuery/reviewQuery never filter, count, sort or page client-side
// ([filters-are-server-side]). reviewQuery's `batchId` is REQUIRED, not optional -- the
// type-level half of cashing 06's un-cashed `#review/<uuid>` safety argument
// (constructing a review request without a batch id is a compile error).
//
// parseReviewHash/formatReviewHash are the pure hash codec only; the
// `window.location.hash` read/write and App.tsx boot wiring land in 09 -- this module
// stays DOM-free so it is node-testable with no jsdom.
//
// 08 STUB (task-284, Stage 2.5/Mode A) -- every export below throws `new Error('not
// implemented')` before Stage 3 implements it; that IS the correct RED reason (mirrors
// the validationApi.ts/importApi.ts stub idiom this repo already uses).
import type { ListInvoicesOptions, InvoiceStatus } from './invoices'
import type { Violation } from './validationApi'
import type { RowError, ImportBatch } from './importApi'
import type { StatusStyle } from '../types'

export interface VerdictInput {
  status: InvoiceStatus
  violations: Violation[]
  kept_as_is_at?: string | null
}

export type BadgeTone = Pick<StatusStyle, 'bg' | 'border' | 'text'>

export interface VerdictBadge {
  kind: 'rules-failed' | 'kept-invalid' | 'advisory'
  count: number
  label: string
  tone: BadgeTone
}

export interface VerdictPill {
  status: StatusStyle
  badges: VerdictBadge[]
}

export function verdictPill(_input: VerdictInput): VerdictPill {
  throw new Error('not implemented')
}

export interface ChannelTiles {
  live: { cleanTotal: number; failingTotal: number }
  frozen: { unreadable: number; ruleSetLabel: string }
  atZero: boolean
}

export function channelTiles(
  _batch: Pick<ImportBatch, 'errors' | 'rule_set_version'>,
  _live: { cleanTotal: number; failingTotal: number },
): ChannelTiles {
  throw new Error('not implemented')
}

export interface UnreadableRow {
  row: number | null
  column: string
  message: string
}

export function unreadableRows(_errors: RowError[]): UnreadableRow[] {
  throw new Error('not implemented')
}

export type ReviewPill = 'all' | 'needs-fix' | 'ready' | 'queued'

export function filterToQuery(_pill: ReviewPill): Partial<ListInvoicesOptions> {
  throw new Error('not implemented')
}

export function pagerLabels(_p: { limit: number; offset: number; total: number }): { showing: string; page: string } {
  throw new Error('not implemented')
}

export function parseReviewHash(_hash: string): string | null {
  throw new Error('not implemented')
}

export function formatReviewHash(_id: string): string {
  throw new Error('not implemented')
}

// The single composer 09/10 use to build every review request -- flagged as an 8th
// export beyond AC-4's list, with justification recorded in the task's Implementation
// Plan §4, not smuggled: it is the only lever this subtask has to discharge 06's
// recorded safety argument, and without it 09/10 would hand-assemble ListInvoicesOptions
// inside a component, contradicting this subtask's own dumb-renderers premise.
export function reviewQuery(
  _batchId: string,
  _pill: ReviewPill,
  _extra?: { ruleKey?: string; q?: string; limit?: number; offset?: number },
): ListInvoicesOptions {
  throw new Error('not implemented')
}
