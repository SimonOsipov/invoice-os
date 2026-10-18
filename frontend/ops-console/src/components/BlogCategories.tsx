import type { CSSProperties } from 'react'
import { catMoveTarget, catRows } from '../blog'
import type { Category } from '../blogApi'
import { G } from './BlogList'

type Props = {
  cats: Category[]
  onMove: (id: string, direction: 'up' | 'down') => void
  onAdd?: () => void
  onEdit?: (cat: Category) => void
  onDelete?: (cat: Category) => void
}

const BTN: CSSProperties = {
  width: 30,
  height: 30,
  borderRadius: 6,
  border: '1px solid var(--line-2)',
  background: 'var(--bg-2)',
  color: 'var(--fg-2)',
  display: 'grid',
  placeItems: 'center',
  cursor: 'pointer',
}
const ELLIPSIS: CSSProperties = { whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }

export function BlogCategories({ cats, onMove, onAdd, onEdit, onDelete }: Props) {
  const move = (i: number, d: -1 | 1) => {
    const t = catMoveTarget(cats, i, d)
    if (t) onMove(t.id, t.direction)
  }
  return (
    <>
      <div data-cat-head style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 16, marginBottom: 14, flexWrap: 'wrap' }}>
        <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>The order here is the order of the bands on the blog home and in its sidebar.</span>
        <button type="button" onClick={onAdd} className="v2-btn v2-btn-ghost" style={{ height: 34, padding: '0 13px', fontSize: 13 }}>
          <G name="plus" size={15} />
          Add category
        </button>
      </div>
      <div data-blog-cats style={{ border: '1px solid var(--line-1)', borderRadius: 6, background: 'var(--bg-2)', overflow: 'hidden' }}>
        {catRows(cats).map((r, i) => (
          <div key={r.cat.id} data-cat-row style={{ borderBottom: '1px solid var(--line-1)' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 16px' }}>
              <span className="mono" style={{ flex: 'none', width: 20, fontSize: 11, color: 'var(--fg-3)' }}>
                {r.num}
              </span>
              <span style={{ flex: 'none', width: 36, height: 36, borderRadius: 6, display: 'grid', placeItems: 'center', background: r.tone.bg, color: r.tone.fg }}>
                <G name={r.icon} size={18} />
              </span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--fg-1)' }}>{r.cat.name}</div>
                <div style={{ fontSize: 12, color: 'var(--fg-3)', ...ELLIPSIS }}>{r.cat.headline}</div>
              </div>
              <span className="mono" style={{ flex: 'none', width: 70, fontSize: 11, color: 'var(--fg-3)' }}>
                {r.count}
              </span>
              <div style={{ flex: 'none', display: 'flex', gap: 4 }}>
                <button type="button" className="ops-cat-btn" aria-label="Move up" onClick={() => move(i, -1)} style={{ ...BTN, opacity: r.upOpacity }}>
                  <G name="arrow-up" size={14} />
                </button>
                <button type="button" className="ops-cat-btn" aria-label="Move down" onClick={() => move(i, 1)} style={{ ...BTN, opacity: r.downOpacity }}>
                  <G name="arrow-down" size={14} />
                </button>
                <button type="button" className="ops-cat-btn" aria-label="Edit" onClick={() => onEdit?.(r.cat)} style={BTN}>
                  <G name="pencil" size={14} />
                </button>
                <button type="button" className="ops-cat-del" aria-label="Delete" onClick={() => onDelete?.(r.cat)} style={{ ...BTN, color: 'var(--destructive)' }}>
                  <G name="trash-2" size={14} />
                </button>
              </div>
            </div>
          </div>
        ))}
      </div>
    </>
  )
}
