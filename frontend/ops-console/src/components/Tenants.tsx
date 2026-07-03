import { SEARCH_ICON, TENANTS } from '../data'

type Props = {
  tenantQuery: string
  onTenantQueryChange: (v: string) => void
  tenantId: string
  onSelectTenant: (id: string) => void
  onViewJobs: () => void
}

export function Tenants({ tenantQuery, onTenantQueryChange, tenantId, onSelectTenant, onViewJobs }: Props) {
  const tq = tenantQuery.toLowerCase()
  const filtered = TENANTS.filter((t) => !tq || (t.name + ' ' + t.tin).toLowerCase().includes(tq))
  const selected = TENANTS.find((t) => t.id === tenantId) || TENANTS[0]
  const statusDot = (status: 'ok' | 'warn' | 'red') =>
    status === 'ok' ? 'var(--status-green-text)' : status === 'warn' ? 'var(--status-amber-text)' : 'var(--status-red-text)'

  return (
    <div className="ops-screen-pad" style={{ padding: '24px 26px 56px' }}>
      <div style={{ marginBottom: 20 }}>
        <div className="label" style={{ marginBottom: 8 }}>
          / SUPPORT LOOKUP
        </div>
        <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>Tenants &amp; entities</h1>
      </div>
      <div className="ops-tenants-grid" style={{ display: 'grid', gridTemplateColumns: '320px minmax(0,1fr)', gap: 18 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div className="ops-input" style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
            <span style={{ color: 'var(--fg-3)' }}>{SEARCH_ICON}</span>
            <input
              className="ops-input"
              style={{ border: 0, boxShadow: 'none', height: 30, flex: 1, padding: 0 }}
              placeholder="TIN or tenant name…"
              value={tenantQuery}
              onChange={(e) => onTenantQueryChange(e.target.value)}
            />
          </div>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, overflow: 'hidden', background: 'var(--bg-2)' }}>
            {filtered.map((t) => (
              <button
                key={t.id}
                type="button"
                onClick={() => onSelectTenant(t.id)}
                className="ops-nav"
                style={{
                  width: '100%',
                  display: 'flex',
                  alignItems: 'center',
                  gap: 11,
                  border: 0,
                  borderBottom: '1px solid var(--line-1)',
                  cursor: 'pointer',
                  textAlign: 'left',
                  padding: '12px 14px',
                  background: t.id === tenantId ? 'var(--bg-3)' : 'var(--bg-2)',
                }}
              >
                <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 6, background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>
                  {t.initials}
                </span>
                <span style={{ flex: 1, minWidth: 0 }}>
                  <span style={{ display: 'block', fontSize: 13, fontWeight: 600, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{t.name}</span>
                  <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)' }}>
                    {t.tin}
                  </span>
                </span>
                <span style={{ flex: 'none', width: 7, height: 7, borderRadius: 99, background: statusDot(t.status) }} />
              </button>
            ))}
          </div>
        </div>

        {/* tenant detail */}
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, background: 'var(--bg-2)', overflow: 'hidden' }}>
          <div style={{ padding: '20px 22px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'flex-start', gap: 16 }}>
            <span style={{ flex: 'none', width: 48, height: 48, borderRadius: 8, background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', fontSize: 16, fontWeight: 700 }}>
              {selected.initials}
            </span>
            <div style={{ flex: 1 }}>
              <h2 style={{ fontSize: 19, fontWeight: 600, letterSpacing: '-0.02em', margin: '0 0 3px' }}>{selected.name}</h2>
              <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                TIN {selected.tin} · {selected.entityCount}
              </div>
            </div>
            <div style={{ display: 'flex', gap: 8 }}>
              <button
                type="button"
                onClick={onViewJobs}
                className="ops-btn"
                style={{ border: '1px solid var(--line-2)', background: 'var(--bg-2)', cursor: 'pointer', height: 32, padding: '0 12px', borderRadius: 6, fontFamily: 'var(--font-sans)', fontSize: 12, fontWeight: 600, color: 'var(--fg-1)' }}
              >
                View jobs
              </button>
              <button
                type="button"
                className="ops-btn"
                style={{ border: '1px solid var(--accent)', background: 'var(--accent)', cursor: 'pointer', height: 32, padding: '0 12px', borderRadius: 6, fontFamily: 'var(--font-sans)', fontSize: 12, fontWeight: 600, color: '#fff' }}
              >
                View-as (read-only)
              </button>
            </div>
          </div>
          <div className="ops-tenant-kpis" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', borderBottom: '1px solid var(--line-1)' }}>
            {selected.kpis.map((k) => (
              <div key={k.label} style={{ padding: '16px 18px', borderRight: '1px solid var(--line-1)' }}>
                <div className="mono" style={{ fontSize: 20, fontWeight: 600, letterSpacing: '-0.02em', color: k.color }}>
                  {k.value}
                </div>
                <div className="label" style={{ marginTop: 3 }}>
                  {k.label}
                </div>
              </div>
            ))}
          </div>
          <div className="ops-tenant-detail-grid" style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1fr) minmax(0,1fr)', gap: 0 }}>
            <div style={{ padding: '18px 22px', borderRight: '1px solid var(--line-1)' }}>
              <div className="label" style={{ marginBottom: 12 }}>
                Memberships &amp; roles
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                {selected.members.map((m) => (
                  <div key={m.name} style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                    <span style={{ flex: 'none', width: 26, height: 26, borderRadius: 99, background: 'var(--slate-800)', color: '#fff', display: 'grid', placeItems: 'center', fontSize: 9, fontWeight: 700 }}>{m.initials}</span>
                    <span style={{ flex: 1, fontSize: 13, fontWeight: 500 }}>{m.name}</span>
                    <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: m.roleColor, background: m.roleBg, border: `1px solid ${m.roleBorder}`, borderRadius: 4, padding: '2px 7px' }}>
                      {m.role}
                    </span>
                  </div>
                ))}
              </div>
            </div>
            <div style={{ padding: '18px 22px' }}>
              <div className="label" style={{ marginBottom: 12 }}>
                Recent submissions
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 9 }}>
                {selected.recent.map((s) => (
                  <div key={s.invoice} style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                    <span className="mono" style={{ fontSize: 11.5, fontWeight: 600, flex: 1 }}>
                      {s.invoice}
                    </span>
                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: s.stBg, border: `1px solid ${s.stBorder}`, borderRadius: 999, padding: '2px 8px' }}>
                      <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: s.stText }}>
                        {s.stLabel}
                      </span>
                    </span>
                    <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', width: 52, textAlign: 'right' }}>
                      {s.age}
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
