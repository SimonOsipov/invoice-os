import { DIFF_ROWS, PUBLISH_ICON } from '../data'

type Props = {
  onClose: () => void
  onConfirm: () => void
}

export function PublishModal({ onClose, onConfirm }: Props) {
  return (
    <div
      onClick={onClose}
      style={{ position: 'fixed', inset: 0, zIndex: 90, background: 'rgba(20,23,26,0.42)', display: 'grid', placeItems: 'center', animation: 'opsFade 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{ width: 560, maxWidth: '94vw', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 10, overflow: 'hidden', boxShadow: '0 24px 60px -20px rgba(20,23,26,0.4)', animation: 'opsPop 160ms ease-out' }}
      >
        <div style={{ padding: '20px 24px', borderBottom: '1px solid var(--line-1)' }}>
          <h3 style={{ fontSize: 17, fontWeight: 600, letterSpacing: '-0.02em', margin: '0 0 4px' }}>Publish draft → v9</h3>
          <p style={{ fontSize: 13, color: 'var(--fg-3)', margin: 0 }}>
            Creates a new immutable version. Diff vs active{' '}
            <span className="mono" style={{ fontWeight: 600 }}>
              v8
            </span>
            .
          </p>
        </div>
        <div style={{ padding: '8px 24px', maxHeight: 320, overflowY: 'auto' }}>
          {DIFF_ROWS.map((d) => (
            <div key={d.key} style={{ display: 'flex', alignItems: 'flex-start', gap: 11, padding: '11px 0', borderBottom: '1px solid var(--line-1)' }}>
              <span className="mono" style={{ flex: 'none', width: 22, height: 22, borderRadius: 5, background: d.bg, color: d.color, display: 'grid', placeItems: 'center', fontSize: 13, fontWeight: 700 }}>
                {d.sign}
              </span>
              <div style={{ flex: 1 }}>
                <div className="mono" style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg-1)' }}>
                  {d.key}
                </div>
                <div style={{ fontSize: 12, color: 'var(--fg-3)', marginTop: 1 }}>{d.detail}</div>
              </div>
              <span className="mono" style={{ fontSize: 10, fontWeight: 700, color: d.color, letterSpacing: '0.04em' }}>
                {d.tag}
              </span>
            </div>
          ))}
        </div>
        <div style={{ padding: '14px 24px', borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
            3 ADDED · 1 CHANGED · 1 REMOVED
          </span>
          <div style={{ display: 'flex', gap: 10 }}>
            <button type="button" onClick={onClose} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 38 }}>
              Cancel
            </button>
            <button type="button" onClick={onConfirm} className="ops-btn v2-btn v2-btn-primary" style={{ height: 38 }}>
              {PUBLISH_ICON} Publish v9
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
