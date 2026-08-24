// The Audit screen. Firm-wide, never scoped to the company in the switcher -- it sends no
// entity_id, which is also why the nav item sits in the FIRM-WIDE group.
//
// `isEmpty: () => false` is deliberate: useAsync's default predicate never classifies a
// plain object as empty anyway, and this screen must take the empty/new-workspace
// distinction from the server's `log_is_empty` flag alone, never from a shape guess.
//
// The filter card (AUDIT-07) is mounted below with search/date-range wired; events/actor/
// company land in 07-04..06. The row expansion's invoice affordance writes into the same
// filter state via a separate entry point -- together they're what makes empty-by-filter
// reachable today.

import { useCallback, useEffect, useMemo, useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, useAsync } from '@invoice-os/api-client'

import { downloadGlyph, shieldGlyph15 } from '../glyphs'
import { getAuditLog, type AuditResponse } from '../lib/audit'
import { auditCsv, auditCsvFilename, auditExportToastCopy } from '../lib/auditCsv'
import { collectExportRows, type AuditExportFetchPage } from '../lib/auditExport'
import { AUDIT_FILTER_DEFAULT, auditFilterQuery, type AuditFilterState } from '../lib/auditFilters'
import {
  auditPageNext,
  auditPagePrev,
  auditPageResize,
  auditRangeLabel,
  auditScreenState,
  auditStripCount,
  AUDIT_COPY,
  AUDIT_EXPORT_CAP,
  AUDIT_IMMUTABILITY_CLAIM,
  AUDIT_PAGE_INITIAL,
  emptyByFilterCopy,
  type AuditPageState,
} from '../lib/auditView'
import { EVIDENCE_COPY } from '../lib/evidenceBundleView'
import { invoicesViewState, shouldFetchInvoices } from '../lib/invoices'
import type { PlatformCtx } from '../types'

import { AuditExportToast } from './AuditExportToast'
import { AuditFilterCard } from './AuditFilterCard'
import { AuditPager } from './AuditPager'
import { AuditRow } from './AuditRow'
import { AuditSkeleton } from './AuditSkeleton'
import { AuditTable } from './AuditTable'
import { EvidenceBundleDrawer } from './EvidenceBundleDrawer'

// The one DOM step in the export: mirrors ReviewUnreadableTab.tsx's downloadCsv. The BOM
// is what makes Excel read non-ASCII names as UTF-8 rather than the local codepage.
// Returns the Blob so the caller can read its byte size for the toast copy.
function downloadAuditCsv(csv: string, filename: string): Blob {
  const blob = new Blob([`\uFEFF${csv}`], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
  return blob
}

export function AuditView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [filterState, setFilterState] = useState<AuditFilterState>(AUDIT_FILTER_DEFAULT)
  const [page, setPage] = useState<AuditPageState>(AUDIT_PAGE_INITIAL)
  // Written only from the unfiltered probe below, never from the main (filtered) request,
  // so this can never be mistaken for a windowed total.
  const [lifetimeTotal, setLifetimeTotal] = useState<number | null>(null)
  // The last landed page, held WITH the page state that fetched it. useAsync nulls `data`
  // on 'start', so without this the table and its pager unmount on every page change --
  // the controls vanish under the pointer and the layout jumps, which is the thing the
  // skeleton exists to prevent. Pairing the response with its page state also keeps the
  // range readout describing the rows actually on screen, never the page being fetched.
  const [landed, setLanded] = useState<{ res: AuditResponse; page: AuditPageState } | null>(null)
  const [exporting, setExporting] = useState(false)
  // testId is optional so the CSV path keeps AuditExportToast's default; the drawer's
  // download names its own (EB-06-8b).
  const [exportToast, setExportToast] = useState<{ kind: 'success' | 'error'; text: string; testId?: string } | null>(
    null,
  )
  // Drives the trigger's aria-expanded and the drawer mount below.
  const [bundleOpen, setBundleOpen] = useState(false)
  // MembersView.tsx:72's idiom: must be STABLE -- it is a useDismiss dependency.
  const closeBundle = useCallback(() => setBundleOpen(false), [])
  const filtered = filterState.invoiceId != null

  // Recomputed only when the filter state itself changes, not on every render -- the date
  // range resolves relative to `now`, and re-deriving it on unrelated re-renders (a page
  // change, a landed response) would drift the `from` timestamp and refetch forever.
  const filterQuery = useMemo(() => auditFilterQuery(filterState), [filterState])

  const log = useAsync<AuditResponse>(
    () =>
      base
        ? getAuditLog(ctx.authedFetch, base, {
            limit: page.limit,
            ...(page.cursor != null ? { cursor: page.cursor } : {}),
            ...filterQuery,
          })
        : Promise.reject(new Error('no gateway configured')),
    { isEmpty: () => false, immediate: shouldFetchInvoices(base), deps: [JSON.stringify(filterQuery), page.limit, page.cursor] },
  )
  const status = invoicesViewState(base, log)
  // First load has nothing to hold on to, so it takes the skeleton. Every load after it
  // keeps the previous page on screen and disables the pager instead.
  const shown = log.data != null ? { res: log.data, page } : landed
  const state = auditScreenState(status, landed == null ? log.data : shown?.res ?? null, filtered)
  const events = shown?.res.events ?? []
  const zeroRows = events.length === 0
  const exportDisabled = zeroRows || exporting

  // A lifetime figure can only come from an unfiltered `total`; internal/audit exposes no
  // other source. Empty deps fires this once per mount, immune to filter/page changes.
  const probe = useAsync<AuditResponse>(
    () =>
      base
        ? getAuditLog(ctx.authedFetch, base, { limit: 1 })
        : Promise.reject(new Error('no gateway configured')),
    { isEmpty: () => false, immediate: shouldFetchInvoices(base), deps: [] },
  )

  useEffect(() => {
    if (log.data == null) return
    setLanded({ res: log.data, page })
    // `page` is read, not tracked: it is whatever fetched this envelope, and adding it here
    // would re-run the effect on a page change before its response lands.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [log.data])

  // A failed probe leaves probe.data null forever, so lifetimeTotal just stays null --
  // never surfaced as an error, never blocking the screen.
  useEffect(() => {
    if (probe.data == null) return
    setLifetimeTotal(probe.data.total)
  }, [probe.data])

  // Every filter change restarts pagination: a cursor addresses a row boundary inside one
  // filtered stream and means nothing in another.
  const applyInvoiceFilter = (id: string, number: string | null) => {
    setFilterState((s) => ({ ...s, invoiceId: id, invoiceNumber: number }))
    setPage(auditPageResize(page.limit))
    setExpandedId(null)
  }

  const handleFilterChange = (next: AuditFilterState) => {
    setFilterState(next)
    setPage(auditPageResize(page.limit))
    setExpandedId(null)
  }

  // filterQuery is memoized on filterState; snapshotting it here means a filter change made
  // after this click can never reach the in-flight loop, the file, or the toast.
  const handleExport = async () => {
    if (exporting || zeroRows || base == null) return
    setExporting(true)
    const startQuery = { ...filterQuery }
    try {
      const fetchPage: AuditExportFetchPage = (query) => getAuditLog(ctx.authedFetch, base, query)
      const { rows, truncated } = await collectExportRows(fetchPage, startQuery, AUDIT_EXPORT_CAP)
      const filename = auditCsvFilename(new Date())
      const blob = downloadAuditCsv(auditCsv(rows), filename)
      setExportToast({
        kind: 'success',
        text: auditExportToastCopy({ rows: rows.length, bytes: blob.size, filename, truncated, cap: AUDIT_EXPORT_CAP }),
      })
    } catch (err) {
      // collectExportRows throws on every abort (reject, bad limit echo, stalled cursor,
      // unfollowable has_more) -- nothing here writes a file on that path.
      const reason = err instanceof Error ? err.message : String(err)
      setExportToast({ kind: 'error', text: `Export stopped: ${reason}. No file was written.` })
    } finally {
      setExporting(false)
    }
  }

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      {/* Two-column row: the title stack on the left, the export control on the right --
          absent on a new workspace, same as the filter card below. */}
      <div style={{ marginBottom: 22, display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 20 }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 10 }}>
            {AUDIT_COPY.eyebrow}
          </div>
          <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>{AUDIT_COPY.h1}</h1>
          <p data-testid="audit-subtitle" style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>
            {ctx.user.tenantName ?? AUDIT_COPY.tenantFallback} · {AUDIT_COPY.subtitle}
          </p>
        </div>
        {/* Gated on `state` alone, not `landed`: `state` already reads `log.data` directly
            on the very first load (see its own computation above), so an initial fetch
            that lands on empty-by-filter would otherwise render this a render-tick behind
            audit-empty-by-filter, racing `landed`'s effect-driven update. */}
        {(state === 'loaded' || state === 'filtered' || state === 'empty-by-filter') && (
          <div style={{ textAlign: 'right', flex: 'none' }}>
            {/* The pair gets its own flex row: this wrapper is textAlign:right with no gap, so a
                second child would sit on collapsed whitespace. The reason line below stays a
                sibling of the row, never a flex item beside the buttons (EB-03-6). */}
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 8 }}>
              {/* Primary first because a primary reads first in a right-aligned group; the ghost
                  keeping the column's right edge is a consequence of that, not the reason for it.
                  aria-haspopup/aria-expanded are the first dialog trigger in this SPA -- the
                  aria-expanded sites in src/ are in-place expanders, a different pattern.
                  No `disabled` at all: the gate below admits only loaded/filtered/
                  empty-by-filter, so no reachable render has this present and inert (EB-03-5). */}
              <button
                type="button"
                data-testid="audit-bundle-open"
                className="v2-btn v2-btn-primary pf-btn"
                aria-haspopup="dialog"
                aria-expanded={bundleOpen}
                onClick={() => setBundleOpen(true)}
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 8,
                  height: 36,
                  padding: '0 14px',
                  fontSize: 13,
                }}
              >
                <span style={{ display: 'inline-flex' }}>{shieldGlyph15}</span>
                <span className="mono">{EVIDENCE_COPY.openCaption}</span>
              </button>
              <button
                type="button"
                data-testid="audit-export"
                className="v2-btn v2-btn-ghost pf-btn"
                disabled={exportDisabled}
                onClick={zeroRows ? undefined : handleExport}
                aria-describedby={zeroRows ? 'audit-export-reason' : undefined}
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 8,
                  height: 36,
                  padding: '0 14px',
                  fontSize: 13,
                  ...(exportDisabled ? { opacity: 0.4, cursor: 'not-allowed', background: 'transparent' } : {}),
                }}
              >
                <span style={{ display: 'inline-flex' }}>{downloadGlyph}</span>
                <span className="mono">{AUDIT_COPY.exportCaption}</span>
              </button>
            </div>
            {zeroRows && (
              <div id="audit-export-reason" data-testid="audit-export-reason" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 6 }}>
                {AUDIT_COPY.exportDisabledReason}
              </div>
            )}
          </div>
        )}
      </div>

      {/* Stated as fact, and rendered whatever the rung below resolves to: the guarantee
          holds on an empty workspace exactly as it does on a full one. */}
      <div
        data-testid="audit-immutability-strip"
        style={{ display: 'flex', alignItems: 'baseline', gap: 12, flexWrap: 'wrap', marginBottom: 16, padding: '10px 14px', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-1)', fontSize: 12.5, color: 'var(--fg-2)', lineHeight: 1.6 }}
      >
        <span>{AUDIT_IMMUTABILITY_CLAIM}</span>
        {auditStripCount(lifetimeTotal) != null && (
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', letterSpacing: '0.04em', marginLeft: 'auto' }}>
            {auditStripCount(lifetimeTotal)}
          </span>
        )}
      </div>

      {/* Gated on `state`, not `landed`: on a first load landing directly on
          empty-by-filter, `landed`'s effect hasn't committed yet on the render where `state`
          already reads it (same lag fixed on the export control above). */}
      {(state === 'loaded' || state === 'filtered' || state === 'empty-by-filter' || state === 'error') && (
        <AuditFilterCard
          state={filterState}
          facets={shown?.res.facets ?? { event: [], actor: [], company: [] }}
          busy={status === 'loading'}
          onChange={handleFilterChange}
        />
      )}

      {/* The real chrome plus shimmer rows, never a spinner: the layout must not move
          when data lands. */}
      {state === 'loading' && (
        <AuditTable>
          <AuditSkeleton />
        </AuditTable>
      )}
      {state === 'error' && log.error && <ErrorState error={log.error} onRetry={log.run} />}

      {/* No filter language: on a workspace that has recorded nothing, mentioning filters
          would invite the user to go looking for one that is not there. */}
      {state === 'new-workspace' && (
        <div data-testid="audit-new-workspace">
          <EmptyState title={AUDIT_COPY.emptyTitle} message={AUDIT_COPY.emptyMessage} />
        </div>
      )}

      {state === 'empty-by-filter' && (
        <div
          data-testid="audit-empty-by-filter"
          style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: '28px 24px', fontSize: 13.5, color: 'var(--fg-2)', lineHeight: 1.6 }}
        >
          {emptyByFilterCopy(lifetimeTotal)}
        </div>
      )}

      {(state === 'loaded' || state === 'filtered') && (
        <>
          <AuditTable>
            {events.map((e) => (
              <AuditRow
                key={e.id}
                event={e}
                expanded={expandedId === e.id}
                onToggle={() => setExpandedId(expandedId === e.id ? null : e.id)}
                onFilterToInvoice={filtered ? undefined : applyInvoiceFilter}
              />
            ))}
          </AuditTable>
          {/* Everything here reads `shown`, not `page`/`log.data`: while a page is in
              flight `page` has already advanced, so a readout built from it would describe
              rows that are not on screen. */}
          <AuditPager
            range={auditRangeLabel(shown?.page ?? page, events.length, shown?.res.total ?? 0)}
            limit={page.limit}
            canPrev={(shown?.page.stack.length ?? 0) > 0}
            canNext={shown?.res.page.has_more === true}
            busy={status === 'loading'}
            onPrev={() => setPage(auditPagePrev(page))}
            onNext={() => setPage(auditPageNext(page, shown?.res.page.next_cursor ?? null))}
            onLimit={(limit) => setPage(auditPageResize(limit))}
          />
        </>
      )}

      {exportToast && (
        <AuditExportToast
          kind={exportToast.kind}
          text={exportToast.text}
          testId={exportToast.testId}
          onDismiss={() => setExportToast(null)}
        />
      )}

      {bundleOpen && base != null && (
        <EvidenceBundleDrawer ctx={ctx} base={base} onClose={closeBundle} onToast={setExportToast} />
      )}
    </div>
  )
}
