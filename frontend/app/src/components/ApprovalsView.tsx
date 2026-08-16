// Approvals screen (APPR-12-03/04) -- the queue over GET /invoices?awaiting_approval=true
// plus its bulk-approve bar. InvoicesList's structural sibling: own useAsync, own entity
// gate, envelope kept whole, shared Pager fed the response's echoed pagination. No poll:
// this queue is always status='validated' (store.go:691), never queued|submitted, so
// shouldPollList would be provably always false here -- reusing it would be dead code. No
// whole-row click either (G6) -- detail navigation out of the queue is APPR-13's scope,
// deliberately omitted here.
//
// Bulk approve (APPR-12-04): arm, then confirm INLINE inside the same bar -- never a
// modal ([no-modal]). bulkPhaseReducer is imported from lib/reviewBatch and driven
// through ReviewInvoicesTab's toPhase() identity-bail idiom, so "do nothing" is expressed
// once, in a tested function. Two structural rules the refetch forces:
//   - the results panel and the bar render OUTSIDE every `state ===` rung. list.run()
//     nulls list.data (async-state.ts:47-48), which would destroy a nested panel at the
//     exact moment it should appear (G-04-D).
//   - nothing is optimistic: list.run() after settle is the affirmation, and no badge is
//     derived from a decision response.

import { useEffect, useMemo, useRef, useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import {
  APPROVALS_COPY,
  approvableIds,
  approvalOutcome,
  approvalProgressLabel,
  approvalRowView,
  approvalSelectAllState,
  approvalSelectRowLabel,
  approvalsBarView,
  approveInvoices,
  listAwaitingApproval,
  pruneApprovalSelection,
  type ApprovalPhase,
  type ApprovalResultRow,
} from '../lib/approvals'
import { fmt, fmtDate } from '../lib/format'
import {
  gateByActiveEntity,
  invoiceListIsEmpty,
  invoicesViewState,
  REGISTER_PAGE_SIZE,
  shouldFetchInvoices,
  toggleSelection,
  type InvoiceListResponse,
} from '../lib/invoices'
import { bulkPhaseReducer } from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'
import { Pager } from './Pager'

// Owed topology assertion: A07-5/A07-6 (APPR-12-07, assertFillsColumn over WIDE_WIDTHS)
// -- kept as ONE shared const across head and rows (InvoicesList.tsx:69's shape), not
// ClientsView.tsx's two-literal shape. The leading checkbox track is InvoicesList's 24px,
// this grid's own shape, not ReviewRow's 26px.
const APPROVALS_GRID_COLUMNS = '24px 140px 1fr 130px 90px 180px 110px'

// The bar's shared disabled treatment. `disabled` is the real gate; this only makes it
// visible (ReviewInvoicesTab.tsx:122's idiom).
function btnStyle(enabled: boolean) {
  return { height: 34, padding: '0 14px', fontSize: 12.5, opacity: enabled ? 1 : 0.45, cursor: enabled ? 'pointer' : 'not-allowed' }
}

// Tags the envelope with the entity it was fetched for -- same [dashboard-scope-per-client]
// staleness idiom as InvoicesList's FetchedInvoiceList.
type FetchedApprovalsList = InvoiceListResponse & { fetchedEntityId: string | undefined }

export function ApprovalsView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState<string[]>([])
  const [phase, setPhase] = useState<ApprovalPhase>('idle')
  const [results, setResults] = useState<ApprovalResultRow[] | null>(null)
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null)
  // The half `disabled` cannot cover: React batches, so two clicks in one tick both reach
  // the handler with the pre-render `phase` still armed (ReviewInvoicesTab.tsx:310-312).
  const approveInFlight = useRef(false)
  // In-house has no business_entities row ([entity-picker] trap 1) -- entity_id omitted
  // entirely, same convention as InvoicesList.tsx:83.
  const activeEntityId = ctx.mode === 'inhouse' ? undefined : (ctx.active.entityId ?? undefined)

  const list = useAsync<FetchedApprovalsList>(
    () =>
      base
        ? listAwaitingApproval(ctx.authedFetch, base, { entityId: activeEntityId, limit: REGISTER_PAGE_SIZE, offset }).then((r) => ({
            ...r,
            fetchedEntityId: activeEntityId,
          }))
        : Promise.reject(new Error('no gateway configured')),
    {
      isEmpty: invoiceListIsEmpty,
      immediate: shouldFetchInvoices(base),
      deps: [ctx.mode, ctx.active.entityId, offset],
    },
  )
  const state = invoicesViewState(base, list)
  const loading = state === 'loading'
  // !fresh: the envelope predates the active entity (a company switch's pre-refetch
  // frame) -- bounded and self-healing, same reasoning as InvoicesList.tsx:388-394.
  const fresh = list.data != null && list.data.fetchedEntityId === activeEntityId

  // G1: gateByActiveEntity is the render-time invariant on top of the server-side
  // entity_id filter -- useAsync's deps effect is a PASSIVE effect, so a company switch
  // commits and paints one frame holding the PREVIOUS entity's rows before the refetch's
  // 'start' dispatch (gateByActiveEntity's own doc comment, lib/invoices.ts). Unfiltered
  // rows here carry can_approve:true and are checkbox-selectable, so this is a
  // leak-prevention gate, not cosmetic (A03-8).
  //
  // MUST stay memoized: a fresh array every render would re-fire the prune effect below
  // forever, which is the half that actually throws "Maximum update depth exceeded"
  // (InvoicesList.tsx:136-148). A04-12 scans for both halves.
  const rows = useMemo(
    () => gateByActiveEntity(list.data?.invoices ?? [], ctx.mode === 'inhouse', ctx.active.entityId),
    [list.data, ctx.mode, ctx.active.entityId],
  )

  // Drops any selected id that left the visible set (paged away, or no longer approvable
  // after a refetch). The updater MUST return the SAME `sel` instance when nothing
  // changed -- otherwise every render hands `rows`' effect a new `selected` and React 19
  // hard-throws (AC-9).
  useEffect(() => {
    setSelected((sel) => {
      const next = pruneApprovalSelection(sel, rows)
      return next.length === sel.length ? sel : next
    })
    // A confirmation armed over page 1 must not still be armed over page 2's rows; the
    // `submitting` guard keeps an in-flight fan-out from being disarmed under itself.
    setPhase((p) => (p === 'submitting' ? p : 'idle'))
  }, [rows])

  const allState = approvalSelectAllState(selected, rows)
  const bar = approvalsBarView(selected, rows, phase, loading)

  // Every phase change goes through the pure reducer and bails on identity.
  function toPhase(action: Parameters<typeof bulkPhaseReducer>[1]): boolean {
    const next = bulkPhaseReducer(phase, action)
    if (next === phase) return false
    setPhase(next)
    return true
  }

  // ANY selection change invalidates an armed confirmation: arm 3 rows, untick them, tick
  // 5 others would otherwise sit armed one click away from a set nobody armed.
  function disarm() {
    toPhase({ type: 'cancel' })
  }

  function toggleAll() {
    setSelected(allState === 'all' ? [] : approvableIds(rows))
    disarm()
  }

  async function confirmApprove() {
    // Reachable for the first time here: this is the only list.run() call site outside
    // the ErrorState retry path (G-04-H, InvoicesList.tsx:283's precedent).
    if (base == null) return
    // `bar.eligible` is pruneApprovalSelection's output -- the ONLY thing that goes on
    // the wire, never `selected`.
    const ids = bar.eligible
    if (ids.length === 0) return
    // Identity IS "do nothing": no arm ⇒ no request.
    if (!toPhase({ type: 'confirm' })) return
    if (approveInFlight.current) return
    approveInFlight.current = true
    // Resolved from THIS page's rows before the refetch below nulls list.data -- looked
    // up live, every result row would flicker to a raw uuid.
    const numbersById = new Map(rows.map((row) => [row.id, row.invoice_number]))
    setProgress({ done: 0, total: ids.length })
    try {
      const res = await approveInvoices(ctx.authedFetch, base, ids, (_result, index) => setProgress({ done: index + 1, total: ids.length }))
      setResults(approvalOutcome(res, numbersById))
    } finally {
      // Cleared deliberately, not inherited from useAsync nulling list.data (G-04-E):
      // the RESULTS PANEL carries the partial failure, not the selection.
      setSelected([])
      setProgress(null)
      // Functional: this runs after an await, so the closure's `phase` is stale.
      setPhase((p) => bulkPhaseReducer(p, { type: 'settled' }))
      approveInFlight.current = false
      // The affirmation. Badges are NEVER derived from the decision responses above.
      list.run()
    }
  }

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 22 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>
          {APPROVALS_COPY.eyebrow}
        </div>
        <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>{APPROVALS_COPY.h1}</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>
          {ctx.user.tenantName ?? APPROVALS_COPY.tenantFallback} · {APPROVALS_COPY.subtitle}
        </p>
      </div>

      {/* The receipt, gated on `results !== null` ALONE and rendered OUTSIDE every rung
          below: the refetch nulls list.data, so a panel nested in the ready rung would be
          unmounted at the exact moment it should appear (G-04-D). Per-item rows only -- a
          headline count cannot say WHICH invoice was refused. */}
      {results !== null && (
        <div
          data-testid="approvals-results"
          style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden', marginBottom: 16 }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
            <span className="label">{APPROVALS_COPY.resultInvoice}</span>
            <span className="label">{APPROVALS_COPY.resultOutcome}</span>
          </div>
          {results.map((r, i) => (
            <div
              key={`${r.invoiceNumber}-${i}`}
              data-testid="approval-result-row"
              style={{ display: 'flex', justifyContent: 'space-between', gap: 12, padding: '10px 18px', borderBottom: '1px solid var(--line-1)' }}
            >
              <span className="mono" style={{ fontSize: 12.5, fontWeight: 500 }}>{r.invoiceNumber}</span>
              <span style={{ fontSize: 12.5, textAlign: 'right', color: r.ok ? 'var(--status-green-text)' : 'var(--fg-2)' }}>
                {r.label}
                {/* The server's own refusal, byte-identical -- this file authors no
                    substitute for it (AC-7's wire exception). */}
                {r.message != null && <span style={{ display: 'block', fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5 }}>{r.message}</span>}
              </span>
            </div>
          ))}
        </div>
      )}

      {/* Two stages in ONE bar, the confirm a borderTop-separated section inside it --
          never a modal ([no-modal]). Also a sibling of every rung: the selection it
          describes outlives the refetch that empties list.data. */}
      {bar.visible && (
        <div
          data-testid="approvals-bulk-bar"
          style={{ background: 'var(--action-tint)', border: '1px solid var(--teal-200)', borderRadius: 'var(--radius-md)', padding: '11px 14px', display: 'flex', flexDirection: 'column', gap: 9, marginBottom: 16 }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
            <span style={{ fontSize: 13, fontWeight: 500, color: 'var(--fg-1)' }}>{bar.countLabel}</span>
            <div style={{ display: 'flex', gap: 8, marginLeft: 'auto' }}>
              {phase === 'idle' ? (
                <>
                  <button
                    data-testid="approvals-bulk-clear"
                    onClick={() => {
                      setSelected([])
                      disarm()
                    }}
                    className="v2-btn v2-btn-ghost pf-btn"
                    style={btnStyle(true)}
                  >
                    {APPROVALS_COPY.clear}
                  </button>
                  <button
                    data-testid="approvals-bulk-submit"
                    onClick={() => toPhase({ type: 'arm' })}
                    disabled={!bar.canApprove}
                    className="v2-btn v2-btn-primary pf-btn"
                    style={btnStyle(bar.canApprove)}
                  >
                    {bar.submitLabel}
                  </button>
                </>
              ) : (
                <>
                  {/* Visibly disabled while submitting: approveInvoices takes no
                      AbortSignal, so there is nothing left to cancel. */}
                  <button
                    data-testid="approvals-bulk-cancel"
                    onClick={disarm}
                    disabled={phase === 'submitting'}
                    className="v2-btn v2-btn-ghost pf-btn"
                    style={btnStyle(phase !== 'submitting')}
                  >
                    {APPROVALS_COPY.cancel}
                  </button>
                  <button
                    data-testid="approvals-bulk-confirm"
                    onClick={() => void confirmApprove()}
                    disabled={!bar.canApprove}
                    className="v2-btn v2-btn-primary pf-btn"
                    style={btnStyle(bar.canApprove)}
                  >
                    {phase === 'submitting' ? APPROVALS_COPY.sending : bar.confirmLabel}
                  </button>
                </>
              )}
            </div>
          </div>

          {/* Page-scoped and cause-free: the count of rows select-all could not pick. The
              ROW states the cause, in the server's own words. */}
          {bar.note != null && (
            <p data-testid="approvals-bulk-note" style={{ fontSize: 11.5, color: 'var(--fg-2)', margin: 0, lineHeight: 1.55 }}>
              {bar.note}
            </p>
          )}

          {phase !== 'idle' && (
            <div style={{ borderTop: '1px solid var(--teal-200)', paddingTop: 9 }}>
              <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg-1)' }}>{bar.confirmPrompt}</div>
              <p style={{ fontSize: 11.5, color: 'var(--fg-2)', margin: '4px 0 0', lineHeight: 1.55 }}>{bar.confirmDetail}</p>
              {/* N sequential requests: the static "Approving…" on the button shows
                  activity, this shows how far along it is (D-b4). */}
              {progress != null && (
                <p data-testid="approvals-progress" className="mono" style={{ fontSize: 11.5, color: 'var(--fg-2)', margin: '6px 0 0' }}>
                  {approvalProgressLabel(progress.done, progress.total)}
                </p>
              )}
            </div>
          )}
        </div>
      )}

      {state === 'loading' && <Loading label={APPROVALS_COPY.loading} />}

      {state === 'error' && list.error && <ErrorState error={list.error} onRetry={list.run} />}

      {(state === 'idle' || state === 'empty') && (
        <div data-testid="approvals-empty">
          <EmptyState title={APPROVALS_COPY.emptyTitle} message={APPROVALS_COPY.emptyMessage} />
        </div>
      )}

      {state === 'ready' && list.data != null && !fresh && <Loading label={APPROVALS_COPY.loading} />}

      {state === 'ready' && list.data != null && fresh && rows.length === 0 && (
        <div data-testid="approvals-empty-page">
          <EmptyState title={APPROVALS_COPY.emptyPageTitle} message={APPROVALS_COPY.emptyPageMessage} />
          <div style={{ marginTop: 16 }}>
            <Pager pagination={list.data.pagination} busy={loading} onGo={setOffset} testId="approvals-pager" />
          </div>
        </div>
      )}

      {state === 'ready' && list.data != null && fresh && rows.length > 0 && (
        <>
          <div data-testid="approvals-list" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
            <div className="pf-list-head" style={{ display: 'grid', gridTemplateColumns: APPROVALS_GRID_COLUMNS, gap: 16, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)', alignItems: 'center' }}>
              <input
                type="checkbox"
                data-testid="approval-select-all"
                aria-label={APPROVALS_COPY.selectAllLabel}
                // React has no `indeterminate` prop (a DOM-only property, never a
                // reflected attribute) -- a ref callback is the only way to set it, and
                // the braces are required for `tsc --noEmit` under strict.
                ref={(el) => { if (el) el.indeterminate = allState === 'some' }}
                checked={allState === 'all'}
                onChange={toggleAll}
              />
              <span className="label">{APPROVALS_COPY.colInvoice}</span>
              <span className="label">{APPROVALS_COPY.colBuyer}</span>
              <span className="label" style={{ textAlign: 'right' }}>{APPROVALS_COPY.colAmount}</span>
              <span className="label">{APPROVALS_COPY.colStep}</span>
              <span className="label">{APPROVALS_COPY.colRole}</span>
              <span className="label">{APPROVALS_COPY.colDue}</span>
            </div>
            {rows.map((r) => {
              const av = approvalRowView(r)
              // Per-row, not the module const every other disabled-with-reason site uses:
              // N rows render at once and any number of them can be blocked
              // (ReviewAlreadyImportedTab.tsx:60-63).
              const reasonId = `approve-blocked-reason-${r.id}`
              const reason = av.approvable ? null : av.blockedReason
              return (
                <div
                  key={r.id}
                  data-testid="approval-row"
                  className="pf-row pf-list-row"
                  style={{ display: 'grid', gridTemplateColumns: APPROVALS_GRID_COLUMNS, gap: 16, padding: '14px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}
                >
                  <input
                    type="checkbox"
                    data-testid="approval-select-row"
                    aria-label={approvalSelectRowLabel(r.invoice_number)}
                    checked={selected.includes(r.id)}
                    // The server's own answer, fail-closed -- no client-side conjunct.
                    disabled={!av.approvable}
                    title={reason ?? undefined}
                    aria-describedby={reason != null ? reasonId : undefined}
                    // Disabled-only: on an enabled control this would kill the legitimate
                    // hover affordance platform.css leaves unguarded.
                    style={av.approvable ? undefined : { cursor: 'not-allowed', opacity: 0.5 }}
                    onChange={() => {
                      setSelected((sel) => toggleSelection(sel, r.id))
                      disarm()
                    }}
                  />
                  <span className="mono" style={{ fontSize: 12.5, fontWeight: 500, color: 'var(--fg-1)' }}>{r.invoice_number}</span>
                  <span style={{ fontSize: 13.5, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{r.buyer_name}</span>
                  <span className="money" style={{ fontSize: 13.5, fontWeight: 600, textAlign: 'right' }}>{r.total != null ? fmt(Number(r.total)) : '—'}</span>
                  <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>{av.stepLabel}</span>
                  <span style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
                    <span style={{ fontSize: 13, color: 'var(--fg-2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{av.roleLabel}</span>
                    {av.pendingHolderWarn && (
                      <span
                        data-testid="approval-unstaffed-warning"
                        title={APPROVALS_COPY.unstaffedSeat}
                        className="mono"
                        style={{ fontSize: 10, fontWeight: 600, color: 'var(--status-amber-text)', letterSpacing: '0.04em' }}
                      >
                        {APPROVALS_COPY.unstaffedSeat}
                      </span>
                    )}
                  </span>
                  <span className="mono" style={{ fontSize: 12, color: av.overdue ? 'var(--status-red-text)' : 'var(--fg-3)' }}>
                    {av.overdue ? <span data-testid="approval-overdue">{APPROVALS_COPY.overdue}</span> : av.dueAt != null ? fmtDate(av.dueAt) : '—'}
                  </span>
                  {/* Layer 3 of disabled-with-reason: the visible sibling a screenshot, a
                      keyboard user and a text assertion can all reach. An implicit second
                      grid row, so the seven cells above keep their positions. */}
                  {reason != null && (
                    <span id={reasonId} data-testid="approval-blocked-reason" style={{ gridColumn: '2 / -1', fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5 }}>
                      {reason}
                    </span>
                  )}
                </div>
              )
            })}
          </div>

          <div style={{ marginTop: 16 }}>
            <Pager pagination={list.data.pagination} busy={loading} onGo={setOffset} testId="approvals-pager" />
          </div>
        </>
      )}
    </div>
  )
}
