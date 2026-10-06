// The audit log's pager. NOT Pager.tsx: that one is offset-based (limit/offset/total,
// onGo(offset)) and this reader is keyset/forward-only, so there is no offset to hand it.
//
// Prev is a client-held cursor stack (lib/auditView.ts) -- the server mints a cursor for
// the next page only.

import { chevDownGlyph } from '../glyphs'
import { AUDIT_PAGE_SIZES } from '../lib/auditView'

function btn(enabled: boolean) {
  return { height: 30, fontSize: 12.5, ...(enabled ? {} : { opacity: 0.45, cursor: 'not-allowed', filter: 'none' }) }
}

export function AuditPager({
  range,
  limit,
  canPrev,
  canNext,
  busy,
  onPrev,
  onNext,
  onLimit,
}: {
  range: string
  limit: number
  canPrev: boolean
  canNext: boolean
  busy: boolean
  onPrev: () => void
  onNext: () => void
  onLimit: (limit: number) => void
}) {
  // Disabled while a page is in flight as well as at the ends: the cursor in hand belongs
  // to the previous response, so a second click would send it twice.
  const prevOn = canPrev && !busy
  const nextOn = canNext && !busy

  return (
    <div data-testid="audit-pager" style={{ display: 'flex', alignItems: 'center', gap: 16, flexWrap: 'wrap', padding: '12px 2px' }}>
      <span className="mono" style={{ fontSize: 11.5, color: 'var(--fg-2)' }}>{range}</span>
      <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, marginLeft: 'auto' }}>
        <span className="label">Rows</span>
        <span style={{ position: 'relative', display: 'inline-block' }}>
          <select
            className="pf-select"
            data-testid="audit-page-size"
            value={limit}
            onChange={(e) => onLimit(Number(e.target.value))}
            style={{ height: 30, padding: '0 30px 0 10px', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-1)', fontSize: 12.5, cursor: 'pointer' }}
          >
            {AUDIT_PAGE_SIZES.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
          <span aria-hidden="true" style={{ position: 'absolute', right: 9, top: '50%', transform: 'translateY(-50%)', display: 'inline-flex', color: 'var(--fg-3)', pointerEvents: 'none' }}>
            {chevDownGlyph}
          </span>
        </span>
      </label>
      <button data-testid="audit-pager-prev" onClick={onPrev} disabled={!prevOn} className="v2-btn v2-btn-ghost pf-btn" style={btn(prevOn)}>
        ← Previous
      </button>
      <button data-testid="audit-pager-next" onClick={onNext} disabled={!nextOn} className="v2-btn v2-btn-ghost pf-btn" style={btn(nextOn)}>
        Next →
      </button>
    </div>
  )
}
