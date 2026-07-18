// Compliance evidence — the client-facing bundle list (prototype lines 312-353).
// Three-way naming is intentional and ported verbatim: nav label `Evidence`, crumb
// `Compliance evidence` (proto:849), screen <h1> `Compliance evidence` (proto:318).

import { CHEVRON_RIGHT_ICON, EVIDENCE_DATA, EXPORT_ICON, LOCK_ICON, SEARCH_ICON, SHIELD_ICON } from '../data'

type Props = {
  query: string
  onQueryChange: (q: string) => void
  onOpen: (id: string) => void
  onExportAll: () => void
}

// Byte-shared between the header and the rows (proto:336, 340) so the two grids
// cannot drift apart.
const EVIDENCE_GRID = '150px 190px minmax(180px,1.2fr) 130px 120px 120px 22px'

export function Evidence({ query, onQueryChange, onOpen, onExportAll }: Props) {
  // proto:986. Unlike Submissions there is no state filter — just the query, matched
  // across invoice + IRN + buyer. The prototype renders zero rows when nothing
  // matches; there is deliberately no empty state on this screen (proto:334-350).
  const q = query.toLowerCase()
  const rows = q ? EVIDENCE_DATA.filter((e) => (e.invoice + ' ' + e.irn + ' ' + e.buyer).toLowerCase().includes(q)) : EVIDENCE_DATA

  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24 }}>
        <div>
          <div className="label" style={{ marginBottom: 8 }}>
            / 03 — COMPLIANCE EVIDENCE
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>Compliance evidence</h1>
        </div>
        <span
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: 7,
            background: 'var(--status-muted-bg)',
            border: '1px solid var(--line-2)',
            borderRadius: 6,
            padding: '7px 12px',
          }}
        >
          {LOCK_ICON}
          <span className="mono" style={{ fontSize: 10.5, fontWeight: 600, color: 'var(--fg-2)', letterSpacing: '0.04em' }}>
            SIGNED · HASH-CHAINED
          </span>
        </span>
      </div>

      <p style={{ fontSize: 13, color: 'var(--fg-2)', margin: '0 0 16px', maxWidth: 720, lineHeight: 1.5 }}>
        Every cleared invoice returns a signed evidence bundle — cryptographic proof it was accepted by FIRS/MBS. Bundles are immutable and chained; export
        them for your own records or audits.
      </p>

      {/* search + export */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16, flexWrap: 'wrap' }}>
        <div className="ops-input" style={{ flex: 1, minWidth: 280, display: 'flex', alignItems: 'center', gap: 9 }}>
          <span style={{ color: 'var(--fg-3)' }}>{SEARCH_ICON}</span>
          <input
            className="ops-input"
            style={{ border: 0, boxShadow: 'none', height: 30, flex: 1, padding: 0 }}
            placeholder="Filter by invoice #, IRN, or buyer…"
            value={query}
            onChange={(e) => onQueryChange(e.target.value)}
          />
        </div>
        <button type="button" onClick={onExportAll} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 34 }}>
          {EXPORT_ICON} Export all
        </button>
      </div>

      {/* bundle table */}
      <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, overflowX: 'auto', background: 'var(--bg-2)' }}>
        <div
          className="ops-evidence-table"
          style={{
            display: 'grid',
            gridTemplateColumns: EVIDENCE_GRID,
            padding: '11px 16px',
            background: 'var(--bg-1)',
            borderBottom: '1px solid var(--line-1)',
            minWidth: 940,
          }}
        >
          <span className="label">Invoice #</span>
          <span className="label">IRN</span>
          <span className="label">Buyer</span>
          <span className="label">Value</span>
          <span className="label">Cleared</span>
          <span className="label">Bundle</span>
          <span />
        </div>
        {rows.map((e) => (
          <div
            key={e.id}
            onClick={() => onOpen(e.id)}
            className="ops-row"
            style={{
              display: 'grid',
              gridTemplateColumns: EVIDENCE_GRID,
              padding: '12px 16px',
              borderBottom: '1px solid var(--line-1)',
              alignItems: 'center',
              minWidth: 940,
            }}
          >
            <span className="mono" style={{ fontSize: 12, fontWeight: 600 }}>
              {e.invoice}
            </span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--accent)' }}>
              {e.irn}
            </span>
            <span style={{ fontSize: 12.5, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>{e.buyer}</span>
            <span className="mono" style={{ fontSize: 12, fontWeight: 600 }}>
              {e.value}
            </span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              {e.cleared}
            </span>
            <span>
              <span
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 5,
                  background: 'var(--status-green-bg)',
                  border: '1px solid var(--status-green-border)',
                  borderRadius: 999,
                  padding: '2px 8px',
                }}
              >
                {SHIELD_ICON}
                <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: 'var(--status-green-text)', letterSpacing: '0.03em' }}>
                  SIGNED
                </span>
              </span>
            </span>
            <span style={{ color: 'var(--fg-4)' }}>{CHEVRON_RIGHT_ICON}</span>
          </div>
        ))}
      </div>
    </div>
  )
}
