import type { CSSProperties } from 'react'
import { formatCost, formatCount, formatLatency, isFailed, type AiCall, type AiFilters, type AiTotal } from '../aiUsageApi'
import { auditStamp, RANGE_REASON, rangeInverted } from '../auditApi'
import type { StaffTenant } from '../tenantsApi'
import { DateFilter, TextFilter, type AuditStatus } from './Audit'
import { chipStyle } from './Submissions'
import { CARD, Note, ONE_LINE, STAFF_ONLY } from './Tenants'

export type AiView = 'calls' | 'failures' | 'totals'

type Props = {
  view: AiView
  onView: (v: AiView) => void
  filters: AiFilters
  onFilters: (patch: AiFilters) => void
  tenants: StaffTenant[]
  /** Set when the tenant select's own read failed; the list still reads. */
  tenantsError?: { text: string; onRetry: () => void }
  status: AuditStatus
  errorText?: string
  calls: AiCall[]
  hasMore: boolean
  moreBusy: boolean
  onMore: () => void
  onRetry: () => void
  totalsStatus: AuditStatus
  totalsErrorText?: string
  totals: AiTotal[]
  totalsHasMore: boolean
  totalsMoreBusy: boolean
  onMoreTotals: () => void
  onRetryTotals: () => void
}

const CALL_COLS = '168px minmax(96px,1.4fr) 56px minmax(96px,1fr) minmax(96px,1.4fr) 64px 56px 84px 72px 36px minmax(96px,1fr)'
// Fixed tracks plus the minimums; fits the 949 px row at a 1280 px window.
const CALL_MIN_WIDTH = 920

const TOTAL_COLS = '104px minmax(96px,1fr) 64px 96px 96px 96px 64px'
// Fixed tracks plus the minimum.
const TOTAL_MIN_WIDTH = 616

const ROW: CSSProperties = {
  display: 'grid',
  gridTemplateColumns: CALL_COLS,
  width: '100%',
  minWidth: CALL_MIN_WIDTH,
  padding: '12px 16px',
  borderBottom: '1px solid var(--line-1)',
  color: 'var(--fg-1)',
  fontFamily: 'var(--font-sans)',
  alignItems: 'center',
}
const CELL: CSSProperties = { minWidth: 0, paddingRight: 8, ...ONE_LINE }
const NUM: CSSProperties = { ...CELL, textAlign: 'right' }

const HEADS: { label: string; num: boolean }[] = [
  { label: 'Time', num: false },
  { label: 'Tenant', num: false },
  { label: 'Client', num: false },
  { label: 'Purpose', num: false },
  { label: 'Model', num: false },
  { label: 'In', num: true },
  { label: 'Out', num: true },
  { label: 'Cost', num: true },
  { label: 'Latency', num: true },
  { label: 'Try', num: true },
  { label: 'Outcome', num: false },
]

const TOTAL_HEADS: { label: string; num: boolean }[] = [
  { label: 'Day', num: false },
  { label: 'Tenant', num: false },
  { label: 'Calls', num: true },
  { label: 'Tokens in', num: true },
  { label: 'Tokens out', num: true },
  { label: 'Cost', num: true },
  { label: 'Failed', num: true },
]

function Rows({ status, errorText, calls, failuresOnly, onRetry }: Pick<Props, 'status' | 'errorText' | 'calls' | 'onRetry'> & { failuresOnly: boolean }) {
  if (status === 'loading') return <Note text="Loading AI calls…" />
  if (status === 'forbidden') return <Note text={STAFF_ONLY} />
  if (status === 'error') return <Note text={errorText ?? 'Unavailable'} onRetry={onRetry} />
  if (calls.length === 0) {
    return (
      <div className="mono" style={{ padding: '28px 16px', textAlign: 'center', fontSize: 12, color: 'var(--fg-3)' }}>
        {failuresOnly ? 'No failed calls match these filters.' : 'No AI calls match these filters.'}
      </div>
    )
  }
  return (
    <>
      {calls.map((c) => (
        <div key={c.id} data-call-id={c.id} style={ROW}>
          <span className="mono" style={{ ...CELL, fontSize: 11, color: 'var(--fg-3)' }}>
            {auditStamp(c.created_at)}
          </span>
          <span style={{ ...CELL, fontSize: 13, fontWeight: 500 }}>{c.tenant_name}</span>
          <span className="mono" style={{ ...CELL, fontSize: 12 }}>
            {c.client}
          </span>
          <span className="mono" style={{ ...CELL, fontSize: 12 }}>
            {c.purpose}
          </span>
          <span className="mono" style={{ ...CELL, fontSize: 12, ...(c.model === '' ? { color: 'var(--fg-3)' } : {}) }}>
            {c.model === '' ? '—' : c.model}
          </span>
          <span className="mono" style={{ ...NUM, fontSize: 12 }}>
            {formatCount(c.input_tokens)}
          </span>
          <span className="mono" style={{ ...NUM, fontSize: 12 }}>
            {formatCount(c.output_tokens)}
          </span>
          <span className="mono" style={{ ...NUM, fontSize: 12 }}>
            {formatCost(c.cost)}
          </span>
          <span className="mono" style={{ ...NUM, fontSize: 12 }}>
            {formatLatency(c.latency_ms)}
          </span>
          <span className="mono" style={{ ...NUM, fontSize: 12 }}>
            {c.attempts}
          </span>
          <span className="mono" style={{ ...CELL, paddingRight: 0, fontSize: 12, color: isFailed(c.outcome) ? 'var(--status-red-text)' : 'var(--fg-2)' }}>
            {c.outcome}
          </span>
        </div>
      ))}
    </>
  )
}

function TotalRows({ status, errorText, totals, onRetry }: { status: AuditStatus; errorText?: string; totals: AiTotal[]; onRetry: () => void }) {
  if (status === 'loading') return <Note text="Loading AI totals…" />
  if (status === 'forbidden') return <Note text={STAFF_ONLY} />
  if (status === 'error') return <Note text={errorText ?? 'Unavailable'} onRetry={onRetry} />
  if (totals.length === 0) {
    return (
      <div className="mono" style={{ padding: '28px 16px', textAlign: 'center', fontSize: 12, color: 'var(--fg-3)' }}>
        No AI calls match these filters.
      </div>
    )
  }
  return (
    <>
      {totals.map((t) => (
        <div key={`${t.day}|${t.tenant_id}`} data-total-key={`${t.day}|${t.tenant_id}`} style={{ ...ROW, gridTemplateColumns: TOTAL_COLS, minWidth: TOTAL_MIN_WIDTH }}>
          <span className="mono" style={{ ...CELL, fontSize: 12 }}>
            {t.day}
          </span>
          <span style={{ ...CELL, fontSize: 13, fontWeight: 500 }}>{t.tenant_name}</span>
          <span className="mono" style={{ ...NUM, fontSize: 12 }}>
            {formatCount(t.calls)}
          </span>
          <span className="mono" style={{ ...NUM, fontSize: 12 }}>
            {formatCount(t.input_tokens)}
          </span>
          <span className="mono" style={{ ...NUM, fontSize: 12 }}>
            {formatCount(t.output_tokens)}
          </span>
          <span className="mono" style={{ ...NUM, fontSize: 12 }}>
            {formatCost(t.cost)}
          </span>
          <span className="mono" style={{ ...NUM, paddingRight: 0, fontSize: 12, color: t.failed > 0 ? 'var(--status-red-text)' : 'var(--fg-2)' }}>
            {formatCount(t.failed)}
          </span>
        </div>
      ))}
    </>
  )
}

export function AiUsage({ view, onView, filters, onFilters, tenants, tenantsError, status, errorText, calls, hasMore, moreBusy, onMore, onRetry, totalsStatus, totalsErrorText, totals, totalsHasMore, totalsMoreBusy, onMoreTotals, onRetryTotals }: Props) {
  const inverted = rangeInverted(filters)
  const views: { key: AiView; label: string }[] = [
    { key: 'calls', label: 'CALLS' },
    { key: 'failures', label: 'FAILURES' },
    { key: 'totals', label: 'DAILY TOTALS' },
  ]
  return (
    <div className="ops-screen-pad">
      <div style={{ marginBottom: 20 }}>
        <div className="eyebrow" style={{ marginBottom: 8 }}>
          AI CALLS
        </div>
        <h1 style={{ fontSize: 24, margin: 0 }}>AI usage</h1>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}>
        {views.map((v) => (
          <button key={v.key} type="button" className="ops-chip" aria-pressed={view === v.key} onClick={() => onView(v.key)} style={chipStyle(view === v.key, null)}>
            {v.label}
          </button>
        ))}
      </div>

      <form onSubmit={(e) => e.preventDefault()} style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 14 }}>
        <select className="ops-input" aria-label="Tenant" value={filters.tenant ?? ''} onChange={(e) => onFilters({ tenant: e.target.value })} style={{ maxWidth: 260 }}>
          <option value="">All tenants</option>
          {tenants.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}
            </option>
          ))}
        </select>
        <TextFilter label="Purpose" placeholder="Purpose (e.g. document)" value={filters.purpose ?? ''} onApply={(v) => onFilters({ purpose: v })} width={160} />
        <TextFilter label="Outcome" placeholder="Outcome (e.g. unavailable)" value={filters.outcome ?? ''} onApply={(v) => onFilters({ outcome: v })} width={180} />
        <DateFilter label="From" value={filters.from ?? ''} onChange={(v) => onFilters({ from: v })} />
        <DateFilter label="To" value={filters.to ?? ''} onChange={(v) => onFilters({ to: v })} />
        {inverted && (
          <div className="mono" style={{ flexBasis: '100%', fontSize: 11, color: 'var(--status-red-text)' }}>
            {RANGE_REASON}
          </div>
        )}
      </form>
      {tenantsError && <Note text={tenantsError.text} onRetry={tenantsError.onRetry} />}

      {view !== 'totals' && (
        <>
          <div style={{ ...CARD, overflowX: 'auto' }}>
            <div style={{ display: 'grid', gridTemplateColumns: CALL_COLS, padding: '10px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', minWidth: CALL_MIN_WIDTH }}>
              {HEADS.map((h, i) => (
                <span key={h.label} className="label" style={{ ...(h.num ? { textAlign: 'right' } : {}), paddingRight: i === HEADS.length - 1 ? 0 : 8 }}>
                  {h.label}
                </span>
              ))}
            </div>
            {!inverted && <Rows status={status} errorText={errorText} calls={calls} failuresOnly={view === 'failures'} onRetry={onRetry} />}
          </div>
          {!inverted && hasMore && (
            <div style={{ marginTop: 14 }}>
              <button type="button" onClick={onMore} disabled={moreBusy} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 30, ...(moreBusy ? { opacity: 0.45, cursor: 'not-allowed' } : {}) }}>
                Load more
              </button>
            </div>
          )}
        </>
      )}

      {view === 'totals' && (
        <>
          <div style={{ ...CARD, overflowX: 'auto' }}>
            <div style={{ display: 'grid', gridTemplateColumns: TOTAL_COLS, padding: '10px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', minWidth: TOTAL_MIN_WIDTH }}>
              {TOTAL_HEADS.map((h, i) => (
                <span key={h.label} className="label" style={{ ...(h.num ? { textAlign: 'right' } : {}), paddingRight: i === TOTAL_HEADS.length - 1 ? 0 : 8 }}>
                  {h.label}
                </span>
              ))}
            </div>
            {!inverted && <TotalRows status={totalsStatus} errorText={totalsErrorText} totals={totals} onRetry={onRetryTotals} />}
          </div>
          {!inverted && totalsHasMore && (
            <div style={{ marginTop: 14 }}>
              <button type="button" onClick={onMoreTotals} disabled={totalsMoreBusy} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 30, ...(totalsMoreBusy ? { opacity: 0.45, cursor: 'not-allowed' } : {}) }}>
                Load more
              </button>
            </div>
          )}
        </>
      )}
    </div>
  )
}
