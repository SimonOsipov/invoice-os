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
// FOUR things here look like extra work until you know why:
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
// The `Ln` (line count) column of §7.3's grid is NOT built: Store.List returns invoice
// HEADERS with LineItems left nil by design (store.go:478-479), so a line count has no
// data source, and putting one on the list wire shape would change the response of three
// shipped endpoints. The grid is decision 19's 7-column form.

import { useEffect, useMemo, useReducer, useRef, useState } from 'react'

import { ErrorState, Loading, useAsync } from '@invoice-os/api-client'

import { chevDownGlyph, searchGlyph } from '../glyphs'
import { fmt, fmtDate } from '../lib/format'
import {
  isRowSelectable,
  listInvoices,
  pruneSelection,
  selectableIds,
  selectAllState,
  toggleSelection,
  violationSummary,
  type InvoiceListResponse,
  type InvoiceRecord,
  // From lib/invoices, NOT lib/dashboard — that module exports an identically-shaped
  // `RuleCount` of its own, so the wrong import compiles and is silently wrong.
  type RuleCount,
} from '../lib/invoices'
import {
  initialReviewFilter,
  pagerLabels,
  pagerNav,
  railPills,
  reviewFilterReducer,
  reviewPageQuery,
  reviewPills,
  verdictPill,
} from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'

// Decision 19's grid, minus the 40px `Ln` track (see the file header):
// select-all · Invoice # (mono) · Buyer · Issue date · Total · Verdict · chevron.
// At 1280×720 the fixed tracks sum to 500px and the six 9px gaps to 54px, leaving the
// `1fr` buyer column ~349px inside this screen's 903px track box — far above its 120px
// floor, with no max-width wrapper anywhere in the chain.
const REVIEW_GRID_COLUMNS = '26px 122px minmax(120px,1fr) 92px 114px 124px 22px'
const REVIEW_GRID_GAP = 9

// The repo's first debounce. 300ms is short enough that the committed value lands
// before the user looks up, long enough that a 12-character buyer name is one request
// and not twelve.
const SEARCH_DEBOUNCE_MS = 300

export function ReviewInvoicesTab({
  ctx,
  base,
  batchId,
  totals,
}: {
  ctx: PlatformCtx
  // Both NON-NULLABLE: the shell has already checked them before mounting this tab, and
  // the types say so rather than re-checking and inventing a second empty state.
  base: string
  batchId: string
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
  const rail = useAsync<RuleCount[]>(() => violationSummary(ctx.authedFetch, base, batchId), {
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
  }, [rows])

  const allState = selectAllState(selected, rows)
  const pills = reviewPills(totals, filter.pill)
  const railList = railPills(rail.data ?? [], filter.ruleKey)
  const filtersActive = filter.pill !== 'all' || filter.ruleKey != null || filter.q !== ''
  const loading = page.status === 'loading'

  function toggleAll() {
    setSelected(allState === 'all' ? [] : selectableIds(rows))
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
                  onOpen={() => ctx.openImportedInvoice(r.id)}
                  onToggle={() => setSelected((sel) => toggleSelection(sel, r.id))}
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
        </>
      )}
    </div>
  )
}

// One invoice row. A LOCAL sub-component, not a ReviewRow.tsx: it has exactly one call
// site and lifting it into its own module would buy nothing but an import.
function Row({
  r,
  checked,
  onOpen,
  onToggle,
}: {
  r: InvoiceRecord
  checked: boolean
  onOpen: () => void
  onToggle: () => void
}) {
  // `kept_as_is_at` is deliberately NOT passed: it is not on InvoiceRecord, subtask 15
  // owns putting it on the wire, and typing it as present now would repeat the shipped
  // `rule_set_version` trap (typed `number | null`, reads `undefined` on list rows —
  // which is also why no rule-set version is rendered per row here).
  const verdict = verdictPill({ status: r.status, violations: r.violations })
  const badge = verdict.badges[0]

  // Click-only row, matching the shipped InvoicesList.tsx precedent. Keyboard activation
  // (role/tabIndex/onKeyDown) for BOTH row surfaces is task-302 — no AC here covers it,
  // and a fake `<a href>` is not an option: this SPA has no router.
  return (
    <div
      onClick={onOpen}
      data-testid="review-row"
      className="pf-row pf-list-row"
      style={{ display: 'grid', gridTemplateColumns: REVIEW_GRID_COLUMNS, gap: REVIEW_GRID_GAP, padding: '14px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}
    >
      <input
        type="checkbox"
        data-testid="review-select"
        aria-label={`Select invoice ${r.invoice_number}`}
        checked={checked}
        disabled={!isRowSelectable(r.status)}
        // BOTH handlers stop propagation — the row's own onClick is whole-row navigation
        // and must never fire from a checkbox interaction.
        onClick={(e) => e.stopPropagation()}
        onChange={(e) => {
          e.stopPropagation()
          onToggle()
        }}
      />
      <span className="mono" style={{ fontSize: 12.5, fontWeight: 500, color: 'var(--fg-1)' }}>{r.invoice_number}</span>
      {/* Buyer name + TIN, InvoicesList.tsx:419-422's treatment verbatim: this is the
          compliance review surface and `buyer-tin-format` is a live rule, so a missing
          TIN is the single most useful thing this column can shout about. */}
      <span style={{ minWidth: 0 }}>
        <span style={{ display: 'block', fontSize: 13.5, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{r.buyer_name ?? '—'}</span>
        <span className="mono" style={{ fontSize: 11, color: r.buyer_tin ? 'var(--fg-3)' : 'var(--status-red-text)' }}>{r.buyer_tin ?? 'TIN MISSING'}</span>
      </span>
      {/* NO `?? created_at` fallback, unlike InvoicesList.tsx:424 — that column is
          labelled "Date"; this one says "Issue date", and labelling a creation timestamp
          as the issue date is a small lie on a compliance screen. */}
      <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>{r.issue_date != null ? fmtDate(r.issue_date) : '—'}</span>
      <span className="money" style={{ fontSize: 13.5, fontWeight: 600, textAlign: 'right' }}>{r.total != null ? fmt(Number(r.total)) : '—'}</span>
      {/* The status badge is InvoicesList.tsx:425-433's markup verbatim, driven entirely
          by verdictPill(...).status — no colour and no label is authored here. The
          derived badge stacks BENEATH rather than beside it: 124px cannot hold both. */}
      <span style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start', gap: 4, minWidth: 0 }}>
        <span
          data-testid="review-verdict"
          style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: verdict.status.bg, border: `1px solid ${verdict.status.border}`, borderRadius: 999, padding: '3px 9px' }}
        >
          <span style={{ width: 6, height: 6, borderRadius: 99, background: verdict.status.text }} />
          <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: verdict.status.text, letterSpacing: '0.04em' }}>{verdict.status.label}</span>
        </span>
        {badge != null && (
          <span
            className="mono"
            style={{ display: 'inline-flex', alignItems: 'center', background: badge.tone.bg, border: `1px solid ${badge.tone.border}`, borderRadius: 999, padding: '2px 8px', fontSize: 9.5, fontWeight: 600, color: badge.tone.text, letterSpacing: '0.04em' }}
          >
            {badge.label}
          </span>
        )}
      </span>
      {/* An INDICATOR for the whole-row navigation above, not a control: no pointer
          events, hidden from assistive tech. Subtask 14 replaces it with the interactive
          row-disclosure and owns resolving navigate-vs-expand. */}
      <span aria-hidden style={{ display: 'inline-flex', color: 'var(--fg-4)', pointerEvents: 'none', transform: 'rotate(-90deg)' }}>{chevDownGlyph}</span>
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
