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

import { useEffect, useMemo, useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, useAsync } from '@invoice-os/api-client'

import { getAuditLog, type AuditResponse } from '../lib/audit'
import { AUDIT_FILTER_DEFAULT, auditFilterQuery, type AuditFilterState } from '../lib/auditFilters'
import {
  auditPageNext,
  auditPagePrev,
  auditPageResize,
  auditRangeLabel,
  auditScreenState,
  auditStripCount,
  AUDIT_COPY,
  AUDIT_IMMUTABILITY_CLAIM,
  AUDIT_PAGE_INITIAL,
  emptyByFilterCopy,
  type AuditPageState,
} from '../lib/auditView'
import { invoicesViewState, shouldFetchInvoices } from '../lib/invoices'
import type { PlatformCtx } from '../types'

import { AuditFilterCard } from './AuditFilterCard'
import { AuditPager } from './AuditPager'
import { AuditRow } from './AuditRow'
import { AuditSkeleton } from './AuditSkeleton'
import { AuditTable } from './AuditTable'

export function AuditView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [filterState, setFilterState] = useState<AuditFilterState>(AUDIT_FILTER_DEFAULT)
  const [page, setPage] = useState<AuditPageState>(AUDIT_PAGE_INITIAL)
  // Only ever written from an unfiltered response (see the effect below), so the
  // empty-by-filter copy can never pass a filtered `total` off as the size of the log.
  const [lifetimeTotal, setLifetimeTotal] = useState<number | null>(null)
  // The last landed page, held WITH the page state that fetched it. useAsync nulls `data`
  // on 'start', so without this the table and its pager unmount on every page change --
  // the controls vanish under the pointer and the layout jumps, which is the thing the
  // skeleton exists to prevent. Pairing the response with its page state also keeps the
  // range readout describing the rows actually on screen, never the page being fetched.
  const [landed, setLanded] = useState<{ res: AuditResponse; page: AuditPageState } | null>(null)
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

  useEffect(() => {
    if (log.data == null) return
    setLanded({ res: log.data, page })
    // useAsync nulls data on 'start', so a landed envelope always belongs to the current
    // filter -- there is no window where a filtered total could be read as the lifetime one.
    if (!filtered && log.data.total > 0) setLifetimeTotal(log.data.total)
    // `page` is read, not tracked: it is whatever fetched this envelope, and adding it here
    // would re-run the effect on a page change before its response lands.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [log.data, filtered])

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

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 22 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>
          {AUDIT_COPY.eyebrow}
        </div>
        <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>{AUDIT_COPY.h1}</h1>
        <p data-testid="audit-subtitle" style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>
          {ctx.user.tenantName ?? AUDIT_COPY.tenantFallback} · {AUDIT_COPY.subtitle}
        </p>
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

      {/* Mounted outside the loaded/filtered rung so the card also survives a refetch that
          lands on error or empty-by-filter -- `landed`'s cache already keeps `state` at
          loaded/filtered through a same-shape refetch, so the mount point only matters for
          the states outside that block. */}
      {landed != null && state !== 'new-workspace' && (
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
    </div>
  )
}
