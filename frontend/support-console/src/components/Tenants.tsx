import { SEARCH_ICON, TENANTS } from '../data'
import { StateBadge } from './StatusBadge'
import type { TenantKpi, TenantMember } from '../types'

type Props = {
  query: string
  tenantId: string
  onQueryChange: (q: string) => void
  onSelect: (id: string) => void
  onViewJobs: () => void
  onViewAs: (name: string) => void
}

const STATUS_DOT = {
  ok: 'var(--status-green-text)',
  warn: 'var(--status-amber-text)',
  red: 'var(--status-red-text)',
} as const

const ROLE_TONE = {
  admin: { bg: 'var(--action-tint)', border: 'var(--teal-200)', color: 'var(--action)' },
  reviewer: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', color: 'var(--status-amber-text)' },
  preparer: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', color: 'var(--status-muted-text)' },
} as const

const KPI_TONE = {
  green: 'var(--status-green-text)',
  amber: 'var(--status-amber-text)',
  red: 'var(--status-red-text)',
} as const

const kpiColor = (k: TenantKpi) => (k.tone ? KPI_TONE[k.tone] : 'var(--fg-1)')
const roleTone = (m: TenantMember) => ROLE_TONE[m.role]

export function Tenants({ query, tenantId, onQueryChange, onSelect, onViewJobs, onViewAs }: Props) {
  const q = query.trim().toLowerCase()
  const rows = TENANTS.filter((t) => !q || `${t.name} ${t.tin}`.toLowerCase().includes(q))
  // The selection survives a query that filters it out of the list — the detail pane keeps
  // showing what the operator opened rather than silently jumping to another tenant.
  const selected = TENANTS.find((t) => t.id === tenantId) ?? TENANTS[0]

  return (
    <div className="ops-screen-pad">
      <div style={{ marginBottom: 20 }}>
        <div className="eyebrow" style={{ marginBottom: 8 }}>
          SUPPORT LOOKUP
        </div>
        <h1 style={{ fontSize: 24, fontWeight: 500, letterSpacing: '-0.03em', margin: 0 }}>Tenants &amp; entities</h1>
      </div>

      <div className="ops-tenants-grid" style={{ display: 'grid', gridTemplateColumns: '320px minmax(0,1fr)', gap: 18 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div className="ops-input" style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
            <span style={{ color: 'var(--fg-3)' }}>{SEARCH_ICON}</span>
            <input
              style={{ border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)', height: 30, flex: 1, padding: 0 }}
              placeholder="TIN or tenant name…"
              aria-label="Search tenants"
              value={query}
              onChange={(e) => onQueryChange(e.target.value)}
            />
          </div>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden', background: 'var(--bg-2)' }}>
            {rows.map((t) => (
              <button
                key={t.id}
                type="button"
                onClick={() => onSelect(t.id)}
                className="ops-nav"
                aria-pressed={t.id === selected.id}
                style={{ width: '100%', display: 'flex', alignItems: 'center', gap: 11, border: 0, borderBottom: '1px solid var(--line-1)', cursor: 'pointer', textAlign: 'left', padding: '12px 14px', background: t.id === selected.id ? 'var(--bg-3)' : 'var(--bg-2)' }}
              >
                <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 'var(--radius-input)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>
                  {t.initials}
                </span>
                <span style={{ flex: 1, minWidth: 0 }}>
                  <span style={{ display: 'block', fontSize: 13, fontWeight: 600, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{t.name}</span>
                  <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)' }}>
                    {t.tin}
                  </span>
                </span>
                <span style={{ flex: 'none', width: 7, height: 7, borderRadius: 99, background: STATUS_DOT[t.status] }} />
              </button>
            ))}
            {rows.length === 0 && (
              <div className="mono" style={{ padding: '24px 14px', textAlign: 'center', fontSize: 12, color: 'var(--fg-4)' }}>
                No tenant matches.
              </div>
            )}
          </div>
        </div>

        {/* tenant detail */}
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', overflow: 'hidden' }}>
          <div style={{ padding: '20px 22px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'flex-start', gap: 16, flexWrap: 'wrap' }}>
            <span style={{ flex: 'none', width: 48, height: 48, borderRadius: 'var(--radius-md)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 16, fontWeight: 700 }}>
              {selected.initials}
            </span>
            <div style={{ flex: 1, minWidth: 180 }}>
              <h2 style={{ fontSize: 19, fontWeight: 500, letterSpacing: '-0.02em', margin: '0 0 3px' }}>{selected.name}</h2>
              <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                TIN {selected.tin} · {selected.entityCount}
              </div>
            </div>
            <div style={{ display: 'flex', gap: 8 }}>
              <button
                type="button"
                onClick={onViewJobs}
                className="ops-btn"
                style={{ border: '1px solid var(--line-2)', background: 'var(--bg-2)', cursor: 'pointer', height: 32, padding: '0 12px', borderRadius: 'var(--radius-input)', fontFamily: 'var(--font-sans)', fontSize: 12, fontWeight: 600, color: 'var(--fg-1)' }}
              >
                View jobs
              </button>
              <button
                type="button"
                onClick={() => onViewAs(selected.name)}
                className="ops-btn"
                style={{ border: '1px solid var(--action)', background: 'var(--action)', cursor: 'pointer', height: 32, padding: '0 12px', borderRadius: 'var(--radius-input)', fontFamily: 'var(--font-sans)', fontSize: 12, fontWeight: 600, color: 'var(--text-on-dark)' }}
              >
                View-as (read-only)
              </button>
            </div>
          </div>

          <div className="ops-tenant-kpis" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', borderBottom: '1px solid var(--line-1)' }}>
            {selected.kpis.map((k) => (
              <div key={k.label} style={{ padding: '16px 18px', borderRight: '1px solid var(--line-1)' }}>
                <div className="mono" style={{ fontSize: 20, fontWeight: 600, letterSpacing: '-0.02em', color: kpiColor(k) }}>
                  {k.value}
                </div>
                <div className="label" style={{ marginTop: 3 }}>
                  {k.label}
                </div>
              </div>
            ))}
          </div>

          <div className="ops-tenant-split" style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1fr) minmax(0,1fr)' }}>
            <div style={{ padding: '18px 22px', borderRight: '1px solid var(--line-1)' }}>
              <div className="label" style={{ marginBottom: 12 }}>
                Memberships &amp; roles
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                {selected.members.map((m) => {
                  const tone = roleTone(m)
                  return (
                    <div key={m.name} style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                      <span style={{ flex: 'none', width: 26, height: 26, borderRadius: 99, background: 'var(--slate-800)', color: 'var(--text-on-dark)', display: 'grid', placeItems: 'center', fontSize: 9, fontWeight: 700 }}>
                        {m.initials}
                      </span>
                      <span style={{ flex: 1, fontSize: 13, fontWeight: 500 }}>{m.name}</span>
                      <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: tone.color, background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 'var(--radius-sm)', padding: '2px 7px' }}>
                        {m.role.toUpperCase()}
                      </span>
                    </div>
                  )
                })}
              </div>
            </div>
            <div style={{ padding: '18px 22px' }}>
              <div className="label" style={{ marginBottom: 12 }}>
                Recent submissions
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 9 }}>
                {selected.recent.map((r) => (
                  <div key={r.invoice} style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                    <span className="mono" style={{ fontSize: 11.5, fontWeight: 600, flex: 1 }}>
                      {r.invoice}
                    </span>
                    <StateBadge state={r.state} dot={false} fontSize={9} />
                    <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', width: 52, textAlign: 'right' }}>
                      {r.age}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
