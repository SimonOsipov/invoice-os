import type { CSSProperties } from 'react'
import { SEARCH_ICON } from '../icons'
import { syncDate, syncStamp } from '../rulesApi'
import { filterTenants, initials, kindLabel, memberName, type DetailStatus, type StaffMember, type StaffTenant, type StaffTenantDetail } from '../tenantsApi'
import { Badge } from './StatusBadge'

export type TenantsStatus = 'loading' | 'ready' | 'empty' | 'forbidden' | 'error'

type Props = {
  status: TenantsStatus
  errorText?: string
  tenants: StaffTenant[]
  query: string
  onQueryChange: (q: string) => void
  selectedId: string | null
  onSelect: (id: string) => void
  onRetry: () => void
  detailStatus: DetailStatus
  detail: StaffTenantDetail | null
  detailErrorText?: string
  onRetryDetail: () => void
  onViewJobs: (id: string) => void
}

type Tone = { bg: string; border: string; text: string }
const MUTED: Tone = { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' }
const AMBER: Tone = { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' }
const RED: Tone = { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)' }
// Role chips: the prototype tones; an unknown role reads muted.
const ROLE_TONE: Record<string, Tone> = {
  admin: { bg: 'var(--action-tint)', border: 'var(--teal-200)', text: 'var(--action)' },
  reviewer: AMBER,
  preparer: MUTED,
}
// The app's member tones (frontend/app/src/components/MemberParts.tsx).
const STATUS_TONE: Record<StaffMember['status'], Tone> = { active: MUTED, invited: AMBER, suspended: RED }

export const ONE_LINE: CSSProperties = { whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }
export const CARD: CSSProperties = { border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden', background: 'var(--bg-2)' }
const NOTE: CSSProperties = { padding: '14px 16px', fontSize: 13, color: 'var(--fg-3)' }
export const STAFF_ONLY = 'Your account is not ASComply staff.'

const Chip = ({ tone, label }: { tone: Tone; label: string }) => (
  <span style={{ flex: 'none', display: 'inline-flex' }}>
    <Badge style={{ ...tone, label }} fontSize={9} />
  </span>
)

const Retry = ({ onClick }: { onClick: () => void }) => (
  <button type="button" onClick={onClick} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 30 }}>
    Retry
  </button>
)

export const Note = ({ text, onRetry }: { text: string; onRetry?: () => void }) => (
  <div style={{ ...NOTE, display: 'flex', alignItems: 'center', gap: 12 }}>
    <span>{text}</span>
    {onRetry && <Retry onClick={onRetry} />}
  </div>
)

const activity = (t: StaffTenant): string => (t.last_activity_at ? `last activity ${syncStamp(t.last_activity_at)}` : 'no activity')

function TenantList({ status, errorText, tenants, query, selectedId, onSelect, onRetry }: Pick<Props, 'status' | 'errorText' | 'tenants' | 'query' | 'selectedId' | 'onSelect' | 'onRetry'>) {
  if (status === 'loading') return <Note text="Loading tenants…" />
  if (status === 'empty') return <Note text="No tenants yet." />
  if (status === 'forbidden') return <Note text={STAFF_ONLY} />
  if (status === 'error') return <Note text={errorText ?? 'Unavailable'} onRetry={onRetry} />
  const rows = filterTenants(tenants, query)
  return (
    <>
      {rows.map((t) => {
        const on = t.id === selectedId
        return (
          <button
            key={t.id}
            type="button"
            onClick={() => onSelect(t.id)}
            className="ops-nav"
            aria-pressed={on}
            data-tenant-id={t.id}
            style={{ width: '100%', display: 'flex', alignItems: 'center', gap: 11, border: 0, borderBottom: '1px solid var(--line-1)', cursor: 'pointer', textAlign: 'left', fontFamily: 'var(--font-sans)', color: 'var(--fg-1)', padding: '12px 14px', background: on ? 'var(--bg-3)' : 'var(--bg-2)' }}
          >
            <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 'var(--radius-md)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>
              {initials(t.name)}
            </span>
            <span style={{ flex: 1, minWidth: 0 }}>
              <span style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
                <span style={{ flex: 1, minWidth: 0, fontSize: 13, fontWeight: 600, ...ONE_LINE }}>{t.name}</span>
                {t.demo && <Chip tone={MUTED} label="DEMO" />}
              </span>
              <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)', ...ONE_LINE }}>
                {`${kindLabel(t.kind)} · ${t.active_members} active · ${t.invoice_count} invoices`}
              </span>
              <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)', ...ONE_LINE }}>
                {activity(t)}
              </span>
            </span>
          </button>
        )
      })}
      {rows.length === 0 && (
        <div className="mono" style={{ padding: '24px 14px', textAlign: 'center', fontSize: 12, color: 'var(--fg-3)' }}>
          No tenant matches.
        </div>
      )}
    </>
  )
}

function Detail({ status, detail, errorText, onRetry, onViewJobs }: { status: DetailStatus; detail: StaffTenantDetail | null; errorText?: string; onRetry: () => void; onViewJobs: (id: string) => void }) {
  if (status === 'none') return <Note text="Select a tenant to see its members and recent invoices." />
  if (status === 'loading') return <Note text="Loading tenant…" />
  if (status === 'not-found') return <Note text="Tenant not found." />
  if (status === 'forbidden') return <Note text={STAFF_ONLY} />
  if (status === 'error') return <Note text={errorText ?? 'Unavailable'} onRetry={onRetry} />
  if (!detail) return <Note text="Loading tenant…" />
  const { tenant: t, members, invoices } = detail
  return (
    <>
      <div style={{ padding: '20px 22px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'flex-start', gap: 16 }}>
        <span style={{ flex: 'none', width: 48, height: 48, borderRadius: 'var(--radius-md)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 16, fontWeight: 700 }}>
          {initials(t.name)}
        </span>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0, marginBottom: 3 }}>
            <h2 style={{ fontSize: 19, margin: 0, minWidth: 0, ...ONE_LINE }}>{t.name}</h2>
            {t.demo && <Chip tone={MUTED} label="DEMO" />}
          </div>
          <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', ...ONE_LINE }}>
            {`${kindLabel(t.kind)} · created ${syncDate(t.created_at)} · ${t.id}`}
          </div>
          <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', ...ONE_LINE }}>
            {`${t.active_members} active members · ${t.invoice_count} invoices · ${activity(t)}`}
          </div>
        </div>
        <button type="button" onClick={() => onViewJobs(t.id)} className="ops-btn v2-btn v2-btn-ghost" style={{ flex: 'none', height: 32, padding: '0 12px', fontSize: 12 }}>
          View jobs
        </button>
      </div>

      <div style={{ padding: '18px 22px', borderBottom: '1px solid var(--line-1)' }}>
        <div className="label" style={{ marginBottom: 12 }}>
          Members
        </div>
        {members.length === 0 && <div style={{ fontSize: 13, color: 'var(--fg-3)' }}>No members.</div>}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {members.map((m) => {
            const name = memberName(m)
            const role = ROLE_TONE[m.role] ?? MUTED
            return (
              <div key={m.user_id} style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
                <span style={{ flex: 'none', width: 26, height: 26, borderRadius: '50%', background: 'var(--slate-800)', color: 'var(--text-on-dark)', display: 'grid', placeItems: 'center', fontSize: 9, fontWeight: 700 }}>
                  {initials(name)}
                </span>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: 13, fontWeight: 500, ...ONE_LINE }}>{name}</div>
                  {m.email && m.email !== name && (
                    <div className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', ...ONE_LINE }}>
                      {m.email}
                    </div>
                  )}
                </div>
                <span className="mono" style={{ flex: 'none', fontSize: 10, fontWeight: 600, color: role.text, background: role.bg, border: `1px solid ${role.border}`, borderRadius: 'var(--radius-sm)', padding: '2px 7px' }}>
                  {m.role.toUpperCase()}
                </span>
                <Chip tone={STATUS_TONE[m.status] ?? MUTED} label={m.status.toUpperCase()} />
              </div>
            )
          })}
        </div>
      </div>

      <div style={{ padding: '18px 22px' }}>
        <div className="label" style={{ marginBottom: 12 }}>
          Recent invoices
        </div>
        {invoices.length === 0 && <div style={{ fontSize: 13, color: 'var(--fg-3)' }}>No invoices yet.</div>}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 9 }}>
          {invoices.map((i) => (
            <div key={i.id} style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
              <span className="mono" style={{ flex: 1, minWidth: 0, fontSize: 11.5, fontWeight: 600, ...ONE_LINE }}>
                {i.invoice_number}
              </span>
              <Chip tone={MUTED} label={i.status.toUpperCase()} />
              <span className="mono" style={{ flex: 'none', fontSize: 10.5, color: 'var(--fg-3)' }}>
                {syncDate(i.created_at)}
              </span>
            </div>
          ))}
        </div>
      </div>
    </>
  )
}

export function Tenants({ status, errorText, tenants, query, onQueryChange, selectedId, onSelect, onRetry, detailStatus, detail, detailErrorText, onRetryDetail, onViewJobs }: Props) {
  return (
    <div className="ops-screen-pad">
      <div style={{ marginBottom: 20 }}>
        <div className="eyebrow" style={{ marginBottom: 8 }}>
          SUPPORT LOOKUP
        </div>
        <h1 style={{ fontSize: 24, margin: 0 }}>Tenants</h1>
      </div>

      <div className="ops-tenants-grid" style={{ display: 'grid', gridTemplateColumns: '320px minmax(0,1fr)', gap: 18 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12, minWidth: 0 }}>
          {status === 'ready' && (
            <div className="ops-input ops-field" style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
              <span style={{ display: 'inline-flex', color: 'var(--fg-3)' }}>{SEARCH_ICON}</span>
              <input
                style={{ border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)', height: 30, flex: 1, padding: 0 }}
                placeholder="Tenant name…"
                aria-label="Search tenants"
                value={query}
                onChange={(e) => onQueryChange(e.target.value)}
              />
            </div>
          )}
          <div style={CARD}>
            <TenantList status={status} errorText={errorText} tenants={tenants} query={query} selectedId={selectedId} onSelect={onSelect} onRetry={onRetry} />
          </div>
        </div>

        <div style={{ ...CARD, minWidth: 0 }}>
          <Detail status={detailStatus} detail={detail} errorText={detailErrorText} onRetry={onRetryDetail} onViewJobs={onViewJobs} />
        </div>
      </div>
    </div>
  )
}
