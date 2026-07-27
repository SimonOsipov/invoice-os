import { DIFF_ROWS, PUBLISH_ICON } from '../data'
import { Modal } from './Modal'

type Props = {
  onClose: () => void
  onConfirm: () => void
}

// proto:663. Publishing seals a new immutable rule-set version, so the diff against the
// active set is shown before the button that does it — never after.
const DIFF_TONE = {
  added: { bg: 'var(--status-green-bg)', color: 'var(--status-green-text)' },
  changed: { bg: 'var(--status-amber-bg)', color: 'var(--status-amber-text)' },
  removed: { bg: 'var(--status-red-bg)', color: 'var(--status-red-text)' },
} as const

export function PublishModal({ onClose, onConfirm }: Props) {
  // Derived rather than hardcoded, so the summary line can't disagree with the rows above
  // it — the prototype printed a fixed "3 ADDED · 1 CHANGED · 1 REMOVED".
  const counts = DIFF_ROWS.reduce<Record<string, number>>((acc, d) => {
    acc[d.tag] = (acc[d.tag] ?? 0) + 1
    return acc
  }, {})
  const summary = (['ADDED', 'CHANGED', 'REMOVED'] as const)
    .filter((t) => counts[t])
    .map((t) => `${counts[t]} ${t}`)
    .join(' · ')

  return (
    <Modal width={560} onClose={onClose}>
      <div style={{ padding: '20px 24px', borderBottom: '1px solid var(--line-1)' }}>
        <h3 style={{ fontSize: 17, fontWeight: 500, letterSpacing: '-0.02em', margin: '0 0 4px' }}>Publish draft → v9</h3>
        <p style={{ fontSize: 13, color: 'var(--fg-3)', margin: 0 }}>
          Creates a new immutable version. Diff vs active{' '}
          <span className="mono" style={{ fontWeight: 600 }}>
            v8
          </span>
          .
        </p>
      </div>
      <div style={{ padding: '8px 24px', maxHeight: 320, overflowY: 'auto' }}>
        {DIFF_ROWS.map((d) => {
          const tone = DIFF_TONE[d.kind]
          return (
            <div key={d.key} style={{ display: 'flex', alignItems: 'flex-start', gap: 11, padding: '11px 0', borderBottom: '1px solid var(--line-1)' }}>
              <span className="mono" style={{ flex: 'none', width: 22, height: 22, borderRadius: 'var(--radius-sm)', background: tone.bg, color: tone.color, display: 'grid', placeItems: 'center', fontSize: 13, fontWeight: 700 }}>
                {d.sign}
              </span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div className="mono" style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg-1)' }}>
                  {d.key}
                </div>
                <div style={{ fontSize: 12, color: 'var(--fg-3)', marginTop: 1 }}>{d.detail}</div>
              </div>
              <span className="mono" style={{ fontSize: 10, fontWeight: 700, color: tone.color, letterSpacing: '0.04em' }}>
                {d.tag}
              </span>
            </div>
          )
        })}
      </div>
      <div style={{ padding: '14px 24px', borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, flexWrap: 'wrap' }}>
        <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
          {summary}
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
    </Modal>
  )
}
