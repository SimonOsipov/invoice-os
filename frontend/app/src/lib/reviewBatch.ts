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
// parseReviewHash/formatReviewHash are the pure hash codec only; reviewHash is the
// App-facing composer over them. The `window.location.hash` read/write itself lives at
// exactly TWO call sites in App.tsx (the boot initializer and the mirror effect) -- this
// module stays DOM-free so it is node-testable with no jsdom.
import {
  invoiceStatusStyle,
  mbsPathToEditField,
  pruneSelection,
  selectableIds,
  skipReasonLabel,
  type BatchSubmitResultItem,
  type EditFieldKey,
  type InvoiceEditInput,
  type InvoiceRecord,
  type ListInvoicesOptions,
  type InvoiceStatus,
  type RuleCount,
} from './invoices'
import { severityStyle, type Severity, type Violation } from './validationApi'
import { rowErrorRows, type RowError, type ImportBatch, type ImportReport } from './importApi'
import { reportSummary } from './importReport'
import { fmtDateTime } from './format'
import type { StatusStyle, View, CreateStep } from '../types'
// Type-only: importRun.ts:74 already imports routeAfterImport FROM this file, so a
// runtime import back would be this codebase's first reviewBatch.ts <-> importRun.ts
// cycle. `import type` is fully erased (verbatimModuleSyntax), so no such cycle exists
// here -- see REVIEW_HASH_MAX_IDS above for why the one genuinely runtime-valued
// constant this file needs (MAX_RUN_FILES) is a documented mirror instead.
import type { ImportRun } from './importRun'

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

// severityStyle's `.label` ('Error'/'Warning'/'Info') is dropped here deliberately -- see
// the module header. A badge carrying both `tone.label` and `label` would let a component
// render 'Error' where 'KEPT · INVALID' belongs.
function tone(sev: Severity): BadgeTone {
  const { bg, border, text } = severityStyle(sev)
  return { bg, border, text }
}

// EXACTLY ONE badge, by precedence: kept-invalid > rules-failed > advisory > none. Every
// row of the story's verdict table falls out of that one line, and it is what makes the
// two exclusivity rules structural rather than remembered -- `KEPT · INVALID` cannot stack
// on `N RULES FAILED`, and `0 RULES FAILED` is unreachable because the rules-failed arm is
// only entered with `errorCount > 0`.
//
// `severity === 'error'` is the ONLY blocking predicate (§10.12's trap): "has a violation"
// is never the input to the failing badge, so a warning-only row is VALIDATED + advisory,
// never failing. All 19 shipped rules are `error` today, which is exactly why a lazy
// `violations.length` impl would pass everything except PILL-4 and then break on the first
// warning rule ever published.
//
// The advisory arm is status-AGNOSTIC on purpose: a draft carrying warnings gets the same
// advisory badge a validated row does. Suppressing it would be forming an opinion the
// server did not send (that a draft's warnings do not count), and this module reports what
// the wire says. Not pinned by the frozen suite either way -- flagged in the task notes.
export function verdictPill(input: VerdictInput): VerdictPill {
  const status = invoiceStatusStyle(input.status)
  const errorCount = input.violations.filter((v) => v.severity === 'error').length
  const advisoryCount = input.violations.length - errorCount

  if (input.kept_as_is_at != null) {
    // `count` is the number of BLOCKING violations the row was kept despite -- machine-
    // readable context for a tooltip, never interpolated into this badge's own label.
    return { status, badges: [{ kind: 'kept-invalid', count: errorCount, label: 'KEPT · INVALID', tone: tone('warning') }] }
  }
  if (errorCount > 0) {
    return {
      status,
      badges: [{ kind: 'rules-failed', count: errorCount, label: `${errorCount} RULES FAILED`, tone: tone('error') }],
    }
  }
  if (advisoryCount > 0) {
    // Tone follows the severity actually present rather than a fixed one: an advisory set
    // holding a warning renders amber, an info-only set renders muted. Hard-coding either
    // would mis-signal the other.
    const advisorySeverity: Severity = input.violations.some((v) => v.severity === 'warning') ? 'warning' : 'info'
    return {
      status,
      badges: [
        { kind: 'advisory', count: advisoryCount, label: `${advisoryCount} ADVISORY`, tone: tone(advisorySeverity) },
      ],
    }
  }
  return { status, badges: [] }
}

export interface ChannelTiles {
  live: { cleanTotal: number; failingTotal: number }
  frozen: {
    unreadable: number
    ruleSetLabel: string
    alreadyImported: number // rows; stubbed 0 until the channel split lands
    alreadyImportedInvoices: number // errors[] entries; stubbed 0
    alreadyImportedAtZero: boolean // stubbed true
  }
  atZero: boolean
}

// D1: the APP supplies the rule-set NAME, the server supplies the NUMBER. Kept a module
// constant rather than imported from lib/rules.ts, whose GOLDEN_SET pins a `v8` the server
// has never emitted -- that is mock data for the Rules screen (which has no endpoint) and
// importing it here would let a hard-coded version leak into a live surface.
const RULE_SET_NAME = 'NG-MBS'

// `undefined` is handled alongside `null` on purpose, despite ImportBatch typing this
// field always-present: the recorded shipped trap is that InvoiceRecord's own
// rule_set_version is typed `number | null` and still reads `undefined` on list rows. A
// missing version renders "not evaluated" and NEVER `v0` -- `?? 0` here would report a
// rule set numbered zero as if it had run.
function ruleSetLabel(v: number | null | undefined): string {
  return v != null ? `${RULE_SET_NAME} v${v}` : 'not evaluated'
}

// The MIN non-null rule-set version across a run's batches (BULK-01-06) -- GetBatch's
// own min() rationale, matching the version the whole run was imported under. `null`
// when every batch's version is null, so ruleSetLabel renders 'not evaluated' rather
// than a fabricated v0. Shared by channelTilesAll and reviewHeaderAll below so the two
// never derive this differently.
function minNonNullVersion(versions: (number | null | undefined)[]): number | null {
  const nums = versions.filter((v): v is number => v != null)
  return nums.length > 0 ? Math.min(...nums) : null
}

// "source not recorded" (BULK-01-06): the shared fallback label for a batch whose
// filename was never recorded (rows written before BULK-01-01's migration) -- used by
// reviewHeaderAll/unreadableRowsAll/filesStrip below, all three of which always render a
// fixed cell/line and so fall back to this fixed string rather than to null. A DIFFERENT
// contract from sourceFileLabel further down, which returns null instead -- a single
// invoice row can be rendered blank, but a whole header/CSV line cannot.
const SOURCE_NOT_RECORDED = 'source not recorded'

function filenameLabel(filename: string | null): string {
  return filename ?? SOURCE_NOT_RECORDED
}

// The two channels stay apart (§7.1). LIVE is `pagination.total` off two filtered list
// queries, PASSED IN -- never counted off whatever page the caller happens to hold, and
// never read from the report's invoices_clean/invoices_with_violations (which the GET
// deliberately does not serve). FROZEN is the batch's own structural errors, one number
// feeding the tile, the tab count and the footer so the three cannot disagree.
//
// `atZero` reports the FROZEN channel's zero state as an explicit fact -- §7.1 renders it
// as a distinct dashed-and-greyed tile, so "no unreadable rows" is a thing to say, not a
// thing to omit. Deliberately NOT coupled to the live totals: the channels are independent
// and an empty batch is not the same fact as a batch with nothing unreadable in it.
export function channelTiles(
  batch: Pick<ImportBatch, 'errors' | 'rule_set_version'>,
  live: { cleanTotal: number; failingTotal: number },
): ChannelTiles {
  const unreadable = unreadableRows(batch.errors).length
  return {
    live: { cleanTotal: live.cleanTotal, failingTotal: live.failingTotal },
    frozen: {
      unreadable,
      ruleSetLabel: ruleSetLabel(batch.rule_set_version),
      alreadyImported: 0,
      alreadyImportedInvoices: 0,
      alreadyImportedAtZero: true,
    },
    atZero: unreadable === 0,
  }
}

// Additive sibling of channelTiles, summing the frozen channel across every batch in a
// run and labelling the rule set with the MIN non-null version (GetBatch's own min()
// rationale -- the version the run was imported under), 'not evaluated' when every
// batch's version is null. Never `?? 0`.
export function channelTilesAll(
  batches: Pick<ImportBatch, 'errors' | 'rule_set_version'>[],
  live: { cleanTotal: number; failingTotal: number },
): ChannelTiles {
  const unreadable = batches.reduce((sum, b) => sum + unreadableRows(b.errors).length, 0)
  return {
    live: { cleanTotal: live.cleanTotal, failingTotal: live.failingTotal },
    frozen: {
      unreadable,
      ruleSetLabel: ruleSetLabel(minNonNullVersion(batches.map((b) => b.rule_set_version))),
      alreadyImported: 0,
      alreadyImportedInvoices: 0,
      alreadyImportedAtZero: true,
    },
    atZero: unreadable === 0,
  }
}

export interface UnreadableRow {
  row: number | null
  column: string
  message: string
}

// Row numbers come off the SHIPPED union reader rowErrorRows, never a local re-read of
// the row/rows union -- two readers of one union is the drift hazard importReport.ts
// already names. `rows:[5,6]` is therefore two entries, not one.
//
// An error carrying NEITHER `row` nor `rows` yields `rowErrorRows(e) === []`, so a naive
// `errors.flatMap(rowErrorRows)` would silently drop a server-reported structural failure
// entirely -- the swallow this story forbids. It becomes one `row: null` entry instead:
// "we cannot tell you which row" is still a report; saying nothing is not.
//
// `column` falls back to an em dash, never a fabricated column name (decision 20).
export function unreadableRows(errors: RowError[]): UnreadableRow[] {
  return errors.flatMap((e): UnreadableRow[] => {
    const column = e.field ?? '—'
    const rows = rowErrorRows(e)
    if (rows.length === 0) return [{ row: null, column, message: e.message }]
    return rows.map((row) => ({ row, column, message: e.message }))
  })
}

// Additive sibling of unreadableRows, attributing each entry to its owning file.
// UnreadableRow itself is UNTOUCHED -- unreadableRows/channelTiles/ReviewBatch.tsx keep
// their existing 3-key shape (SHELL-7/UNREAD-3 pin exactly {row, column, message}, no
// fourth key) -- `file` lives on this separate, additive type instead of widening
// UnreadableRow in place.
export interface UnreadableRowAll extends UnreadableRow {
  // Resolved filename, or "source not recorded" when the owning batch's filename is
  // null -- NEVER the raw null, NEVER the literal string 'null' (BULK-06-23).
  file: string
}

export function unreadableRowsAll(batches: Pick<ImportBatch, 'id' | 'filename' | 'errors'>[]): UnreadableRowAll[] {
  return batches.flatMap((b) => unreadableRows(b.errors).map((r) => ({ ...r, file: filenameLabel(b.filename) })))
}

export type ReviewPill = 'all' | 'needs-fix' | 'ready' | 'queued'

// `all` returns an object with ZERO keys, not `{needsFix:false, status:undefined}` -- the
// latter survives a spread into ListInvoicesOptions and, in a less strict emitter than
// listInvoices', serializes as `?status=undefined`. No pill is ever satisfied by filtering
// a page in the browser ([filters-are-server-side]): each one is a server query, so a pill
// count is the server's `pagination.total`, not the length of whatever page is on screen.
export function filterToQuery(pill: ReviewPill): Partial<ListInvoicesOptions> {
  switch (pill) {
    case 'needs-fix':
      return { needsFix: true }
    case 'ready':
      return { status: 'validated' }
    case 'queued':
      return { status: 'queued' }
    case 'all':
      return {}
  }
}

// Renders §7.3's two pager strings from `pagination` alone. The separator in
// "SHOWING 1–50 OF 500" is an EN DASH (U+2013), per the design -- a hyphen is a silent
// copy bug.
//
// The `limit < 1` arm is what makes AC-9's "no NaN, no Infinity" true: `ceil(total/0)` is
// Infinity and `floor(0/0)` is NaN, and both would render into the string. That one IS
// unreachable from the server (ListHandler 400s on `limit < 1`, and `pagination` echoes
// the EFFECTIVE limit) -- a guard against a synthetic caller, not a case this expects.
//
// The `last < first` arm is DIFFERENT: it is genuinely reachable from a real response.
// Any `offset >= total` lands there -- e.g. sitting on page 2 (`offset:50`) when a filter
// narrows the result set to 10 -- and it renders "SHOWING 0 OF 10", not an inverted
// "51–10". `page` clamps to `pages` in the same situation, so the two strings agree that
// there is nothing on this page rather than disagreeing about where it is.
export function pagerLabels(p: { limit: number; offset: number; total: number }): { showing: string; page: string } {
  const first = p.total === 0 ? 0 : p.offset + 1
  const last = Math.min(p.offset + p.limit, p.total)
  const pages = p.limit < 1 ? 1 : Math.max(1, Math.ceil(p.total / p.limit))
  const page = Math.min(pages, (p.limit < 1 ? 0 : Math.floor(p.offset / p.limit)) + 1)

  let showing: string
  if (p.total === 0) showing = 'SHOWING 0 OF 0'
  else if (last < first) showing = `SHOWING 0 OF ${p.total}`
  else showing = `SHOWING ${first}–${last} OF ${p.total}`

  return { showing, page: `PAGE ${page} / ${pages}` }
}

const REVIEW_HASH_PREFIX = '#review/'

// Canonical 8-4-4-4-12 hex. Anchored at both ends on purpose: a `startsWith` + `slice`
// parser would happily return '../../etc' or a '<uuid>/extra' suffix as if it were a batch
// id, and that id goes straight into a request whose empty/absent form is a TENANT-WIDE
// list. Case is accepted in both directions because uuid.Parse is (server side).
const REVIEW_UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/

// [five-file-cap] mirror -- importRun.ts:15 owns the real MAX_RUN_FILES, and that module
// already imports routeAfterImport FROM this file (importRun.ts:74), so importing the
// constant back would be this codebase's first reviewBatch.ts <-> importRun.ts cycle.
// Documented mirror + drift guard, NOT a runtime clamp -- the same idiom
// BATCH_SUBMIT_MAX_IDS uses below for handlers.go's server-side cap. The test file may
// import both MAX_RUN_FILES and this constant freely; only the two SOURCE modules must
// stay acyclic.
export const REVIEW_HASH_MAX_IDS = 5 // importRun.ts:15's MAX_RUN_FILES

// Returns null -- NEVER '' -- for anything that is not one-to-REVIEW_HASH_MAX_IDS comma-
// separated `#review/<uuid>[,<uuid>...]` segments, EVERY one of which must match
// REVIEW_UUID: one bad segment poisons the WHOLE hash, never a partial list. '' is a
// string a caller can accidentally treat as a usable id, and passing it to listInvoices
// omits the param and silently widens the query to the whole tenant. The prefix match is
// case-SENSITIVE ('#REVIEW/...' is not our route) while each uuid's own case is preserved
// VERBATIM -- lower-casing it here would be rewriting the caller's data.
//
// Widened in place from `(hash): string | null` -- its only caller, App.tsx's boot
// initializer, was updated in the same commit (BULK-01-05/06).
export function parseReviewHash(hash: string): string[] | null {
  if (!hash.startsWith(REVIEW_HASH_PREFIX)) return null
  const segments = hash.slice(REVIEW_HASH_PREFIX.length).split(',')
  if (segments.length === 0 || segments.length > REVIEW_HASH_MAX_IDS) return null
  return segments.every((s) => REVIEW_UUID.test(s)) ? segments : null
}

// Widened in place from `(id: string): string` -- a single-id array still formats
// byte-identically to the shipped `#review/x` (AC-2).
export function formatReviewHash(ids: string[]): string {
  return `${REVIEW_HASH_PREFIX}${ids.join(',')}`
}

// The single composer 09/10 use to build every review request -- flagged as an 8th
// export beyond AC-4's list, with justification recorded in the task's Implementation
// Plan §4, not smuggled: it is the only lever this subtask has to discharge 06's
// recorded safety argument, and without it 09/10 would hand-assemble ListInvoicesOptions
// inside a component, contradicting this subtask's own dumb-renderers premise.
//
// The throw is the runtime half of the same argument, and it is the only enforcement a
// pure function has left: the TYPE stops `undefined`, but `''` is still a `string`, and
// listInvoices treats an empty id as ABSENT (matching the server's own empty-is-absent
// rule, applied per value) -- which returns a tenant-wide page carrying a plausible
// total, i.e. exactly the wrong-page-instead-of-an-honest-error failure mode
// ListHandler's own doc comment refuses. Loud beats a review table full of another
// batch's invoices.
//
// `batchId` stays a single `string` here (BULK-01-02 widens ListInvoicesOptions'
// importBatchIds to string[], but reviewQuery/reviewPageQuery's OWN signatures are
// unchanged -- deriving several ids from a run is BULK-01-06's job): the one-element
// array below is this subtask's plumbing only, not a widening of what this function
// accepts.
//
// The extras follow listInvoices' emission rules rather than being spread in blind: a
// blank search box must not become `q=''` on the wire, and `offset: 0` must survive.
export function reviewQuery(
  batchId: string,
  pill: ReviewPill,
  extra: { ruleKey?: string; q?: string; limit?: number; offset?: number } = {},
): ListInvoicesOptions {
  if (batchId === '') throw new Error('reviewQuery: batchId is required — an empty one lists the whole tenant')
  const opts: ListInvoicesOptions = { importBatchIds: [batchId], ...filterToQuery(pill) }
  if (extra.ruleKey) opts.ruleKey = extra.ruleKey
  if (extra.q) opts.q = extra.q
  if (extra.limit != null) opts.limit = extra.limit
  if (extra.offset != null) opts.offset = extra.offset
  return opts
}

// Additive sibling of reviewQuery, over every id in a run. reviewQuery itself is
// UNTOUCHED and stays exported -- ReviewBatch.tsx's four call sites and App.tsx's
// startRun() still build a single-batch request through it today (the latter genuinely
// has one id, so it never needs to switch); BULK-01-07 owns switching ReviewBatch.tsx's
// four before reviewQuery can be deleted (task-307's own recorded obligation). Throws on
// an empty array AND on any '' member -- reviewQuery's own argument, applied per
// element: an absent/empty batch id lists the WHOLE TENANT with a plausible total.
export function reviewQueryAll(
  batchIds: string[],
  pill: ReviewPill,
  extra: { ruleKey?: string; q?: string; limit?: number; offset?: number } = {},
): ListInvoicesOptions {
  if (batchIds.length === 0 || batchIds.some((id) => id === '')) {
    throw new Error(
      'reviewQueryAll: batchIds must be non-empty with no empty id — an absent/empty batch id lists the whole tenant',
    )
  }
  const opts: ListInvoicesOptions = { importBatchIds: batchIds, ...filterToQuery(pill) }
  if (extra.ruleKey) opts.ruleKey = extra.ruleKey
  if (extra.q) opts.q = extra.q
  if (extra.limit != null) opts.limit = extra.limit
  if (extra.offset != null) opts.offset = extra.offset
  return opts
}

// --- Post-import routing (AC-9, task-285 Implementation Plan §3) ---
//
// The SOLE decision boundary between "startImport succeeded" and where the app lands.
// Order is load-bearing: status first (call the shipped reportSummary -- never a second
// `status !== 'completed'` copy, that is the fork AC-10 forbids), then count, then id.
// Truthiness on `resolvedInvoiceId`, NEVER `!= null` -- `''` is a string and a null-check
// would let it fall through into 'single' with an empty invoiceId.
export type PostImportRoute =
  | { kind: 'single'; invoiceId: string }
  | { kind: 'review'; batchId: string }
  | { kind: 'rejected'; batchId: string }

export function routeAfterImport(report: ImportReport, resolvedInvoiceId: string | null): PostImportRoute {
  // 1. STATUS first, through the SHIPPED predicate. reportSummary keys `failed` on
  //    `status !== 'completed'` (importReport.ts, Trap B), which is deliberately not a
  //    `=== 'failed'` whitelist -- normalizeReport does not validate `status`, so an
  //    unrecognised one must fail safe. Re-writing that comparison here would be a
  //    second copy of a spec-pinned predicate, free to drift (the fork AC-10 forbids).
  if (reportSummary(report).kind === 'failed') return { kind: 'rejected', batchId: report.id }
  // 2. Then the COUNT. Nothing was created, so there is nothing to open.
  if (report.ready_invoices === 0) return { kind: 'rejected', batchId: report.id }
  // 3. Then the ID, and only at EXACTLY one -- `> 1` falls through to the batch surface
  //    even when an id resolved, which is what makes `if (resolvedInvoiceId) return
  //    single` a failing implementation rather than an equivalent one.
  //
  //    Truthiness, NEVER `!= null`: the id comes from `r.invoices[0]?.id`, so `''` is
  //    representable, and `''` is a string that passes a null check. Routing the detail
  //    view at an empty id is exactly the failure this degrade-to-review branch exists
  //    to prevent -- a clean single invoice is COUNTED by the report but never LISTED
  //    in it (Go appends InvoiceViolations only when a violation exists), so the id can
  //    only ever come from the follow-up list page, which can legitimately come back
  //    empty.
  if (report.ready_invoices === 1 && resolvedInvoiceId) return { kind: 'single', invoiceId: resolvedInvoiceId }
  return { kind: 'review', batchId: report.id }
}

// --- §7.5-vs-batch resolution (AC-7, resolves the AC-7/AC-9 divergence -- §4) ---
//
// The SOLE owner of the §7.5-vs-batch decision, keyed on the batch GET alone -- NEVER on
// routeAfterImport's `kind` and NEVER on `ready_invoices` -- so both arrival paths
// (POST-then-route, and a deep-link revisit that never calls routeAfterImport at all)
// run the identical derivation. An all-quarantined batch (`completed`, `ready_invoices:
// 0`, `rows_invalid > 0`) therefore renders the BATCH surface: routeAfterImport answers
// a different question ("is there one invoice to open") and legitimately says `rejected`
// for the same import -- the two are not required to agree.
export type ReviewShellState = 'batch' | 'rejected'

export function reviewShellState(batch: Pick<ImportBatch, 'status'>): ReviewShellState {
  // `!== 'completed'`, matching reportSummary's own direction but over a DIFFERENT type
  // (ImportBatch's 4-value status union, not ImportReport's 2-value one) answering a
  // DIFFERENT question -- which is why this is not a second copy of that predicate.
  return batch.status !== 'completed' ? 'rejected' : 'batch'
}

// Additive sibling of reviewShellState, 'batch' iff ANY batch is 'completed' --
// byte-identical to reviewShellState over exactly one batch, which it will eventually
// replace once BULK-01-07 switches ReviewBatch.tsx's call site.
export function reviewShellStateAll(batches: Pick<ImportBatch, 'status'>[]): ReviewShellState {
  return batches.some((b) => b.status === 'completed') ? 'batch' : 'rejected'
}

// --- Header copy (AC-2, §7.1) ---
//
// `allTotal` is the LIVE total (the `all` query's pagination.total), never
// `report.ready_invoices` -- one source feeds both arrival paths. reviewHeader itself
// takes no file/filename parameter; reviewHeaderAll below is what BULK-01-06 adds for a
// run's filesLine (AC-6), now that import_batches carries `filename` (BULK-01-01).
export interface ReviewHeader {
  title: string
  batchId: string
  subline: string
}

export function reviewHeader(
  batch: Pick<ImportBatch, 'id' | 'rows_total' | 'rule_set_version' | 'created_at'>,
  live: { allTotal: number },
): ReviewHeader {
  return {
    title: `${live.allTotal} invoices imported`,
    batchId: batch.id,
    // ruleSetLabel is the SHIPPED one above ('NG-MBS v3', or 'not evaluated' when the
    // version is null) -- rendered verbatim and never re-cased here, because uppercasing
    // is CSS's job and a `.toUpperCase()` would also uppercase 'not evaluated' into a
    // shout. fmtDateTime is lib/format.ts's; no date formatting is authored here.
    subline: `${batch.rows_total} ROWS READ · SERVER VERDICT · RULE SET ${ruleSetLabel(batch.rule_set_version)} · ${fmtDateTime(batch.created_at)}`,
  }
}

// Additive sibling of reviewHeader, over every batch in a run. `filesLine` is `from
// ${filename}` at one batch (byte-identical prose to what a human would expect from the
// shipped single-file case) or `from N files` above one -- and "source not recorded"
// (never `from null`, never a bare `from `) replaces the whole clause when a single
// batch's filename is null, matching sourceFileLabel/filesStrip's own fallback
// (BULK-06-18/22/23). `rows_total` sums across every batch.
export interface ReviewHeaderAll {
  title: string
  batchIds: string[]
  subline: string
  filesLine: string
}

export function reviewHeaderAll(
  batches: Pick<ImportBatch, 'id' | 'filename' | 'rows_total' | 'rule_set_version' | 'created_at'>[],
  live: { allTotal: number },
): ReviewHeaderAll {
  const rowsTotal = batches.reduce((sum, b) => sum + b.rows_total, 0)
  const label = ruleSetLabel(minNonNullVersion(batches.map((b) => b.rule_set_version)))
  const filesLine =
    batches.length === 1
      ? batches[0].filename != null
        ? `from ${batches[0].filename}`
        : SOURCE_NOT_RECORDED
      : `from ${batches.length} files`
  return {
    title: `${live.allTotal} invoices imported`,
    batchIds: batches.map((b) => b.id),
    subline: `${rowsTotal} ROWS READ · SERVER VERDICT · RULE SET ${label} · ${fmtDateTime(batches[0]?.created_at)}`,
    filesLine,
  }
}

// --- Tabs (AC-4, §7.2) ---
//
// The second tab is OMITTED from the returned array entirely at zero unreadable rows,
// not hidden with CSS -- the array's own length is the fact 09/10 render off.
// `invoices` is the `all` query's pagination.total; `unreadable` is the caller's
// channelTiles(...).frozen.unreadable (the expansion count) -- never `batch.rows_invalid`
// -- one number feeding the tile, the tab and the footer.
export interface ReviewTab {
  id: 'invoices' | 'unreadable'
  label: string
}

// `alreadyImported` is required (not optional) so a caller cannot silently omit it —
// the body doesn't act on it yet (channel split lands separately); the required
// parameter alone forces every call site to state it explicitly.
export function reviewTabs(counts: { invoices: number; unreadable: number; alreadyImported: number }): ReviewTab[] {
  const tabs: ReviewTab[] = [{ id: 'invoices', label: `Invoices (${counts.invoices})` }]
  if (counts.unreadable > 0) tabs.push({ id: 'unreadable', label: `Unreadable rows (${counts.unreadable})` })
  return tabs
}

// --- Hash codec, the App-facing half (AC-1, §2) ---
//
// Mirrors App.tsx's mirror effect exactly: the hash is written iff view==='create' AND
// createStep==='review' AND reviewBatchId is a NON-EMPTY string; every other combination
// clears it. This is what stops the hash lingering after `Finish` or any other exit from
// review -- App.tsx has ONE effect calling this, not N call sites each responsible for
// remembering to clear it. `window` itself is never touched here -- only at the two call
// sites this function's own module comment (top of file) names in App.tsx.
// Widened in place from `(..., reviewBatchId: string | null)` -- the third param is now
// every id in the run (App.tsx's `reviewBatchIds` state, BULK-01-05), not just the
// first. Its only caller, App.tsx's mirror effect, was updated in the same commit; the
// effect's own dep array joins the array (`reviewBatchIds.join(',')`), never passes the
// array reference itself -- a fresh array every render would otherwise re-fire the
// effect forever. This is a plain useEffect, not one of the three genuine useAsync sites
// packages/api-client/src/async-state.ts:117's spread-deps hazard actually names
// (ReviewBatch.tsx, ReviewInvoicesTab.tsx x2).
export function reviewHash(view: View, createStep: CreateStep, reviewBatchIds: string[]): string | null {
  if (view === 'create' && createStep === 'review' && reviewBatchIds.length > 0) return formatReviewHash(reviewBatchIds)
  return null
}

// --- Unreadable-rows CSV (AC-5, §7.4 "Download this list (CSV)") ---
//
// RFC-4180 quoting: a field is wrapped in double quotes, with embedded double quotes
// doubled, whenever it contains a comma, a double quote or a newline. Header row is
// exactly `Row,Field,Why it could not be read`; `row: null` renders as an empty cell,
// never the string 'null' -- there are no *rows* to download once the raw source line is
// gone (`[raw-source-line-dropped]`), only this rendered table.
export const UNREADABLE_CSV_HEADER = 'Row,Field,Why it could not be read'

// A field is quoted iff it contains a comma, a double quote, CR or LF; embedded quotes
// are doubled. The em dash needs no quoting (it is neither) and must survive verbatim --
// escaping it would put visible quotes around a placeholder in Excel.
function csvCell(value: string): string {
  return /[",\r\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value
}

export function unreadableCsv(rows: UnreadableRow[]): string {
  const lines = rows.map((r) =>
    // `row: null` is an EMPTY cell, never the string 'null': the server told us it could
    // not attribute the failure to a line, and "null" in a spreadsheet reads as data.
    [r.row == null ? '' : String(r.row), r.column, r.message].map(csvCell).join(','),
  )
  return [UNREADABLE_CSV_HEADER, ...lines].join('\n')
}

// Additive sibling of UNREADABLE_CSV_HEADER/unreadableCsv, gaining the File column a
// run's download needs. UNREADABLE_CSV_HEADER and unreadableCsv are UNTOUCHED --
// ReviewUnreadableTab.tsx's one call site still downloads the single-batch CSV through
// them today, and BULK-01-07 owns switching it over.
export const UNREADABLE_CSV_HEADER_ALL = 'File,Row,Field,Why it could not be read'

export function unreadableCsvAll(rows: UnreadableRowAll[]): string {
  const lines = rows.map((r) =>
    [r.file, r.row == null ? '' : String(r.row), r.column, r.message].map(csvCell).join(','),
  )
  return [UNREADABLE_CSV_HEADER_ALL, ...lines].join('\n')
}

// --- Files strip (AC-8/9/10/11/12, BULK-01-06) ---
//
// filesStrip is the whole per-file report the run screen renders: one row per batch GET
// (filename resolved through the same "source not recorded" fallback as
// reviewHeaderAll/sourceFileLabel below, never '' and never the literal null), plus one
// row per run-only failure -- a file the server refused before a batch ever existed,
// which vanishes after a reload because there is nothing left to prove it happened.
// `run` is `| null` on purpose: a deep-link revisit (BULK-06-17) has no run to read
// run-only failures from, and the strip is TOTAL over that state -- one row per batch,
// zero run-only rows, never a crash.
//
// `[reason-comes-from-errors-not-status]`: a rejected-by-verdict file's reason is read
// from `batch.errors` alone, NEVER `batch.status` -- a fully-quarantined file has
// `rows_total > 0` and still finalizes 'completed' server-side
// (internal/importer/service.go:923), its quarantine reason living only in `errors`
// (:838-849 -> handlers.go:371). BULK-06-21 asserts this holds identically whether the
// run is still live (outcome kind:'imported', ready_invoices:0) or `run === null` after
// a reload, where `errors` is the only surviving source -- a `status`-keyed
// implementation fails both halves.
export interface FileStripRow {
  id: string // ImportBatch.id for a batch row; RunFile.id for a run-only failure row
  filename: string // resolved label; never '', never the literal null
  reason: string | null // null iff the file produced at least one ready invoice
}

// `rows_valid` is the batch-level proxy for "produced at least one ready invoice":
// service.go's readyGroups either succeed WHOLESALE (their rows stay counted in
// rowsValid) or fail WHOLESALE (rowsValid -= len(g.rowIdxs), service.go:852) -- there is
// no partial-invoice state -- so `rows_valid === 0` and `ready_invoices === 0` are the
// same fact for a given batch. NEVER `batch.status`
// ([reason-comes-from-errors-not-status]) -- a fully-quarantined file still finalizes
// 'completed' server-side (service.go:923), and its reason lives only in `errors`.
function batchReason(batch: Pick<ImportBatch, 'rows_valid' | 'rows_total' | 'errors'>): string | null {
  if (batch.rows_valid > 0) return null
  if (batch.errors.length > 0) return batch.errors.map((e) => e.message).join('; ')
  return `0 of ${batch.rows_total} rows produced an invoice`
}

export function filesStrip(batches: ImportBatch[], run: ImportRun | null): FileStripRow[] {
  const batchRows: FileStripRow[] = batches.map((b) => ({
    id: b.id,
    filename: filenameLabel(b.filename),
    reason: batchReason(b),
  }))
  // Run-only failures: a file whose outcome never became a batch at all
  // (FileOutcome's 'failed' kind carries no batchId), so it is never represented among
  // `batches` above and vanishes the moment `run` is null (a reload). Mirrors
  // importRun.ts's own runFailures loop.
  const runOnlyFailures: FileStripRow[] = []
  for (const f of run?.files ?? []) {
    if (f.outcome.kind === 'failed') runOnlyFailures.push({ id: f.id, filename: f.name, reason: f.outcome.message })
  }
  return [...batchRows, ...runOnlyFailures]
}

// --- Per-row source file (AC-11) ---
//
// sourceFileLabel resolves ONE invoice row's import_batch_id back to its batch's
// filename -- `null` for an id absent from `batches` AND for a batch whose filename was
// never recorded (rows written before BULK-01-01's migration), so the caller can choose
// to render nothing rather than a fabricated label. This is a DIFFERENT contract from
// filesStrip/reviewHeaderAll/unreadableRowsAll above, which always render a fixed
// cell/line and so fall back to the literal "source not recorded" instead of null.
export function sourceFileLabel(
  importBatchId: string | null,
  batches: Pick<ImportBatch, 'id' | 'filename'>[],
): string | null {
  if (importBatchId == null) return null
  return batches.find((b) => b.id === importBatchId)?.filename ?? null
}

// showsSourceFile is the SOLE owner of the "only when the run touched more than one
// batch" rule (AC-11) -- a single-file review has nothing to disambiguate, so no
// component re-derives this threshold on its own.
export function showsSourceFile(batches: unknown[]): boolean {
  return batches.length > 1
}

// --- INVCR-01-10 (task-286): the review Invoices tab's whole model ---
//
// Everything below is what makes ReviewInvoicesTab.tsx a dumb renderer: the filter state
// machine, the request composer, the pill row, the failing-rules rail and the pager's
// two disable gates. Nothing here filters, counts, sorts or pages client-side
// ([filters-are-server-side]) -- every one of these outputs either describes a request
// the SERVER will answer, or renders a number the server already sent.

// --- Filter state (AC-2/3/4/10, Implementation Plan §3) ---
//
// `reviewFilterReducer` is the SOLE place `offset:0` is written on a filter change --
// only `type:'page'` may ever produce a non-zero offset; `pill`/`rule`/`search` all reset
// to 0. Each arm returns the IDENTICAL state object when nothing changed (a re-clicked
// active pill, a trimmed `q` equal to the current one) -- this stops a redundant click
// refetching AND neutralises the debounce effect's mount-time `{type:'search', q:''}`
// dispatch. `rule` TOGGLES: the same key clicked twice clears it to null. `search` TRIMS
// the draft -- `q:' '` is truthy in JS and non-empty in Go, and would reach
// `ILIKE '% %'` untrimmed, silently returning only rows with a literal space.
export const REVIEW_PAGE_SIZE = 50

export interface ReviewFilterState {
  pill: ReviewPill
  ruleKey: string | null
  q: string
  offset: number
}

export const initialReviewFilter: ReviewFilterState = { pill: 'all', ruleKey: null, q: '', offset: 0 }

export type ReviewFilterAction =
  | { type: 'pill'; pill: ReviewPill }
  | { type: 'rule'; ruleKey: string }
  | { type: 'search'; q: string }
  | { type: 'page'; offset: number }

export function reviewFilterReducer(s: ReviewFilterState, a: ReviewFilterAction): ReviewFilterState {
  switch (a.type) {
    // Re-clicking the ACTIVE pill returns `s` itself, not a copy: the component feeds
    // this state straight into useAsync's `deps`, so a `{...s}` here would refetch the
    // identical page on every redundant click.
    case 'pill':
      return a.pill === s.pill ? s : { ...s, pill: a.pill, offset: 0 }
    // A re-clicked rule key IS a change -- it clears the filter -- so this arm never
    // takes the identity shortcut. Toggling is the whole contract (AC-4).
    case 'rule':
      return { ...s, ruleKey: s.ruleKey === a.ruleKey ? null : a.ruleKey, offset: 0 }
    case 'search': {
      // TRIM here, not in the component: `q: ' '` is truthy in JS and non-empty in Go,
      // so it survives every emission rule and reaches `ILIKE '% %'`, silently returning
      // only rows whose number or buyer contains a space. Trimming in the reducer also
      // makes a trailing-space keystroke a no-op via the identity return below, instead
      // of a refetch.
      const q = a.q.trim()
      return q === s.q ? s : { ...s, q, offset: 0 }
    }
    // The ONLY arm that may produce a non-zero offset. Clamped at 0 because pagerNav's
    // `prevOffset` is the only intended caller and a synthetic negative would go
    // straight to a handler that 400s on it.
    case 'page':
      return { ...s, offset: Math.max(0, a.offset) }
  }
}

// The single composer 10's component uses to build the page request -- consumes the
// SHIPPED `reviewQuery` (08) above rather than re-implementing its emission rules. A
// blank `q` is therefore absent at three layers (this reducer's trim -> reviewQuery's
// truthiness -> listInvoices' truthiness) before the server's own `if raw != ""`
// absence rule.
export function reviewPageQuery(batchId: string, s: ReviewFilterState): ListInvoicesOptions {
  return reviewQuery(batchId, s.pill, {
    // `?? undefined` rather than `as string`: reviewQuery's `ruleKey` is optional and a
    // literal `null` would be emitted by neither its truthiness check nor listInvoices'.
    ruleKey: s.ruleKey ?? undefined,
    q: s.q,
    limit: REVIEW_PAGE_SIZE,
    offset: s.offset,
  })
}

// Additive sibling of reviewPageQuery, over every id in a run. Consumes reviewQueryAll
// (not reviewQuery), matching reviewPageQuery's own discipline of never re-implementing
// the composer it sits on.
export function reviewPageQueryAll(batchIds: string[], s: ReviewFilterState): ListInvoicesOptions {
  return reviewQueryAll(batchIds, s.pill, {
    ruleKey: s.ruleKey ?? undefined,
    q: s.q,
    limit: REVIEW_PAGE_SIZE,
    offset: s.offset,
  })
}

// --- Pills (AC-2, D3) ---
//
// Takes the four TOTALS, no rows parameter -- "no count is derived from a row length" is
// enforced by the signature, not by discipline. Labels are D3's, superseding §7.3's
// earlier pair, which used the D2-forbidden vocabulary.
export interface ReviewPillView {
  id: ReviewPill
  label: string
  count: number
  active: boolean
}

const REVIEW_PILL_LABELS: Record<ReviewPill, string> = {
  all: 'All',
  'needs-fix': 'Needs a fix',
  ready: 'Ready to submit',
  queued: 'Queued',
}

// Declared as an explicit array rather than `Object.keys(REVIEW_PILL_LABELS)`: the pill
// ORDER is a §7.3 fact, and key-insertion order is not a contract worth resting it on.
const REVIEW_PILL_ORDER: ReviewPill[] = ['all', 'needs-fix', 'ready', 'queued']

export function reviewPills(
  t: { allTotal: number; cleanTotal: number; failingTotal: number; queuedTotal: number },
  active: ReviewPill,
): ReviewPillView[] {
  // `cleanTotal` is the `ready` (status=validated) query's total and `failingTotal` is
  // the `needs_fix` one's -- the two are easy to swap and the swap is invisible on any
  // batch where they happen to be close.
  const counts: Record<ReviewPill, number> = {
    all: t.allTotal,
    'needs-fix': t.failingTotal,
    ready: t.cleanTotal,
    queued: t.queuedTotal,
  }
  return REVIEW_PILL_ORDER.map((id) => ({ id, label: REVIEW_PILL_LABELS[id], count: counts[id], active: id === active }))
}

// --- Failing-rules rail (AC-4, store.go:661-671) ---
//
// Takes NO page and NO invoice array -- page-derivation is unrepresentable by this
// signature. Server order (count DESC, rule_key ASC) is passed through UNTOUCHED: no
// sort, no filter, no dedupe, no severity clause -- store.go:661-671 makes the summary
// severity-agnostic ON PURPOSE, so "fixing" that here would make the rail disagree with
// the query it fires. `active` is exact string equality; an `activeRuleKey` absent from
// `summary` marks nothing and is NEVER synthesised into a pill.
export interface RailPill {
  ruleKey: string
  count: number
  active: boolean
}

export function railPills(summary: RuleCount[], activeRuleKey: string | null): RailPill[] {
  // A bare positional `map` -- no sort, no filter, no dedupe. Every one of those would
  // be a client opinion about a set the server already ordered, and the rail is a
  // PREVIEW of the `rule_key` query each of its pills fires, so any divergence here
  // shows the user a count they can never reproduce by clicking it.
  return summary.map((r) => ({ ruleKey: r.rule_key, count: r.invoices, active: r.rule_key === activeRuleKey }))
}

// --- Pager (AC-5) ---
//
// Fed the RESPONSE's echoed pagination, never REVIEW_PAGE_SIZE, so a server clamp stays
// visible. `prevOffset` never goes negative; `canNext`/`canPrev` are the disable gates
// the two buttons read directly.
export interface PagerNav {
  canPrev: boolean
  canNext: boolean
  prevOffset: number
  nextOffset: number
}

export function pagerNav(p: { limit: number; offset: number; total: number }): PagerNav {
  return {
    canPrev: p.offset > 0,
    // `+ limit < total`, never `<=`: at offset 450 of 500 with a limit of 50 the last
    // row is already on screen, so a `<=` here would offer a Next that lands on an
    // empty page and a `SHOWING 0 OF 500`.
    canNext: p.offset + p.limit < p.total,
    prevOffset: Math.max(0, p.offset - p.limit),
    nextOffset: p.offset + p.limit,
  }
}

// --- INVCR-01-11 (task-287): the bulk-submit bar's pure model ---
//
// `eligible` is `pruneSelection(selected, rows)` (lib/invoices.ts) CONSUMED, never
// re-derived -- it IS the wire payload the confirm handler sends. `notReady`/
// `canSubmitAll`/`submitAllLabel` are page-scoped off `rows` via `selectableIds`,
// independent of the current selection. `BATCH_SUBMIT_MAX_IDS` mirrors
// handlers.go:718's server constant as a drift guard, so BULK-14 is GREEN-BEFORE by
// design, the same idiom PILL-3b already uses in this file.
//
// EVERY user-visible string of this feature is built HERE, never inline in the component
// ([bulk-copy-lives-in-the-lib]): LIB-SCAN-1 scans this file WHOLE, so the D2 vocabulary
// rule is a guard rather than a thing to remember. Count-dependent copy is a field on
// BulkBarView; the static chrome is BULK_COPY below.

export const BATCH_SUBMIT_MAX_IDS = 200 // handlers.go:718 -- documented mirror + drift guard, NOT a runtime clamp

export type BulkPhase = 'idle' | 'armed' | 'submitting'
export type BulkAction = { type: 'arm' } | { type: 'cancel' } | { type: 'confirm' } | { type: 'settled' }

// Every no-op arm returns the state INSTANCE it was given, and the component's contract
// is `const next = reducer(phase, a); if (next === phase) return` -- identity IS "do
// nothing", which is how "no arm => no request" (AC-3) and the double-click guard get an
// oracle under environment:'node' rather than living in a click handler no unit test can
// reach. Two arms are load-bearing:
//   - `confirm` from 'idle' is identity: a confirm that was never armed fires NOTHING.
//   - `confirm`/`cancel` from 'submitting' are identity: the request is already gone, so
//     a second click cannot re-enter it and a cancel cannot un-send it.
// No `default` arm, deliberately: a new BulkAction member becomes a `tsc --noEmit` error
// here instead of silently falling through to the current phase.
export function bulkPhaseReducer(p: BulkPhase, a: BulkAction): BulkPhase {
  switch (a.type) {
    case 'arm':
      return p === 'idle' ? 'armed' : p
    case 'cancel':
      return p === 'submitting' ? p : 'idle'
    case 'confirm':
      return p === 'armed' ? 'submitting' : p
    case 'settled':
      return 'idle'
  }
}

export interface BulkBarView {
  visible: boolean
  eligible: string[]
  notReady: number
  countLabel: string
  note: string | null
  submitLabel: string
  confirmPrompt: string
  confirmDetail: string
  confirmLabel: string
  canSubmit: boolean
  submitAllLabel: string
  canSubmitAll: boolean
}

// THE LOADING GATE (`canSubmit`/`canSubmitAll`) is this subtask's primary correctness
// requirement, and it is here rather than in the component because the component cannot
// be unit-tested. ReviewInvoicesTab keeps the PREVIOUS page's envelope across the whole
// loading gap (`page.data ?? lastPage.current`), so `rows` keeps its identity, the prune
// effect never fires, the selection survives and the table is only DIMMED -- never
// disabled. Ungated, "select 5 -> Next -> submit before the response lands" puts the
// PREVIOUS page's ids on the wire, gets a 200, and leaves nothing to notice.
//
// `notReady` is page-scoped and cause-FREE on purpose. A non-selectable row may be a
// draft failing a rule, a draft awaiting re-validation, or already queued/submitted/
// accepted/rejected/failed -- only its own verdict pill knows which, so the note answers
// the one question this bar can honestly answer ("why did select-all pick 12 of 50?")
// and names no cause. The status word is interpolated from invoiceStatusStyle, never
// written as a literal, so it cannot drift from the pill in the Verdict column.
export function bulkBarView(
  selected: string[],
  rows: InvoiceRecord[],
  phase: BulkPhase,
  pageLoading: boolean,
): BulkBarView {
  // CONSUMED, never re-derived: this exact array is what the confirm handler sends.
  const eligible = pruneSelection(selected, rows)
  // Page-scoped, independent of `selected` -- clicking submit-all is what selects them.
  const pageEligible = selectableIds(rows).length
  const notReady = rows.length - pageEligible
  const n = eligible.length
  // ONE gate, shared by both actions: nothing may be sent while the page the ids came
  // from is being replaced, and nothing may be sent twice.
  const open = !pageLoading && phase !== 'submitting'

  return {
    visible: n > 0,
    eligible,
    notReady,
    // The scope is IN the string. `pruneSelection` drops any id absent from `rows`, and
    // under server paging "absent from rows" means "on another page" -- so a bare
    // "12 selected" would be a lie about what a click sends at 500 invoices over 10 pages.
    countLabel: `${n} selected on this page`,
    note:
      notReady > 0
        ? `Only ${invoiceStatusStyle('validated').label} rows can be sent. ${notReady} of the ${rows.length} on this page cannot.`
        : null,
    submitLabel: `Submit ${n} for transmission`,
    // Singular at one, so the confirmation never reads "Send 1 invoices".
    confirmPrompt: `Send ${n} ${n === 1 ? 'invoice' : 'invoices'} for transmission?`,
    // Names the ACTION and the OBSERVABLE OUTCOME, never FIRS: `sandbox` is a mock
    // toggle and the environment banner already owns that claim in both of its states.
    // The irreversibility is backed by legalTransitions (store.go:1070-1076), which
    // gives `queued` no edge back to validated/draft -- it is not a scare sentence.
    confirmDetail: `They move to ${invoiceStatusStyle('queued').label} for transmission. Nothing on this screen can pull them back.`,
    confirmLabel: `Yes, send ${n} now`,
    canSubmit: n > 0 && open,
    // Page-scoped, never batch-wide: the server caps a batch submit at 200 ids with a
    // flat 400 and no partial progress, `cleanTotal` can be far larger, and there is no
    // chunking anywhere in this codebase.
    submitAllLabel: `Submit all ${pageEligible} on this page for transmission`,
    canSubmitAll: pageEligible > 0 && open,
  }
}

// The static chrome of the bar and its results panel. An 8th export beyond the task
// plan's §9 list, declared rather than smuggled: [bulk-copy-lives-in-the-lib] requires
// every user-visible string of this feature to sit in this file (LIB-SCAN-1 scans it
// whole), and these five are not derived from any count, so putting them on BulkBarView
// would make a view-model out of chrome.
export const BULK_COPY = {
  clear: 'Clear',
  cancel: 'Cancel',
  sending: 'Sending…',
  resultInvoice: 'Invoice #',
  resultOutcome: 'Result',
} as const

// NO `status` field, by design (AC-5): batch_submit.go's duplicate-request branch
// hard-codes a known-wrong `queued` status on a SKIPPED item (M5-11) -- omitting the
// field here makes that wrong value unrepresentable in the output, rather than merely
// asserted against.
export interface SubmitResultRow {
  invoiceNumber: string
  label: string
  enqueued: boolean
}

export interface BulkOutcome {
  results: SubmitResultRow[] | null
  clearSelection: boolean
}

// The SOLE builder of the results panel, on BOTH legs. A request-level failure (a 404 on
// an id that went stale between the click and the request) returns `results: null` and
// `clearSelection: false` -- no panel to read, and the selection survives so the operator
// can retry without re-picking rows.
//
// `items ?? []` is here rather than only at the call site because submitInvoices unwraps
// `.results` unguarded (invoices.ts:508-519) and SUB-3 pins a malformed 2xx resolving to
// `undefined` -- reaching `.map` on that would throw out of a click handler with no error
// boundary under it.
//
// `item.status` is read NOWHERE, and SubmitResultRow has no field for it: batch_submit.go
// hard-codes `queued` on a SKIPPED item in its duplicate-request branch (M5-11), so the
// label comes from `enqueued` + `reason` alone. Row badges are refetched, never derived
// from this response.
export function bulkOutcome(
  res: { ok: true; items: BatchSubmitResultItem[] | undefined } | { ok: false },
  numbersById: Map<string, string>,
): BulkOutcome {
  if (!res.ok) return { results: null, clearSelection: false }
  const items = res.items ?? []
  return {
    results: items.map((item) => ({
      // Captured BEFORE the post-submit refetch by the caller: looked up live, every row
      // would flicker to its raw uuid while the list reloads, and a submitted row may not
      // be on the page at all once it lands.
      invoiceNumber: numbersById.get(item.invoice_id) ?? item.invoice_id,
      // InvoicesList.tsx:313 verbatim. `reason` is optional and `enqueued` is a plain
      // boolean, not a discriminant, so `!enqueued` alone does not narrow it.
      label: item.enqueued ? 'Queued' : item.reason != null ? skipReasonLabel(item.reason) : 'Not queued',
      enqueued: item.enqueued,
    })),
    clearSelection: true,
  }
}

// --- INVCR-01-14 (task-290): row expansion — inline fix editor + per-invoice re-validate
//
// Field targeting is ENTIRELY server-driven (D8): fixCard reads violation.path through
// the SHIPPED mbsPathToEditField (lib/invoices.ts) alone -- no rule-key map is authored
// here or anywhere in this file, and none of the three rules §7.3's illustrative table
// invents (buyer-address-required, currency-enum, issue-date-open-period) are named
// below ([fix-editor-is-rule-agnostic]). A path with no counterpart (`line_items`,
// `invoice_number`, any APP-only vocabulary) resolves to `null`; the card's own
// `message` still carries the server's reason in full regardless -- `null` means "no
// field to flag", never "drop the reason".

// `hint` composes expected/actual (D9) as opaque strings, NEVER parsed or reformatted:
// both are decimal STRINGS end-to-end ([D13]), and re-running them through a numeric
// formatter here would risk the exact precision loss the string typing exists to avoid.
// `null` when `expected` is absent, matching the wire's own `omitempty` -- never an
// empty string, which would render as an expectation carrying nothing.
export interface FixCard {
  ruleKey: string
  severity: Severity
  message: string
  blocking: boolean
  field: EditFieldKey | null
  hint: string | null
}

function fixHint(v: Pick<Violation, 'expected' | 'actual'>): string | null {
  if (v.expected == null) return null
  return v.actual != null ? `Expected ${v.expected} · got ${v.actual}` : `Expected ${v.expected}`
}

// `blocking` is `severity === 'error'` alone -- the SAME predicate verdictPill uses
// (§10.12's trap), never `true` unconditionally: a warning-only violation still gets a
// card (so the operator can review and, where mappable, fix it), just styled
// non-blocking/advisory rather than suppressed.
export function fixCard(v: Violation): FixCard {
  return {
    ruleKey: v.rule_key,
    severity: v.severity,
    message: v.message,
    blocking: v.severity === 'error',
    field: mbsPathToEditField(v.path),
    hint: fixHint(v),
  }
}

// Field labels for the inline editor's own input, VERBATIM from InvoiceDetail.tsx's edit
// form labels -- a FIELD-name label, not a rule-key label, so this is generic across
// whichever of the 19 rules happens to flag that field and is not the client rule-key map
// D8 forbids. Kept here rather than duplicated per-card inline, per this file's own
// [bulk-copy-lives-in-the-lib] convention (see the INVCR-01-11 section header above).
export const EDIT_FIELD_LABELS: Record<EditFieldKey, string> = {
  issue_date: 'Issue date',
  supplier_tin: 'Supplier TIN',
  supplier_name: 'Supplier name',
  buyer_tin: 'Buyer TIN',
  buyer_name: 'Buyer name',
  currency: 'Currency',
  subtotal: 'Subtotal',
  vat: 'VAT',
  total: 'Total',
}

// §7.3's exact provenance/scope copy (AC-7), verbatim.
export const ROW_EXPANSION_NOTE = 'Re-validating touches this invoice only — the rest of the import stays as it is.'

export const ROW_EXPANSION_COPY = {
  sectionLabel: 'Failed rules · fix here, then re-validate this invoice only',
  // AC-8/§10.12's trap: a WARNING-ONLY invoice (zero errors, ≥1 warning) is not
  // "failing" -- the top-level verdict pill already renders it VALIDATED + advisory
  // (verdictPill above), and this section label must not contradict that by calling
  // its violations "failed rules". Synthetic today (all 19 shipped rules are error),
  // exercised only by FIX-10b/c's fixtures -- see `[warning-pill-unreachable-today]`.
  advisorySectionLabel: 'Advisory notes · nothing here is blocking this invoice',
  revalidateLabel: 'Re-validate this invoice',
  revalidating: 'Revalidating…',
  saveLabel: 'Save changes',
  saving: 'Saving…',
  // A WARNING beside Re-validate, never a gate (product-advisor review, pre-push): AC-4
  // pins the button's disabled state to `!inv.can_revalidate` alone, so an unsaved edit
  // must not block the click -- it only explains that clicking now re-checks the
  // invoice as LAST SAVED, not as currently typed.
  unsavedHint: 'You have unsaved changes — save them first, or re-validating will check the invoice as last saved.',
  // INVCR-01-15 (D6, task-291): the Keep as-is action, alongside Re-validate --
  // [bulk-copy-lives-in-the-lib] applies to this section exactly as it does to the
  // rest of ROW_EXPANSION_COPY above.
  keepLabel: 'Keep as-is',
  keeping: 'Keeping…',
  keepReasonPlaceholder: 'Why are you keeping this despite the failure? (required)',
  keptPrefix: 'Kept as-is — ',
} as const

export interface RowExpansionView {
  passing: boolean
  // True iff at least one violation is `severity === 'error'` -- the SAME predicate
  // verdictPill/fixCard use (§10.12's trap). Drives which of the two non-passing
  // section labels renders; a mix of one error and any number of warnings is still
  // blocking overall, matching verdictPill's own errorCount-first precedence.
  blocking: boolean
  summary: string | null
  // `null` iff `passing` -- the green summary line replaces it entirely, never both.
  sectionLabel: string | null
  cards: FixCard[]
  revalidateDisabled: boolean
  revalidateReason: string | null
  note: string
  // keptReason (INVCR-01-15, D6, task-291): the persisted reason, VERBATIM, iff this
  // invoice is currently kept -- `null` both when un-kept AND (belt-and-suspenders,
  // never a fabricated fallback) when kept_as_is_at is set but the server somehow sent
  // no reason. "The reason text is the point": this is the one field on this view-model
  // that exists solely so ReviewRow.tsx never has to read the server's copy off
  // anything other than this string.
  keptReason: string | null
}

// The row-expansion panel's WHOLE model, mirroring bulkBarView's own one-function
// composite: ReviewRow.tsx reads every field off THIS, never re-deriving any of it,
// including which of the two non-passing section labels to show. `passing` is
// `violations.length === 0` -- a genuinely CLEAN invoice, distinct from a warning-only
// one (which still renders its violations as non-blocking cards under the ADVISORY
// label, §10.12's trap) -- so the green summary line is unreachable while anything,
// even a single warning, fired. `can_revalidate`/`revalidate_blocked_reason` are
// consumed AS GIVEN -- this function performs no `?? false`/fallback-string authoring
// of its own; that discipline lives at the wire boundary (getInvoice, lib/invoices.ts)
// and is not repeated here.
export function rowExpansionView(
  invoice: {
    violations: Violation[]
    rule_set_version: number | null | undefined
    // Optional (INVCR-01-15, D6, task-291), mirroring VerdictInput.kept_as_is_at's own
    // optionality above: every REAL caller is InvoiceDetailRecord, which always carries
    // both, but every existing test call site in this file's own suite predates this
    // subtask and passes neither -- widening these as required would force each of
    // those to change for no behavioural reason of their own.
    kept_as_is_at?: string | null
    kept_as_is_reason?: string | null
  },
  gate: { can_revalidate: boolean; revalidate_blocked_reason: string | null },
): RowExpansionView {
  const passing = invoice.violations.length === 0
  const blocking = invoice.violations.some((v) => v.severity === 'error')
  return {
    passing,
    blocking,
    // D1: the app supplies the name, the server supplies the number -- ruleSetLabel
    // (defined above, INVCR-01-08) is the SAME function channelTiles/reviewHeader use,
    // never a second copy or a hardcoded fallback.
    summary: passing ? `Every rule in ${ruleSetLabel(invoice.rule_set_version)} passed.` : null,
    sectionLabel: passing ? null : blocking ? ROW_EXPANSION_COPY.sectionLabel : ROW_EXPANSION_COPY.advisorySectionLabel,
    cards: invoice.violations.map(fixCard),
    revalidateDisabled: !gate.can_revalidate,
    revalidateReason: gate.revalidate_blocked_reason,
    note: ROW_EXPANSION_NOTE,
    // Verbatim from the server, never a client-authored fallback -- `?? null` only
    // guards the (should-never-happen) case where kept_as_is_at is set but the reason
    // came back empty; it never SUBSTITUTES a made-up string for a real one.
    keptReason: invoice.kept_as_is_at != null ? (invoice.kept_as_is_reason ?? null) : null,
  }
}

// --- INVCR-01-15 (D6, task-291): `Keep as-is` ---
//
// canKeepAsIs is the WHOLE client-side gate for the Keep as-is action (KEEP-2): a
// reason is REQUIRED, and this is the ONE place that decides what "required" means --
// non-empty after trimming, so a click with only whitespace typed produces no request,
// exactly like reviewFilterReducer's own search-trim discipline above. ReviewRow.tsx
// consults this BEFORE calling keepInvoiceAsIs (lib/invoices.ts) and disables the
// button on the same predicate, so "no arm => no request" holds here the same way
// bulkPhaseReducer's identity-return makes it hold for the bulk bar.
//
// Server-side validation (KeepAsIsHandler, handlers.go) is the authoritative guard --
// this is advisory only, same division of labour as canRevalidate/can_revalidate
// elsewhere in this file: never trusted alone, but what stops an obviously-empty
// click from ever reaching the network.
export function canKeepAsIs(reason: string): boolean {
  return reason.trim().length > 0
}

// The row editor's own patch builder -- mirrors InvoiceDetail.tsx's private
// diffEditInput. That file is out of scope for this subtask (§14: no redesign of the
// invoice detail screen, and its helper is unexported besides), so this is a deliberate,
// documented duplication of its normalization rules, not independent reinvention.
// Iterates the DRAFT's own keys, never the full EDIT_FIELD_KEYS set -- this editor only
// ever touches the specific field(s) a fired rule flagged, so a field the operator never
// opened an editor for cannot appear in the patch even if some unrelated value happens
// to differ from `original`.
//
// issue_date is special-cased identically to InvoiceDetail.tsx: PATCH /v1/invoices/{id}
// decodes IssueDate into a *time.Time, which only accepts a full RFC3339 string, so a
// bare "YYYY-MM-DD" is normalized to midnight UTC before it is sent. A cleared ("")
// issue_date is DROPPED, never sent as "" -- JSON "null" and an absent key both decode to
// "leave unchanged" ([D9]), so an explicit clear cannot be represented over this PATCH at
// all, and sending "" would only surface a confusing decode failure.
export function fixEditPatch(
  original: Pick<InvoiceRecord, EditFieldKey>,
  draft: Partial<Record<EditFieldKey, string>>,
): InvoiceEditInput {
  const patch: InvoiceEditInput = {}
  for (const key of Object.keys(draft) as EditFieldKey[]) {
    const value = draft[key]
    if (value === undefined) continue
    if (value === (original[key] ?? '')) continue
    if (key === 'issue_date') {
      const trimmed = value.trim()
      if (!trimmed) continue
      patch.issue_date = /^\d{4}-\d{2}-\d{2}$/.test(trimmed) ? `${trimmed}T00:00:00Z` : trimmed
      continue
    }
    patch[key] = value
  }
  return patch
}
