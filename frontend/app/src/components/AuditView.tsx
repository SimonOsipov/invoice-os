// The Audit screen. Firm-wide, never scoped to the company in the switcher -- it sends no
// entity_id, which is also why the nav item sits in the FIRM-WIDE group.
//
// `isEmpty: () => false` is deliberate: useAsync's default predicate never classifies a
// plain object as empty anyway, and this screen must take the empty/new-workspace
// distinction from the server's `log_is_empty` flag alone, never from a shape guess.
//
// The filter CARD is AUDIT-07's. The one filter this story can set is the row expansion's
// invoice affordance, which is what makes the empty-by-filter state reachable at all.

import { useEffect, useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, useAsync } from '@invoice-os/api-client'

import { getAuditLog, type AuditResponse } from '../lib/audit'
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
  invoiceFilterPillLabel,
  type AuditPageState,
} from '../lib/auditView'
import { invoicesViewState, shouldFetchInvoices } from '../lib/invoices'
import type { PlatformCtx } from '../types'

import { AuditPager } from './AuditPager'
import { AuditRow } from './AuditRow'
import { AuditSkeleton } from './AuditSkeleton'
import { AuditTable } from './AuditTable'

interface InvoiceFilter {
  id: string
  number: string | null
}

export function AuditView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [invoiceFilter, setInvoiceFilter] = useState<InvoiceFilter | null>(null)
  const [page, setPage] = useState<AuditPageState>(AUDIT_PAGE_INITIAL)
  // Only ever written from an unfiltered response (see the effect below), so the
  // empty-by-filter copy can never pass a filtered `total` off as the size of the log.
  const [lifetimeTotal, setLifetimeTotal] = useState<number | null>(null)
  const filtered = invoiceFilter != null

  const log = useAsync<AuditResponse>(
    () =>
      base
        ? getAuditLog(ctx.authedFetch, base, {
            limit: page.limit,
            ...(page.cursor != null ? { cursor: page.cursor } : {}),
            ...(invoiceFilter ? { invoice_id: invoiceFilter.id } : {}),
          })
        : Promise.reject(new Error('no gateway configured')),
    { isEmpty: () => false, immediate: shouldFetchInvoices(base), deps: [invoiceFilter?.id, page.limit, page.cursor] },
  )
  const status = invoicesViewState(base, log)
  const state = auditScreenState(status, log.data, filtered)
  const events = log.data?.events ?? []

  useEffect(() => {
    // useAsync nulls data on 'start', so a landed envelope always belongs to the current
    // filter -- there is no window where a filtered total could be read as the lifetime one.
    if (log.data != null && !filtered && log.data.total > 0) setLifetimeTotal(log.data.total)
  }, [log.data, filtered])

  // Every filter change restarts pagination: a cursor addresses a row boundary inside one
  // filtered stream and means nothing in another.
  const applyInvoiceFilter = (id: string, number: string | null) => {
    setInvoiceFilter({ id, number })
    setPage(auditPageResize(page.limit))
    setExpandedId(null)
  }

  const clearFilter = () => {
    setInvoiceFilter(null)
    setPage(auditPageResize(page.limit))
    setExpandedId(null)
  }

  const pills = filtered && (
    <div data-testid="audit-filter-pills" style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginBottom: 14 }}>
      <button
        type="button"
        data-testid="audit-filter-pill"
        className="pf-chip pf-btn"
        onClick={clearFilter}
        aria-label={AUDIT_COPY.clearFilter}
        style={{ display: 'inline-flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}
      >
        {invoiceFilterPillLabel(invoiceFilter?.number ?? null)}
        <span aria-hidden style={{ color: 'var(--fg-3)' }}>×</span>
      </button>
    </div>
  )

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 22 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>
          {AUDIT_COPY.eyebrow}
        </div>
        <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>{AUDIT_COPY.h1}</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>
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

      {pills}

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
          <AuditPager
            range={auditRangeLabel(page, events.length, log.data?.total ?? 0)}
            limit={page.limit}
            canPrev={page.stack.length > 0}
            canNext={log.data?.page.has_more === true}
            busy={status === 'loading'}
            onPrev={() => setPage(auditPagePrev(page))}
            onNext={() => setPage(auditPageNext(page, log.data?.page.next_cursor ?? null))}
            onLimit={(limit) => setPage(auditPageResize(limit))}
          />
        </>
      )}
    </div>
  )
}
