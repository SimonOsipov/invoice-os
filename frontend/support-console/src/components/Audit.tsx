import { AUDIT_ENTRIES, AUDIT_FILTERS, CHEVRON_RIGHT_ICON, LOCK_ICON, SEARCH_ICON } from '../data'
import type { AuditEntry, AuditFilter } from '../types'

type Props = {
  query: string
  filter: AuditFilter
  onQueryChange: (q: string) => void
  onFilterChange: (f: AuditFilter) => void
  onOpen: (id: string) => void
}

const AUDIT_COLS = '150px 28px minmax(200px,1.4fr) minmax(140px,1.1fr) 130px 22px'

// proto:944. The icon tint carries the outcome, not the row — green accepted, red
// rejected/killed, amber recovered, teal everything else.
const TONE = {
  red: { bg: 'var(--status-red-bg)', color: 'var(--status-red-text)' },
  amber: { bg: 'var(--status-amber-bg)', color: 'var(--status-amber-text)' },
  green: { bg: 'var(--status-green-bg)', color: 'var(--status-green-text)' },
  teal: { bg: 'var(--action-tint)', color: 'var(--action)' },
} as const

// Exported so the drawer resolves the same entry the row did, and so the filter is
// testable without rendering. proto:938 — the free-text query spans action, object,
// tenant and actor; the chip filter matches the object type exactly.
export function filterAudit(entries: AuditEntry[], query: string, filter: AuditFilter): AuditEntry[] {
  const q = query.trim().toLowerCase()
  return entries.filter((a) => {
    const matchQ = !q || `${a.action} ${a.object} ${a.tenant} ${a.actor}`.toLowerCase().includes(q)
    const matchF = filter === 'all' || a.objectType === filter
    return matchQ && matchF
  })
}

export function Audit({ query, filter, onQueryChange, onFilterChange, onOpen }: Props) {
  const rows = filterAudit(AUDIT_ENTRIES, query, filter)

  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24, flexWrap: 'wrap' }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 8 }}>
            AUDIT &amp; EVIDENCE
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 500, letterSpacing: '-0.03em', margin: 0 }}>Audit &amp; evidence explorer</h1>
        </div>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 7, background: 'var(--status-muted-bg)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', padding: '7px 12px' }}>
          {LOCK_ICON}
          <span className="mono" style={{ fontSize: 10.5, fontWeight: 600, color: 'var(--fg-2)', letterSpacing: '0.04em' }}>
            APPEND-ONLY · IMMUTABLE
          </span>
        </span>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16, flexWrap: 'wrap' }}>
        <div className="ops-input" style={{ flex: 1, minWidth: 280, display: 'flex', alignItems: 'center', gap: 9 }}>
          <span style={{ color: 'var(--fg-3)' }}>{SEARCH_ICON}</span>
          <input
            style={{ border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)', height: 30, flex: 1, padding: 0 }}
            placeholder="Filter by tenant, invoice, actor, action…"
            aria-label="Filter audit entries"
            value={query}
            onChange={(e) => onQueryChange(e.target.value)}
          />
        </div>
        {AUDIT_FILTERS.map((f) => {
          const active = filter === f.key
          return (
            <button
              key={f.key}
              type="button"
              onClick={() => onFilterChange(f.key)}
              className="ops-chip"
              aria-pressed={active}
              style={{ border: `1px solid ${active ? 'var(--teal-200)' : 'var(--line-1)'}`, background: active ? 'var(--action-tint)' : 'var(--bg-2)', color: active ? 'var(--action)' : 'var(--fg-3)', borderRadius: 99, height: 34, padding: '0 13px', fontFamily: 'var(--font-mono)', fontSize: 10.5, fontWeight: 600, letterSpacing: '0.04em' }}
            >
              {f.label}
            </button>
          )
        })}
      </div>

      <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflowX: 'auto', background: 'var(--bg-2)' }}>
        <div style={{ padding: '11px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', minWidth: 760 }}>
          <span className="label">Event timeline</span>
          <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)' }}>
            {rows.length} OF {AUDIT_ENTRIES.length} ENTRIES
          </span>
        </div>
        {rows.map((a) => {
          const tone = TONE[a.tone]
          return (
            <div
              key={a.id}
              className="ops-row"
              onClick={() => onOpen(a.id)}
              style={{ display: 'grid', gridTemplateColumns: AUDIT_COLS, padding: '13px 16px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: 760 }}
            >
              <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                {a.ts}
              </span>
              <span style={{ display: 'inline-flex', width: 22, height: 22, borderRadius: 'var(--radius-sm)', background: tone.bg, color: tone.color, alignItems: 'center', justifyContent: 'center' }}>{a.glyph}</span>
              <span style={{ minWidth: 0, paddingRight: 12 }}>
                <span style={{ display: 'block', fontSize: 13, fontWeight: 500 }}>{a.action}</span>
                <span className="mono" style={{ display: 'block', fontSize: 10.5, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {a.object}
                </span>
              </span>
              <span style={{ fontSize: 12, color: 'var(--fg-2)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>{a.tenant}</span>
              <span style={{ display: 'flex', alignItems: 'center', gap: 7 }}>
                <span style={{ width: 22, height: 22, borderRadius: 99, background: 'var(--slate-800)', color: 'var(--text-on-dark)', display: 'grid', placeItems: 'center', fontSize: 9, fontWeight: 700 }}>{a.who}</span>
                <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)' }}>
                  {a.actor}
                </span>
              </span>
              <span style={{ color: 'var(--fg-4)' }}>{CHEVRON_RIGHT_ICON}</span>
            </div>
          )
        })}
        {rows.length === 0 && (
          <div className="mono" style={{ padding: '28px 16px', textAlign: 'center', fontSize: 12, color: 'var(--fg-4)' }}>
            No audit entries match this filter.
          </div>
        )}
      </div>
    </div>
  )
}
