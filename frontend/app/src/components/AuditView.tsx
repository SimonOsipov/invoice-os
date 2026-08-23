// The Audit screen. Firm-wide, never scoped to the company in the switcher -- it sends no
// entity_id, which is also why the nav item sits in the FIRM-WIDE group.
//
// `isEmpty: () => false` is deliberate: useAsync's default predicate never classifies a
// plain object as empty anyway, and this screen must take the empty/new-workspace
// distinction from the server's `log_is_empty` flag alone, never from a shape guess.

import { useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { getAuditLog, type AuditResponse } from '../lib/audit'
import { AUDIT_COPY } from '../lib/auditView'
import { invoicesViewState, shouldFetchInvoices } from '../lib/invoices'
import type { PlatformCtx } from '../types'

import { AuditRow } from './AuditRow'
import { AuditTable } from './AuditTable'

export function AuditView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [expandedId, setExpandedId] = useState<string | null>(null)

  const log = useAsync<AuditResponse>(
    () => (base ? getAuditLog(ctx.authedFetch, base, {}) : Promise.reject(new Error('no gateway configured'))),
    { isEmpty: () => false, immediate: shouldFetchInvoices(base) },
  )
  const state = invoicesViewState(base, log)
  const events = log.data?.events ?? []

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

      {state === 'loading' && <Loading label={AUDIT_COPY.loading} />}
      {state === 'error' && log.error && <ErrorState error={log.error} onRetry={log.run} />}
      {log.data != null && events.length === 0 && <EmptyState title={AUDIT_COPY.emptyTitle} message={AUDIT_COPY.emptyMessage} />}
      {events.length > 0 && (
        <AuditTable>
          {events.map((e) => (
            <AuditRow key={e.id} event={e} expanded={expandedId === e.id} onToggle={() => setExpandedId(expandedId === e.id ? null : e.id)} />
          ))}
        </AuditTable>
      )}
    </div>
  )
}
