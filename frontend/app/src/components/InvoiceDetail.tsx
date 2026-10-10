// Invoice detail dispatcher: for an imported invoice, mounts the live detail surface
// (status pill, line items + totals, compliance/violations panel, fiscal record, APP
// rejection reasons, the Edit / Re-validate actions bar + inline edit mode, failed dead
// end, the five-node state strip) fetched from the gateway; otherwise renders an honest
// EmptyState ("No invoice selected"). INVED-01-07 split the former fused "Fix & re-validate" card
// into two independently-gated actions ([edit-ux]); the bar holding them is always present,
// each control disabled off its own wire flag ([actions-visibility]). The
// Platform.dc.html-ported mock detail branch — fabricated fiscal record (IRN/CSID/QR),
// the "Transmit to FIRS" affordance, synthesized audit trail, and mock validation/totals
// — was removed in M5-09-04 ([mock-branch-fully-removed]); the real fiscal record and APP
// rejection cards below (M5-09-05) render only server-sourced data.

import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { closeGlyph, plusGlyph } from '../glyphs'
import { actorLabel } from '../lib/actor'
import { newestJob } from '../lib/documentRun'
import {
  canRejectReason,
  decideInvoice,
  DETAIL_DECISION_COPY,
  getInvoiceApprovalRun,
  type ApprovalRun,
} from '../lib/approvals'
import { fmt, fmtDate, fmtDateTime, fmtPlain } from '../lib/format'
import { getExtractions, type ExtractionJobsResponse } from '../lib/importApi'
import type { LineEditKey } from '../lib/invoiceFields'
import { EXPLAIN_COPY } from '../lib/explain'
import { violationKey, type LineTarget } from '../lib/validationApi'
import { stripNodes } from '../lib/invoiceStrip'
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
import { ExplainPanel } from './ExplainPanel'
import { ApprovalStateCard } from './ApprovalStateCard'
import { InvoiceActivityCard } from './InvoiceActivityCard'
import { SourceDocumentCard } from './SourceDocumentCard'
import { SourceDocumentModal } from './SourceDocumentModal'
import { StatusStrip } from './StatusStrip'
import { UblDocumentCard } from './UblDocumentCard'
import { ViolationsTable } from './ViolationsTable'
import { XmlModal } from './XmlModal'
import type { PlatformCtx } from '../types'

export function InvoiceDetail({ ctx }: { ctx: PlatformCtx }) {
  // key={invoiceId}: forces a full remount on invoice SWITCH so the previous invoice's
  // local state (edit-form field values, staleSinceEdit, revalidateError) doesn't leak
  // into the next one. The key stays stable while invoiceId is unchanged, so the
  // in-place history/detail refresh after edit/re-validate within one invoice is
  // unaffected — only switching invoices remounts.
  if (ctx.importedInvoiceId !== null) {
    return <LiveInvoiceDetail key={ctx.importedInvoiceId} ctx={ctx} invoiceId={ctx.importedInvoiceId} />
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
// ClientsView: gatewayBase() + useAsync + a Loading/ErrorState/ready
// ladder, zero network when no gateway is configured.
// One editable line row (INVED-01-07). LineItemEditInput-shaped but with '' where the wire
// carries null, because a controlled React input holds '' and never null. Deliberately no
// `id` and no `line_no`: line_no is system-assigned 1..N by array POSITION
// ([line-no-by-position], lib/invoices.ts:234-245), so this array's order IS the wire's
// line ordering, and diffLineItems compares by position over the five content fields only.
type LineRowState = Record<LineEditKey, string>

// description / qty / unit / amount / tax / remove, declared once so header and rows cannot drift.
// Tracks are the prototype's, with a 120px Description floor; the box scrolls sideways only when the floor no longer fits.
// The numeric inputs override .pf-input's side padding down to 8px to keep the mono digits legible.
const LINE_EDIT_GRID = 'minmax(120px, 1fr) 64px 110px 110px 100px 28px'
const EDIT_INPUT = { height: 34, fontSize: 13, padding: '0 10px' } as const
const LINE_INPUT = { ...EDIT_INPUT, height: 30 } as const

function rowsFromInvoice(inv: Pick<InvoiceRecord, 'line_items'>): LineRowState[] {
  return (inv.line_items ?? []).map((it) => ({
    description: it.description ?? '',
    quantity: it.quantity ?? '',
    unit_price: it.unit_price ?? '',
    line_total: it.line_total ?? '',
    line_tax: it.line_tax ?? '',
  }))
}

function LiveInvoiceDetail({ ctx, invoiceId }: { ctx: PlatformCtx; invoiceId: string }) {
  const base = gatewayBase()
  // Same `base ? … : …` narrowing as ClientsView ([A-e]/[A-m]) —
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
  // The extraction behind the source document, for the review-screen entry control.
  // `deps: [documentId]`, not [invoiceId]: LiveInvoiceDetail is keyed by invoiceId, so an
  // invoice switch remounts and the id goes null -> this record's document exactly once.
  // `immediate` is false until then, so a manually typed invoice makes no request at all.
  const documentId = source.data?.document?.id ?? null
  const extractions = useAsync<ExtractionJobsResponse>(
    () =>
      base && documentId
        ? getExtractions(ctx.authedFetch, base, documentId)
        : Promise.reject(new Error('no document to look up')),
    { immediate: shouldFetchInvoices(base) && documentId != null, deps: [documentId] },
  )
  // Only a settled 200 may claim no job exists. 'empty' is folded into `failed` on purpose --
  // it means the body was null, which answered the question no better than a 500 did.
  const extraction = {
    jobId: newestJob(extractions.data?.jobs ?? [])?.id ?? null,
    loading: extractions.status === 'idle' || extractions.status === 'loading',
    failed: extractions.status === 'error' || extractions.status === 'empty',
  }

  // Not extended to the live-refresh tick below (D-23): shouldPollInvoice only ticks
  // queued/submitted, by which point every approval run has closed.
  const approval = useAsync<ApprovalRun | null>(
    () => (base ? getInvoiceApprovalRun(ctx.authedFetch, base, invoiceId) : Promise.reject(new Error('no gateway configured'))),
    { immediate: shouldFetchInvoices(base), deps: [invoiceId] },
  )
  const [previewOpen, setPreviewOpen] = useState(false)
  // Stable — `useDismiss` re-registers its listeners on every identity change.
  const closePreview = useCallback(() => setPreviewOpen(false), [])
  const openPreview = useCallback(() => setPreviewOpen(true), [])
  const [ublOpen, setUblOpen] = useState(false)
  const closeUbl = useCallback(() => setUblOpen(false), [])
  const openUbl = useCallback(() => setUblOpen(true), [])

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

  // Single-invoice approve machine (task-547), same reducer/ref shape as Submit above.
  const [approvePhase, setApprovePhase] = useState<BulkPhase>('idle')
  const approveInFlight = useRef(false)

  // Inline reject row (task-547) -- resolve-outside's state shape, plus its own in-flight
  // ref (submitInFlight's stronger precedent, not resolve-outside's state-only guard).
  const [rejectOpen, setRejectOpen] = useState(false)
  const [rejectReason, setRejectReason] = useState('')
  const [rejecting, setRejecting] = useState(false)
  const rejectInFlight = useRef(false)
  const [decisionError, setDecisionError] = useState<string | null>(null)

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
  // onFocusApplied resets this to null after focusing, so a repeat click on the same line re-focuses.
  const [lineFocus, setLineFocus] = useState<{ seq: number; target: LineTarget } | null>(null)
  const [explainOpen, setExplainOpen] = useState<string | null>(null)
  const [explainNotice, setExplainNotice] = useState<string | null>(null)

  // Read once at mount: the review screen's Open line target, applied when the record first loads.
  const pendingLine = useRef(ctx.importedInvoiceLine ?? null)
  useEffect(() => {
    const target = pendingLine.current
    if (target == null || inv == null) return
    pendingLine.current = null
    ctx.consumeImportedInvoiceLine() // one-shot: a later visit must not replay it
    if (!inv.can_edit) return
    setEditing(true)
    setLineFocus({ seq: 1, target })
  }, [inv])

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
    // historical one stays below Approval state so it doesn't overstate a resolved event.
    const rejectionLeadsRail = rejectionProvenance(inv.status) === 'current'
    const rejectionCard = shouldShowRejectionCard(inv) ? (
      <div
        data-testid="rejection-reasons"
        style={{ background: 'var(--bg-2)', border: `1px solid ${rejectionLeadsRail ? 'var(--status-red-border)' : 'var(--line-1)'}`, borderRadius: 'var(--radius-md)', overflow: 'hidden' }}
      >
        <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', ...(rejectionLeadsRail ? { background: 'var(--status-red-bg)' } : null) }}>
          <span className="card-title" style={rejectionLeadsRail ? { color: 'var(--status-red-text)' } : undefined}>
            {rejectionLeadsRail ? 'This invoice was rejected' : 'Last APP rejection'}
          </span>
        </div>
        <div>
          {inv.rejection_reasons.map((reason, i) => (
            <div
              key={i}
              data-testid="rejection-reason-row"
              style={{ display: 'flex', gap: 10, alignItems: 'baseline', padding: '12px 18px', ...(i > 0 ? { borderTop: '1px solid var(--line-1)' } : null) }}
            >
              <span className="mono" style={{ fontSize: 11, fontWeight: 600, color: 'var(--status-red-text)' }}>{reason.code}</span>
              <span style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>{reason.message}</span>
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
    const handleSaved = (renamed: boolean) => {
      // FIRST: leave edit mode before the refresh below flips the ladder to <Loading/>, so
      // the editor can never remount against a half-refreshed record (INVED-01-07).
      setEditing(false)
      setStaleSinceEdit(true)
      setExplainOpen(null)
      setExplainNotice(null)
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
      approval.run() // Q13's visible face (D-22) -- an edit can demote to draft and close the live run
      // A rename re-runs validation; no other edit does. `handleRevalidate` is declared
      // below, but this closure only runs from a click, by which time it already exists.
      if (renamed) void handleRevalidate()
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
        setExplainOpen(null)
        setExplainNotice(null)
        gen.current++ // see handleSaved above -- invalidate any already-in-flight tick too
        // See handleSaved above -- a stale submit banner must not survive a re-validate either.
        setSubmitPhase('idle')
        setSubmitSkipped(null)
        setSubmitError(null)
        setLive(null) // see handleSaved above -- clear the overlay before the real refresh
        detail.run()
        history.run()
        approval.run() // see handleSaved above -- a re-validate opens a NEW run
      } catch (err) {
        setRevalidateError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
      } finally {
        setRevalidating(false)
      }
    }

    // Same reducer as Submit's own machine below -- identity IS "do nothing", so an
    // unarmed confirm or a second confirm mid-flight fires no request.
    const toApprovePhase = (action: Parameters<typeof bulkPhaseReducer>[1]): boolean => {
      const next = bulkPhaseReducer(approvePhase, action)
      if (next === approvePhase) return false
      setApprovePhase(next)
      return true
    }

    // Indistinguishable in shape from handleSubmit below (D-47's identifier correction):
    // guard -> arm/confirm gate -> in-flight ref -> POST with the returned run DISCARDED
    // (non-optimistic, AC-6) -> refetch trio -> catch surfaces the server's own sentence.
    const handleApprove = async () => {
      setDecisionError(null)
      if (!inv.can_approve) return
      if (!toApprovePhase({ type: 'confirm' })) return // no arm => no request
      if (approveInFlight.current) return
      approveInFlight.current = true
      try {
        await decideInvoice(ctx.authedFetch, base, invoiceId, 'approved')
        gen.current++
        setLive(null)
        detail.run()
        history.run()
        approval.run()
      } catch (err) {
        setDecisionError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
      } finally {
        // Functional setter -- see handleSubmit's own finally comment below; same stale-
        // closure rationale applies here.
        setApprovePhase((p) => bulkPhaseReducer(p, { type: 'settled' }))
        approveInFlight.current = false
      }
    }

    // Same shape as handleApprove above, guarded by the trim rule instead of the reducer
    // (reject has no arm stage -- the inline row IS the arm) and reset via `rejecting`
    // (a plain boolean, not a phase reducer) rather than a functional setter.
    const handleReject = async () => {
      setDecisionError(null)
      if (!inv.can_reject) return
      if (!canRejectReason(rejectReason)) return
      if (rejectInFlight.current) return
      rejectInFlight.current = true
      setRejecting(true)
      try {
        await decideInvoice(ctx.authedFetch, base, invoiceId, 'rejected', rejectReason)
        setRejectOpen(false)
        setRejectReason('')
        gen.current++
        setLive(null)
        detail.run()
        history.run()
        approval.run()
      } catch (err) {
        setDecisionError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
      } finally {
        setRejecting(false)
        rejectInFlight.current = false
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
              <h1 className="mono" style={{ fontSize: 22, letterSpacing: '-0.01em', margin: 0, whiteSpace: 'nowrap' }}>{inv.invoice_number}</h1>
              <span data-testid="invoice-status-badge" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: st.bg, border: `1px solid ${st.border}`, borderRadius: 'var(--radius-sm)', padding: '3px 9px' }}>
                <span style={{ width: 6, height: 6, borderRadius: '50%', background: st.text }} />
                <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text, letterSpacing: '0.04em' }}>{st.label}</span>
              </span>
            </div>
            <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{inv.buyer_name ?? '—'} · {fmtDate(inv.issue_date ?? inv.created_at)}</p>
          </div>

          {/* Always present ([actions-visibility]); each control disables off its own wire flag
              ([gates-on-the-wire]); `!editing` hides it ([D-actions-hidden-while-editing]). canEdit/canRevalidate: store.go:1536-1538 / :1558. */}
          {/* Outer column wraps the actions bar AND the submit skip/error banners together,
              so both stay in one right-aligned flex item ([D-actions-column]). The banners
              live OUTSIDE the `invoice-actions` gate on purpose: the bar unmounts while the
              inline editor owns the screen, and the banner describing a skipped submit must
              still render -- [never-report-success-on-a-skip] is not allowed to depend on
              the bar being mounted. */}
          {(!editing || submitSkipped != null || submitError != null) && (
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 10, maxWidth: 320 }}>
              {/* The decision pair, gated on `!editing` alone -- NOT
                  `can_edit` (task-554, AC-1/AC-2): approve/reject must survive on statuses
                  where `can_edit` is false (queued, submitted, failed, ...), and a decision
                  is taken on the STORED record, never on a dirty edit form.
                  A row wrapper (`detail-decision-actions`), not bare siblings, sits outside
                  `invoice-actions` so it survives that div's disappearance. Disabled: the
                  ghost swaps background/color; Approve dims to .45 with `filter: 'none'`. */}
              {!editing && (
                <>
                  <div data-testid="detail-decision-actions" style={{ display: 'flex', gap: 8, flexWrap: 'wrap', justifyContent: 'flex-end' }}>
                    {/* Arm -> confirm, same inline machine as Submit below ([no-modal]) --
                        swaps in place to detail-approve-cancel/-confirm while armed. Reject
                        stays independently clickable throughout (disabled only while its own
                        row is open, below), so the two decisions are never mutually exclusive. */}
                    {approvePhase === 'idle' ? (
                      <button
                        type="button"
                        data-testid="detail-approve"
                        onClick={() => toApprovePhase({ type: 'arm' })}
                        disabled={!inv.can_approve}
                        title={!inv.can_approve ? (inv.approve_blocked_reason ?? undefined) : undefined}
                        className="v2-btn v2-btn-primary pf-btn"
                        style={{
                          height: 34,
                          ...(!inv.can_approve
                            ? { opacity: 0.45, cursor: 'not-allowed', filter: 'none' }
                            : null),
                        }}
                      >
                        {DETAIL_DECISION_COPY.approve}
                      </button>
                    ) : (
                      <>
                        <button
                          type="button"
                          data-testid="detail-approve-cancel"
                          onClick={() => toApprovePhase({ type: 'cancel' })}
                          disabled={approvePhase === 'submitting'}
                          className="v2-btn v2-btn-ghost pf-btn"
                          style={{
                            height: 34,
                            ...(approvePhase === 'submitting' ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                          }}
                        >
                          {DETAIL_DECISION_COPY.cancel}
                        </button>
                        <button
                          type="button"
                          data-testid="detail-approve-confirm"
                          onClick={() => void handleApprove()}
                          disabled={approvePhase === 'submitting'}
                          className="v2-btn v2-btn-primary pf-btn"
                          style={{
                            height: 34,
                            ...(approvePhase === 'submitting' ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                          }}
                        >
                          {approvePhase === 'submitting' ? DETAIL_DECISION_COPY.approveSending : DETAIL_DECISION_COPY.approveConfirm}
                        </button>
                      </>
                    )}
                    <button
                      type="button"
                      data-testid="detail-reject"
                      onClick={() => setRejectOpen(true)}
                      disabled={!inv.can_reject || rejectOpen}
                      title={!inv.can_reject ? (inv.reject_blocked_reason ?? undefined) : undefined}
                      className="v2-btn v2-btn-ghost pf-btn"
                      style={{
                        height: 34,
                        ...(!inv.can_reject || rejectOpen ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                      }}
                    >
                      {DETAIL_DECISION_COPY.reject}
                    </button>
                  </div>
                  {/* Founder-pinned copy, verbatim (DETAIL_DECISION_COPY) -- same placement
                      and styling as detail-submit-confirm-prompt below. */}
                  {approvePhase !== 'idle' && (
                    <div data-testid="detail-approve-confirm-prompt" style={{ textAlign: 'right' }}>
                      <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg-1)' }}>{DETAIL_DECISION_COPY.approvePrompt}</div>
                      <div style={{ fontSize: 12, color: 'var(--fg-3)' }}>{DETAIL_DECISION_COPY.approveDetail}</div>
                    </div>
                  )}
                  {/* Inline reject row, never a modal ([no-modal]) -- a sibling OUTSIDE
                      detail-decision-actions: flexWrap:'wrap' is mandatory, not cosmetic -- the
                      input plus both button labels don't fit on one line at 320px. */}
                  {rejectOpen && (
                    <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', justifyContent: 'flex-end' }}>
                      <input
                        type="text"
                        data-testid="detail-reject-reason"
                        aria-label="Reason for rejection"
                        placeholder={DETAIL_DECISION_COPY.rejectPlaceholder}
                        value={rejectReason}
                        onChange={(e) => setRejectReason(e.target.value)}
                        disabled={rejecting}
                        className="pf-input"
                        style={{ flex: '1 1 100%', height: 34, fontSize: 13, padding: '0 10px' }}
                      />
                      <button
                        type="button"
                        data-testid="detail-reject-cancel"
                        onClick={() => {
                          setRejectOpen(false)
                          setRejectReason('')
                        }}
                        disabled={rejecting}
                        className="v2-btn v2-btn-ghost pf-btn"
                        style={{
                          height: 34,
                          flexShrink: 0,
                          whiteSpace: 'nowrap',
                          ...(rejecting ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                        }}
                      >
                        {DETAIL_DECISION_COPY.cancel}
                      </button>
                      <button
                        type="button"
                        data-testid="detail-reject-confirm"
                        onClick={() => void handleReject()}
                        disabled={rejecting || !canRejectReason(rejectReason)}
                        className="v2-btn v2-btn-primary pf-btn"
                        style={{
                          height: 34,
                          flexShrink: 0,
                          whiteSpace: 'nowrap',
                          ...(rejecting || !canRejectReason(rejectReason)
                            ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed', filter: 'none' }
                            : null),
                        }}
                      >
                        {rejecting ? DETAIL_DECISION_COPY.rejectSending : DETAIL_DECISION_COPY.rejectConfirm}
                      </button>
                    </div>
                  )}
                </>
              )}
              {!editing && (
                <div data-testid="invoice-actions" style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 10 }}>
                  <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', justifyContent: 'flex-end' }}>
                    <button
                      type="button"
                      data-testid="edit-toggle"
                      // Second line of defence, like handleSubmit's own gate: since the bar
                      // mounts at every status, `disabled` is otherwise the only barrier.
                      onClick={() => {
                        if (inv.can_edit) setEditing(true)
                      }}
                      disabled={!inv.can_edit}
                      className="v2-btn v2-btn-primary pf-btn"
                      style={{
                        height: 34,
                        ...(!inv.can_edit ? { opacity: 0.45, cursor: 'not-allowed', filter: 'none' } : null),
                      }}
                    >
                      Edit
                    </button>
                    {/* Disabled rather than hidden ([revalidate-visibility]) -- hiding it makes
                        the edit -> demote -> re-validate loop undiscoverable. Real `disabled` attribute;
                        the ghost swaps background/color inline (`.v2-btn-ghost:hover` is unguarded),
                        Edit dims to .45.
                        `title` rides along ([title-survives]) but only while the WIRE says blocked,
                        never on an enabled control and never on a transient in-flight disable. */}
                    <button
                      type="button"
                      data-testid="revalidate"
                      onClick={handleRevalidate}
                      disabled={revalidateDisabled}
                      title={!inv.can_revalidate ? (inv.revalidate_blocked_reason ?? undefined) : undefined}
                      className="v2-btn v2-btn-ghost pf-btn"
                      style={{
                        height: 34,
                        // Spread ONLY when disabled: an inline `background` on the enabled
                        // button would also kill its legitimate :hover affordance.
                        ...(revalidateDisabled ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                      }}
                    >
                      {revalidating ? 'Revalidating…' : 'Re-validate'}
                    </button>
                    {/* Inline arm -> confirm, not a modal ([no-modal], ReviewInvoicesTab.tsx file
                        header) -- the second stage renders below, in this same actions column.
                        Always rendered, disabled rather than hidden when `!inv.can_submit`. Disabled
                        Submit dims to .45 with `filter: 'none'` (`.v2-btn-primary:hover` brightens);
                        `handleSubmit`'s `!inv.can_submit` guard backs the `disabled` attribute. */}
                    {submitPhase === 'idle' ? (
                        <button
                          type="button"
                          data-testid="detail-submit"
                          onClick={() => toSubmitPhase({ type: 'arm' })}
                          disabled={!inv.can_submit}
                          title={!inv.can_submit ? (inv.submit_blocked_reason ?? undefined) : undefined}
                          className="v2-btn v2-btn-primary pf-btn"
                          style={{
                            height: 34,
                            ...(!inv.can_submit ? { opacity: 0.45, cursor: 'not-allowed', filter: 'none' } : null),
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
                              height: 34,
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
                              height: 34,
                              ...(submitPhase === 'submitting' ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                            }}
                          >
                            {submitPhase === 'submitting' ? DETAIL_SUBMIT_COPY.sending : DETAIL_SUBMIT_COPY.confirm}
                          </button>
                        </>
                      )}
                  </div>
                  {/* Genuine-failure surface, moved here from the deleted fused card. Style
                      unchanged; only the card-relative `margin` is dropped, since the column's
                      own `gap` now does that spacing. */}
                  {revalidateError && (
                    <div style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)', textAlign: 'left' }}>
                      {revalidateError}
                    </div>
                  )}
                  {/* Founder-pinned copy, verbatim (DETAIL_SUBMIT_COPY) -- two sentences as two
                      lines, matching ReviewInvoicesTab's bulk-bar confirm stage. */}
                  {submitPhase !== 'idle' && (
                    <div data-testid="detail-submit-confirm-prompt" style={{ textAlign: 'right' }}>
                      <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg-1)' }}>{DETAIL_SUBMIT_COPY.prompt}</div>
                      <div style={{ fontSize: 12, color: 'var(--fg-3)' }}>{DETAIL_SUBMIT_COPY.detail}</div>
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
              {/* Copies detail-submit-error's shape exactly, sourced from the decide
                  handlers' catch. A late child of this column, outside the can_edit gate
                  above -- a decision that lands can flip can_edit on refetch, and this
                  banner must survive that. Carries ApiError.message verbatim (D-24: never
                  the ErrorState retry surface). */}
              {decisionError != null && (
                <div data-testid="detail-decision-error" style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)', textAlign: 'left' }}>
                  {decisionError}
                </div>
              )}
            </div>
          )}
        </div>

        {/* Gated on the approval fetch settling: useAsync carries data:null while loading,
            and a null run captions node 3 `Not required` -- a false compliance claim on an
            invoice that does need approval (InvoiceDetail.test.tsx 'node 3 never flashes').
            A poll tick's history.run() still nulls history.data for one round trip
            (async-state.ts 'start'), so captions blank rather than lie. No last-good ref:
            deps:[invoiceId] resets useAsync and a ref would paint the previous invoice's
            timestamps. */}
        {approval.status !== 'idle' && approval.status !== 'loading' && (
          <StatusStrip
            nodes={stripNodes(history.data ?? [], approval.status === 'ready' ? approval.data : null, inv.status)}
          />
        )}

        <div className="pf-detail-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 340px', gap: 16, alignItems: 'start' }}>
          {/* A wrapper, not a third grid child: a third child auto-places into row 2 and
              starts below the RAIL's bottom edge whenever the rail is taller, stranding a
              gap under the record card. minWidth:0 is load-bearing -- `1fr` is
              minmax(auto,1fr), whose automatic minimum is content-based, and the activity
              card's 868px table would raise it. Held by the activity card's geometry spec C. */}
          <div data-testid="invoice-main-column" style={{ display: 'flex', flexDirection: 'column', gap: 16, minWidth: 0 }}>
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
                  focus={lineFocus}
                  onFocusApplied={() => setLineFocus(null)}
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
                    <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
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

            <div data-testid="compliance-card" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
              <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
                <span className="card-title">Compliance</span>
                {/* Gated on the same condition that chooses the table over not-validated:
                    an invoice never validated must not be told a version. */}
                {inv.rule_set_version != null && (
                  <span data-testid="compliance-ruleset-version" className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)' }}>
                    Rule-set v{inv.rule_set_version}
                  </span>
                )}
              </div>
              <div style={{ padding: '14px 18px', display: 'flex', flexDirection: 'column', gap: 10 }}>
                {/* The persisted reason, verbatim (BUG-03-03) -- amber, matching
                    ReviewRow.tsx's own kept-as-is banner rather than inventing a second
                    tone for the same fact. */}
                {kept && (
                  <div
                    data-testid="detail-kept-banner"
                    style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', fontSize: 12.5, color: 'var(--status-amber-text)', lineHeight: 1.5 }}
                  >
                    <div>{ROW_EXPANSION_COPY.keptPrefix}{kept.reason}</div>
                    <div className="mono" style={{ marginTop: 4, opacity: 0.85 }}>{actorLabel(kept.by).text} · {fmtDateTime(kept.at)}</div>
                  </div>
                )}
                {verdict === 'stale' && (
                  <div
                    data-testid="stale-verdict"
                    style={{ padding: '9px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', fontSize: 12.5, color: 'var(--status-amber-text)' }}
                  >
                    Edited since the last validation — this verdict is stale. Run Re-validate to refresh it.
                  </div>
                )}
                {explainNotice != null && (
                  <div
                    data-testid="explain-notice"
                    style={{ padding: '9px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', fontSize: 12.5, color: 'var(--status-amber-text)', overflowWrap: 'anywhere' }}
                  >
                    {EXPLAIN_COPY.acceptFailed} {explainNotice}
                  </div>
                )}
                {inv.rule_set_version != null ? (
                  <div data-testid="violations-table">
                    <ViolationsTable
                      violations={inv.violations}
                      ruleSetVersion={inv.rule_set_version}
                      lineDisabled={!inv.can_edit}
                      onOpenLine={(target) => {
                        if (!inv.can_edit) return
                        setEditing(true)
                        setLineFocus((f) => ({ seq: (f?.seq ?? 0) + 1, target }))
                      }}
                      explainOpen={explainOpen}
                      explainDisabled={verdict === 'stale'}
                      explainTitle={EXPLAIN_COPY.stale}
                      onExplain={(v) => {
                        const k = violationKey(v)
                        setExplainOpen((cur) => (cur === k ? null : k))
                        setExplainNotice(null)
                      }}
                      renderExplanation={(v) => (
                        <ExplainPanel
                          key={violationKey(v)}
                          ctx={ctx}
                          base={base}
                          invoiceId={invoiceId}
                          violation={v}
                          lines={inv.line_items ?? []}
                          acceptDisabled={!inv.can_edit || editing}
                          acceptTitle={editing ? EXPLAIN_COPY.editorOpen : undefined}
                          onAccepted={() => handleSaved(false)}
                          onAcceptFailed={(m) => {
                            setExplainOpen(null)
                            setExplainNotice(m)
                            gen.current++
                            setLive(null)
                            detail.run()
                          }}
                        />
                      )}
                    />
                  </div>
                ) : (
                  <div data-testid="not-validated" style={{ fontSize: 13, color: 'var(--fg-3)' }}>
                    {ROW_EXPANSION_COPY.notValidated}
                  </div>
                )}
              </div>
            </div>

            <InvoiceActivityCard ctx={ctx} invoiceId={invoiceId} invoiceNumber={inv.invoice_number} />
          </div>

          <div data-testid="invoice-rail" style={{ display: 'flex', flexDirection: 'column', gap: 16, minWidth: 0 }}>
            {inv.status === 'failed' && (
              <div data-testid="failed-dead-end" style={{ background: 'var(--bg-2)', border: '1px solid var(--status-red-border)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
                <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--status-red-bg)' }}>
                  <span className="card-title" style={{ color: 'var(--status-red-text)' }}>Submission failed</span>
                </div>
                <div style={{ padding: '14px 18px', display: 'flex', flexDirection: 'column', gap: 9, fontSize: 12.5, lineHeight: 1.5 }}>
                  <div style={{ color: 'var(--fg-2)' }}>
                    This submission failed and is terminal — it cannot be re-driven from this screen.
                  </div>
                  <div data-testid="failure-headline" style={{ fontWeight: 600 }}>{failure.headline}</div>
                  <div data-testid="failure-detail" style={{ color: 'var(--fg-2)' }}>{failure.detail}</div>
                  <div data-testid="failure-next-step" style={{ color: 'var(--fg-2)' }}>
                    {failure.nextStep}
                  </div>
                  {/* Resolve-outside (Core AC #1/#4/#5/#6) -- inline, never a modal
                      ([no-modal]), the only affordance this diagnosis-only card carries.
                      Resolved and unresolved are mutually exclusive renders: the banner +
                      Undo replace the reason input + mark-resolved button entirely. */}
                  {resolvedMark ? (
                    <div style={{ paddingTop: 10, borderTop: '1px solid var(--line-1)' }}>
                      <div data-testid="detail-resolved-banner">
                        <div style={{ fontWeight: 600, color: 'var(--fg-1)' }}>{RESOLVE_OUTSIDE_COPY.resolvedPrefix}{resolvedMark.reason}</div>
                        <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', margin: '3px 0 8px' }}>{actorLabel(resolvedMark.by).text} · {fmtDateTime(resolvedMark.at)}</div>
                      </div>
                      {/* Re-resolving is legal (the wire's can_resolve_outside does not go
                          false once resolved), so Undo reads the same flag as the mark
                          button below rather than a separate one. Disabled, never hidden
                          (Core AC #4). */}
                      <button
                        type="button"
                        data-testid="resolve-outside-undo"
                        onClick={() => void handleUndoResolveOutside()}
                        disabled={!inv.can_resolve_outside || undoing}
                        title={!inv.can_resolve_outside ? (inv.resolve_outside_blocked_reason ?? undefined) : undefined}
                        className="v2-btn v2-btn-ghost pf-btn"
                        style={{
                          height: 30,
                          fontSize: 12.5,
                          ...(!inv.can_resolve_outside || undoing ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                        }}
                      >
                        {RESOLVE_OUTSIDE_COPY.undoLabel}
                      </button>
                    </div>
                  ) : (
                    <div style={{ paddingTop: 10, borderTop: '1px solid var(--line-1)' }}>
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
                          style={{ flex: '1 1 220px', minWidth: 160, height: 34, fontSize: 13 }}
                        />
                        {/* Disabled recipe as Reject / Re-validate: inline fill, because the ghost
                            `:hover` is not guarded by `:not(:disabled)`. */}
                        <button
                          type="button"
                          data-testid="resolve-outside"
                          onClick={() => void handleResolveOutside()}
                          disabled={resolveOutsideDisabled}
                          title={!inv.can_resolve_outside ? (inv.resolve_outside_blocked_reason ?? undefined) : undefined}
                          className="v2-btn v2-btn-ghost pf-btn"
                          style={{
                            height: 32,
                            fontSize: 12.5,
                            // Must not flex-shrink below its own text -- that's what wrapped the label.
                            flexShrink: 0,
                            whiteSpace: 'nowrap',
                            ...(resolveOutsideDisabled ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                          }}
                        >
                          {RESOLVE_OUTSIDE_COPY.label}
                        </button>
                      </div>
                    </div>
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
                <div style={{ padding: '16px 18px', display: 'flex', flexDirection: 'column', gap: 11 }}>
                  <div>
                    <div className="label" style={{ marginBottom: 3 }}>IRN</div>
                    <div data-testid="fiscal-irn" className="mono" style={{ fontSize: 11.5, fontWeight: 600, wordBreak: 'break-all', lineHeight: 1.4 }}>{inv.irn}</div>
                  </div>
                  <div>
                    <div className="label" style={{ marginBottom: 3 }}>CSID</div>
                    <div data-testid="fiscal-csid" className="mono" style={{ fontSize: 11, color: 'var(--fg-2)', wordBreak: 'break-all', lineHeight: 1.4 }}>{inv.csid ?? '—'}</div>
                  </div>
                  {inv.qr_png_base64 != null && (
                    // Literal #fff, not var(--bg-2): a QR plate must keep scanner contrast
                    // regardless of theme, so this one swatch deliberately does not follow
                    // a design token (story §6 / task-251 Stage-1 correction K).
                    <div style={{ background: '#fff', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: 7, display: 'flex', alignSelf: 'flex-start' }}>
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

            <ApprovalStateCard run={approval} />

            {!rejectionLeadsRail && rejectionCard}

            {/* INVED-01-07 deleted the fused "Fix & re-validate" card that used to sit
                here -- one card that welded an always-mounted edit form to a Re-validate
                button, so Edit and Re-validate could never be gated apart. Both now live
                in the page-header actions bar above, independently gated. */}

            {/* Not titled "Audit trail" (the design's name): import-wizard.spec.ts:576 pins
                zero matches. */}
            <SourceDocumentCard meta={source} onOpen={openPreview} extraction={extraction} onOpenExtraction={ctx.openExtraction} />
            <UblDocumentCard ctx={ctx} base={base} invoiceId={invoiceId} invoiceNumber={inv.invoice_number} canView={inv.can_view_ubl} blockedReason={inv.ubl_blocked_reason} editing={editing} onView={openUbl} />
          </div>
        </div>

        {/* Rendered inline, never portalled: `--bg-*`/`--fg-*` are declared on `.asc-app`
            (v2/app-layer.css), and this tree is inside it. Modal open state is local
            to this component -- nothing about it belongs on PlatformCtx. */}
        {previewOpen && (
          <SourceDocumentModal
            ctx={ctx}
            meta={source}
            invoiceNumber={inv.invoice_number}
            invoiceCreatedAt={inv.created_at}
            createdBy={history.data?.[0]?.actor ?? null}
            createdByResolved={
              history.data?.[0] ? { name: history.data[0].actor_name, kind: history.data[0].actor_kind } : undefined
            }
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
// here (add / edit / remove), and the invoice number, editable only while `can_correct_invoice_number`.
//
// Both state slices are seeded once from `inv` at mount, matching EntityFormModal's
// once-per-open init: the component only ever mounts while `editing`, so Cancel is
// literally unmount-and-discard and re-opening re-seeds from the current `inv` — there is
// no manual reset path to keep in sync. diffEditInput/diffLineItems still diff against the
// current `inv` prop (fresh on every parent re-render), so a later edit's patch is computed
// against the latest saved content even though the fields were seeded once.
//
// Field labels and `.pf-input` markup follow the app's shipped form convention; the
// table, remove ✕ and dashed add chip are the line-item repeater idiom.
function InvoiceEditBody({
  ctx,
  base,
  invoiceId,
  inv,
  focus,
  onFocusApplied,
  onSaved,
  onCancel,
}: {
  ctx: PlatformCtx
  base: string
  invoiceId: string
  inv: InvoiceDetailRecord
  focus: { seq: number; target: LineTarget } | null
  onFocusApplied: () => void
  onSaved: (renamed: boolean) => void
  onCancel: () => void
}) {
  const [form, setForm] = useState<EditFormState>(() => formFromInvoice(inv))
  // Seeded once at mount, like `form`. Kept out of EditFormState: the number is not an
  // EditFieldKey, so diffEditInput never sees it.
  const [number, setNumber] = useState(inv.invoice_number)
  const [rows, setRows] = useState<LineRowState[]>(() => rowsFromInvoice(inv))
  const [submitting, setSubmitting] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const linesRef = useRef<HTMLDivElement>(null)

  // Runs on mount too, so the first click (which mounts the editor) lands focus.
  useEffect(() => {
    if (!focus) return
    const row = linesRef.current?.querySelectorAll('[data-testid="line-row"]')[focus.target.line - 1]
    onFocusApplied() // consume once, so a later Edit open does not replay it
    if (!row) return
    const inputs = Array.from(row.querySelectorAll<HTMLInputElement>('[data-line-field]'))
    const hit = inputs.find((el) => el.dataset.lineField === focus.target.field) ?? inputs[0]
    hit?.focus()
  }, [focus?.seq])

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
          borderRadius: 'var(--radius-sm)',
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
    // Trimmed on both sides: padding, or a legacy untrimmed stored number, is not a rename.
    const typedNumber = number.trim()
    if (typedNumber !== inv.invoice_number.trim()) patch.invoice_number = typedNumber
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
      onSaved(patch.invoice_number !== undefined)
    } catch (err) {
      setFormError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form data-testid="edit-invoice" onSubmit={handleSubmit}>
      <div style={{ padding: '22px 24px', display: 'flex', flexDirection: 'column', gap: 16 }}>
        {formError && (
          <div style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)' }}>
            {formError}
          </div>
        )}
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
          <div>
            <div className="label" style={{ marginBottom: 5 }}>Invoice number</div>
            {inv.can_correct_invoice_number ? (
              <input data-testid="edit-invoice-number" className="pf-input" type="text" value={number} onChange={(e) => setNumber(e.target.value)} style={{ ...EDIT_INPUT, fontFamily: 'var(--font-mono)' }} disabled={submitting} />
            ) : (
              <>
                <input data-testid="edit-invoice-number" className="pf-input" type="text" value={number} readOnly aria-readonly="true" style={{ ...EDIT_INPUT, background: 'var(--bg-3)', fontFamily: 'var(--font-mono)', color: 'var(--fg-3)' }} disabled={submitting} />
                {inv.invoice_number_blocked_reason != null && (
                  <div style={{ fontSize: 12, color: 'var(--fg-3)', marginTop: 5, lineHeight: 1.4 }}>{inv.invoice_number_blocked_reason}</div>
                )}
              </>
            )}
          </div>
          <div>
            <div className="label" style={{ marginBottom: 5 }}>Issue date</div>
            {fieldFlag('issue_date')}
            <input className="pf-input" type="text" value={form.issue_date} onChange={(e) => updateField('issue_date', e.target.value)} placeholder="YYYY-MM-DD" style={{ ...EDIT_INPUT, fontFamily: 'var(--font-mono)' }} disabled={submitting} />
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
              hint below) is a concession to ".pf-input has no :disabled/:read-only style
              at all today" (product-advisor review, 2026-07-31) -- without it this field is
              visually IDENTICAL to every editable one beside it, which is worse than "unstyled"
              for a control that no longer does what it looks like it does. */}
          <div>
            <div className="label" style={{ marginBottom: 5 }}>Supplier name</div>
            {fieldFlag('supplier_name')}
            <input className="pf-input" type="text" value={form.supplier_name} readOnly aria-readonly="true" disabled={submitting} style={{ ...EDIT_INPUT, background: 'var(--bg-3)', color: 'var(--fg-3)' }} />
          </div>
          <div>
            <div className="label" style={{ marginBottom: 5 }}>Supplier TIN</div>
            {fieldFlag('supplier_tin')}
            <input className="pf-input" type="text" value={form.supplier_tin} readOnly aria-readonly="true" placeholder="########-####" style={{ ...EDIT_INPUT, background: 'var(--bg-3)', fontFamily: 'var(--font-mono)', color: 'var(--fg-3)' }} disabled={submitting} />
            {/* CreateMapping.tsx's existing vocabulary ("Supplier details come from <entity>,
                not the file"), reused rather than inventing new copy -- adapted to this screen
                (no file here to contrast against). */}
            <div style={{ fontSize: 12, color: 'var(--fg-3)', marginTop: 5, lineHeight: 1.4 }}>
              Supplier details come from {form.supplier_name || 'the linked entity'}, not editable here.
            </div>
          </div>
          <div>
            <div className="label" style={{ marginBottom: 5 }}>Buyer name</div>
            {fieldFlag('buyer_name')}
            <input className="pf-input" type="text" value={form.buyer_name} onChange={(e) => updateField('buyer_name', e.target.value)} style={{ ...EDIT_INPUT }} disabled={submitting} />
          </div>
          <div>
            <div className="label" style={{ marginBottom: 5 }}>Buyer TIN</div>
            {fieldFlag('buyer_tin')}
            <input className="pf-input" type="text" value={form.buyer_tin} onChange={(e) => updateField('buyer_tin', e.target.value)} placeholder="########-####" style={{ ...EDIT_INPUT, fontFamily: 'var(--font-mono)' }} disabled={submitting} />
          </div>
          <div>
            <div className="label" style={{ marginBottom: 5 }}>Subtotal</div>
            {fieldFlag('subtotal')}
            <input className="pf-input" type="text" value={form.subtotal} onChange={(e) => updateField('subtotal', e.target.value)} style={{ ...EDIT_INPUT }} disabled={submitting} />
            {/* The computed line-sum hint ([totals-ownership], Core AC #5). Rendered as a
                sibling AFTER the input, never between the label and the input — that slot
                belongs to field-flag, and the e2e label->input XPath must keep resolving.
                Deliberately PASSIVE: it never writes subtotal/VAT/total, never blocks Save,
                and carries no red/amber/border/icon and no "mismatch" wording, so it cannot
                be misread as a validation error. `lineSum` is rendered RAW — never through
                fmt(), which rounds to whole naira (lib/format.ts:5-7) and would erase the
                sub-naira disagreement this hint exists to expose. No subtotal-vs-hint
                comparison is drawn here: deciding they disagree is the rule engine's job. */}
            <div data-testid="computed-line-sum" style={{ fontSize: 11.5, color: 'var(--fg-3)', marginTop: 4, lineHeight: 1.5 }}>
              Lines total <span className="money">{lineSum ?? '—'}</span>
            </div>
          </div>
          <div>
            <div className="label" style={{ marginBottom: 5 }}>VAT</div>
            {fieldFlag('vat')}
            <input className="pf-input" type="text" value={form.vat} onChange={(e) => updateField('vat', e.target.value)} style={{ ...EDIT_INPUT }} disabled={submitting} />
          </div>
          <div>
            <div className="label" style={{ marginBottom: 5 }}>Total</div>
            {fieldFlag('total')}
            <input className="pf-input" type="text" value={form.total} onChange={(e) => updateField('total', e.target.value)} style={{ ...EDIT_INPUT }} disabled={submitting} />
          </div>
          <div>
            <div className="label" style={{ marginBottom: 5 }}>Currency</div>
            {fieldFlag('currency')}
            <input className="pf-input" type="text" value={form.currency} onChange={(e) => updateField('currency', e.target.value)} style={{ ...EDIT_INPUT }} disabled={submitting} />
          </div>
        </div>

        {/* Line items, editable ([edit-ux], Core AC #2 — the read-only table above has no
            equivalent). `key={i}` is safe: every input is fully controlled from `rows`, so a
            removal re-renders correct values regardless of key identity.
            No per-line rejection flags here ([line-level-flags-not-mapped]) — reasons whose
            MBS path points at a line still render in full on the rejection card. `line_tax`
            gets a column here only; widening the READ-ONLY table is out of scope, so a
            stored line_tax stays invisible until Edit is clicked (noted as a follow-up). */}
        <div>
          <div className="label" style={{ marginBottom: 5 }}>
            Line items
          </div>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflowX: 'auto' }}>
            <div ref={linesRef} style={{ minWidth: 'min-content' }}>
            <div style={{ display: 'grid', gridTemplateColumns: LINE_EDIT_GRID, gap: 8, padding: '8px 12px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
              <span className="label">Description</span>
              <span className="label">Qty</span>
              <span className="label">Unit</span>
              <span className="label" style={{ textAlign: 'right' }}>Amount</span>
              <span className="label" style={{ textAlign: 'right' }}>Tax</span>
              <span />
            </div>
            {rows.map((row, i) => (
              <div key={i} data-testid="line-row" style={{ display: 'grid', gridTemplateColumns: LINE_EDIT_GRID, gap: 8, padding: '8px 12px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}>
                <input data-line-field="description" className="pf-input" type="text" value={row.description} onChange={(e) => updateRow(i, 'description', e.target.value)} style={{ ...LINE_INPUT }} disabled={submitting} />
                <input data-line-field="quantity" className="pf-input" type="text" value={row.quantity} onChange={(e) => updateRow(i, 'quantity', e.target.value)} style={{ ...LINE_INPUT, fontFamily: 'var(--font-mono)', padding: '0 8px' }} disabled={submitting} />
                <input data-line-field="unit_price" className="pf-input" type="text" value={row.unit_price} onChange={(e) => updateRow(i, 'unit_price', e.target.value)} style={{ ...LINE_INPUT, fontFamily: 'var(--font-mono)', padding: '0 8px' }} disabled={submitting} />
                <input data-line-field="line_total" className="pf-input" type="text" value={row.line_total} onChange={(e) => updateRow(i, 'line_total', e.target.value)} style={{ ...LINE_INPUT, fontFamily: 'var(--font-mono)', padding: '0 8px' }} disabled={submitting} />
                <input data-line-field="line_tax" className="pf-input" type="text" value={row.line_tax} onChange={(e) => updateRow(i, 'line_tax', e.target.value)} style={{ ...LINE_INPUT, fontFamily: 'var(--font-mono)', padding: '0 8px' }} disabled={submitting} />
                <button
                  type="button"
                  data-testid="line-remove"
                  onClick={() => removeRow(i)}
                  disabled={submitting}
                  className="pf-btn"
                  aria-label="Remove line item"
                  style={{ width: 30, height: 30, borderRadius: 'var(--radius-md)', border: 0, background: 'transparent', color: 'var(--fg-3)', cursor: submitting ? 'not-allowed' : 'pointer', display: 'grid', placeItems: 'center' }}
                >
                  {closeGlyph}
                </button>
              </div>
            ))}
            </div>
          </div>
          <button
            type="button"
            data-testid="line-add"
            onClick={addRow}
            disabled={submitting}
            className="v2-btn v2-btn-ghost pf-btn"
            style={{ height: 30, fontSize: 12.5, marginTop: 10 }}
          >
            <span style={{ display: 'inline-flex' }}>{plusGlyph}</span> Add line item
          </button>
        </div>
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, paddingTop: 14, borderTop: '1px solid var(--line-1)' }}>
          <button type="button" data-testid="edit-cancel" onClick={onCancel} disabled={submitting} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34 }}>
            Cancel
          </button>
          <button type="submit" disabled={submitting} className="v2-btn v2-btn-primary pf-btn" style={{ height: 34 }}>
            {submitting ? 'Saving…' : 'Save changes'}
          </button>
        </div>
      </div>
    </form>
  )
}
