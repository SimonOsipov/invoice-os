// The invoice-scoped activity feed: the Audit screen's table, one invoice's events.
//
// AuditRow/AuditTable are mounted, never restated, and `onFilterToInvoice` is OMITTED --
// that is what keeps the narrow-to-invoice affordance off an already-scoped page.
// invoiceActivity_reusesAuditRowExpansion and _omitsTheNarrowToInvoiceAffordance hold both.
//
// This card is the ONLY owner of the toggle's `shown`. activityToggleCopy never sees the
// chip, so `shown` must be the ACTIVE chip's full row count --
// invoiceActivity_toggleLabelEqualsTheRowsItRenders is the one thing that holds it.

import { useState, type ReactNode } from 'react'

import { ErrorState, Loading, gatewayBase, useAsync } from '@invoice-os/api-client'

import { getAuditLog, type AuditResponse } from '../lib/audit'
import {
  ACTIVITY_COPY,
  ACTIVITY_FETCH_LIMIT,
  activityChips,
  activityRows,
  activityToggleCopy,
  type ActivityChipKey,
} from '../lib/invoiceActivity'
import { shouldFetchInvoices } from '../lib/invoices'
import type { PlatformCtx } from '../types'

import { AuditRow } from './AuditRow'
import { AuditTable } from './AuditTable'

// One id per disabled control (ReviewRow.tsx:78's idiom): a title= on a disabled
// button never fires in Chromium, so the reason has to be a text node the control names.
const DOCUMENTS_REASON_ID = 'activity-chip-documents-reason'
const EMPTY_CHIP_REASON_ID = 'activity-chip-empty-reason'

export function InvoiceActivityCard({
  ctx,
  invoiceId,
  invoiceNumber,
}: { ctx: PlatformCtx; invoiceId: string; invoiceNumber: string }): ReactNode {
  const base = gatewayBase()
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [chip, setChip] = useState<ActivityChipKey>('all')
  const [showAll, setShowAll] = useState(false)

  // No from/to: an invoice's whole life belongs on its own page, and the 30-day default is
  // the Audit SCREEN's (auditFilters.ts), not the server's. `isEmpty: () => false` because
  // the empty split comes from the server's own counters, never from a shape guess.
  const log = useAsync<AuditResponse>(
    () =>
      base
        ? getAuditLog(ctx.authedFetch, base, { invoice_id: invoiceId, limit: ACTIVITY_FETCH_LIMIT })
        : Promise.reject(new Error('no gateway configured')),
    { isEmpty: () => false, immediate: shouldFetchInvoices(base), deps: [invoiceId] },
  )

  const res = log.data
  let body: ReactNode

  if (log.status === 'error') {
    body = log.error ? <ErrorState error={log.error} onRetry={log.run} /> : null
  } else if (res == null) {
    body = <Loading label={ACTIVITY_COPY.loading} />
  } else if (res.events.length === 0) {
    // log_is_empty is the server's own unfiltered probe -- read, never re-derived.
    body = (
      <div
        data-testid="invoice-activity-empty"
        style={{ padding: '14px 16px', border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', background: 'transparent' }}
      >
        <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg-2)' }}>{ACTIVITY_COPY.emptyScopedTitle}</div>
        <div style={{ marginTop: 4, fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)' }}>{ACTIVITY_COPY.emptyScopedBody}</div>
        {res.log_is_empty && (
          <div data-testid="invoice-activity-empty-workspace" style={{ marginTop: 6, fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)' }}>
            {ACTIVITY_COPY.emptyWorkspaceAlso}
          </div>
        )}
      </div>
    )
  } else {
    const events = res.events
    const chips = activityChips(events)
    const rows = activityRows(events, chip, showAll)
    // The ACTIVE chip's full row count -- never res.total, never events.length.
    const shown = activityRows(events, chip, true).length
    const toggle = activityToggleCopy({ shown, total: res.total, fetched: events.length, showAll })
    const incidentalInert = chips.some((c) => c.inert && c.reason == null)

    body = (
      <>
        <div data-testid="invoice-activity-chips" style={{ display: 'flex', flexWrap: 'wrap', gap: 7 }}>
          {chips.map((c) => {
            const on = chip === c.key
            return (
              <button
                key={c.key}
                type="button"
                data-testid={`activity-chip-${c.key}`}
                aria-pressed={on}
                aria-describedby={c.inert ? (c.reason != null ? DOCUMENTS_REASON_ID : EMPTY_CHIP_REASON_ID) : undefined}
                disabled={c.inert}
                onClick={() => {
                  setChip(c.key)
                  // A filter change is a new population: a held-over id would re-open a row
                  // the user never opened on this chip.
                  setExpandedId(null)
                }}
                className="pf-chip"
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 7,
                  height: 28,
                  padding: '0 12px',
                  fontFamily: 'var(--font-sans)',
                  fontSize: 12.5,
                  fontWeight: 500,
                  border: `1px solid ${on ? 'var(--action)' : 'var(--line-2)'}`,
                  background: on ? 'var(--action)' : 'var(--bg-1)',
                  color: on ? 'var(--text-on-dark)' : 'var(--fg-2)',
                  // Inline, matching the repo: there is no `:disabled` rule anywhere.
                  ...(c.inert ? { opacity: 0.4, cursor: 'not-allowed' } : {}),
                }}
              >
                {c.label}
                <span className="mono" style={{ fontSize: 11, opacity: 0.75 }}>
                  {c.count}
                </span>
              </button>
            )
          })}
        </div>

        {/* Siblings of the chip row, never flex items beside a chip. The Documents line is
            unconditional: D-AC-6 makes that zero permanent. */}
        <div style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 4 }}>
          <div id={DOCUMENTS_REASON_ID} data-testid="activity-chip-documents-reason" style={{ fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
            {ACTIVITY_COPY.documentsInert}
          </div>
          {incidentalInert && (
            <div id={EMPTY_CHIP_REASON_ID} data-testid="activity-chip-empty-reason" style={{ fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
              {ACTIVITY_COPY.chipZeroInert}
            </div>
          )}
        </div>

        <div style={{ marginTop: 14 }}>
          <AuditTable>
            {rows.map((e) => (
              <AuditRow
                key={e.id}
                event={e}
                expanded={expandedId === e.id}
                onToggle={() => setExpandedId(expandedId === e.id ? null : e.id)}
              />
            ))}
          </AuditTable>
        </div>

        {/* The toggle and the hand-off share one row: the cap note directly below points the
            reader at ACTIVITY_COPY.auditLink, so pointer and target must be adjacent. marginTop
            moves to the wrapper so spacing is the same whether or not the toggle renders. */}
        <div style={{ marginTop: 12, display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          {toggle.label != null && (
            <button
              type="button"
              data-testid="activity-toggle"
              onClick={() => setShowAll(!showAll)}
              className="v2-btn v2-btn-ghost pf-btn"
              style={{ height: 32, padding: '0 12px', fontSize: 12.5 }}
            >
              {toggle.label}
            </button>
          )}
          {/* A <button>, not an <a>: there is no URL to point at. The SPA has no router. */}
          <button
            type="button"
            data-testid="activity-open-in-audit"
            onClick={() => ctx.openAuditForInvoice(invoiceId, invoiceNumber)}
            className="v2-btn v2-btn-ghost pf-btn"
            style={{ height: 32, padding: '0 12px', fontSize: 12.5 }}
          >
            {ACTIVITY_COPY.auditLink}
          </button>
        </div>
        {toggle.note != null && (
          <div data-testid="activity-cap-note" style={{ marginTop: 10, fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
            {toggle.note}
          </div>
        )}
      </>
    )
  }

  return (
    <div
      data-testid="invoice-activity"
      style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}
    >
      <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
        <span className="card-title">{ACTIVITY_COPY.cardTitle}</span>
        <span className="mono" style={{ flex: 'none', fontSize: 9.5, letterSpacing: '0.06em', color: 'var(--fg-3)' }}>
          READ ONLY
        </span>
      </div>
      <div data-testid="invoice-activity-body" style={{ padding: '16px 18px' }}>
        {body}
      </div>
    </div>
  )
}
