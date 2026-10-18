import type { TocEntry } from '../model'

type TocProps = { entries: TocEntry[]; active: number; onSelect: (i: number) => void }

export function Toc({ entries, active, onSelect }: TocProps) {
  return (
    <aside
      aria-label="On this page"
      style={{ flex: '0 1 230px', position: 'sticky', top: 96, display: 'flex', flexDirection: 'column', gap: 14 }}
    >
      <div className="t-eyebrow">On this page</div>
      <div style={{ display: 'flex', flexDirection: 'column' }}>
        {entries.map((t) => {
          const on = t.i === active
          return (
            <button
              key={t.i}
              type="button"
              className="blog-toc"
              onClick={() => onSelect(t.i)}
              style={{
                display: 'flex',
                gap: 12,
                alignItems: 'baseline',
                textAlign: 'left',
                cursor: 'pointer',
                padding: '10px 0',
                background: 'transparent',
                border: 0,
                borderTop: '1px solid var(--border)',
                color: on ? 'var(--ink)' : 'var(--text-copy)',
              }}
            >
              <span className="mono" style={{ fontSize: 11, fontWeight: 700, color: on ? 'var(--teal)' : 'var(--step-label)' }}>
                {t.num}
              </span>
              <span style={{ fontSize: 13.5, fontWeight: on ? 700 : 500, lineHeight: 1.45 }}>{t.text}</span>
            </button>
          )
        })}
      </div>
    </aside>
  )
}
