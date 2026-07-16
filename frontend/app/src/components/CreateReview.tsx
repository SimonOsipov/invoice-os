// Create flow · step 3 (many-invoice variant) — review the invoices detected in
// the mapped sheet before creating drafts. Ported from Platform.dc.html
// ~L542-578 (markup) + ~L1591-1602 (render values).
//
// Quarantine is per INVOICE: rows of one invoice that disagree on a header field
// take the whole invoice out of the import, but the error cites the spreadsheet
// row numbers so the user can find them in their own file.

import { groupInvoices } from '../lib/mapping'
import { shieldGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

export function CreateReview({ ctx }: { ctx: PlatformCtx }) {
  const { active, uploadFile, mapping } = ctx
  const invGroups = groupInvoices(uploadFile, mapping)
  const cleanCount = invGroups.filter((g) => !g.quarantined).length
  const quarantined = invGroups.length - cleanCount
  const hasQuarantine = quarantined > 0

  const rows = invGroups.map((g) => ({
    number: g.number,
    issueDate: g.issueDate || '—',
    buyer: g.buyer || '—',
    lineCount: `${g.lineCount} ${g.lineCount === 1 ? 'line' : 'lines'}`,
    total: g.total ? `₦${g.total}` : '—',
    quarantined: g.quarantined,
    rowBg: g.quarantined ? 'var(--status-red-bg)' : 'var(--bg-2)',
    numColor: g.quarantined ? 'var(--status-red-text)' : 'var(--fg-1)',
    st: g.quarantined
      ? { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'QUARANTINED' }
      : { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'READY' },
    detail: g.conflicts.map((c) => `Rows ${c.rows.join('–')} disagree on ${c.label} — ${c.values.join(' vs ')}`).join('; '),
  }))

  const createBtn = {
    label: `Create ${cleanCount} draft${cleanCount === 1 ? '' : 's'}`,
    bg: cleanCount > 0 ? 'var(--accent)' : 'var(--bg-3)',
    color: cleanCount > 0 ? '#fff' : 'var(--fg-4)',
    cursor: cleanCount > 0 ? 'pointer' : 'not-allowed',
  }

  const GRID = '128px 96px 1fr 64px 120px'

  return (
    <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
      <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <span style={{ fontSize: 15, fontWeight: 600 }}>Review detected invoices · {active.short}</span>
        <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
          {cleanCount} READY · {quarantined} QUARANTINED
        </span>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: GRID, gap: 10, padding: '10px 20px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
        <span className="label">Invoice</span>
        <span className="label">Issue date</span>
        <span className="label">Buyer</span>
        <span className="label" style={{ textAlign: 'right' }}>Lines</span>
        <span className="label" style={{ textAlign: 'right' }}>Total</span>
      </div>
      {rows.map((g) => (
        <div key={g.number} style={{ borderBottom: '1px solid var(--line-1)', background: g.rowBg }}>
          <div style={{ display: 'grid', gridTemplateColumns: GRID, gap: 10, padding: '13px 20px', alignItems: 'center' }}>
            <span style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
              <span className="mono" style={{ fontSize: 12.5, fontWeight: 600, color: g.numColor }}>{g.number}</span>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5, alignSelf: 'flex-start', background: g.st.bg, border: `1px solid ${g.st.border}`, borderRadius: 999, padding: '2px 7px' }}>
                <span style={{ width: 5, height: 5, borderRadius: 99, background: g.st.text }} />
                <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: g.st.text, letterSpacing: '0.04em' }}>{g.st.label}</span>
              </span>
            </span>
            <span className="mono" style={{ fontSize: 12, color: 'var(--fg-2)' }}>{g.issueDate}</span>
            <span style={{ fontSize: 13, color: 'var(--fg-1)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{g.buyer}</span>
            <span style={{ fontSize: 12.5, color: 'var(--fg-2)', textAlign: 'right' }}>{g.lineCount}</span>
            <span className="money" style={{ fontSize: 13, fontWeight: 600, textAlign: 'right' }}>{g.total}</span>
          </div>
          {g.quarantined && (
            <div style={{ padding: '0 20px 12px 20px' }}>
              <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, padding: '8px 11px', background: 'var(--bg-2)', border: '1px solid var(--status-red-border)', borderRadius: 6 }}>
                <span style={{ fontSize: 11.5, color: 'var(--status-red-text)', lineHeight: 1.45 }}>Quarantined — {g.detail}. Fix the file and re-import.</span>
              </div>
            </div>
          )}
        </div>
      ))}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 16, padding: '16px 20px' }}>
        <p style={{ fontSize: 12, color: 'var(--fg-3)', margin: 0, maxWidth: 420, lineHeight: 1.5 }}>
          {hasQuarantine && <span>One bad line quarantines its whole invoice. Only ready invoices are imported.</span>}
        </p>
        <div style={{ display: 'flex', gap: 10, flex: 'none' }}>
          <button onClick={ctx.backToMapping} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 42, padding: '0 16px' }}>
            ← Back to mapping
          </button>
          <button onClick={ctx.createDrafts} className="v2-btn pf-btn" style={{ height: 42, padding: '0 18px', justifyContent: 'center', background: createBtn.bg, color: createBtn.color, cursor: createBtn.cursor }}>
            <span style={{ display: 'inline-flex' }}>{shieldGlyph}</span> {createBtn.label}
          </button>
        </div>
      </div>
    </div>
  )
}
