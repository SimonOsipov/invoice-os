import { useEffect, useState, type CSSProperties } from 'react'
import { CHEVRON_RIGHT_ICON, LOCK_ICON } from '../icons'
import { auditStamp, RANGE_REASON, rangeInverted, type AuditFilters, type StaffAuditRow, type StaffChange } from '../auditApi'
import type { StaffTenant } from '../tenantsApi'
import { chipStyle } from './Submissions'
import { CARD, Note, ONE_LINE, STAFF_ONLY } from './Tenants'

export type AuditStatus = 'loading' | 'ready' | 'empty' | 'forbidden' | 'error'
export type AuditView = 'tenant' | 'staff'

type Props = {
  view: AuditView
  onView: (v: AuditView) => void
  status: AuditStatus
  errorText?: string
  rows: StaffAuditRow[]
  hasMore: boolean
  moreBusy: boolean
  onMore: () => void
  filters: AuditFilters
  onFilters: (patch: AuditFilters) => void
  tenants: StaffTenant[]
  /** Set when the tenant select's own read failed; the log still reads. */
  tenantsError?: { text: string; onRetry: () => void }
  onRetry: () => void
  onOpen: (id: string) => void
  changes: StaffChange[]
  changesStatus: AuditStatus
  changesErrorText?: string
  onRetryChanges: () => void
  onOpenChange: (id: string) => void
}

const AUDIT_COLS = '170px minmax(180px,1fr) minmax(180px,1fr) minmax(200px,1.3fr) 22px'
const MIN_WIDTH = 760

const ROW: CSSProperties = {
  display: 'grid',
  gridTemplateColumns: AUDIT_COLS,
  width: '100%',
  minWidth: MIN_WIDTH,
  padding: '12px 16px',
  border: 0,
  borderBottom: '1px solid var(--line-1)',
  background: 'transparent',
  textAlign: 'left',
  color: 'var(--fg-1)',
  fontFamily: 'var(--font-sans)',
  alignItems: 'center',
}

const FIELD_INPUT: CSSProperties = { border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)', height: 30, flex: 1, minWidth: 0, padding: 0 }

function Rows({ status, errorText, rows, onRetry, onOpen }: Pick<Props, 'status' | 'errorText' | 'rows' | 'onRetry' | 'onOpen'>) {
  if (status === 'loading') return <Note text="Loading audit entries…" />
  if (status === 'forbidden') return <Note text={STAFF_ONLY} />
  if (status === 'error') return <Note text={errorText ?? 'Unavailable'} onRetry={onRetry} />
  if (rows.length === 0) {
    return (
      <div className="mono" style={{ padding: '28px 16px', textAlign: 'center', fontSize: 12, color: 'var(--fg-3)' }}>
        No audit entries match these filters.
      </div>
    )
  }
  return (
    <>
      {rows.map((r) => (
        <button key={r.id} type="button" className="ops-row" data-audit-id={r.id} style={ROW} onClick={() => onOpen(r.id)}>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', ...ONE_LINE }}>
            {auditStamp(r.created_at)}
          </span>
          {r.tenant_name === null ? (
            <span className="mono" style={{ fontSize: 11, paddingRight: 12, minWidth: 0, ...ONE_LINE }}>
              {r.tenant_id}
            </span>
          ) : (
            <span style={{ fontSize: 13, fontWeight: 500, paddingRight: 12, minWidth: 0, ...ONE_LINE }}>{r.tenant_name}</span>
          )}
          <span style={{ fontSize: 12, color: 'var(--fg-2)', paddingRight: 12, minWidth: 0, ...ONE_LINE }}>{r.actor_name}</span>
          <span className="mono" style={{ fontSize: 12, paddingRight: 12, minWidth: 0, ...ONE_LINE }}>
            {r.event}
          </span>
          <span style={{ display: 'inline-flex', color: 'var(--fg-3)' }}>{CHEVRON_RIGHT_ICON}</span>
        </button>
      ))}
    </>
  )
}

function ChangeRows({ status, errorText, changes, onRetry, onOpen }: { status: AuditStatus; errorText?: string; changes: StaffChange[]; onRetry: () => void; onOpen: (id: string) => void }) {
  if (status === 'loading') return <Note text="Loading staff changes…" />
  if (status === 'forbidden') return <Note text={STAFF_ONLY} />
  if (status === 'error') return <Note text={errorText ?? 'Unavailable'} onRetry={onRetry} />
  if (changes.length === 0) return <Note text="No staff changes yet." />
  return (
    <>
      {changes.map((c) => (
        <button key={c.id} type="button" className="ops-row" data-change-id={c.id} style={ROW} onClick={() => onOpen(c.id)}>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', ...ONE_LINE }}>
            {auditStamp(c.created_at)}
          </span>
          <span className="mono" style={{ fontSize: 11, paddingRight: 12, minWidth: 0, ...ONE_LINE }}>
            {c.actor}
          </span>
          <span className="mono" style={{ fontSize: 12, paddingRight: 12, minWidth: 0, ...ONE_LINE }}>
            {c.event}
          </span>
          {c.tenant_id === null ? (
            <span style={{ fontSize: 13, color: 'var(--fg-2)', paddingRight: 12, minWidth: 0, ...ONE_LINE }}>All tenants</span>
          ) : c.tenant_name === null ? (
            <span className="mono" style={{ fontSize: 11, paddingRight: 12, minWidth: 0, ...ONE_LINE }}>
              {c.tenant_id}
            </span>
          ) : (
            <span style={{ fontSize: 13, fontWeight: 500, paddingRight: 12, minWidth: 0, ...ONE_LINE }}>{c.tenant_name}</span>
          )}
          <span style={{ display: 'inline-flex', color: 'var(--fg-3)' }}>{CHEVRON_RIGHT_ICON}</span>
        </button>
      ))}
    </>
  )
}

function TextFilter({ label, placeholder, value, onApply, width }: { label: string; placeholder: string; value: string; onApply: (v: string) => void; width: number }) {
  const [draft, setDraft] = useState(value)
  useEffect(() => setDraft(value), [value])
  const apply = () => onApply(draft.trim())
  return (
    <div className="ops-input ops-field" style={{ display: 'flex', alignItems: 'center', flex: `1 1 ${width}px`, minWidth: width }}>
      <input
        aria-label={label}
        placeholder={placeholder}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={apply}
        onKeyDown={(e) => e.key === 'Enter' && apply()}
        style={FIELD_INPUT}
      />
    </div>
  )
}

function DateFilter({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  return (
    <div className="ops-input ops-field" style={{ display: 'flex', alignItems: 'center', flex: 'none' }}>
      <input type="date" aria-label={label} value={value} onChange={(e) => onChange(e.target.value)} style={{ ...FIELD_INPUT, flex: 'none' }} />
    </div>
  )
}

export function Audit({ view, onView, status, errorText, rows, hasMore, moreBusy, onMore, filters, onFilters, tenants, tenantsError, onRetry, onOpen, changes, changesStatus, changesErrorText, onRetryChanges, onOpenChange }: Props) {
  const inverted = rangeInverted(filters)
  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-end', flexWrap: 'wrap', gap: 24, marginBottom: 20 }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 8 }}>
            AUDIT &amp; EVIDENCE
          </div>
          <h1 style={{ fontSize: 24, margin: 0 }}>Audit &amp; evidence explorer</h1>
        </div>
        <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 7, background: 'var(--status-muted-bg)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', color: 'var(--fg-2)', padding: '7px 12px' }}>
            {LOCK_ICON}
            <span className="mono" style={{ fontSize: 10.5, fontWeight: 600, color: 'var(--fg-2)', letterSpacing: '0.04em' }}>
              APPEND-ONLY · IMMUTABLE
            </span>
          </span>
        </div>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}>
        <button type="button" className="ops-chip" aria-pressed={view === 'tenant'} onClick={() => onView('tenant')} style={chipStyle(view === 'tenant', null)}>
          TENANT LOG
        </button>
        <button type="button" className="ops-chip" aria-pressed={view === 'staff'} onClick={() => onView('staff')} style={chipStyle(view === 'staff', null)}>
          STAFF CHANGES
        </button>
      </div>

      {view === 'tenant' && (
        <>
          <form onSubmit={(e) => e.preventDefault()} style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 14 }}>
            <select className="ops-input" aria-label="Tenant" value={filters.tenant ?? ''} onChange={(e) => onFilters({ tenant: e.target.value })} style={{ maxWidth: 260 }}>
              <option value="">All tenants</option>
              {tenants.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
            <TextFilter label="Event" placeholder="Event (e.g. invoice.created)" value={filters.event ?? ''} onApply={(v) => onFilters({ event: v })} width={180} />
            <TextFilter label="Actor" placeholder="Actor id (UUID, system, …)" value={filters.actor ?? ''} onApply={(v) => onFilters({ actor: v })} width={240} />
            <DateFilter label="From" value={filters.from ?? ''} onChange={(v) => onFilters({ from: v })} />
            <DateFilter label="To" value={filters.to ?? ''} onChange={(v) => onFilters({ to: v })} />
            {inverted && (
              <div className="mono" style={{ flexBasis: '100%', fontSize: 11, color: 'var(--status-red-text)' }}>
                {RANGE_REASON}
              </div>
            )}
          </form>
          {tenantsError && <Note text={tenantsError.text} onRetry={tenantsError.onRetry} />}

          <div style={{ ...CARD, overflowX: 'auto' }}>
            <div style={{ display: 'grid', gridTemplateColumns: AUDIT_COLS, padding: '10px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', minWidth: MIN_WIDTH }}>
              <span className="label">Time</span>
              <span className="label">Tenant</span>
              <span className="label">Actor</span>
              <span className="label">Event</span>
              <span />
            </div>
            {!inverted && <Rows status={status} errorText={errorText} rows={rows} onRetry={onRetry} onOpen={onOpen} />}
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

      {view === 'staff' && (
        <div style={{ ...CARD, overflowX: 'auto' }}>
          <div style={{ display: 'grid', gridTemplateColumns: AUDIT_COLS, padding: '10px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', minWidth: MIN_WIDTH }}>
            <span className="label">Time</span>
            <span className="label">Staff member</span>
            <span className="label">Event</span>
            <span className="label">Tenant</span>
            <span />
          </div>
          <ChangeRows status={changesStatus} errorText={changesErrorText} changes={changes} onRetry={onRetryChanges} onOpen={onOpenChange} />
        </div>
      )}
    </div>
  )
}
