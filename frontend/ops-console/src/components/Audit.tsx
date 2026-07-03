import { AUDIT_ENTRIES, CHEVRON_RIGHT_ICON, LOCK_ICON, SEARCH_ICON } from '../data'
import { auditToneColor } from '../helpers'

type Props = {
  auditQuery: string
  onAuditQueryChange: (v: string) => void
  auditFilter: string
  onAuditFilterChange: (v: string) => void
  onOpenAudit: (id: string) => void
}

const AUDIT_FILTERS: { key: string; label: string }[] = [
  { key: 'all', label: 'ALL' },
  { key: 'submission', label: 'SUBMISSION' },
  { key: 'rule', label: 'RULE' },
  { key: 'state', label: 'STATE CHANGE' },
]

export function Audit({ auditQuery, onAuditQueryChange, auditFilter, onAuditFilterChange, onOpenAudit }: Props) {
  const q = auditQuery.toLowerCase()
  const filtered = AUDIT_ENTRIES.filter((a) => {
    const matchQ = !q || (a.action + ' ' + a.object + ' ' + a.tenant + ' ' + a.actor).toLowerCase().includes(q)
    const matchF = auditFilter === 'all' || a.objectType.toLowerCase().includes(auditFilter)
    return matchQ && matchF
  })

  return (
    <div className="ops-screen-pad" style={{ padding: '24px 26px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24 }}>
        <div>
          <div className="label" style={{ marginBottom: 8 }}>
            / 08 — AUDIT &amp; EVIDENCE
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>Audit &amp; evidence explorer</h1>
        </div>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 7, background: 'var(--status-muted-bg)', border: '1px solid var(--line-2)', borderRadius: 6, padding: '7px 12px' }}>
          {LOCK_ICON}
          <span className="mono" style={{ fontSize: 10.5, fontWeight: 600, color: 'var(--fg-2)', letterSpacing: '0.04em' }}>
            APPEND-ONLY · IMMUTABLE
          </span>
        </span>
      </div>

      {/* search + filters */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16, flexWrap: 'wrap' }}>
        <div className="ops-input" style={{ flex: 1, minWidth: 280, display: 'flex', alignItems: 'center', gap: 9 }}>
          <span style={{ color: 'var(--fg-3)' }}>{SEARCH_ICON}</span>
          <input
            className="ops-input"
            style={{ border: 0, boxShadow: 'none', height: 30, flex: 1, padding: 0 }}
            placeholder="Filter by tenant, invoice, actor, action…"
            value={auditQuery}
            onChange={(e) => onAuditQueryChange(e.target.value)}
          />
        </div>
        {AUDIT_FILTERS.map((f) => {
          const active = auditFilter === f.key
          return (
            <button
              key={f.key}
              type="button"
              onClick={() => onAuditFilterChange(f.key)}
              className="ops-chip"
              style={{
                border: `1px solid ${active ? 'var(--teal-200)' : 'var(--line-1)'}`,
                background: active ? 'var(--accent-tint)' : 'var(--bg-2)',
                color: active ? 'var(--accent)' : 'var(--fg-3)',
                borderRadius: 99,
                height: 34,
                padding: '0 13px',
                fontFamily: 'var(--font-mono)',
                fontSize: 10.5,
                fontWeight: 600,
                letterSpacing: '0.04em',
              }}
            >
              {f.label}
            </button>
          )
        })}
      </div>

      {/* timeline */}
      <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, overflowX: 'auto', background: 'var(--bg-2)' }}>
        <div style={{ padding: '11px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', minWidth: 760 }}>
          <span className="label">Event timeline</span>
          <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)' }}>
            {filtered.length} OF {AUDIT_ENTRIES.length} ENTRIES
          </span>
        </div>
        {filtered.map((a) => {
          const c = auditToneColor(a.tone)
          return (
            <div
              key={a.id}
              className="ops-row"
              onClick={() => onOpenAudit(a.id)}
              style={{ display: 'grid', gridTemplateColumns: '150px 28px minmax(200px,1.4fr) minmax(140px,1.1fr) 130px 22px', gap: 0, padding: '13px 16px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: 760 }}
            >
              <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                {a.ts}
              </span>
              <span style={{ display: 'inline-flex', width: 22, height: 22, borderRadius: 5, background: c.bg, color: c.color, alignItems: 'center', justifyContent: 'center' }}>{a.glyph}</span>
              <span style={{ minWidth: 0, paddingRight: 12 }}>
                <span style={{ display: 'block', fontSize: 13, fontWeight: 500 }}>{a.action}</span>
                <span className="mono" style={{ display: 'block', fontSize: 10.5, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {a.object}
                </span>
              </span>
              <span style={{ fontSize: 12, color: 'var(--fg-2)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>{a.tenant}</span>
              <span style={{ display: 'flex', alignItems: 'center', gap: 7 }}>
                <span style={{ width: 22, height: 22, borderRadius: 99, background: 'var(--slate-800)', color: '#fff', display: 'grid', placeItems: 'center', fontSize: 9, fontWeight: 700 }}>{a.who}</span>
                <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)' }}>
                  {a.actor}
                </span>
              </span>
              <span style={{ color: 'var(--fg-4)' }}>{CHEVRON_RIGHT_ICON}</span>
            </div>
          )
        })}
      </div>
    </div>
  )
}
