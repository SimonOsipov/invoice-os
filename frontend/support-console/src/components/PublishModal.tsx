import { useState } from 'react'
import { PUBLISH_ICON } from '../icons'
import type { DiffRow } from '../rulesApi'
import { Fault } from './Fault'
import { Modal } from './Modal'

type Props = {
  draftVersion: number
  inForceVersion: number | null
  /** null while the draft and the version in force are still being read. */
  rows: DiffRow[] | null
  warnings: string[]
  today: string
  busy: boolean
  error: string | null
  onClose: () => void
  onConfirm: (date: string) => void
}

// Publishing seals a new immutable rule-set version, so the diff against the version in
// force is shown before the button that does it — never after.
const DIFF_TONE = {
  added: { bg: 'var(--status-green-bg)', color: 'var(--status-green-text)' },
  changed: { bg: 'var(--status-amber-bg)', color: 'var(--status-amber-text)' },
  removed: { bg: 'var(--status-red-bg)', color: 'var(--status-red-text)' },
} as const

const DATE_RE = /^\d{4}-\d{2}-\d{2}$/

export function PublishModal({ draftVersion, inForceVersion, rows, warnings, today, busy, error, onClose, onConfirm }: Props) {
  const [date, setDate] = useState('')
  const counts = (rows ?? []).reduce<Record<string, number>>((acc, d) => {
    acc[d.tag] = (acc[d.tag] ?? 0) + 1
    return acc
  }, {})
  const summary = (['ADDED', 'CHANGED', 'REMOVED'] as const)
    .filter((t) => counts[t])
    .map((t) => `${counts[t]} ${t}`)
    .join(' · ')
  const blocked = busy || rows === null || error !== null || !DATE_RE.test(date) || date < today

  return (
    <Modal width={560} onClose={onClose}>
      <div style={{ padding: '20px 24px', borderBottom: '1px solid var(--line-1)' }}>
        <h3 style={{ fontSize: 17, margin: '0 0 4px' }}>{`Publish draft → v${draftVersion}`}</h3>
        <p style={{ fontSize: 13, color: 'var(--fg-3)', margin: 0 }}>
          Creates a new immutable version. Diff vs in force{' '}
          <span className="mono" style={{ fontWeight: 600 }}>
            {inForceVersion === null ? '—' : `v${inForceVersion}`}
          </span>
          .
        </p>
      </div>
      <div style={{ padding: '8px 24px', maxHeight: 320, overflowY: 'auto' }}>
        {error !== null && <Fault text={error} />}
        {warnings.map((w) => (
          <Fault key={w} text={w} tone="amber" />
        ))}
        {rows === null && error === null && <div style={{ padding: '11px 0', fontSize: 13, color: 'var(--fg-3)' }}>Loading diff…</div>}
        {rows !== null && rows.length === 0 && <div style={{ padding: '11px 0', fontSize: 13, color: 'var(--fg-3)' }}>No changes against the version in force.</div>}
        {(rows ?? []).map((d) => {
          const tone = DIFF_TONE[d.kind]
          return (
            <div key={d.key} style={{ display: 'flex', alignItems: 'flex-start', gap: 11, padding: '11px 0', borderBottom: '1px solid var(--line-1)' }}>
              <span className="mono" style={{ flex: 'none', width: 22, height: 22, borderRadius: 'var(--radius-sm)', background: tone.bg, color: tone.color, display: 'grid', placeItems: 'center', fontSize: 13, fontWeight: 700 }}>
                {d.sign}
              </span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div className="mono" style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg-1)', overflowWrap: 'anywhere' }}>
                  {d.key}
                </div>
                <div style={{ fontSize: 12, color: 'var(--fg-3)', marginTop: 1, overflowWrap: 'anywhere' }}>{d.detail}</div>
              </div>
              <span className="mono" style={{ fontSize: 10, fontWeight: 700, color: tone.color, letterSpacing: '0.04em' }}>
                {d.tag}
              </span>
            </div>
          )
        })}
      </div>
      <div style={{ padding: '14px 24px', borderTop: '1px solid var(--line-1)' }}>
        <div className="label" style={{ marginBottom: 6 }}>
          Start date
        </div>
        <div className="ops-input ops-field" style={{ display: 'flex', alignItems: 'center' }}>
          <input
            type="date"
            aria-label="Start date"
            min={today}
            value={date}
            onChange={(e) => setDate(e.target.value)}
            style={{ border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)', height: 30, flex: 1, padding: 0 }}
          />
        </div>
      </div>
      <div style={{ padding: '14px 24px', borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, flexWrap: 'wrap' }}>
        <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
          {summary}
        </span>
        <div style={{ display: 'flex', gap: 10 }}>
          <button type="button" onClick={onClose} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 38 }}>
            Cancel
          </button>
          <button
            type="button"
            disabled={blocked}
            onClick={() => onConfirm(date)}
            className="ops-btn v2-btn v2-btn-primary"
            style={{ height: 38, ...(blocked ? { opacity: 0.45, filter: 'none', cursor: 'not-allowed' } : {}) }}
          >
            {PUBLISH_ICON} {`Publish v${draftVersion}`}
          </button>
        </div>
      </div>
    </Modal>
  )
}
