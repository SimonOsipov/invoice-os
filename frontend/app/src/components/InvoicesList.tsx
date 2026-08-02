// Invoices list — live invoice feed (M4-09-04, task-185). Fetches the signed-in
// tenant's real invoices from the invoice service and renders them with per-row 7-state
// status badges, replacing the mock `active.invoices` feed + mock 5-label `statusStyle`
// for this surface only (Obsidian M4-09 System Design §4). Ported shell from
// Platform.dc.html ~L343-387. M5-09-04 removed the mock invoice-DETAIL branch
// (InvoiceDetail.tsx) — the mock generators used here (genInvoices/buildClients) are a
// separate concern and stay intact; they still feed Reports/Customers (Sidebar's own
// badges moved off them onto the live rollup, [dashboard-scope-per-client] — see
// Sidebar.tsx).
//
// The "Needs attention" toggle re-fetches server-side (`needs_attention=true` via
// `deps:[needsAttention]`) rather than re-deriving the predicate in the browser
// ([server-side-needs-attention]); this list is a CLIENT-scoped surface (Sidebar.tsx's
// CLIENT nav group), narrowed to the ACTIVE client server-side too, via listInvoices'
// own `entity_id` param ([entity-id-restored] regression fix -- ListHandler,
// handlers.go) -- `deps` below also includes `ctx.mode`/`ctx.active.entityId`, so
// switching companies in the workspace switcher re-fetches rather than leaving a stale,
// previous-entity page on screen. gateByActiveEntity (lib/invoices.ts) stays as a
// render-time invariant on TOP of the server-side filter -- see its own doc comment for
// why (a company switch's pre-refetch frame would otherwise flash the previous client's
// rows). Row click routes through the existing selectImported/importedInvoiceId/
// detailTarget->'imported' seam with the real invoice UUID ([reuse-imported-seam]);
// rename deferred.
//
// M5-09-06 (task-257, Core AC #1) adds the selection model + batch submit below without
// touching any of the above: per-row/select-all checkboxes, a selection bar, and a
// results panel over `POST /invoices/submissions`. Selection logic itself is not
// reimplemented here — every decision (which rows are selectable, the header
// tri-state, pruning a stale selection, the skip-reason copy) lives in the pure,
// unit-tested helpers in lib/invoices.ts (M5-09-03); this component is markup and hook
// plumbing over them.

import { useEffect, useMemo, useRef, useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, toApiError, useAsync, type ApiError } from '@invoice-os/api-client'

import { plusGlyph } from '../glyphs'
import { fmt, fmtDate } from '../lib/format'
import {
  gateByActiveEntity,
  hasBlockingViolation,
  invoiceListIsEmpty,
  invoiceStatusStyle,
  invoicesViewState,
  isRowSelectable,
  LIVE_POLL_MS,
  listInvoices,
  newIdempotencyKey,
  pruneSelection,
  REGISTER_PAGE_SIZE,
  selectableIds,
  selectAllState,
  shouldFetchInvoices,
  shouldPollList,
  skipReasonLabel,
  submitInvoices,
  toggleSelection,
  type BatchSubmitResultItem,
  type InvoiceListResponse,
  type InvoiceRecord,
} from '../lib/invoices'
import { useDocumentVisible, useLiveRefresh } from '../lib/useLiveRefresh'
import type { PlatformCtx } from '../types'
import { Pager } from './Pager'

const INVOICE_GRID_COLUMNS = '24px 150px 1fr 140px 120px 130px'

// Tags the envelope with the entity it was fetched for, so staleness is read off the response itself rather than inferred from row count (row count alone can't tell a stale envelope apart from a poll that legitimately emptied the page).
type FetchedInvoiceList = InvoiceListResponse & { fetchedEntityId: string | undefined }

export function InvoicesList({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [needsAttention, setNeedsAttention] = useState(false)
  const [offset, setOffset] = useState(0)
  // In-house has no business_entities row ([entity-picker] trap 1) so its one "client"
  // IS the tenant -- entity_id omitted entirely, same as before this param existed. A
  // null entityId in firm mode (entities still loading/errored) ALSO omits it -- the
  // network call is a harmless tenant-wide fetch in that transient window,
  // gateByActiveEntity is what keeps it from ever rendering (see its own doc comment).
  const activeEntityId = ctx.mode === 'inhouse' ? undefined : (ctx.active.entityId ?? undefined)
  // Same `base ? … : …` narrowing as ClientsView.tsx:38-41 — `immediate:
  // shouldFetchInvoices(base)` keeps the no-gateway build at zero network. `deps`
  // re-runs the effect (and re-fetches) whenever the toggle flips, the page changes, OR
  // the active company changes ([entity-id-restored]: entity_id is now a server-side
  // param, so switching companies must trigger a real refetch, not just a client-side
  // recompute).
  const list = useAsync<FetchedInvoiceList>(
    () =>
      base
        ? // Keeps the {invoices, pagination} envelope whole -- the pager below reads
          // `pagination` off this same response, never a client constant.
          listInvoices(ctx.authedFetch, base, { needsAttention, entityId: activeEntityId, limit: REGISTER_PAGE_SIZE, offset, q: ctx.invoiceQuery || undefined }).then(
            (r) => ({ ...r, fetchedEntityId: activeEntityId }),
          )
        : Promise.reject(new Error('no gateway configured')),
    {
      isEmpty: invoiceListIsEmpty,
      immediate: shouldFetchInvoices(base),
      deps: [needsAttention, ctx.mode, ctx.active.entityId, offset, ctx.invoiceQuery],
    },
  )
  const state = invoicesViewState(base, list)
  // A plain boolean, not a re-compared `state === 'loading'`, at the two Pager call
  // sites below: both sit inside a `state === 'ready'` branch, where TS narrows `state`
  // to the literal 'ready' and rejects that comparison as unreachable.
  const loading = state === 'loading'
  // Both sides are `string | undefined` (in-house has no entity) -- `undefined ===
  // undefined` correctly reads as fresh there. False whenever `list.data` is still
  // null so this can be read safely ahead of the `list.data != null` render guards.
  const fresh = list.data != null && list.data.fetchedEntityId === activeEntityId

  // M5-09-07 live-refresh overlay ([poll-overlay-not-rerun]) — a poll tick never calls
  // list.run() (THE LOAD-BEARING TRAP: that dispatches useAsync's 'start' action, nulls
  // `data`, and flashes <Loading/> every 2s), so it writes here instead. Declared before
  // the `rows` memo below, which layers `live` over `list.data`.
  const [live, setLive] = useState<InvoiceRecord[] | null>(null)
  // `gen` closes the in-flight-tick race: a tick's GET that was already in flight when
  // `list.data` changed (any of submitSelection's list.run(), the retry list.run(), or
  // the deps-driven refetch on `needsAttention`) must not resolve afterwards and
  // re-install a stale overlay over the new filter's rows. Mirrors useAsync's own
  // runId idiom (packages/api-client/src/async-state.ts:89/92/96).
  const gen = useRef(0)
  // Single place that covers all THREE `list.run()` call sites (submitSelection below,
  // the ErrorState retry, and the deps:[needsAttention] refetch that has no explicit
  // call site of its own — it lives inside useAsync's mount/deps effect) — `list.data`
  // changes identity on every one of them (null on 'start', a new array on 'success'),
  // and a tick never touches `list.data` itself, so this fires only on genuine runs.
  useEffect(() => {
    gen.current++
    setLive(null)
  }, [list.data])

  // The single row source for the whole component (replaces the old inline `(list.data
  // ?? []).map`). MUST be memoized: `live ?? list.data ?? []` as a bare expression
  // allocates a fresh `[]` on every render even when neither input changed, which would
  // make the prune effect below re-run every render forever. `live` MUST be in the deps
  // — the overlay tick never calls `list.run()`, so `list.data` alone would never change
  // on a poll and the prune effect would never fire for a row that advances past
  // `validated` between polls.
  //
  // [dashboard-scope-per-client]: the fetch itself is already entity-scoped
  // ([entity-id-restored]); gateByActiveEntity (see the file-header comment) is the
  // render-time invariant on top of it — blanks the "not yet resolved" transient
  // window entirely, and still row-filters otherwise so a company switch that hasn't
  // finished refetching yet never flashes the previous client's rows for a frame.
  // Every downstream consumer (selection, the live-poll gate, the empty-state check
  // below) sees only this client's own rows; there is exactly one `rows` in this
  // component. `ctx.mode`/`ctx.active.entityId` join the dep list alongside
  // `live`/`list.data` so that frame is covered immediately, without waiting on the
  // refetch.
  const rows = useMemo(
    () => gateByActiveEntity(live ?? list.data?.invoices ?? [], ctx.mode === 'inhouse', ctx.active.entityId),
    [live, list.data, ctx.mode, ctx.active.entityId],
  )

  const [selected, setSelected] = useState<string[]>([])

  // A new search term resets the page and clears the selection
  // ([paging-clears-the-selection]), mirroring the inline reset the needs-attention
  // toggle does below. A no-op setOffset(0) when already on page 1 bails out of the
  // re-render, so this doesn't double-fetch in the common case.
  useEffect(() => {
    setOffset(0)
    setSelected([])
  }, [ctx.invoiceQuery])

  // Drops any selected id that fell out of `rows` (paged/filtered away) or is no longer
  // `validated` (submitted, edited, or re-validated out from under a stale selection)
  // whenever `rows` changes — e.g. a live-refresh poll tick (M5-09-07) that advances a
  // selected row to `queued` mid-selection. `rows` is a fresh array reference on every
  // render even when its contents are identical, so the updater must return the SAME
  // `sel` instance when nothing actually changed; otherwise every render would produce a
  // new `selected` array, which would re-trigger this effect on the next render and
  // React 19 would hard-throw "Maximum update depth exceeded".
  useEffect(() => {
    setSelected((sel) => {
      const next = pruneSelection(sel, rows)
      return next.length === sel.length ? sel : next
    })
  }, [rows])

  const allState = selectAllState(selected, rows)

  // One result item plus the invoice number it resolves to, captured from `rows` at
  // submit time (see submitSelection) rather than looked up live at render time: the
  // success path calls `list.run()` right after, which nulls `list.data` for the
  // duration of the refetch, so a live `rows.find` would flicker every row to its raw
  // UUID until the refetch lands.
  const [results, setResults] = useState<{ item: BatchSubmitResultItem; invoiceNumber: string }[] | null>(null)
  const [submitError, setSubmitError] = useState<ApiError | null>(null)
  const [submitting, setSubmitting] = useState(false)
  // Ref guard mirrors App.tsx's `reqInFlight` (App.tsx:106-113): React batches state
  // updates, so a fast double-click on Submit can fire this handler a second time before
  // `submitting` re-renders the button as `disabled`. The ref is checked and set
  // synchronously, before anything async, so it can't lose that race the way `disabled`
  // alone would. `submitting` stays alongside it purely to drive the visible
  // disabled/opacity state — mutating a ref never triggers a re-render on its own.
  const submitInFlight = useRef(false)

  // M5-09-07 live-refresh poll ([poll-interval-2s]): gated on any row being in-flight
  // AND the tab being visible — both from the tested predicate, never re-derived inline.
  const visible = useDocumentVisible()
  const active = shouldPollList(rows, visible)
  // CodeRabbit fix cycle 2, finding 4: overlapping ticks. useLiveRefresh's interval fires
  // unconditionally, so a round-trip slower than LIVE_POLL_MS leaves two listInvoices()
  // calls in flight under the same `gen`; the older can resolve last and re-install stale
  // rows for one visible tick before the next tick self-heals it. Same re-entrancy-ref
  // idiom as `submitInFlight` above / App.tsx's `reqInFlight`.
  //
  // `entityId: activeEntityId` here too ([entity-id-restored]) -- a poll tick that
  // omitted it would silently re-fetch the FULL tenant-wide page every LIVE_POLL_MS and
  // install it via setLive below, which gateByActiveEntity would then render UNGATED
  // (entityId is non-null once resolved) -- the exact class of leak this whole fix
  // exists to close, just on a 2s timer instead of the initial fetch.
  const tickInFlight = useRef(false)
  useLiveRefresh(
    () => {
      if (base == null) return
      if (tickInFlight.current) return
      tickInFlight.current = true
      const g = gen.current
      listInvoices(ctx.authedFetch, base, { needsAttention, entityId: activeEntityId, limit: REGISTER_PAGE_SIZE, offset, q: ctx.invoiceQuery || undefined })
        .then((r) => {
          if (g === gen.current) setLive(r.invoices)
        })
        .catch(() => {}) // a transient blip is silent -- the next tick retries (AC-6)
        .finally(() => { tickInFlight.current = false })
    },
    active,
    LIVE_POLL_MS,
  )

  function toggleAll() {
    setSelected(allState === 'all' ? [] : selectableIds(rows))
    setSubmitError(null) // stale error from a previous, now-superseded selection
  }

  // Every submit mints a brand-new idempotency key (never reused across clicks or
  // renders). The batch-submit endpoint's own concurrency control (a `SELECT ... FOR
  // UPDATE` over invoice status) already makes a fast double-click or two concurrent
  // requests for the SAME selection safe regardless of the key — the real reason to mint
  // fresh every time is the resubmit-after-rejection leg (Core AC #5): an invoice
  // submitted with key K, rejected by the APP, fixed and re-validated back to
  // `validated`. Resubmitting with a REUSED K derives the identical per-invoice
  // dedupe key, the enqueue is skipped as a duplicate, and no status transition runs —
  // the invoice silently strands at `validated` while the results panel reads
  // "Already submitted with this request" as if it succeeded.
  //
  // No client-side 200-id cap check, and the reason is now STRUCTURAL rather than a
  // property of this call site: `ListHandler` silently clamps `limit` down to 200
  // (handlers.go:370-372), so ANY page of GET /v1/invoices holds ≤200 rows for ANY `limit`
  // ANY caller passes — `listInvoices` gaining a `limit` param (INVCR-01-08) cannot
  // breach it. `rows` here is exactly one such page, and `selected` is pruned to
  // `selectableIds(rows)`, so `selected.length ≤ 200` while the endpoint rejects only
  // `> 200`. THE HONEST COST: the margin used to be 150 (a 50-row page against a 200-id
  // cap) and is now ZERO — a full-page select-all at `limit=200` saturates the cap
  // exactly. Two facts hold it up, and either one breaking needs a real clamp here: the
  // two 200s staying equal (the server's limit ceiling and batch-submit's id cap are
  // independent constants that nothing ties together), and selection never spanning pages.
  async function submitSelection() {
    if (base == null) return
    if (submitInFlight.current) return
    submitInFlight.current = true
    setSubmitting(true)
    setSubmitError(null)
    try {
      const res = await submitInvoices(ctx.authedFetch, base, selected, newIdempotencyKey())
      // submitInvoices returns `res.results` unguarded (invoices.test.ts SUB-3 pins a
      // malformed 2xx body resolving to `undefined` and names this call site as the
      // guard) — normalize once, here, so `results !== null` below can never see
      // `undefined` and throw out of `.map` with no error boundary to catch it.
      const items = res ?? []
      // Resolve invoice numbers from `rows` NOW, before `list.run()` below nulls
      // `list.data` for the duration of the refetch (see the `results` state comment).
      const numbersById = new Map(rows.map((row) => [row.id, row.invoice_number]))
      setResults(items.map((item) => ({ item, invoiceNumber: numbersById.get(item.invoice_id) ?? item.invoice_id })))
      setSelected([])
      // Badges are never derived from this response — batch_submit.go's
      // duplicate-request branch hard-codes a known-wrong "queued" status (M5-11).
      // Re-fetching the list is the only trustworthy source for the new badges.
      list.run()
    } catch (err) {
      // An unknown or cross-tenant invoice id hard-fails the WHOLE batch with a single
      // 404 rather than producing a per-item skip result (pruneSelection narrows this
      // window — it can't close it, since an id can go stale between a poll tick and the
      // click). Leave the selection intact so the operator can retry without re-picking
      // rows; skip the refetch and results panel, since there is no results array to
      // show for a request-level failure.
      setSubmitError(toApiError(err))
    } finally {
      submitInFlight.current = false
      setSubmitting(false)
    }
  }

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      {/* No page-level "New invoice" here: the persistent header-bar CTA (Header.tsx) is
          the single create affordance for the populated list. The empty state below keeps
          its own button (standard zero-state pattern). The "Needs attention" toggle sits
          in the header row (not gated by async state) so it stays reachable even when the
          filtered result set is itself empty. */}
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 22 }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 10 }}>
            INVOICE REGISTER
          </div>
          <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Invoices</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{ctx.user.tenantName ?? 'Your workspace'} · create, validate, and transmit.</p>
        </div>
        <button
          onClick={() => {
            setNeedsAttention((v) => !v)
            setOffset(0)
            setSelected([])
          }}
          data-testid="needs-attention-toggle"
          className="pf-chip"
          style={{
            height: 30,
            padding: '0 12px',
            borderRadius: 'var(--radius-md)',
            fontFamily: 'var(--font-sans)',
            fontSize: 12.5,
            fontWeight: 500,
            border: `1px solid ${needsAttention ? 'var(--action)' : 'var(--line-2)'}`,
            background: needsAttention ? 'var(--action)' : 'var(--bg-2)',
            color: needsAttention ? 'var(--text-on-dark)' : 'var(--fg-2)',
          }}
        >
          Needs attention
        </button>
      </div>

      {/* Rendered independent of both `selected.length > 0` (submit clears the
          selection, which would unmount a panel nested under that condition the instant
          it should appear) and `state === 'ready'` (list.run()'s refetch flips `state`
          away from 'ready' while data:null, which would blow the panel away mid-read). */}
      {results !== null && (
        <div data-testid="batch-submit-results" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden', marginBottom: 22 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
            <span className="label">Invoice #</span>
            <span className="label">Result</span>
          </div>
          {results.map(({ item: r, invoiceNumber }, i) => {
            // reason is optional and enqueued is a plain boolean, not a discriminant, so
            // `!r.enqueued` alone doesn't narrow `r.reason` — guard explicitly before
            // calling skipReasonLabel (a required string).
            const resultLabel = r.enqueued ? 'Queued' : r.reason != null ? skipReasonLabel(r.reason) : 'Not queued'
            return (
              <div key={`${r.invoice_id}-${i}`} style={{ display: 'flex', justifyContent: 'space-between', padding: '10px 18px', borderBottom: '1px solid var(--line-1)' }}>
                <span className="mono" style={{ fontSize: 12.5, fontWeight: 500 }}>{invoiceNumber}</span>
                <span style={{ fontSize: 12.5, color: r.enqueued ? 'var(--status-green-text)' : 'var(--fg-3)' }}>{resultLabel}</span>
              </div>
            )
          })}
        </div>
      )}

      {state === 'loading' && <Loading label="Loading invoices…" />}

      {state === 'error' && list.error && <ErrorState error={list.error} onRetry={list.run} />}

      {/* `state` reflects the ENTITY-SCOPED fetch itself ([entity-id-restored]) -- a
          genuinely invoice-less entity resolves to 'empty' directly, server-side
          (invoiceListIsEmpty reads `pagination.total`, never `rows.length`), so this rung
          fires only on a genuine zero-total set. */}
      {(state === 'idle' || state === 'empty') && (
        <div data-testid="invoices-empty">
          <EmptyState title="No invoices yet" message="Create or import an invoice to start tracking compliance." />
          <div style={{ display: 'flex', justifyContent: 'center', marginTop: 16 }}>
            <button onClick={ctx.openCreate} className="v2-btn v2-btn-primary pf-btn">
              <span style={{ display: 'inline-flex', marginRight: -2 }}>{plusGlyph}</span> New invoice
            </button>
          </div>
        </div>
      )}

      {/* !fresh: the envelope predates the active entity (a company switch's
          pre-refetch frame) -- bounded and self-healing, since `activeEntityId` is in
          `deps` and a refetch is always scheduled. Row count can't stand in for this:
          a poll tick that legitimately empties every in-flight row on THIS entity's
          page produces the identical `invoices: []` signature, and routing that case
          here too (f3b2b54) stranded the user on this same spinner with no refetch
          ever scheduled, since nothing in `deps` had changed. */}
      {state === 'ready' && list.data != null && !fresh && <Loading label="Loading invoices…" />}

      {/* Fresh and empty: total>0 (else `state` would be 'empty' above) and this
          entity's own slice for this offset is [] -- either a genuine mid-set-empty
          page or a poll tick that just emptied it. Both are trustworthy pagination
          now that the envelope is known to belong to this entity, so the Pager is
          mounted either way and the user can page back. */}
      {state === 'ready' && list.data != null && fresh && rows.length === 0 && (
        <div data-testid="invoices-empty-page">
          <EmptyState title="No invoices on this page" message="Go back to see the rest of the register." />
          <div style={{ marginTop: 16 }}>
            <Pager
              pagination={list.data.pagination}
              busy={loading}
              onGo={(o) => {
                setOffset(o)
                setSelected([])
              }}
              testId="invoices-pager"
            />
          </div>
        </div>
      )}

      {state === 'ready' && list.data != null && fresh && rows.length > 0 && (
        <>
          {selected.length > 0 && (
            <div data-testid="batch-submit-summary" style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '11px 18px', marginBottom: 14 }}>
              <span style={{ fontSize: 13, fontWeight: 500, color: 'var(--fg-1)' }}>{selected.length} selected on this page</span>
              <button
                data-testid="batch-submit"
                onClick={submitSelection}
                disabled={submitting}
                className="v2-btn v2-btn-primary pf-btn"
                style={{ height: 34, fontSize: 13, opacity: submitting ? 0.5 : 1, cursor: submitting ? 'not-allowed' : 'pointer' }}
              >
                {submitting ? 'Submitting…' : 'Submit'}
              </button>
            </div>
          )}

          {selected.length > 0 && submitError && (
            <div style={{ marginBottom: 14 }}>
              <ErrorState error={submitError} onRetry={submitSelection} />
            </div>
          )}

          <div data-testid="invoices-list" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
            <div className="pf-list-head" style={{ display: 'grid', gridTemplateColumns: INVOICE_GRID_COLUMNS, gap: 16, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)', alignItems: 'center' }}>
              <input
                type="checkbox"
                data-testid="invoice-select-all"
                aria-label="Select all validated invoices"
                // React has no `indeterminate` prop (it's a DOM-only property, not a
                // reflected HTML attribute) — a ref callback is the only way to set it.
                // Braces are required: `RefCallback` returns `void | (() => void)`, so an
                // implicit-return arrow here is a `tsc --noEmit` error under `strict`.
                ref={(el) => { if (el) el.indeterminate = allState === 'some' }}
                checked={allState === 'all'}
                onChange={toggleAll}
              />
              <span className="label">Invoice #</span>
              <span className="label">Buyer</span>
              <span className="label" style={{ textAlign: 'right' }}>Amount</span>
              <span className="label">Date</span>
              <span className="label">Status</span>
            </div>
            {rows.map((r) => {
              const st = invoiceStatusStyle(r.status)
              const errorCount = r.violations.filter((v) => v.severity === 'error').length
              return (
                // Click-only row (no keyboard affordance) predates this story --
                // CodeRabbit fix cycle 2 flagged it alongside the checkbox aria-label gap,
                // but no AC here covers row keyboard-reachability. Deferred as a follow-up
                // rather than expanded into this fix's scope.
                <div
                  key={r.id}
                  onClick={() => ctx.openImportedInvoice(r.id)}
                  data-testid="invoice-row"
                  className="pf-row pf-list-row"
                  style={{ display: 'grid', gridTemplateColumns: INVOICE_GRID_COLUMNS, gap: 16, padding: '14px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}
                >
                  <input
                    type="checkbox"
                    data-testid="invoice-select"
                    aria-label={`Select invoice ${r.invoice_number}`}
                    checked={selected.includes(r.id)}
                    disabled={!isRowSelectable(r.status)}
                    // Both handlers stop propagation — the row's own onClick (whole-row
                    // navigation) must never fire from a checkbox interaction.
                    onClick={(e) => e.stopPropagation()}
                    onChange={(e) => {
                      e.stopPropagation()
                      setSelected((sel) => toggleSelection(sel, r.id))
                      setSubmitError(null) // stale error from a previous, now-superseded selection
                    }}
                  />
                  <span className="mono" style={{ fontSize: 12.5, fontWeight: 500, color: 'var(--fg-1)' }}>{r.invoice_number}</span>
                  <span style={{ minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: 13.5, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{r.buyer_name}</span>
                    <span className="mono" style={{ fontSize: 11, color: r.buyer_tin ? 'var(--fg-3)' : 'var(--status-red-text)' }}>{r.buyer_tin ?? 'TIN MISSING'}</span>
                  </span>
                  <span className="money" style={{ fontSize: 13.5, fontWeight: 600, textAlign: 'right' }}>{r.total != null ? fmt(Number(r.total)) : '—'}</span>
                  <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>{fmtDate(r.issue_date ?? r.created_at)}</span>
                  <span style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start', gap: 4 }}>
                    <span
                      data-testid="invoice-status-badge"
                      style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: st.bg, border: `1px solid ${st.border}`, borderRadius: 999, padding: '3px 9px' }}
                    >
                      <span style={{ width: 6, height: 6, borderRadius: 99, background: st.text }} />
                      <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text, letterSpacing: '0.04em' }}>{st.label}</span>
                    </span>
                    {hasBlockingViolation(r) && (
                      <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: 'var(--status-red-text)', letterSpacing: '0.04em' }}>
                        {errorCount} ERROR{errorCount === 1 ? '' : 'S'}
                      </span>
                    )}
                  </span>
                </div>
              )
            })}
          </div>

          {/* Fed the response's own echoed pagination, never REGISTER_PAGE_SIZE -- the
              server clamps `limit`, and a client constant here would hide that clamp. */}
          <div style={{ marginTop: 16 }}>
            <Pager
              pagination={list.data.pagination}
              busy={loading}
              onGo={(o) => {
                setOffset(o)
                setSelected([])
              }}
              testId="invoices-pager"
            />
          </div>
        </>
      )}
    </div>
  )
}
