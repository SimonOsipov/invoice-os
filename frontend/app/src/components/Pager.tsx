import { pagerLabels, pagerNav } from '../lib/reviewBatch'

// §7.3's pager. `pagerLabels` and `pagerNav` both read the RESPONSE's echoed pagination;
// this component holds no page-size constant of its own.
export function Pager({
  pagination,
  busy,
  onGo,
  testId = 'review-pager',
}: {
  pagination: { limit: number; offset: number; total: number }
  busy: boolean
  onGo: (offset: number) => void
  testId?: string
}) {
  const nav = pagerNav(pagination)
  const labels = pagerLabels(pagination)
  // Disabled while a page is in flight as well as at the ends: `nav` is computed from the
  // PREVIOUS response during that window, so a second click would recompute the same
  // offset off stale numbers.
  const btn = (enabled: boolean) => ({
    height: 32,
    padding: '0 12px',
    fontSize: 12.5,
    opacity: enabled ? 1 : 0.4,
    cursor: enabled ? 'pointer' : 'not-allowed',
  })
  const canPrev = nav.canPrev && !busy
  const canNext = nav.canNext && !busy

  return (
    <div data-testid={testId} style={{ display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap' }}>
      <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>{labels.showing}</span>
      <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>{labels.page}</span>
      <div style={{ display: 'flex', gap: 8, marginLeft: 'auto' }}>
        <button onClick={() => onGo(nav.prevOffset)} disabled={!canPrev} className="v2-btn v2-btn-ghost pf-btn" style={btn(canPrev)}>
          ← Previous
        </button>
        <button onClick={() => onGo(nav.nextOffset)} disabled={!canNext} className="v2-btn v2-btn-ghost pf-btn" style={btn(canNext)}>
          Next →
        </button>
      </div>
    </div>
  )
}
