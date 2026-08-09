// Invoice detail dispatcher: for an imported invoice, mounts the live detail surface
// (status pill, line items + totals, compliance/violations panel, fiscal record, APP
// rejection reasons, the Edit / Re-validate actions bar + inline edit mode, failed dead
// end, status history) fetched from the gateway; otherwise renders an honest EmptyState
// ("No invoice selected"). INVED-01-07 split the former fused "Fix & re-validate" card
// into two independently-gated actions ([actions-visibility], [edit-ux]). The
// Platform.dc.html-ported mock detail branch — fabricated fiscal record (IRN/CSID/QR),
// the "Transmit to FIRS" affordance, synthesized audit trail, and mock validation/totals
// — was removed in M5-09-04 ([mock-branch-fully-removed]); the real fiscal record and APP
// rejection cards below (M5-09-05) render only server-sourced data.

import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { closeGlyph, docGlyph2, plusGlyph } from '../glyphs'
import { actorLabel } from '../lib/actor'
import { fmt, fmtDate, fmtDateTime, fmtPlain } from '../lib/format'
import { detailTarget } from '../lib/importReport'
import {
  BUYER_TIN_MISSING,
  canResolveOutside,
  computedLineSum,
  DETAIL_SUBMIT_COPY,
  diffEditInput,
  diffLineItems,
  editInvoice,
  failureExplanation,
  formFromInvoice,
  getInvoice,
  getInvoiceHistory,
  invoiceStatusStyle,
  isBuyerTinMissing,
  keptAsIs,
  LIVE_POLL_MS,
  newIdempotencyKey,
  reasonFieldFlags,
  rejectionProvenance,
  resolveInvoiceOutside,
  RESOLVE_OUTSIDE_COPY,
  resolvedOutside,
  revalidateInvoice,
  shouldFetchInvoices,
  shouldPollInvoice,
  shouldRefreshHistory,
  shouldShowFiscalRecord,
  shouldShowRejectionCard,
  singleSubmitOutcome,
  submitInvoices,
  unresolveInvoiceOutside,
  verdictStatus,
  type EditFieldKey,
  type EditFormState,
  type InvoiceDetailRecord,
  type InvoiceRecord,
  type InvoiceStatus,
  type StatusChange,
} from '../lib/invoices'
import { bulkPhaseReducer, ROW_EXPANSION_COPY, type BulkPhase } from '../lib/reviewBatch'
import { getSourceDocument, type SourceDocumentResponse } from '../lib/sourceDocument'
import { useDocumentVisible, useLiveRefresh } from '../lib/useLiveRefresh'
import { SourceDocumentCard } from './SourceDocumentCard'
import { SourceDocumentModal } from './SourceDocumentModal'
import { ViolationsTable } from './ViolationsTable'
import { XmlModal } from './XmlModal'
import type { PlatformCtx } from '../types'

export function InvoiceDetail({ ctx }: { ctx: PlatformCtx }) {
  const { selectedId } = ctx

  // Click-through from the import report (M4-08-05, AC7). This MUST return before the
  // "no invoice selected" EmptyState below: an imported invoice is a real server UUID,
  // and rendering the empty state instead would silently drop a valid click-through.
  // ([click-through-honest-placeholder])
  // M4-09-05: this branch mounts the live detail surface (LiveInvoiceDetail, below).
  // M5-09-04: the mock detail branch that used to render beneath this check (fabricated
  // fiscal record, "Transmit to FIRS", synthesized audit trail) is deleted — selecting no
  // invoice now renders an honest EmptyState instead of a fabricated one
  // ([mock-branch-fully-removed]).
  const target = detailTarget({ selectedId, importedInvoiceId: ctx.importedInvoiceId })
  if (target.kind === 'imported') {
    // key={invoiceId}: forces a full remount on invoice SWITCH so the previous invoice's
    // local state (edit-form field values, staleSinceEdit, revalidateError) doesn't leak
    // into the next one. The key stays stable while invoiceId is unchanged, so the
    // in-place history/detail refresh after edit/re-validate within one invoice is
    // unaffected — only switching invoices remounts.
    return <LiveInvoiceDetail key={target.invoiceId} ctx={ctx} invoiceId={target.invoiceId} />
  }

  return (
    <div style={{ padding: '24px 36px 56px' }}>
      <button onClick={() => ctx.nav('invoices')} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 32, padding: '0 12px', fontSize: 13, marginBottom: 18 }}>
        ← All invoices
      </button>
      <EmptyState title="No invoice selected" />
    </div>
  )
}

// --- Live detail surface (M4-09-05, task-186) -------------------------------
//
// Own component (not extra hooks bolted onto InvoiceDetail's conditional-return body)
// because InvoiceDetail's `target.kind === 'imported'` branch returns before this point —
// calling useAsync/useState after that return would break the rules of hooks. Mirrors
// ClientsView/ValidationView: gatewayBase() + useAsync + a Loading/ErrorState/ready
// ladder, zero network when no gateway is configured.
// One editable line row (INVED-01-07). LineItemEditInput-shaped but with '' where the wire
// carries null, because a controlled React input holds '' and never null. Deliberately no
// `id` and no `line_no`: line_no is system-assigned 1..N by array POSITION
// ([line-no-by-position], lib/invoices.ts:234-245), so this array's order IS the wire's
// line ordering, and diffLineItems compares by position over the five content fields only.
type LineRowState = Record<'description' | 'quantity' | 'unit_price' | 'line_total' | 'line_tax', string>

// Six columns: description / qty / unit / amount / tax / remove. Declared once so the
// header row and the body rows can never drift apart (ValidationView.tsx repeats the
// literal in both places; one const is the same shape without that hazard).
//
// Widths are budgeted against the row's REAL content box, which is narrow: this table
// sits in the left cell of the page's `1fr 340px` split, so at a viewport V the row has
// V - 772px to spend (V, less the 252px sidebar + scrollbar, the 36px page gutters, the
// 340px right rail + 16px gap, and the card/table borders + 24px card and 14px row
// padding). At V=1280 that is 508px. The five fixed tracks used to total 432px and the
// five 10px gaps another 50, leaving the flexible description track 26px -- narrower than
// its own input chrome, so the text was invisible. Trimming the numerics (qty holds 2-4
// digits, not 5) hands description 164px at 1280.
//
// Deliberately NO `minmax()` floor on description: 508px is fully spent at 1280, so a
// floor at the resolved width buys nothing there and below it would push the row past a
// parent that clips (`overflow: hidden`), hiding the remove button rather than the
// description. A bare `1fr` degrades by shrinking, which is the safer failure. The
// numeric inputs override .pf-input's 12px side padding down to 8px so the trimmed tracks
// still show ~6 mono characters instead of ~5.
const LINE_EDIT_GRID = '1fr 52px 70px 70px 70px 32px'

function rowsFromInvoice(inv: InvoiceRecord): LineRowState[] {
  return (inv.line_items ?? []).map((it) => ({
    description: it.description ?? '',
    quantity: it.quantity ?? '',
    unit_price: it.unit_price ?? '',
    line_total: it.line_total ?? '',
    line_tax: it.line_tax ?? '',
  }))
}

// `aria-describedby` target for the disabled Re-validate button's reason text. A module
// const rather than useId(): InvoiceDetail renders exactly one LiveInvoiceDetail at a time
// (it returns a single element), so this id cannot collide with itself in one document.
const REVALIDATE_REASON_ID = 'revalidate-blocked-reason-text'

// Same rationale as REVALIDATE_REASON_ID above; a distinct id so the two disabled buttons'
// aria-describedby targets never collide on a rejected invoice, where both render together.
const SUBMIT_REASON_ID = 'submit-blocked-reason-text'

// Same rationale again; a third distinct id, so no two disabled controls'
// aria-describedby targets collide when they render together.
const VIEW_UBL_REASON_ID = 'view-ubl-blocked-reason-text'

// Same rationale again; a fourth distinct id, for the resolve-outside button.
const RESOLVE_OUTSIDE_REASON_ID = 'resolve-outside-blocked-reason-text'

function LiveInvoiceDetail({ ctx, invoiceId }: { ctx: PlatformCtx; invoiceId: string }) {
  const base = gatewayBase()
  // Same `base ? … : …` narrowing as ClientsView/ValidationView ([A-e]/[A-m]) —
  // `immediate: shouldFetchInvoices(base)` keeps a no-gateway build at zero network.
  const detail = useAsync<InvoiceDetailRecord>(
    () => (base ? getInvoice(ctx.authedFetch, base, invoiceId) : Promise.reject(new Error('no gateway configured'))),
    { immediate: shouldFetchInvoices(base), deps: [invoiceId] },
  )
  const history = useAsync<StatusChange[]>(
    () => (base ? getInvoiceHistory(ctx.authedFetch, base, invoiceId) : Promise.reject(new Error('no gateway configured'))),
    { immediate: shouldFetchInvoices(base), deps: [invoiceId] },
  )
  // The source-document record, shared by the right-rail card and the previewer modal —
  // one fetch, two readers. `immediate: shouldFetchInvoices(base)` is not stylistic: the
  // topology specs gate on an unfiltered console collector, and Chromium logs a failed
  // request as a console error. Safe as specified — the endpoint returns 200 with
  // `document: null` for a manually created invoice.
  const source = useAsync<SourceDocumentResponse>(
    () => (base ? getSourceDocument(ctx.authedFetch, base, invoiceId) : Promise.reject(new Error('no gateway configured'))),
    { immediate: shouldFetchInvoices(base), deps: [invoiceId] },
  )
  const [previewOpen, setPreviewOpen] = useState(false)
  // Stable — `useDismiss` re-registers its listeners on every identity change.
  const closePreview = useCallback(() => setPreviewOpen(false), [])
  const openPreview = useCallback(() => setPreviewOpen(true), [])
  const [ublOpen, setUblOpen] = useState(false)
  const closeUbl = useCallback(() => setUblOpen(false), [])

  // M5-09-07 live-refresh overlay ([poll-overlay-not-rerun]) -- HOISTED above the status
  // ladder below: hooks can't be called from inside a conditional branch, and `inv` (the
  // ladder's rendered record) now has to exist before the ladder decides what to render.
  // A poll tick NEVER calls detail.run() (THE LOAD-BEARING TRAP -- that would dispatch
  // useAsync's 'start' action, null `data`, and flash <Loading/> every 2s); it writes
  // this overlay instead, and `inv` layers it over detail.data.
  const [live, setLive] = useState<InvoiceDetailRecord | null>(null)
  const inv = live ?? detail.data

  // `gen` closes the in-flight-tick race ([poll-overlay-not-rerun], mirrors InvoicesList's
  // own runId idiom, packages/api-client/src/async-state.ts:89/92/96). `can_edit`
  // (draft/validated/rejected) and isInFlight (queued/submitted) are disjoint, so
  // Save/Re-validate can't be clicked while a NEW tick gets scheduled -- but clearInterval
  // only stops FUTURE ticks; it does not cancel a tick's getInvoice() promise that was
  // already in flight. Reachable sequence: a tick fires while `queued`, a LATER tick
  // observes `rejected` and polling stops, the operator clicks Save or Re-validate, then
  // the first tick's promise finally resolves and would overwrite the fresh result with
  // the stale `queued` record. Bumped wherever the overlay is invalidated (handleSaved,
  // handleRevalidate), alongside the existing setLive(null).
  const gen = useRef(0)

  // CodeRabbit fix cycle 2, finding 4: overlapping ticks. useLiveRefresh's interval
  // fires unconditionally (it holds no data and makes no decisions -- see
  // lib/useLiveRefresh.ts), so a round-trip slower than LIVE_POLL_MS leaves two
  // getInvoice() calls in flight under the SAME `gen`; the older can resolve last and
  // re-install stale data for one visible tick before the next tick self-heals it. `gen`
  // alone doesn't close this -- it only guards against a tick that survived an
  // invalidation (Save/Re-validate), not two ticks racing each other under one
  // invalidation epoch. Same re-entrancy-ref idiom as App.tsx's `reqInFlight`
  // (App.tsx:106-113) and InvoicesList's `submitInFlight`: checked and set synchronously
  // before the async call, so a fast-firing second tick can't lose the race the way a
  // state flag would.
  const tickInFlight = useRef(false)

  // shouldRefreshHistory's `prev` (M5-09-03 predicate, [history-refresh-predicate]) --
  // seeded from the loaded record so the FIRST observed transition isn't silently
  // dropped: shouldRefreshHistory(null, x) === false (I-hist-2), and the mock's default
  // (non-reserved-TIN) path converges queued -> accepted in ~800ms of adapter latency
  // plus near-immediate River pickup, so a detail opened at `queued` can legitimately see
  // `accepted` on its very first tick. Updated inside the tick strictly AFTER the
  // shouldRefreshHistory comparison, never before.
  const prevStatus = useRef<InvoiceStatus | null>(null)
  useEffect(() => {
    if (detail.data) prevStatus.current = detail.data.status
  }, [detail.data])

  const visible = useDocumentVisible()
  const active = inv != null && shouldPollInvoice(inv.status, visible)
  useLiveRefresh(
    () => {
      if (base == null) return
      if (tickInFlight.current) return // finding 4: previous tick's GET hasn't resolved yet
      tickInFlight.current = true
      const g = gen.current
      getInvoice(ctx.authedFetch, base, invoiceId)
        .then((fresh) => {
          // CodeRabbit fix cycle 2, finding 1: the whole resolved body now lives inside
          // this guard, not just setLive. A discarded tick (g !== gen.current, e.g. the
          // operator hit Save/Re-validate while this GET was in flight) must have NO side
          // effects. Invoice statuses only advance monotonically, so a stale `fresh.status`
          // can never equal the next genuine transition's status -- the history refresh
          // was never actually at risk of being silently skipped. The real bug is a
          // discarded tick still calling history.run() and flashing the timeline card for
          // a transition the UI is about to discard anyway.
          if (g !== gen.current) return
          setLive(fresh)
          if (shouldRefreshHistory(prevStatus.current, fresh.status)) history.run()
          prevStatus.current = fresh.status
        })
        .catch(() => {}) // a transient blip is silent -- the next tick retries (AC-6)
        .finally(() => { tickInFlight.current = false })
    },
    active,
    LIVE_POLL_MS,
  )

  // Within-session fix-loop indicator (Core AC #7 / [stale-violations-honest] /
  // [stale-is-session-state]): set on a successful edit, cleared on Re-validate. On
  // initial load this stays false, so the stored verdict renders WITHOUT a stale banner
  // — the on-load honesty derivation is [stale-on-load-followup], deferred.
  const [staleSinceEdit, setStaleSinceEdit] = useState(false)
  const [revalidating, setRevalidating] = useState(false)
  const [revalidateError, setRevalidateError] = useState<string | null>(null)

  // Single-invoice submit machine, reusing the bulk-submit reducer verbatim
  // ([no-bulk-on-detail]) -- always [invoiceId], never a selection.
  const [submitPhase, setSubmitPhase] = useState<BulkPhase>('idle')
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [submitSkipped, setSubmitSkipped] = useState<string | null>(null)
  const submitInFlight = useRef(false)

  // Resolve-outside control (failed invoices only, Core AC #1/#4/#5/#6). Both handlers DO
  // gen-bump / setLive(null) before detail.run() (handleRevalidate's precedent) -- `live`
  // can still hold a stale snapshot from watching queued -> failed earlier in this session.
  const [resolveReason, setResolveReason] = useState('')
  const [resolving, setResolving] = useState(false)
  const [undoing, setUndoing] = useState(false)
  const [resolveOutsideError, setResolveOutsideError] = useState<string | null>(null)

  // Inline edit mode ([edit-ux]/[edit-mode-in-body], INVED-01-07). The ONLY new state this
  // component gains: the editor itself is a child mounted only while true, seeding its own
  // field/row state once at mount, so Cancel is just setEditing(false) -> unmount -> state
  // discarded, and re-opening re-seeds from the current `inv`. Nothing to reset by hand.
  // Safe because `inv` cannot mutate underneath a mounted editor: polling runs only while
  // isInFlight (queued/submitted, lib/invoices.ts:683-688), which is disjoint from the
  // can_edit set the Edit button lives behind (see the `gen` comment above).
  const [editing, setEditing] = useState(false)

  let content: ReactNode

  // invoicesViewState (lib/invoices.ts) is pinned to AsyncState<InvoiceRecord[]> (the
  // list surface's shape) and can't type-check against this single-record fetch, so the
  // same base==null -> idle short-circuit is inlined here rather than widening that
  // helper — this subtask's edit map scopes changes to InvoiceDetail.tsx only.
  if (base == null) {
    content = <EmptyState title="No gateway configured" message="Connect a gateway to load this invoice." />
  } else if (detail.status === 'loading') {
    content = <Loading label="Loading invoice…" />
  } else if (detail.status === 'error') {
    content = detail.error ? <ErrorState error={detail.error} onRetry={detail.run} /> : null
  } else if (inv == null) {
    content = <EmptyState title="Invoice not found" message="This invoice could not be loaded." />
  } else {
    const st = invoiceStatusStyle(inv.status)
    const items = inv.line_items ?? []
    const subtotal = inv.subtotal != null ? Number(inv.subtotal) : null
    const vat = inv.vat != null ? Number(inv.vat) : null
    const total = inv.total != null ? Number(inv.total) : null
    const verdict = verdictStatus(staleSinceEdit, inv)
    const failure = failureExplanation(inv.failure_kind)
    // A live rejection leads the rail, matching failed-dead-end's position; a demoted/
    // historical one stays below Compliance so it doesn't overstate a resolved event.
    const rejectionLeadsRail = rejectionProvenance(inv.status) === 'current'
    const rejectionCard = shouldShowRejectionCard(inv) ? (
      <div data-testid="rejection-reasons" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
        <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)' }}>
          <span className="card-title">
            {rejectionProvenance(inv.status) === 'current' ? 'This invoice was rejected' : 'Last APP rejection'}
          </span>
        </div>
        <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 10 }}>
          {inv.rejection_reasons.map((reason, i) => (
            <div
              key={i}
              data-testid="rejection-reason-row"
              style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)' }}
            >
              <div className="mono" style={{ fontSize: 11, fontWeight: 600, color: 'var(--status-red-text)' }}>{reason.code}</div>
              <div style={{ fontSize: 12.5, color: 'var(--fg-2)', marginTop: 3 }}>{reason.message}</div>
            </div>
          ))}
        </div>
      </div>
    ) : null
    // keptAsIs has no status field to test; gate here since the mark means "kept as-is" only on a draft.
    const kept = inv.status === 'draft' ? keptAsIs(inv) : null
    // Unlike keptAsIs above, resolvedOutside self-gates on status === 'failed', so no
    // extra status check is needed here.
    const resolvedMark = resolvedOutside(inv)
    // Two independent reasons to disable, styled identically: the wire says the action is
    // unavailable (persistent, carries a reason), or one is already in flight (transient,
    // the label says "Revalidating…"). No status comparison -- `can_revalidate` only.
    const revalidateDisabled = !inv.can_revalidate || revalidating
    const resolveOutsideDisabled = !inv.can_resolve_outside || resolving || !canResolveOutside(resolveReason)

    // Arrow functions (not `function` declarations): narrowing of `base` to non-null
    // (established by the `if (base == null)` branch above) does not survive into a
    // nested function DECLARATION — TS resets it there because declarations are
    // hoisted — but does survive into a closure/arrow function.
    const handleSaved = () => {
      // FIRST: leave edit mode before the refresh below flips the ladder to <Loading/>, so
      // the editor can never remount against a half-refreshed record (INVED-01-07).
      setEditing(false)
      setStaleSinceEdit(true)
      // M5-09-07: clear the poll overlay BEFORE detail.run(), so this user-initiated
      // refresh's own result -- success or error -- is what renders next, never a stale
      // `live` value ([poll-overlay-not-rerun]). Unreachable while a tick is in flight
      // (`can_edit` is draft/validated/rejected only, never queued/submitted -- see A2),
      // but required for the queued->rejected->edit path, where `live` still holds the
      // rejected record from polling that has since stopped.
      // gen bump: invalidates any tick whose getInvoice() promise was ALREADY in flight
      // when the rejected->edit transition happened -- clearInterval stopped it from
      // scheduling again, but not from resolving later and clobbering this fresh result
      // with the stale record it fetched (QA finding, [poll-overlay-not-rerun]).
      gen.current++
      // A skip/error banner from a submit attempt describes the PRE-edit record --
      // it must not survive onto the one this save just produced.
      setSubmitPhase('idle')
      setSubmitSkipped(null)
      setSubmitError(null)
      setLive(null)
      detail.run()
      history.run()
    }

    // INVED-01-07: the button this drives is now DISABLED whenever `!inv.can_revalidate`,
    // so the old "click it on an untouched validated/rejected invoice and eat a 409
    // (ErrNotDraft) from Store.ApplyValidation's draft-only gate" path is unreachable from
    // the UI -- that dead end is precisely what this story removes. The catch stays: it is
    // still the surface for a GENUINE failure (network blip, 5xx, a race where the wire
    // said can_revalidate but the row moved on), never for a self-inflicted gate hit.
    const handleRevalidate = async () => {
      if (revalidating) return
      setRevalidating(true)
      setRevalidateError(null)
      try {
        await revalidateInvoice(ctx.authedFetch, base, invoiceId)
        setStaleSinceEdit(false)
        gen.current++ // see handleSaved above -- invalidate any already-in-flight tick too
        // See handleSaved above -- a stale submit banner must not survive a re-validate either.
        setSubmitPhase('idle')
        setSubmitSkipped(null)
        setSubmitError(null)
        setLive(null) // see handleSaved above -- clear the overlay before the real refresh
        detail.run()
        history.run()
      } catch (err) {
        setRevalidateError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
      } finally {
        setRevalidating(false)
      }
    }

    // Same reducer as ReviewInvoicesTab's bulk bar ([no-bulk-on-detail]) -- identity IS
    // "do nothing", so an unarmed confirm or a second confirm mid-flight fires no request.
    const toSubmitPhase = (action: Parameters<typeof bulkPhaseReducer>[1]): boolean => {
      const next = bulkPhaseReducer(submitPhase, action)
      if (next === submitPhase) return false
      setSubmitPhase(next)
      return true
    }

    const handleSubmit = async () => {
      if (!inv.can_submit) return
      if (!toSubmitPhase({ type: 'confirm' })) return // no arm => no request
      if (submitInFlight.current) return
      submitInFlight.current = true
      setSubmitError(null)
      setSubmitSkipped(null)
      try {
        // Minted HERE, not at arm time, so every confirmed click gets a fresh key
        // ([fresh-key-per-confirm]) -- a retry after a failed attempt must not replay
        // the dead one and get silently deduped by the server.
        const items = await submitInvoices(ctx.authedFetch, base, [invoiceId], newIdempotencyKey())
        const outcome = singleSubmitOutcome(invoiceId, items)
        if (outcome.kind !== 'queued') {
          if (outcome.kind === 'skipped') setSubmitSkipped(outcome.message)
          else setSubmitError(outcome.message)
        }
        gen.current++
        setLive(null)
        detail.run()
        history.run()
      } catch (err) {
        setSubmitError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
      } finally {
        // Functional setter: this runs after an await, so the closure's `submitPhase` is
        // stale -- and `cancel` is a no-op from 'submitting', so only `settled` can unstick
        // the bar on the error leg.
        setSubmitPhase((p) => bulkPhaseReducer(p, { type: 'settled' }))
        submitInFlight.current = false
      }
    }

    // Mirrors keepInvoiceAsIs's caller shape (ReviewRow.tsx handleKeep) -- a thin POST
    // wrapper, one re-entrancy guard, refetch on success. Core AC #6: this never chains
    // into revalidateInvoice/submitInvoices.
    const handleResolveOutside = async () => {
      if (resolving) return
      setResolving(true)
      setResolveOutsideError(null)
      try {
        await resolveInvoiceOutside(ctx.authedFetch, base, invoiceId, resolveReason)
        setResolveReason('')
        // See handleRevalidate above -- clear the overlay before the real refresh, so a
        // `live` value left over from watching queued -> failed can't mask this result.
        gen.current++
        setLive(null)
        detail.run()
      } catch (err) {
        setResolveOutsideError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
      } finally {
        setResolving(false)
      }
    }

    const handleUndoResolveOutside = async () => {
      if (undoing) return
      setUndoing(true)
      setResolveOutsideError(null)
      try {
        await unresolveInvoiceOutside(ctx.authedFetch, base, invoiceId)
        // See handleResolveOutside above -- same overlay-clear, same rationale.
        gen.current++
        setLive(null)
        detail.run()
      } catch (err) {
        setResolveOutsideError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
      } finally {
        setUndoing(false)
      }
    }

    content = (
      <>
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 22, gap: 24, flexWrap: 'wrap' }}>
          <div>
            <div className="eyebrow" style={{ marginBottom: 10 }}>
              INVOICE DETAIL
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 6 }}>
              <h1 className="mono" style={{ fontSize: 22, fontWeight: 600, letterSpacing: '-0.01em', margin: 0, whiteSpace: 'nowrap' }}>{inv.invoice_number}</h1>
              <span data-testid="invoice-status-badge" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: st.bg, border: `1px solid ${st.border}`, borderRadius: 999, padding: '4px 10px' }}>
                <span style={{ width: 6, height: 6, borderRadius: 99, background: st.text }} />
                <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text }}>{st.label}</span>
              </span>
            </div>
            <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{inv.buyer_name ?? '—'} · {fmtDate(inv.issue_date ?? inv.created_at)}</p>
          </div>

          {/* The actions bar ([actions-visibility], INVED-01-07). `inv.can_edit` is the
              ONE and ONLY gate on it, read straight off the wire ([gates-on-the-wire]) --
              this component holds no status list of its own for these two actions, which
              is the whole point of the story: the backend derives both flags from
              legalTransitions (canEdit/canRevalidate, store.go:919-960), so a lifecycle
              change can never leave the SPA showing an action the machine refuses. On
              queued/submitted/accepted/failed can_edit is false and nothing here renders,
              leaving the failed dead-end card as the whole story on a failed invoice.
              `!editing` hides the bar while the inline editor owns the screen; it returns
              on Save or Cancel ([D-actions-hidden-while-editing]).

              Why `can_edit` ALONE and not `can_edit || can_revalidate`: canRevalidate is
              draft-only and canEdit yields {draft, validated, rejected} (store.go:940/960),
              so `can_revalidate` IMPLIES `can_edit` -- the `||` arm is unreachable today,
              and adding it would erase AC #7's intent that NEITHER action renders past the
              editable states. Widening the gate is a deliberate human decision, guarded on
              the backend by TestCanRevalidate_AgreesWithThePromotionEdge; it must not be
              pre-empted here by a defensive `||`. Submit nests inside this same gate too,
              independently controlled by `inv.can_submit` -- see TestCanSubmit_ImpliesCanEdit. */}
          {/* Outer column wraps the can_edit-gated bar AND the submit skip/error banners
              together, so both stay in one right-aligned flex item ([D-actions-column]).
              The banners live OUTSIDE the `invoice-actions` gate on purpose: a submit that
              lands can flip can_edit to false on refetch (e.g. a duplicate_request skip on
              an invoice a PRIOR request already queued), and the banner describing that
              outcome must still render -- [never-report-success-on-a-skip] is not allowed
              to depend on the record still being editable afterward. */}
          {(!editing || submitSkipped != null || submitError != null) && (
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 8, maxWidth: 320 }}>
              {/* The canonical document, always offered -- NOT behind `can_edit` like the bar
                  below ([ubl-button-outside-invoice-actions]): can_view_ubl tracks CONTENT
                  completeness, not lifecycle, and a compliance user needs the document most on
                  queued/submitted/accepted/failed, where that bar is gone. Same four disabled
                  layers as :523-542, minus `filter: 'none'` -- that neutralises
                  .v2-btn-primary's brightening :hover (app-layer.css:213); .v2-btn-ghost's
                  :hover (:215) sets no filter. Hidden while `editing`
                  ([ubl-hidden-while-editing]): the form is dirty and the server would render
                  the STORED record. A fragment, never a wrapping div -- a wrapper would fuse
                  button and reason into one flex item of this column. */}
              {!editing && (
                <>
                  <button
                    type="button"
                    data-testid="view-ubl"
                    onClick={() => setUblOpen(true)}
                    disabled={!inv.can_view_ubl}
                    title={inv.ubl_blocked_reason ?? undefined}
                    aria-describedby={inv.ubl_blocked_reason != null ? VIEW_UBL_REASON_ID : undefined}
                    className="v2-btn v2-btn-ghost pf-btn"
                    style={{
                      height: 32,
                      padding: '0 14px',
                      fontSize: 13,
                      ...(!inv.can_view_ubl ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                    }}
                  >
                    <span style={{ display: 'inline-flex' }}>{docGlyph2}</span> View UBL/XML
                  </button>
                  {inv.ubl_blocked_reason != null && (
                    <div id={VIEW_UBL_REASON_ID} data-testid="view-ubl-blocked-reason" style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5, textAlign: 'right' }}>
                      {inv.ubl_blocked_reason}
                    </div>
                  )}
                </>
              )}
              {inv.can_edit && !editing && (
                <div data-testid="invoice-actions" style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 8 }}>
                  <div style={{ display: 'flex', gap: 8 }}>
                    <button
                      type="button"
                      data-testid="edit-toggle"
                      onClick={() => setEditing(true)}
                      className="v2-btn v2-btn-primary pf-btn"
                      style={{ height: 32, padding: '0 14px', fontSize: 13 }}
                    >
                      Edit
                    </button>
                    {/* Disabled-with-reason rather than hidden ([revalidate-visibility]) --
                        hiding it makes the edit -> demote -> re-validate loop undiscoverable.
                        FOUR layers, all load-bearing, because a disabled button gets NO styling
                        for free in this codebase: packages/design-tokens/*.css contains zero
                        `:disabled` rules, and `.v2-btn-ghost` (app-layer.css:214) sets
                        background/color explicitly with a :hover rule (:215) that is NOT guarded
                        by `:not(:disabled)`.
                        (1) the real HTML `disabled` attribute -- genuinely unclickable;
                        (2) the inline background/color/cursor swap below, which both mutes the
                            button and, being inline, outranks that unguarded :hover rule so a
                            disabled button stops reacting to the pointer. Treatment copied from
                            CreateUpload.tsx:154-156/:217-219, the repo's shipped PERSISTENT
                            disabled gating; deliberately NOT InvoicesList.tsx:347's `opacity`,
                            which is a sub-second in-flight state and provably does not suppress
                            the hover swap (Surface Conflicts -- one precedent picked, not blended);
                        (3) the visible sibling text below, carrying the backend's reason
                            verbatim -- the only layer a keyboard/screen-reader user and a
                            Playwright text assertion can both reach, since a disabled button is
                            out of the tab order;
                        (4) title + aria-describedby, as ADDITIONS to (3), never the sole carrier. */}
                    <button
                      type="button"
                      data-testid="revalidate"
                      onClick={handleRevalidate}
                      disabled={revalidateDisabled}
                      title={inv.revalidate_blocked_reason ?? undefined}
                      aria-describedby={inv.revalidate_blocked_reason != null ? REVALIDATE_REASON_ID : undefined}
                      className="v2-btn v2-btn-ghost pf-btn"
                      style={{
                        height: 32,
                        padding: '0 14px',
                        fontSize: 13,
                        // Spread ONLY when disabled: an inline `background` on the enabled
                        // button would also kill its legitimate :hover affordance.
                        ...(revalidateDisabled ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                      }}
                    >
                      {revalidating ? 'Revalidating…' : 'Re-validate'}
                    </button>
                    {/* Inline arm -> confirm, not a modal ([no-modal], ReviewInvoicesTab.tsx file
                        header) -- the second stage renders below, in this same actions column.
                        Always rendered, disabled-with-reason rather than hidden when
                        `!inv.can_submit` ([revalidate-visibility], same convention as Re-validate
                        above) -- the same four layers, plus `filter: 'none'`: Submit is
                        `.v2-btn-primary`, whose unguarded `:hover` (app-layer.css:213) also sets
                        `filter: brightness(1.22)`, which the ghost recipe above never had to
                        neutralise. A disabled button emits no click, so the arm/confirm flow
                        below is unreachable while disabled; `handleSubmit`'s own `!inv.can_submit`
                        guard is the second line of defence. */}
                    {submitPhase === 'idle' ? (
                        <button
                          type="button"
                          data-testid="detail-submit"
                          onClick={() => toSubmitPhase({ type: 'arm' })}
                          disabled={!inv.can_submit}
                          title={inv.submit_blocked_reason ?? undefined}
                          aria-describedby={inv.submit_blocked_reason != null ? SUBMIT_REASON_ID : undefined}
                          className="v2-btn v2-btn-primary pf-btn"
                          style={{
                            height: 32,
                            padding: '0 14px',
                            fontSize: 13,
                            ...(!inv.can_submit ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed', filter: 'none' } : null),
                          }}
                        >
                          {DETAIL_SUBMIT_COPY.submit}
                        </button>
                      ) : (
                        <>
                          <button
                            type="button"
                            data-testid="detail-submit-cancel"
                            onClick={() => toSubmitPhase({ type: 'cancel' })}
                            disabled={submitPhase === 'submitting'}
                            className="v2-btn v2-btn-ghost pf-btn"
                            style={{
                              height: 32,
                              padding: '0 14px',
                              fontSize: 13,
                              ...(submitPhase === 'submitting' ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                            }}
                          >
                            {DETAIL_SUBMIT_COPY.cancel}
                          </button>
                          <button
                            type="button"
                            data-testid="detail-submit-confirm"
                            onClick={() => void handleSubmit()}
                            disabled={submitPhase === 'submitting'}
                            className="v2-btn v2-btn-primary pf-btn"
                            style={{
                              height: 32,
                              padding: '0 14px',
                              fontSize: 13,
                              ...(submitPhase === 'submitting' ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                            }}
                          >
                            {submitPhase === 'submitting' ? DETAIL_SUBMIT_COPY.sending : DETAIL_SUBMIT_COPY.confirm}
                          </button>
                        </>
                      )}
                  </div>
                  {/* The backend's copy, verbatim ([revalidate-reason-from-backend]). The wire
                      guarantees it is non-null exactly when can_edit && !can_revalidate
                      (lib/invoices.ts:186-196); if it is somehow null we render NOTHING rather
                      than invent a fallback string the SPA has no authority to author. */}
                  {inv.revalidate_blocked_reason != null && (
                    <div id={REVALIDATE_REASON_ID} data-testid="revalidate-blocked-reason" style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5, textAlign: 'right' }}>
                      {inv.revalidate_blocked_reason}
                    </div>
                  )}
                  {/* Same convention, for Submit ([gates-on-the-wire]). The wire guarantees
                      submit_blocked_reason != null implies !can_submit; the converse does NOT
                      hold, so key off the reason alone and never off can_edit/can_submit. */}
                  {inv.submit_blocked_reason != null && (
                    <div id={SUBMIT_REASON_ID} data-testid="submit-blocked-reason" style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5, textAlign: 'right' }}>
                      {inv.submit_blocked_reason}
                    </div>
                  )}
                  {/* Genuine-failure surface, moved here from the deleted fused card. Style
                      unchanged; only the card-relative `margin` is dropped, since the column's
                      own `gap: 8` now does that spacing. */}
                  {revalidateError && (
                    <div style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)', textAlign: 'left' }}>
                      {revalidateError}
                    </div>
                  )}
                  {/* Founder-pinned copy, verbatim (DETAIL_SUBMIT_COPY) -- two sentences as two
                      lines, matching ReviewInvoicesTab's bulk-bar confirm stage. */}
                  {submitPhase !== 'idle' && (
                    <div data-testid="detail-submit-confirm-prompt" style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5, textAlign: 'right' }}>
                      <div>{DETAIL_SUBMIT_COPY.prompt}</div>
                      <div>{DETAIL_SUBMIT_COPY.detail}</div>
                    </div>
                  )}
                </div>
              )}
              {/* A skip is not a failure -- amber, like the stale-verdict banner, never red.
                  Outside the `invoice-actions` gate above -- see the wrapper's own comment. */}
              {submitSkipped != null && (
                <div data-testid="detail-submit-skipped" style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', fontSize: 12, color: 'var(--status-amber-text)', textAlign: 'left' }}>
                  {submitSkipped}
                </div>
              )}
              {submitError != null && (
                <div data-testid="detail-submit-error" style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)', textAlign: 'left' }}>
                  {submitError}
                </div>
              )}
            </div>
          )}
        </div>

        <div className="pf-detail-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 340px', gap: 16, alignItems: 'start' }}>
          {/* The left-column card has two mutually exclusive bodies ([edit-mode-in-body]).
              The read-only one below is unchanged from before INVED-01-07 -- deliberately,
              so the split ships zero read-mode visual diff. */}
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
            {editing ? (
              <InvoiceEditBody
                ctx={ctx}
                base={base}
                invoiceId={invoiceId}
                inv={inv}
                onSaved={handleSaved}
                onCancel={() => setEditing(false)}
              />
            ) : (
              <>
                <div style={{ padding: 24, borderBottom: '1px solid var(--line-1)' }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 24, gap: 24 }}>
                    <div>
                      <div style={{ fontSize: 16, fontWeight: 700, letterSpacing: '-0.02em' }}>{inv.supplier_name ?? '—'}</div>
                      <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 3 }}>TIN {inv.supplier_tin ?? '—'}</div>
                    </div>
                    <div style={{ textAlign: 'right' }}>
                      <div className="label" style={{ marginBottom: 3 }}>Bill to</div>
                      <div style={{ fontSize: 13, fontWeight: 600 }}>{inv.buyer_name ?? '—'}</div>
                      <div data-testid="buyer-tin" className="mono" style={{ fontSize: 11, color: isBuyerTinMissing(inv.buyer_tin) ? 'var(--status-red-text)' : 'var(--fg-3)' }}>{isBuyerTinMissing(inv.buyer_tin) ? BUYER_TIN_MISSING : inv.buyer_tin}</div>
                    </div>
                  </div>
                  <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-input)', overflow: 'hidden' }}>
                    <div style={{ display: 'grid', gridTemplateColumns: '1fr 60px 120px 120px', gap: 10, padding: '9px 14px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
                      <span className="label">Description</span>
                      <span className="label" style={{ textAlign: 'right' }}>Qty</span>
                      <span className="label" style={{ textAlign: 'right' }}>Unit</span>
                      <span className="label" style={{ textAlign: 'right' }}>Amount</span>
                    </div>
                    {items.map((it) => (
                      <div key={it.id} style={{ display: 'grid', gridTemplateColumns: '1fr 60px 120px 120px', gap: 10, padding: '11px 14px', borderBottom: '1px solid var(--line-1)' }}>
                        <span style={{ fontSize: 13 }}>{it.description ?? '—'}</span>
                        <span className="mono" style={{ fontSize: 12, textAlign: 'right', color: 'var(--fg-2)' }}>{it.quantity ?? '—'}</span>
                        <span className="money" style={{ fontSize: 12, textAlign: 'right', color: 'var(--fg-2)' }}>{it.unit_price != null ? fmtPlain(Number(it.unit_price)) : '—'}</span>
                        <span className="money" style={{ fontSize: 12.5, textAlign: 'right', fontWeight: 600 }}>{it.line_total != null ? fmt(Number(it.line_total)) : '—'}</span>
                      </div>
                    ))}
                  </div>
                </div>
                <div style={{ padding: '16px 24px', display: 'flex', justifyContent: 'flex-end' }}>
                  <div style={{ width: 240, display: 'flex', flexDirection: 'column', gap: 8 }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                      <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>Subtotal</span>
                      <span className="money" style={{ fontSize: 13 }}>{subtotal != null ? fmt(subtotal) : '—'}</span>
                    </div>
                    <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                      <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>VAT</span>
                      <span className="money" style={{ fontSize: 13 }}>{vat != null ? fmt(vat) : '—'}</span>
                    </div>
                    <div style={{ display: 'flex', justifyContent: 'space-between', paddingTop: 9, borderTop: '1px solid var(--line-1)' }}>
                      <span style={{ fontSize: 14, fontWeight: 600 }}>Total</span>
                      <span className="money" style={{ fontSize: 16, fontWeight: 700 }}>{total != null ? fmt(total) : '—'}</span>
                    </div>
                  </div>
                </div>
              </>
            )}
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            {inv.status === 'failed' && (
              <div data-testid="failed-dead-end" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
                <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)' }}>
                  <span className="card-title">Submission failed</span>
                </div>
                <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 12 }}>
                  <div style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>
                    This submission failed and is terminal — it cannot be re-driven from this screen.
                  </div>
                  <div data-testid="failure-headline" style={{ fontSize: 13, fontWeight: 600 }}>{failure.headline}</div>
                  <div data-testid="failure-detail" style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{failure.detail}</div>
                  <div
                    data-testid="failure-next-step"
                    style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', fontSize: 12.5, color: 'var(--status-amber-text)' }}
                  >
                    {failure.nextStep}
                  </div>
                  {/* Resolve-outside (Core AC #1/#4/#5/#6) -- inline, never a modal
                      ([no-modal]), the only affordance this diagnosis-only card carries.
                      Resolved and unresolved are mutually exclusive renders: the banner +
                      Undo replace the reason input + mark-resolved button entirely. */}
                  {resolvedMark ? (
                    <>
                      <div
                        data-testid="detail-resolved-banner"
                        style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', fontSize: 12.5, color: 'var(--status-amber-text)', lineHeight: 1.5 }}
                      >
                        <div>{RESOLVE_OUTSIDE_COPY.resolvedPrefix}{resolvedMark.reason}</div>
                        <div className="mono" style={{ marginTop: 4, opacity: 0.85 }}>{actorLabel(resolvedMark.by).text} · {fmtDateTime(resolvedMark.at)}</div>
                      </div>
                      {/* Re-resolving is legal (the wire's can_resolve_outside does not go
                          false once resolved), so Undo reads the same flag as the mark
                          button below rather than a separate one. Same reason wiring as
                          `resolve-outside` below (Core AC #4, disabled-with-reason, never
                          hidden) -- reusing RESOLVE_OUTSIDE_REASON_ID/its testid is safe
                          because resolved and unresolved never render at once. */}
                      <button
                        type="button"
                        data-testid="resolve-outside-undo"
                        onClick={() => void handleUndoResolveOutside()}
                        disabled={!inv.can_resolve_outside || undoing}
                        title={inv.resolve_outside_blocked_reason ?? undefined}
                        aria-describedby={inv.resolve_outside_blocked_reason != null ? RESOLVE_OUTSIDE_REASON_ID : undefined}
                        className="v2-btn v2-btn-ghost pf-btn"
                        style={{
                          height: 32,
                          padding: '0 14px',
                          fontSize: 13,
                          alignSelf: 'flex-start',
                          ...(!inv.can_resolve_outside || undoing ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                        }}
                      >
                        {RESOLVE_OUTSIDE_COPY.undoLabel}
                      </button>
                      {inv.resolve_outside_blocked_reason != null && (
                        <div id={RESOLVE_OUTSIDE_REASON_ID} data-testid="resolve-outside-blocked-reason" style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5 }}>
                          {inv.resolve_outside_blocked_reason}
                        </div>
                      )}
                    </>
                  ) : (
                    <>
                      {/* The label is wider than the rail can hold beside the input, so the
                          row wraps to a second line instead of squeezing text inside the pill. */}
                      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                        <input
                          type="text"
                          data-testid="resolve-outside-reason"
                          aria-label="Resolved outside reason"
                          placeholder={RESOLVE_OUTSIDE_COPY.reasonPlaceholder}
                          value={resolveReason}
                          onChange={(e) => setResolveReason(e.target.value)}
                          disabled={resolving}
                          className="pf-input"
                          style={{ flex: '1 1 220px', minWidth: 160, height: 32, fontSize: 12.5 }}
                        />
                        {/* Same four-layer disabled recipe as Submit (:573-590) -- `filter:
                            'none'` is mandatory: this is `.v2-btn-primary`, whose unguarded
                            `:hover` (app-layer.css:213) sets `filter: brightness(1.22)`. */}
                        <button
                          type="button"
                          data-testid="resolve-outside"
                          onClick={() => void handleResolveOutside()}
                          disabled={resolveOutsideDisabled}
                          title={inv.resolve_outside_blocked_reason ?? undefined}
                          aria-describedby={inv.resolve_outside_blocked_reason != null ? RESOLVE_OUTSIDE_REASON_ID : undefined}
                          className="v2-btn v2-btn-primary pf-btn"
                          style={{
                            height: 32,
                            padding: '0 14px',
                            fontSize: 13,
                            // Must not flex-shrink below its own text -- that's what wrapped the label.
                            flexShrink: 0,
                            whiteSpace: 'nowrap',
                            ...(resolveOutsideDisabled ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed', filter: 'none' } : null),
                          }}
                        >
                          {RESOLVE_OUTSIDE_COPY.label}
                        </button>
                      </div>
                      {/* The backend's copy, verbatim -- never an SPA-authored fallback. */}
                      {inv.resolve_outside_blocked_reason != null && (
                        <div id={RESOLVE_OUTSIDE_REASON_ID} data-testid="resolve-outside-blocked-reason" style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5 }}>
                          {inv.resolve_outside_blocked_reason}
                        </div>
                      )}
                    </>
                  )}
                  {/* Outside the resolved/unresolved ternary on purpose: a failed
                      handleUndoResolveOutside sets this too, and it must not be stranded
                      with no branch to render in while the resolved banner is still shown. */}
                  {resolveOutsideError && (
                    <div style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)' }}>
                      {resolveOutsideError}
                    </div>
                  )}
                </div>
              </div>
            )}

            {rejectionLeadsRail && rejectionCard}

            {shouldShowFiscalRecord(inv) && (
              <div data-testid="fiscal-record-card" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
                <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)' }}>
                  <span className="card-title">Fiscal record</span>
                </div>
                <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 12 }}>
                  <div>
                    <div className="label" style={{ marginBottom: 3 }}>IRN</div>
                    <div data-testid="fiscal-irn" className="mono" style={{ fontSize: 12.5, wordBreak: 'break-all', lineHeight: 1.4 }}>{inv.irn}</div>
                  </div>
                  <div>
                    <div className="label" style={{ marginBottom: 3 }}>CSID</div>
                    <div data-testid="fiscal-csid" className="mono" style={{ fontSize: 12.5, wordBreak: 'break-all', lineHeight: 1.4 }}>{inv.csid ?? '—'}</div>
                  </div>
                  {inv.qr_png_base64 != null && (
                    // Literal #fff, not var(--bg-2): a QR plate must keep scanner contrast
                    // regardless of theme, so this one swatch deliberately does not follow
                    // a design token (story §6 / task-251 Stage-1 correction K).
                    <div style={{ background: '#fff', borderRadius: 'var(--radius-md)', padding: 12, display: 'flex', justifyContent: 'center' }}>
                      <img
                        data-testid="fiscal-qr"
                        src={`data:image/png;base64,${inv.qr_png_base64}`}
                        alt="NRS QR code"
                        width={132}
                        height={132}
                        style={{ imageRendering: 'pixelated' }}
                      />
                    </div>
                  )}
                </div>
              </div>
            )}

            <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
              <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)' }}>
                <span className="card-title">Compliance</span>
              </div>
              <div style={{ padding: 16 }}>
                {/* The persisted reason, verbatim (BUG-03-03) -- amber, matching
                    ReviewRow.tsx's own kept-as-is banner rather than inventing a second
                    tone for the same fact. */}
                {kept && (
                  <div
                    data-testid="detail-kept-banner"
                    style={{ marginBottom: 12, padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', fontSize: 12.5, color: 'var(--status-amber-text)', lineHeight: 1.5 }}
                  >
                    <div>{ROW_EXPANSION_COPY.keptPrefix}{kept.reason}</div>
                    <div className="mono" style={{ marginTop: 4, opacity: 0.85 }}>{actorLabel(kept.by).text} · {fmtDateTime(kept.at)}</div>
                  </div>
                )}
                {verdict === 'stale' && (
                  <div
                    data-testid="stale-verdict"
                    style={{ marginBottom: 12, padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', fontSize: 12.5, color: 'var(--status-amber-text)' }}
                  >
                    Edited since the last validation — this verdict is stale. Run Re-validate to refresh it.
                  </div>
                )}
                {inv.rule_set_version != null ? (
                  <div data-testid="violations-table">
                    <ViolationsTable violations={inv.violations} ruleSetVersion={inv.rule_set_version} />
                  </div>
                ) : (
                  <div
                    data-testid="not-validated"
                    style={{ padding: '12px 14px', borderRadius: 'var(--radius-md)', background: 'var(--bg-3)', border: '1px solid var(--line-2)', fontSize: 12.5, color: 'var(--fg-2)' }}
                  >
                    Not yet validated — run Re-validate to check compliance.
                  </div>
                )}
              </div>
            </div>

            {!rejectionLeadsRail && rejectionCard}

            {/* INVED-01-07 deleted the fused "Fix & re-validate" card that used to sit
                here -- one card that welded an always-mounted edit form to a Re-validate
                button, so Edit and Re-validate could never be gated apart. Both now live
                in the page-header actions bar above, independently gated. */}

            {/* Directly above `Status history`, because that is where the evidence sits.
                NOT titled "Audit trail" (the design's name for the same card):
                import-wizard.spec.ts:538 asserts that string has zero matches here. */}
            <SourceDocumentCard meta={source} onOpen={openPreview} />

            {/* M5-09-07 residual (Stage-1 finding H, AC-2 scoped to the invoice body/badge
                above, not this card): on the one poll tick where shouldRefreshHistory
                fires, history.run() dispatches useAsync's 'start' action and this card
                drops to <Loading/> before returning at N+1 rows -- a real, accepted
                flash. Not overlaid like `inv`/`live` above: this card is the ONE render
                path that is allowed to show its async state honestly, the flash is
                confined to a single card (never the badge or invoice body), and
                M5-09-08's oracle already asserts the post-flip count with an
                auto-retrying toHaveCount(N+1), not a point-in-time read across the
                window. Overlaying history too would duplicate the runId/live-clearing
                machinery above for a card whose own oracle tolerates the dip. */}
            <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
              <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)' }}>
                <span className="card-title">Status history</span>
              </div>
              <div data-testid="status-history" style={{ padding: '16px 18px' }}>
                {history.status === 'loading' && <Loading label="Loading history…" />}
                {history.status === 'error' && history.error && <ErrorState error={history.error} onRetry={history.run} />}
                {history.status === 'empty' && <div style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>No history yet.</div>}
                {history.status === 'ready' &&
                  (history.data ?? []).map((h, i, arr) => (
                    <div key={i} data-testid="status-history-row" style={{ display: 'flex', gap: 12 }}>
                      <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', flex: 'none' }}>
                        <span style={{ width: 8, height: 8, borderRadius: 99, background: 'var(--fg-3)', marginTop: 4 }} />
                        <span style={{ width: 1, flex: 1, background: 'var(--line-2)', minHeight: i === arr.length - 1 ? '0px' : '20px' }} />
                      </div>
                      <div style={{ paddingBottom: 16 }}>
                        <div style={{ fontSize: 13, fontWeight: 500 }}>
                          {h.from_status === null ? `Created · ${h.to_status}` : `${h.from_status} → ${h.to_status}`}
                        </div>
                        <div style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>
                          <span className={actorLabel(h.actor).mono ? 'mono' : undefined}>{actorLabel(h.actor).text}</span>
                          {' · '}
                          <span className="mono">{fmtDateTime(h.changed_at)}</span>
                        </div>
                      </div>
                    </div>
                  ))}
              </div>
            </div>
          </div>
        </div>

        {/* Rendered inline, never portalled: `--bg-*`/`--fg-*` are declared on `.asc-app`
            (app-layer.css:25-27), and this tree is inside it. Modal open state is local
            to this component -- nothing about it belongs on PlatformCtx. */}
        {previewOpen && (
          <SourceDocumentModal
            ctx={ctx}
            meta={source}
            invoiceNumber={inv.invoice_number}
            invoiceCreatedAt={inv.created_at}
            createdBy={history.data?.[0]?.actor ?? null}
            onClose={closePreview}
          />
        )}
        {ublOpen && (
          <XmlModal ctx={ctx} base={base} invoiceId={invoiceId} invoiceNumber={inv.invoice_number} onClose={closeUbl} />
        )}
      </>
    )
  }

  // No width cap: this page fills its column like every other screen in the app. BUG-03-05's
  // 1080 cap is deliberately reverted -- it stranded a third of a 1920 window. E2E-10 pins it.
  return (
    <div data-testid="invoice-detail" style={{ padding: '24px 36px 56px' }}>
      <button onClick={() => ctx.nav('invoices')} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 32, padding: '0 12px', fontSize: 13, marginBottom: 18 }}>
        ← All invoices
      </button>
      {content}
    </div>
  )
}

// The inline edit body ([edit-mode-in-body]/[edit-ux], INVED-01-07 — was InvoiceEditForm,
// the always-mounted 9-field form inside the deleted fused card). It now replaces the
// left-column card's read-only body while `editing`, and covers the 9 header fields
// ([edit-form-nine-fields]) PLUS the line items, which are editable for the first time
// here (add / edit / remove).
//
// Both state slices are seeded once from `inv` at mount, matching EntityFormModal's
// once-per-open init: the component only ever mounts while `editing`, so Cancel is
// literally unmount-and-discard and re-opening re-seeds from the current `inv` — there is
// no manual reset path to keep in sync. diffEditInput/diffLineItems still diff against the
// current `inv` prop (fresh on every parent re-render), so a later edit's patch is computed
// against the latest saved content even though the fields were seeded once.
//
// Reuses ValidationView.tsx's field-label + `.pf-input` markup convention throughout, and
// its line-item repeater (:118-151) for the table, remove ✕ and dashed add chip.
function InvoiceEditBody({
  ctx,
  base,
  invoiceId,
  inv,
  onSaved,
  onCancel,
}: {
  ctx: PlatformCtx
  base: string
  invoiceId: string
  inv: InvoiceRecord
  onSaved: () => void
  onCancel: () => void
}) {
  const [form, setForm] = useState<EditFormState>(() => formFromInvoice(inv))
  const [rows, setRows] = useState<LineRowState[]>(() => rowsFromInvoice(inv))
  const [submitting, setSubmitting] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)

  // Field flags (task-251 AC #3/#5): one per rejection reason whose MBS path maps to one
  // of this form's editable fields, carrying the reason's code — so the operator sees
  // which field the APP's rejection actually pointed at. A reason with an unmapped (or
  // absent) path is never swallowed here; it's still listed in full on the rejection card
  // above, just without a field flag. Extracted to lib/invoices.ts's reasonFieldFlags (QA
  // follow-up to task-251) -- the first-reason-wins collision rule now has a test oracle
  // there instead of living unspecified in this component.
  const fieldFlags = reasonFieldFlags(inv.rejection_reasons)

  // Rendered as a SIBLING between the label div and the input — never merged into the
  // label's own text node, never wrapping the label+input pair in a new container.
  // e2e/topology/invoice-surfaces.spec.ts locates each input via
  // `.//div[normalize-space(text())="<Label>"]/following-sibling::input`; that XPath axis
  // matches ANY following sibling named `input`, so an extra sibling in between is safe.
  function fieldFlag(key: EditFieldKey): ReactNode {
    const code = fieldFlags.get(key)
    if (code == null) return null
    return (
      <span
        data-testid="field-flag"
        className="mono"
        style={{
          display: 'inline-block',
          fontSize: 10,
          fontWeight: 600,
          color: 'var(--status-red-text)',
          background: 'var(--status-red-bg)',
          border: '1px solid var(--status-red-border)',
          borderRadius: 999,
          padding: '1px 6px',
          marginBottom: 5,
        }}
      >
        {code}
      </span>
    )
  }

  function updateField(field: EditFieldKey, value: string) {
    setForm((f) => ({ ...f, [field]: value }))
  }

  function updateRow(idx: number, field: keyof LineRowState, value: string) {
    setRows((rs) => rs.map((row, i) => (i === idx ? { ...row, [field]: value } : row)))
  }

  function addRow() {
    setRows((rs) => [...rs, { description: '', quantity: '', unit_price: '', line_total: '', line_tax: '' }])
  }

  function removeRow(idx: number) {
    setRows((rs) => rs.filter((_, i) => i !== idx))
  }

  // The passive computed line-sum hint ([totals-ownership], Core AC #5). Fed the LIVE
  // edited rows, not inv.line_items, so it moves as the operator types.
  //
  // THE TRAP: computedLineSum tests `!= null` and does NOT canonicalize '' (lib/invoices.ts
  // :608-628) — unlike diffLineItems, which canonicalizes internally (:492-504). A
  // controlled React input holds '', never null, so a stored-NULL quantity arrives here as
  // ''; passing that straight through runs parseScaled(''), which fails DECIMAL_RE and
  // makes the helper return null for the WHOLE sum. An ABSENT quantity is supposed to
  // weight the line at 1, so without this mapping the hint would be silently dead on most
  // real invoices. Map '' -> null first.
  const lineSum = computedLineSum(
    rows.map((row) => ({
      quantity: row.quantity === '' ? null : row.quantity,
      unit_price: row.unit_price === '' ? null : row.unit_price,
    })),
  )

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (submitting) return
    const patch = diffEditInput(inv, form)
    // `undefined` (content-identical lines) leaves the key ABSENT, so a header-only save
    // never touches the stored lines or churns their ids ([fingerprint-excludes-line-ids]).
    // `[]` — emptying a populated invoice — IS assigned, making the patch non-empty, so the
    // delete-all PATCH is genuinely sent rather than swallowed by the no-op guard below.
    const lines = diffLineItems(inv.line_items ?? [], rows)
    if (lines !== undefined) patch.line_items = lines
    // Nothing changed — skip the PATCH (it would 400 on the backend's all-nil check) and
    // leave edit mode ([D-noop-save-exits]). Before INVED-01-07 this returned silently with
    // the form still mounted; in an explicit edit mode that reads as a dead Save button.
    if (Object.keys(patch).length === 0) {
      onCancel()
      return
    }
    setSubmitting(true)
    setFormError(null)
    try {
      await editInvoice(ctx.authedFetch, base, invoiceId, patch)
      onSaved()
    } catch (err) {
      setFormError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form data-testid="edit-invoice" onSubmit={handleSubmit}>
      <div style={{ padding: 24, borderBottom: '1px solid var(--line-1)' }}>
        {formError && (
          <div style={{ marginBottom: 12, padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)' }}>
            {formError}
          </div>
        )}
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginBottom: 14 }}>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Issue date</div>
            {fieldFlag('issue_date')}
            <input className="pf-input" type="text" value={form.issue_date} onChange={(e) => updateField('issue_date', e.target.value)} placeholder="YYYY-MM-DD" style={{ fontFamily: 'var(--font-mono)' }} disabled={submitting} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Currency</div>
            {fieldFlag('currency')}
            <input className="pf-input" type="text" value={form.currency} onChange={(e) => updateField('currency', e.target.value)} disabled={submitting} />
          </div>
          {/* Supplier name/TIN are DISPLAY-ONLY (INVCR-01-18, C7 fix, edit path -- a narrowly
              authorized §14 exception, no other field or layout on this screen touched): the
              backend now ALWAYS re-derives both from the invoice's entity on every PATCH,
              discarding whatever these inputs used to send ([supplier-from-entity], mirroring
              Store.Create's own override) -- a live editable supplier_tin here let an operator
              retype a bare-digit TIN and reintroduce the exact false supplier-tin-format defect
              C7 fixed on create. readOnly (not disabled): still focusable/selectable so the
              value can be copied, just not typed into -- no onChange, so diffEditInput can never
              see these two fields differ from formFromInvoice(inv) and they are never sent.
              aria-readonly is redundant with the native `readonly` attribute for assistive tech
              (already implicit) but stated explicitly anyway. color: var(--fg-3) (an EXISTING
              token this file already uses for de-emphasized text, e.g. the computed-line-sum
              hint below) is the one concession to ".pf-input has no :disabled/:read-only style
              at all today" (product-advisor review, 2026-07-31) -- without it this field is
              visually IDENTICAL to every editable one beside it, which is worse than "unstyled"
              for a control that no longer does what it looks like it does; not a new visual
              language, just this file's own existing muted-text color applied to two inputs. */}
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Supplier name</div>
            {fieldFlag('supplier_name')}
            <input className="pf-input" type="text" value={form.supplier_name} readOnly aria-readonly="true" disabled={submitting} style={{ color: 'var(--fg-3)' }} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Supplier TIN</div>
            {fieldFlag('supplier_tin')}
            <input className="pf-input" type="text" value={form.supplier_tin} readOnly aria-readonly="true" placeholder="########-####" style={{ fontFamily: 'var(--font-mono)', color: 'var(--fg-3)' }} disabled={submitting} />
            {/* CreateMapping.tsx's existing vocabulary ("Supplier details come from <entity>,
                not the file"), reused rather than inventing new copy -- adapted to this screen
                (no file here to contrast against). */}
            <div style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 5, lineHeight: 1.4 }}>
              Supplier details come from {form.supplier_name || 'the linked entity'}, not editable here.
            </div>
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Buyer name</div>
            {fieldFlag('buyer_name')}
            <input className="pf-input" type="text" value={form.buyer_name} onChange={(e) => updateField('buyer_name', e.target.value)} disabled={submitting} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Buyer TIN</div>
            {fieldFlag('buyer_tin')}
            <input className="pf-input" type="text" value={form.buyer_tin} onChange={(e) => updateField('buyer_tin', e.target.value)} placeholder="########-####" style={{ fontFamily: 'var(--font-mono)' }} disabled={submitting} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Subtotal</div>
            {fieldFlag('subtotal')}
            <input className="pf-input" type="text" value={form.subtotal} onChange={(e) => updateField('subtotal', e.target.value)} disabled={submitting} />
            {/* The computed line-sum hint ([totals-ownership], Core AC #5). Rendered as a
                sibling AFTER the input, never between the label and the input — that slot
                belongs to field-flag, and the e2e label->input XPath must keep resolving.
                Deliberately PASSIVE: it never writes subtotal/VAT/total, never blocks Save,
                and carries no red/amber/border/icon and no "mismatch" wording, so it cannot
                be misread as a validation error. `lineSum` is rendered RAW — never through
                fmt(), which rounds to whole naira (lib/format.ts:5-7) and would erase the
                sub-naira disagreement this hint exists to expose. No subtotal-vs-hint
                comparison is drawn here: deciding they disagree is the rule engine's job. */}
            <div data-testid="computed-line-sum" style={{ fontSize: 11.5, color: 'var(--fg-3)', marginTop: 5, lineHeight: 1.5 }}>
              Lines total <span className="money">{lineSum ?? '—'}</span>
            </div>
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>VAT</div>
            {fieldFlag('vat')}
            <input className="pf-input" type="text" value={form.vat} onChange={(e) => updateField('vat', e.target.value)} disabled={submitting} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Total</div>
            {fieldFlag('total')}
            <input className="pf-input" type="text" value={form.total} onChange={(e) => updateField('total', e.target.value)} disabled={submitting} />
          </div>
        </div>

        {/* Line items, editable ([edit-ux], Core AC #2 — the read-only table above has no
            equivalent). Markup follows ValidationView.tsx:115-151, the repo's shipped
            line-item repeater. `key={i}` matches it too: every input is fully controlled
            from `rows`, so a removal re-renders correct values regardless of key identity.
            No per-line rejection flags here ([line-level-flags-not-mapped]) — reasons whose
            MBS path points at a line still render in full on the rejection card. `line_tax`
            gets a column here only; widening the READ-ONLY table is out of scope, so a
            stored line_tax stays invisible until Edit is clicked (noted as a follow-up). */}
        <div className="label" style={{ margin: '18px 0 12px' }}>
          Line items
        </div>
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-input)', overflow: 'hidden', marginBottom: 14 }}>
          <div style={{ display: 'grid', gridTemplateColumns: LINE_EDIT_GRID, gap: 10, padding: '9px 14px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
            <span className="label">Description</span>
            <span className="label">Qty</span>
            <span className="label">Unit</span>
            <span className="label">Amount</span>
            <span className="label">Tax</span>
            <span />
          </div>
          {rows.map((row, i) => (
            <div key={i} data-testid="line-row" style={{ display: 'grid', gridTemplateColumns: LINE_EDIT_GRID, gap: 10, padding: '9px 14px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}>
              <input className="pf-input" type="text" value={row.description} onChange={(e) => updateRow(i, 'description', e.target.value)} disabled={submitting} />
              <input className="pf-input" type="text" value={row.quantity} onChange={(e) => updateRow(i, 'quantity', e.target.value)} style={{ fontFamily: 'var(--font-mono)', padding: '0 8px' }} disabled={submitting} />
              <input className="pf-input" type="text" value={row.unit_price} onChange={(e) => updateRow(i, 'unit_price', e.target.value)} style={{ fontFamily: 'var(--font-mono)', padding: '0 8px' }} disabled={submitting} />
              <input className="pf-input" type="text" value={row.line_total} onChange={(e) => updateRow(i, 'line_total', e.target.value)} style={{ fontFamily: 'var(--font-mono)', padding: '0 8px' }} disabled={submitting} />
              <input className="pf-input" type="text" value={row.line_tax} onChange={(e) => updateRow(i, 'line_tax', e.target.value)} style={{ fontFamily: 'var(--font-mono)', padding: '0 8px' }} disabled={submitting} />
              <button
                type="button"
                data-testid="line-remove"
                onClick={() => removeRow(i)}
                disabled={submitting}
                className="pf-btn"
                aria-label="Remove line item"
                style={{ width: 30, height: 30, borderRadius: 'var(--radius-md)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: submitting ? 'not-allowed' : 'pointer', display: 'grid', placeItems: 'center' }}
              >
                {closeGlyph}
              </button>
            </div>
          ))}
        </div>
        <button
          type="button"
          data-testid="line-add"
          onClick={addRow}
          disabled={submitting}
          className="pf-chip"
          style={{ height: 30, padding: '0 12px', borderRadius: 'var(--radius-md)', fontFamily: 'var(--font-sans)', fontSize: 12.5, fontWeight: 500, border: '1px dashed var(--line-3)', background: 'transparent', color: 'var(--fg-2)', display: 'inline-flex', alignItems: 'center', gap: 6 }}
        >
          <span style={{ display: 'inline-flex' }}>{plusGlyph}</span> Add line item
        </button>
      </div>
      {/* Cancel + Save pairing, heights and button variants from EntityFormModal.tsx:190/193
          — the repo's only shipped Cancel+Submit pair. There is no `.v2-btn-secondary` in
          packages/design-tokens/app-layer.css; ghost and primary are the two that exist. */}
      <div style={{ padding: '16px 24px', display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
        <button type="button" data-testid="edit-cancel" onClick={onCancel} disabled={submitting} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36, fontSize: 13 }}>
          Cancel
        </button>
        <button type="submit" disabled={submitting} className="v2-btn v2-btn-primary pf-btn" style={{ height: 36, fontSize: 13 }}>
          {submitting ? 'Saving…' : 'Save changes'}
        </button>
      </div>
    </form>
  )
}
