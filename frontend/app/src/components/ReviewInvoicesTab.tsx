// Review · Invoices tab — the compliance half of the review surface (INVCR-01-10,
// §7.2/§7.3, Core AC 7/10). Everything on screen is sourced from
// `GET /v1/invoices?import_batch_id=…` plus the batch-wide violation summary; NOTHING is
// filtered, counted, sorted or paged in the browser ([filters-are-server-side]).
// Server-paged plus client-filtered would make every count and every page number a lie
// at 500 invoices (D4).
//
// This file is markup and hook plumbing over pure helpers, in the shape InvoicesList.tsx
// established: the filter state machine, the request composer, the pill row, the rail and
// the pager's disable gates all live in lib/reviewBatch.ts, and the selection model is
// lib/invoices.ts' shipped one, re-derived nowhere.
//
// Extracted OUT of ReviewBatch.tsx rather than inlined into it: this tab needs
// `authedFetch`, owns its own filter state and runs two live queries, and hoisting that
// into the shell would put a pager reducer beside the §7.5 rejected-file surface. The
// asymmetry with ReviewUnreadableTab (a props-only renderer over frozen batch data) is
// the point — that channel is frozen, this one is live.
//
// FIVE things here look like extra work until you know why:
//
//  1. `useAsync<InvoiceListResponse>` over the WHOLE ENVELOPE, never `.invoices`.
//     asyncReducer's `success` arm nulls `data` whenever resolveStatus says empty, and
//     the default predicate calls a zero-length array empty (async-state.ts:47-52,64-68).
//     Unwrapping to the row array would make a zero-result filter set `data: null` and
//     DESTROY the `pagination` the pager reads — an honest-looking empty table with no
//     pager at all, and no way to page back out of it. `isEmpty: () => false` on both
//     queries says "empty is not a null" explicitly at the call site rather than resting
//     on a predicate defined in another package.
//
//  2. Keep-previous-page (`lastPage`). `dispatch({type:'start'})` nulls `data` on EVERY
//     re-run (async-state.ts:47), and `deps` here includes every filter field — so a
//     naive `page.data ?? []` would unmount the table and flash <Loading/> on every
//     keystroke. `page.data ?? lastPage.current` keeps the SAME envelope reference across
//     the start→success gap, so `rows` changes identity only when a genuinely new page
//     lands (which is also what keeps the prune effect below from looping). The ref is
//     written only in an effect, never during render. It is CLEARED on error, so a
//     pre-error page can never ghost back under a retry of a different query.
//
//  3. The rail is a SECOND useAsync with `deps: [batchId]` alone. Its request structurally
//     cannot carry a pill, a search or an offset, so a filter change never refetches it
//     and never blinks it. Its counts are batch-wide and stay batch-wide — disclosed in
//     two words on the eyebrow (`· WHOLE BATCH`) rather than an authored sentence, because
//     the pager's own `SHOWING x–y OF n` is the single on-screen claim about the visible
//     rows. A rail error renders an honest one-liner and a Retry, NEVER silence: AC-4
//     makes rail silence mean "nothing failed", so a false absence is exactly the failure
//     this story's counter discipline exists to prevent.
//
//  4. NO `gateByActiveEntity` and no `entity_id`, unlike InvoicesList — same reason
//     ReviewBatch.tsx gives: the batch id already narrows to one entity, so narrowing
//     AGAIN by the workspace switcher would render a partially-empty batch on a
//     sibling-entity deep link, and it would look correct in every local test.
//
//  5. The bulk bar's THREE structural placements (INVCR-01-11). (a) The submit and
//     confirm buttons are gated on `loading`, because point 2's keep-previous serves the
//     PREVIOUS page's ids across the whole loading gap — the table is dimmed, never
//     disabled, so an ungated confirm after `Next →` sends invoices the user is no
//     longer looking at and gets a 200 for it. (b) The results panel is a sibling gated
//     on `results !== null` ALONE: nested under `selected.length > 0` a successful
//     submit's own `setSelected([])` would unmount it the instant it should appear
//     (InvoicesList.tsx:299-302 records hitting exactly that), and nested under the
//     `shown != null` branch a later page error would destroy the receipt. (c) The
//     confirmation is a second action INSIDE the bar, not a modal: this app has no
//     confirm-modal precedent, WorkflowsView.tsx:124-126 refuses to invent one, and the
//     repo's only such modal (a different SPA) records no Escape, no focus trap and no
//     `role="dialog"` — a11y debt this would inherit for one button.
//
// The `Ln` (line count) column of §7.3's grid is NOT built: Store.List returns invoice
// HEADERS with LineItems left nil by design (store.go:478-479), so a line count has no
// data source, and putting one on the list wire shape would change the response of three
// shipped endpoints. The grid is decision 19's 7-column form.

import { useEffect, useMemo, useReducer, useRef, useState } from 'react'

import { ErrorState, Loading, toApiError, useAsync, type ApiError } from '@invoice-os/api-client'

import { searchGlyph } from '../glyphs'
import {
  listInvoices,
  newIdempotencyKey,
  pruneSelection,
  selectableIds,
  selectAllState,
  submitInvoices,
  toggleSelection,
  violationSummary,
  type InvoiceListResponse,
  // From lib/invoices, NOT lib/dashboard — that module exports an identically-shaped
  // `RuleCount` of its own, so the wrong import compiles and is silently wrong.
  type RuleCount,
} from '../lib/invoices'
import {
  BULK_COPY,
  bulkBarView,
  bulkOutcome,
  bulkPhaseReducer,
  initialReviewFilter,
  pagerLabels,
  pagerNav,
  railPills,
  reviewFilterReducer,
  reviewPageQuery,
  reviewPills,
  type BulkPhase,
  type SubmitResultRow,
} from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'
// The row + its row-expansion fix editor (INVCR-01-14). REVIEW_GRID_COLUMNS/GAP are
// owned there now (not duplicated here): the row and this table's own head row must
// never disagree about the grid, and a single export is what makes that structural
// rather than remembered.
import { REVIEW_GRID_COLUMNS, REVIEW_GRID_GAP, Row } from './ReviewRow'

// The repo's first debounce. 300ms is short enough that the committed value lands
// before the user looks up, long enough that a 12-character buyer name is one request
// and not twelve.
const SEARCH_DEBOUNCE_MS = 300

// The bulk bar's shared disabled treatment. `disabled` is the real gate; this only makes
// it visible, matching the Pager's own `btn` idiom 400 lines below (deliberately NOT
// hoisted out of it — that component holds a second, unrelated height).
function btnStyle(enabled: boolean) {
  return { height: 34, padding: '0 14px', fontSize: 12.5, opacity: enabled ? 1 : 0.45, cursor: enabled ? 'pointer' : 'not-allowed' }
}

export function ReviewInvoicesTab({
  ctx,
  base,
  batchId,
  totals,
  onSubmitted,
}: {
  ctx: PlatformCtx
  // Both NON-NULLABLE: the shell has already checked them before mounting this tab, and
  // the types say so rather than re-checking and inventing a second empty state.
  base: string
  batchId: string
  // Fired after a successful bulk submit so the SHELL re-runs its four count queries.
  // Without it the row badges correctly read QUEUED while all four pills, the green
  // tile, the `Invoices (N)` tab label and the footer stay at their pre-submit numbers —
  // and clicking `Ready to submit` then fires a query whose result contradicts the badge
  // above it. The shell keeps its previous data across that refresh (`lastShell`), so
  // this tab is NOT unmounted by it.
  onSubmitted: () => void
  // The four pill counts, fetched by the SHELL in one Promise.all. Passing them down
  // makes AC-2 structurally true (each count IS its own filtered response's
  // pagination.total) and guarantees the `Ready to submit` pill can never disagree with
  // the header's green tile 40px above it — same number, not two queries.
  totals: { allTotal: number; cleanTotal: number; failingTotal: number; queuedTotal: number }
}) {
  const [filter, dispatch] = useReducer(reviewFilterReducer, initialReviewFilter)
  // The RAW draft stays here so the user always sees what they typed; only the trimmed,
  // debounced value reaches the reducer (and therefore the wire).
  const [draft, setDraft] = useState('')

  // WorkflowBuilder.tsx:73-78's cleanup idiom: each keystroke cancels the timer in
  // flight rather than stacking a second one. The mount-time run commits `''` against an
  // already-`''` state, which reviewFilterReducer returns IDENTICALLY — so a tab mount
  // costs zero extra requests.
  useEffect(() => {
    const t = window.setTimeout(() => dispatch({ type: 'search', q: draft }), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(t)
  }, [draft])

  // ONE composed, server-side AND of every active filter. `deps` lists the filter's
  // FIELDS, not the state object: the reducer returns the identical object on a no-op, so
  // field-wise comparison and object identity agree — but spelling the fields out makes
  // the refetch triggers readable at the call site.
  //
  // ⚠️ WHATEVER THIS PRODUCER READS MUST APPEAR IN `deps`. useAsync's fetch effect is
  // keyed on `opts.deps` alone and never on `run`, so a producer that closes over some
  // future piece of state left out of the list below would pin a STALE closure and keep
  // serving the old query — silently, with no error and no loading state to notice.
  // A superseded in-flight response is already safe (useAsync's own `runId` cleanup
  // discards it before it can dispatch); this is the other half of that guarantee.
  const page = useAsync<InvoiceListResponse>(
    () => listInvoices(ctx.authedFetch, base, reviewPageQuery(batchId, filter)),
    { isEmpty: () => false, deps: [batchId, filter.pill, filter.ruleKey, filter.q, filter.offset] },
  )

  // `deps: [batchId]` — one fetch per batch, for the life of the tab. See the file
  // header, point 3.
  const rail = useAsync<RuleCount[]>(() => violationSummary(ctx.authedFetch, base, [batchId]), {
    isEmpty: () => false,
    deps: [batchId],
  })

  const lastPage = useRef<InvoiceListResponse | null>(null)
  useEffect(() => {
    if (page.data != null) lastPage.current = page.data
    // Cleared on error so a retry of a DIFFERENT query cannot ghost the pre-error page
    // back under it, dimmed, as if it were that query's own result.
    else if (page.error != null) lastPage.current = null
  }, [page.data, page.error])

  // Referentially stable across the loading gap: `page.data` going null falls back to the
  // very same object the ref holds. That is what keeps `rows` (and therefore the prune
  // effect) from firing on a transition that changed no data.
  const shown = page.data ?? lastPage.current
  const rows = useMemo(() => shown?.invoices ?? [], [shown])

  const [selected, setSelected] = useState<string[]>([])
  // Row expansion (INVCR-01-14) — AT MOST ONE row expanded at a time, so the panel's own
  // getInvoice fetch is never more than one in flight. Local state, like `selected`: the
  // tab-switch unmount already resets it along with everything else in this component
  // (task-287 QA Stage 4's cross-referenced finding, this subtask's own Implementation
  // Notes), so there is no persistence to build.
  const [expandedId, setExpandedId] = useState<string | null>(null)
  // The bulk-submit machine. `useState`, not `useReducer`: the contract is
  // `const next = bulkPhaseReducer(phase, a); if (next === phase) return` — identity has
  // to be able to stop the REQUEST, and useReducer's identity return only suppresses a
  // re-render (the handler would fire anyway).
  const [phase, setPhase] = useState<BulkPhase>('idle')
  const [results, setResults] = useState<SubmitResultRow[] | null>(null)
  const [submitError, setSubmitError] = useState<ApiError | null>(null)
  // Checked and set SYNCHRONOUSLY, before anything async — React batches state updates,
  // so a fast double-click re-enters the handler before `disabled` re-renders. The
  // reducer's `submitting` identity arm is the structural half of the same guard; this
  // ref is the half that closes the window `disabled` alone loses
  // (InvoicesList.tsx:160-166 / App.tsx's `reqInFlight`).
  const submitInFlight = useRef(false)
  // AC-6: an id that left the visible set leaves the selection — and under SERVER paging
  // "absent from `rows`" now means "on another page", which is what holds up the
  // zero-margin 200-id batch-submit cap InvoicesList.tsx:217-227 rests on (selection
  // never spans pages; page size stays 50, never 200).
  //
  // The updater MUST return the SAME `sel` instance when nothing changed: `rows` would
  // otherwise get a new `selected` on every render, re-triggering this effect, and React
  // 19 hard-throws "Maximum update depth exceeded" (InvoicesList.tsx:143-148, shipped and
  // hard-won).
  useEffect(() => {
    setSelected((sel) => {
      const next = pruneSelection(sel, rows)
      return next.length === sel.length ? sel : next
    })
    // A confirmation armed over page 1 must not still be armed over page 2's rows. The
    // `submitting` guard keeps an unrelated `rows` change (a debounce landing) from
    // re-enabling the button mid-request, and both branches return the SAME value when
    // nothing changed, so this cannot loop the way an unguarded updater would.
    setPhase((p) => (p === 'submitting' ? p : 'idle'))
  }, [rows])

  const allState = selectAllState(selected, rows)
  const pills = reviewPills(totals, filter.pill)
  const railList = railPills(rail.data ?? [], filter.ruleKey)
  const filtersActive = filter.pill !== 'all' || filter.ruleKey != null || filter.q !== ''
  const loading = page.status === 'loading'
  const bar = bulkBarView(selected, rows, phase, loading)

  // Every phase change goes through the pure reducer and bails on identity — "do
  // nothing" is expressed once, in a tested function, rather than as an `if` per handler.
  function toPhase(action: Parameters<typeof bulkPhaseReducer>[1]): boolean {
    const next = bulkPhaseReducer(phase, action)
    if (next === phase) return false
    setPhase(next)
    return true
  }

  // ANY selection change invalidates an armed confirmation, for the same reason the key
  // is minted late: arm 3 rows → untick them → tick 5 others would otherwise re-show the
  // bar already armed, one click away from sending a set the operator never armed. It
  // also drops a stale error from the superseded selection (InvoicesList.tsx:203).
  function disarm() {
    setSubmitError(null)
    toPhase({ type: 'cancel' })
  }

  function toggleAll() {
    setSelected(allState === 'all' ? [] : selectableIds(rows))
    disarm()
  }

  // Click 1 selects every eligible row on the page AND arms; click 2 runs the SAME
  // submit() the bar's confirm runs, over ONE shared `phase` — so the footer and the bar
  // can never hold two confirmations at once or disagree about which one is armed.
  // Recorded edge: if the operator narrows the selection between the two clicks, click 2
  // sends that narrower set (it is `bar.eligible`, always), not the whole page.
  function submitAll() {
    if (phase === 'idle') {
      setSelected(selectableIds(rows))
      toPhase({ type: 'arm' })
      return
    }
    void submit()
  }

  async function submit() {
    // `bar.eligible` is pruneSelection's output — the ONLY thing that ever goes on the
    // wire. Never `selected`: an id that left the page or lost `validated` since the
    // click would otherwise 404 the whole batch. Empty is reachable from the footer
    // (its gate is page-scoped, so unticking every row leaves it enabled at zero
    // eligible) and an empty `invoice_ids` is a 400 — checked BEFORE the transition
    // below, or the bar would sit in `submitting` with no request to settle it.
    const ids = bar.eligible
    if (ids.length === 0) return
    // Identity IS "do nothing": no arm ⇒ no request (AC-3), and a second confirm cannot
    // re-enter a request the server already has.
    if (!toPhase({ type: 'confirm' })) return
    if (submitInFlight.current) return
    submitInFlight.current = true
    setSubmitError(null)
    // Resolved from THIS page's rows before the refetch below nulls `page.data`: looked
    // up live, every result row would flicker to a raw uuid, and a submitted invoice may
    // leave the page entirely under the `Ready to submit` pill.
    const numbersById = new Map(rows.map((row) => [row.id, row.invoice_number]))
    try {
      // A FRESH key per attempt, minted HERE and held nowhere. Arm → cancel → change the
      // selection → arm → confirm would reuse a key minted at arm time; the server then
      // derives the identical per-invoice dedupe key, skips the enqueue, runs NO
      // transition, and the panel reads "Already submitted with this request" while the
      // invoice silently strands at validated. Freshness is the invariant, not
      // stability: BatchSubmit re-reads status FOR UPDATE and skips anything not
      // validated, so a fresh key on a crash-retry is safe by construction.
      const res = await submitInvoices(ctx.authedFetch, base, ids, newIdempotencyKey())
      // submitInvoices unwraps `.results` unguarded and SUB-3 pins a malformed 2xx
      // resolving to `undefined` — normalised here as well as inside bulkOutcome.
      const items = res ?? []
      const outcome = bulkOutcome({ ok: true, items }, numbersById)
      setResults(outcome.results)
      if (outcome.clearSelection) setSelected([])
      // Functional, unlike the click-driven transitions above: this runs AFTER an await,
      // so the closure's `phase` is stale by definition — the reducer must read the
      // phase React actually holds.
      setPhase((p) => bulkPhaseReducer(p, { type: 'settled' }))
      // BOTH refetches, neither optional. `page.run()` moves the row badges to QUEUED;
      // `onSubmitted()` moves the shell's four counts, the tiles, the tab label and the
      // footer. One without the other is a screen that contradicts itself.
      // Badges are NEVER derived from the response above: batch_submit.go's
      // duplicate-request branch hard-codes a known-wrong status (M5-11).
      page.run()
      onSubmitted()
    } catch (err) {
      // A request-level failure (a 404 on one stale id fails the WHOLE batch — there is
      // no per-item result for it) keeps the selection so the operator can retry without
      // re-picking rows, and shows NO results panel. `results: null` also clears a panel
      // left by an earlier submit, which would otherwise read as this attempt's receipt.
      const outcome = bulkOutcome({ ok: false }, numbersById)
      setResults(outcome.results)
      setSubmitError(toApiError(err))
      setPhase((p) => bulkPhaseReducer(p, { type: 'settled' }))
    } finally {
      submitInFlight.current = false
    }
  }

  return (
    <div data-testid="review-invoices-tab" style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      {/* §7.3 toolbar — the four filter pills and the search field. */}
      <div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          {pills.map((p) => (
            <button
              key={p.id}
              data-testid="review-filter-pill"
              onClick={() => dispatch({ type: 'pill', pill: p.id })}
              aria-pressed={p.active}
              className="pf-chip"
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: 7,
                height: 30,
                padding: '0 12px',
                borderRadius: 'var(--radius-md)',
                fontFamily: 'var(--font-sans)',
                fontSize: 12.5,
                fontWeight: 500,
                border: `1px solid ${p.active ? 'var(--action)' : 'var(--line-2)'}`,
                background: p.active ? 'var(--action)' : 'var(--bg-2)',
                color: p.active ? 'var(--text-on-dark)' : 'var(--fg-2)',
              }}
            >
              {p.label}
              <span className="mono" style={{ fontSize: 11, opacity: 0.75 }}>
                {p.count}
              </span>
            </button>
          ))}

          {/* Header.tsx:66-69's search shape, WITHOUT its `.pf-header-search` class:
              that class carries a `display:none` at ≤480px which is right for a global
              header affordance and wrong for the only way to find a row in a 500-invoice
              batch. */}
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              height: 30,
              padding: '0 12px',
              marginLeft: 'auto',
              border: '1px solid var(--line-2)',
              borderRadius: 'var(--radius-input)',
              background: 'var(--bg-2)',
              width: 240,
            }}
          >
            <span style={{ color: 'var(--fg-3)', display: 'inline-flex' }}>{searchGlyph}</span>
            {/* NO maxLength. The server's cap is 200 BYTES and maxLength counts UTF-16
                units, so it cannot enforce it — and a client truncation would turn
                "find X" into "find a prefix of X", which is precisely what the handler
                refuses to do on the server side. An over-long paste surfaces the
                server's own message through the ErrorState below. */}
            <input
              data-testid="review-search"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder="Find an invoice or buyer"
              aria-label="Find an invoice or buyer"
              style={{
                flex: 1,
                minWidth: 0,
                border: 0,
                outline: 'none',
                background: 'transparent',
                fontFamily: 'var(--font-sans)',
                fontSize: 13,
                color: 'var(--fg-1)',
              }}
            />
          </div>
        </div>
        <p style={{ fontSize: 11.5, color: 'var(--fg-3)', margin: '9px 0 0', lineHeight: 1.55 }}>
          These are verdicts and queues, not lifecycle states.
        </p>
      </div>

      {/* §7.3's failing-rules rail — the primary anti-drowning affordance at 500
          invoices, more important than the pager. Batch-wide by construction: railPills
          takes no page and no invoice array, so a page-derived rail is unrepresentable. */}
      {rail.error != null && (
        <div
          data-testid="review-rail"
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 12,
            flexWrap: 'wrap',
            background: 'var(--bg-2)',
            border: '1px solid var(--line-2)',
            borderRadius: 'var(--radius-md)',
            padding: '11px 14px',
          }}
        >
          {/* Spoken, never silent. An empty rail MEANS "nothing failed" (AC-4), so a
              failed summary fetch that rendered nothing would be a false all-clear. */}
          <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>Could not load the failing-rules summary.</span>
          <button
            onClick={rail.run}
            className="v2-btn v2-btn-ghost pf-btn"
            style={{ marginLeft: 'auto', height: 30, padding: '0 12px', fontSize: 12.5 }}
          >
            Retry
          </button>
        </div>
      )}

      {rail.error == null && railList.length > 0 && (
        <div
          data-testid="review-rail"
          style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '12px 14px' }}
        >
          {/* `· WHOLE BATCH` is the whole disclosure that these counts do not narrow with
              the pill or the search — two words on the eyebrow, not an authored sentence
              explaining a table the user can already read. */}
          <div className="eyebrow" style={{ marginBottom: 9 }}>
            FAILING RULES · WHOLE BATCH
          </div>
          <div style={{ display: 'flex', gap: 7, flexWrap: 'wrap' }}>
            {railList.map((r) => (
              <button
                key={r.ruleKey}
                data-testid="review-rail-pill"
                onClick={() => dispatch({ type: 'rule', ruleKey: r.ruleKey })}
                aria-pressed={r.active}
                className="pf-chip mono"
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 7,
                  height: 26,
                  padding: '0 10px',
                  borderRadius: 'var(--radius-md)',
                  fontSize: 11,
                  fontWeight: 500,
                  border: `1px solid ${r.active ? 'var(--action)' : 'var(--line-2)'}`,
                  background: r.active ? 'var(--action)' : 'var(--bg-3)',
                  color: r.active ? 'var(--text-on-dark)' : 'var(--fg-2)',
                }}
              >
                {r.ruleKey}
                <span style={{ opacity: 0.75 }}>{r.count}</span>
              </button>
            ))}
          </div>
          <p style={{ fontSize: 11.5, color: 'var(--fg-3)', margin: '9px 0 0', lineHeight: 1.55 }}>
            At this size you fix by rule, not by row — one bad mapping usually explains a whole cluster.
          </p>
        </div>
      )}

      {/* The receipt. Gated on `results !== null` ALONE and rendered OUTSIDE both the
          selection branch and the `shown != null` branch — see the file header, point 5b.
          Per-item rows only: a headline "N submitted" would have to come from
          `items.filter(i => i.enqueued)`, and the one derived from `selected.length`
          instead reports a server-skipped invoice as sent.
          It deliberately SURVIVES a new selection and a new arm, and is replaced only by
          the next outcome: its per-row skip labels are exactly what tells the operator
          which invoices to re-pick, so clearing it the moment they start doing that would
          destroy the information driving the action. */}
      {results !== null && (
        <div
          data-testid="review-submit-results"
          style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
            <span className="label">{BULK_COPY.resultInvoice}</span>
            <span className="label">{BULK_COPY.resultOutcome}</span>
          </div>
          {results.map((r, i) => (
            <div key={`${r.invoiceNumber}-${i}`} style={{ display: 'flex', justifyContent: 'space-between', gap: 12, padding: '10px 18px', borderBottom: '1px solid var(--line-1)' }}>
              <span className="mono" style={{ fontSize: 12.5, fontWeight: 500 }}>{r.invoiceNumber}</span>
              <span style={{ fontSize: 12.5, color: r.enqueued ? 'var(--status-green-text)' : 'var(--fg-3)' }}>{r.label}</span>
            </div>
          ))}
        </div>
      )}

      {/* A request-level failure. NO `onRetry`, deliberately: retrying is arming and
          confirming again in the bar below, which AC-3 requires and which the intact
          selection makes a two-click job. A one-click Retry here would either fire a
          request with no confirmation, or — since `confirm` from `idle` is the reducer's
          identity arm — be a button that does nothing at all. The error clears on the
          next selection change (`disarm`), so it can never outlive the selection it
          describes. */}
      {submitError != null && (
        <div data-testid="review-submit-error">
          <ErrorState error={submitError} />
        </div>
      )}

      {/* §7.3's bulk bar. Accent-tinted per AC-1, using the ENV_BANNER.live token pair. */}
      {bar.visible && (
        <div
          data-testid="review-bulk-bar"
          style={{ background: 'var(--action-tint)', border: '1px solid var(--teal-200)', borderRadius: 'var(--radius-md)', padding: '11px 14px', display: 'flex', flexDirection: 'column', gap: 9 }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
            <span style={{ fontSize: 13, fontWeight: 500, color: 'var(--fg-1)' }}>{bar.countLabel}</span>
            <div style={{ display: 'flex', gap: 8, marginLeft: 'auto' }}>
              {phase === 'idle' ? (
                <>
                  <button
                    data-testid="review-bulk-clear"
                    onClick={() => {
                      setSelected([])
                      disarm()
                    }}
                    className="v2-btn v2-btn-ghost pf-btn"
                    style={btnStyle(true)}
                  >
                    {BULK_COPY.clear}
                  </button>
                  <button
                    data-testid="review-bulk-submit"
                    onClick={() => toPhase({ type: 'arm' })}
                    disabled={!bar.canSubmit}
                    className="v2-btn v2-btn-primary pf-btn"
                    style={btnStyle(bar.canSubmit)}
                  >
                    {bar.submitLabel}
                  </button>
                </>
              ) : (
                <>
                  <button
                    data-testid="review-bulk-cancel"
                    onClick={disarm}
                    disabled={phase === 'submitting'}
                    className="v2-btn v2-btn-ghost pf-btn"
                    style={btnStyle(phase !== 'submitting')}
                  >
                    {BULK_COPY.cancel}
                  </button>
                  <button
                    data-testid="review-bulk-confirm"
                    onClick={() => void submit()}
                    disabled={!bar.canSubmit}
                    className="v2-btn v2-btn-primary pf-btn"
                    style={btnStyle(bar.canSubmit)}
                  >
                    {phase === 'submitting' ? BULK_COPY.sending : bar.confirmLabel}
                  </button>
                </>
              )}
            </div>
          </div>

          {/* Page-scoped and cause-free — it answers "why did select-all pick 12 of 50?",
              which is the only version of §7.3's split note this screen can honestly make:
              a non-selectable row may be failing, mid-edit, or already sent, and only its
              own verdict pill knows which. */}
          {bar.note != null && (
            <p data-testid="review-bulk-note" style={{ fontSize: 11.5, color: 'var(--fg-2)', margin: 0, lineHeight: 1.55 }}>
              {bar.note}
            </p>
          )}

          {/* The second stage. Inside the bar, beside the count it qualifies — not a
              modal (file header, point 5c). */}
          {phase !== 'idle' && (
            <div style={{ borderTop: '1px solid var(--teal-200)', paddingTop: 9 }}>
              <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg-1)' }}>{bar.confirmPrompt}</div>
              <p style={{ fontSize: 11.5, color: 'var(--fg-2)', margin: '4px 0 0', lineHeight: 1.55 }}>{bar.confirmDetail}</p>
            </div>
          )}
        </div>
      )}

      {/* Error is checked FIRST, before any stale page can render: rows the user could
          mistake for the result of the query that just failed are worse than no rows. */}
      {page.error != null && <ErrorState error={page.error} onRetry={page.run} />}

      {page.error == null && shown == null && <Loading label="Reading this batch…" />}

      {page.error == null && shown != null && (
        <>
          <div
            data-testid="review-table"
            style={{
              background: 'var(--bg-2)',
              border: '1px solid var(--line-1)',
              borderRadius: 'var(--radius-md)',
              overflow: 'hidden',
              // Dimmed, not unmounted, while the next page or filter is in flight —
              // see the file header, point 2.
              opacity: loading ? 0.55 : 1,
              transition: 'opacity 120ms ease-out',
            }}
          >
            <div
              className="pf-list-head"
              style={{ display: 'grid', gridTemplateColumns: REVIEW_GRID_COLUMNS, gap: REVIEW_GRID_GAP, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)', alignItems: 'center' }}
            >
              <input
                type="checkbox"
                data-testid="review-select-all"
                aria-label="Select all validated invoices on this page"
                // React has no `indeterminate` prop (a DOM-only property, never a
                // reflected attribute) — a ref callback is the only way to set it, and
                // the braces are required for `tsc --noEmit` under strict.
                ref={(el) => { if (el) el.indeterminate = allState === 'some' }}
                checked={allState === 'all'}
                onChange={toggleAll}
              />
              <span className="label">Invoice #</span>
              <span className="label">Buyer</span>
              <span className="label">Issue date</span>
              <span className="label" style={{ textAlign: 'right' }}>Total</span>
              <span className="label">Verdict</span>
              <span />
            </div>

            {rows.length === 0 ? (
              // TWO branches because they are two different facts: a filter that matched
              // nothing is recoverable by clearing it, and a batch that produced no
              // invoices at all is not.
              <div style={{ padding: '22px 18px', fontSize: 13, color: 'var(--fg-3)' }}>
                {filtersActive ? 'No invoices match these filters.' : 'No invoices were created from this import.'}
              </div>
            ) : (
              rows.map((r) => (
                <Row
                  key={r.id}
                  r={r}
                  checked={selected.includes(r.id)}
                  expanded={expandedId === r.id}
                  // Toggling the SAME row collapses it; expanding a different row moves
                  // the single expansion slot rather than stacking a second one open.
                  onToggleExpand={() => setExpandedId((id) => (id === r.id ? null : r.id))}
                  onToggle={() => {
                    setSelected((sel) => toggleSelection(sel, r.id))
                    disarm()
                  }}
                  ctx={ctx}
                  base={base}
                  // A successful Save or Re-validate moved this invoice's server-side
                  // state — refetch THIS page (moves the row's own verdict pill) and the
                  // shell's four count queries (moves the pills/tiles/tab label/footer),
                  // the identical pair the bulk-submit path already fires for the same
                  // reason.
                  onChanged={() => {
                    page.run()
                    onSubmitted()
                  }}
                />
              ))
            )}
          </div>

          {/* §7.3's pager. Every number comes off the RESPONSE's echoed pagination, never
              REVIEW_PAGE_SIZE — the handler silently clamps `limit` down to 200, and a
              client constant here would hide a clamp instead of showing it. */}
          <Pager
            pagination={shown.pagination}
            busy={loading}
            onGo={(offset) => dispatch({ type: 'page', offset })}
          />

          {/* AC-7's footer action, built HERE rather than in the shell footer: §7.3's
              Footer paragraph is inside the Invoices tab, the selection it operates on
              lives in this component, and the shell footer also renders under the
              Unreadable-rows tab where a submit-all is nonsense.
              PAGE-SCOPED, and that is a hard limit rather than a choice — the endpoint
              caps a submit at 200 ids with a flat 400 carrying no partial progress, a
              filtered total can be far larger, and nothing in this repo chunks. */}
          <div style={{ display: 'flex' }}>
            <button
              data-testid="review-submit-all"
              onClick={submitAll}
              disabled={!bar.canSubmitAll}
              className="v2-btn v2-btn-ghost pf-btn"
              style={btnStyle(bar.canSubmitAll)}
            >
              {bar.submitAllLabel}
            </button>
          </div>
        </>
      )}
    </div>
  )
}

// §7.3's pager. `pagerLabels` and `pagerNav` both read the RESPONSE's echoed pagination;
// this component holds no page-size constant of its own.
function Pager({
  pagination,
  busy,
  onGo,
}: {
  pagination: { limit: number; offset: number; total: number }
  busy: boolean
  onGo: (offset: number) => void
}) {
  const nav = pagerNav(pagination)
  const labels = pagerLabels(pagination)
  // Disabled while a page is in flight as well as at the ends: `nav` is computed from the
  // PREVIOUS response during that window, so a second click would recompute the same
  // offset off stale numbers.
  const btn = (enabled: boolean) => ({
    height: 32,
    padding: '0 12px',
    fontSize: 12.5,
    opacity: enabled ? 1 : 0.4,
    cursor: enabled ? 'pointer' : 'not-allowed',
  })
  const canPrev = nav.canPrev && !busy
  const canNext = nav.canNext && !busy

  return (
    <div data-testid="review-pager" style={{ display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap' }}>
      <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>{labels.showing}</span>
      <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>{labels.page}</span>
      <div style={{ display: 'flex', gap: 8, marginLeft: 'auto' }}>
        <button onClick={() => onGo(nav.prevOffset)} disabled={!canPrev} className="v2-btn v2-btn-ghost pf-btn" style={btn(canPrev)}>
          ← Previous
        </button>
        <button onClick={() => onGo(nav.nextOffset)} disabled={!canNext} className="v2-btn v2-btn-ghost pf-btn" style={btn(canNext)}>
          Next →
        </button>
      </div>
    </div>
  )
}
