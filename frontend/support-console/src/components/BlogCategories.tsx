import type { CSSProperties } from 'react'
import { CATEGORY_ICONS, catMoveTarget, catRows, deleteView, TONES, type CatDel, type CatForm } from '../blog'
import type { Category, Post } from '../blogApi'
import { G } from './BlogList'

type Props = {
  cats: Category[]
  posts: Post[]
  onMove: (id: string, direction: 'up' | 'down') => void
  onAdd?: () => void
  onEdit?: (cat: Category) => void
  onDelete?: (cat: Category) => void
  form?: CatForm | null
  del?: CatDel | null
  onField?: (patch: Partial<Omit<CatForm, 'error'>>) => void
  onSubmit?: () => void
  onCancelForm?: () => void
  onMoveTo?: (id: string) => void
  onCancelDel?: () => void
  onConfirmDel?: () => void
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
const LABEL: CSSProperties = { fontSize: 10, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--muted-foreground)' }
const FIELD: CSSProperties = { width: '100%', fontFamily: 'var(--font-sans)', fontSize: 13.5, color: 'var(--fg-1)', background: 'var(--bg-2)', borderRadius: 6, outline: 'none' }
const FIELD_LABEL: CSSProperties = { display: 'flex', flexDirection: 'column', gap: 6 }
const PANEL: CSSProperties = { padding: '4px 16px 18px 70px' }

type FormProps = Pick<Props, 'onField' | 'onSubmit'> & { form: CatForm; onCancel?: () => void }

function CatFormCard({ form, onField, onSubmit, onCancel }: FormProps) {
  const isNew = form.id === null
  return (
    <div data-cat-form style={{ border: '1px solid var(--line-2)', borderRadius: 6, background: 'var(--bg-1)', padding: 18, display: 'flex', flexDirection: 'column', gap: 14 }}>
      {isNew && <span style={{ fontSize: 15, fontWeight: 700, letterSpacing: '-0.01em' }}>New category</span>}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 12 }}>
        <label style={FIELD_LABEL}>
          <span style={LABEL}>Name</span>
          <input
            value={form.name}
            onChange={(e) => onField?.({ name: e.target.value })}
            placeholder={isNew ? 'Events & webinars' : undefined}
            style={{ ...FIELD, height: 38, padding: '0 12px', border: `1px solid ${form.error ? 'var(--destructive)' : 'var(--input)'}` }}
          />
        </label>
        <label style={FIELD_LABEL}>
          <span style={LABEL}>Band headline</span>
          <input
            value={form.headline}
            onChange={(e) => onField?.({ headline: e.target.value })}
            placeholder={isNew ? 'Where to find us.' : 'What changed in ASComply.'}
            style={{ ...FIELD, height: 38, padding: '0 12px', border: '1px solid var(--input)' }}
          />
        </label>
      </div>
      <label style={FIELD_LABEL}>
        <span style={LABEL}>Intro</span>
        <textarea
          value={form.intro}
          onChange={(e) => onField?.({ intro: e.target.value })}
          rows={2}
          placeholder="One sentence shown under the headline."
          style={{ ...FIELD, padding: '10px 12px', border: '1px solid var(--input)', resize: 'vertical', lineHeight: 1.5 }}
        />
      </label>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 24 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <span style={LABEL}>Icon</span>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, maxWidth: 420 }}>
            {CATEGORY_ICONS.map((name) => {
              const on = form.icon === name
              return (
                <button
                  key={name}
                  type="button"
                  aria-label={name}
                  aria-pressed={on}
                  onClick={() => onField?.({ icon: name })}
                  style={{ width: 32, height: 32, borderRadius: 6, border: `1px solid ${on ? 'var(--primary)' : 'var(--line-2)'}`, background: on ? 'var(--primary)' : 'var(--bg-2)', color: on ? 'var(--primary-foreground)' : 'var(--fg-2)', display: 'grid', placeItems: 'center', cursor: 'pointer' }}
                >
                  <G name={name} size={15} />
                </button>
              )
            })}
          </div>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <span style={LABEL}>Cover colour</span>
          <div style={{ display: 'flex', gap: 8 }}>
            {TONES.map((t, i) => {
              const on = form.tone === i
              return (
                <button
                  key={i}
                  type="button"
                  aria-label="Colour"
                  aria-pressed={on}
                  onClick={() => onField?.({ tone: i })}
                  style={{ width: 32, height: 32, borderRadius: 6, border: '1px solid var(--line-2)', background: t.bg, cursor: 'pointer', boxShadow: on ? '0 0 0 2px var(--ring)' : 'none' }}
                />
              )
            })}
          </div>
        </div>
      </div>
      {form.error && <span style={{ fontSize: 12.5, fontWeight: 600, color: 'var(--destructive)' }}>{form.error}</span>}
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button type="button" onClick={onCancel} className="v2-btn v2-btn-ghost" style={{ height: 34, padding: '0 14px', fontSize: 13 }}>
          Cancel
        </button>
        <button type="button" onClick={onSubmit} className="v2-btn v2-btn-primary" style={{ height: 34, padding: '0 14px', fontSize: 13 }}>
          {isNew ? 'Add category' : 'Save changes'}
        </button>
      </div>
    </div>
  )
}

function CatConfirm({ cats, posts, del, onMoveTo, onCancel, onConfirm }: { cats: Category[]; posts: Post[]; del: CatDel; onMoveTo?: (id: string) => void; onCancel?: () => void; onConfirm?: () => void }) {
  const view = deleteView(cats, posts, del.id)
  if (!view) return null
  return (
    <div data-cat-confirm style={{ border: '1px solid var(--status-red-border)', borderRadius: 6, background: 'var(--status-red-bg)', padding: '14px 16px', display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
      <span style={{ flex: '1 1 260px', minWidth: 0, overflowWrap: 'anywhere', fontSize: 13, fontWeight: 600, color: 'var(--status-red-text)' }}>{view.text}</span>
      {view.kind === 'withposts' && (
        <select className="ops-input" value={del.moveTo ?? view.defaultMoveTo} onChange={(e) => onMoveTo?.(e.target.value)} style={{ borderRadius: 6, height: 32, cursor: 'pointer', maxWidth: '100%' }}>
          {view.options.map((o) => (
            <option key={o.id} value={o.id}>
              {o.name}
            </option>
          ))}
        </select>
      )}
      <button type="button" onClick={onCancel} className="v2-btn v2-btn-ghost" style={{ height: 32, padding: '0 12px', fontSize: 12.5, background: 'var(--bg-2)' }}>
        Cancel
      </button>
      {view.kind !== 'only' && (
        <button type="button" onClick={onConfirm} className="v2-btn" style={{ height: 32, padding: '0 12px', fontSize: 12.5, background: 'var(--destructive)', color: 'var(--destructive-foreground)' }}>
          Delete category
        </button>
      )}
    </div>
  )
}

export function BlogCategories({ cats, posts, onMove, onAdd, onEdit, onDelete, form, del, onField, onSubmit, onCancelForm, onMoveTo, onCancelDel, onConfirmDel }: Props) {
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
        {catRows(cats, posts).map((r, i) => (
          <div key={r.cat.id} data-cat-row style={{ borderBottom: '1px solid var(--line-1)' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 16px' }}>
              <span className="mono" style={{ flex: 'none', width: 20, fontSize: 11, color: 'var(--fg-3)' }}>
                {r.num}
              </span>
              <span style={{ flex: 'none', width: 36, height: 36, borderRadius: 6, display: 'grid', placeItems: 'center', background: r.tone.bg, color: r.tone.fg }}>
                <G name={r.icon} size={18} />
              </span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--fg-1)', overflowWrap: 'anywhere' }}>{r.cat.name}</div>
                <div style={{ fontSize: 12, color: 'var(--fg-3)', ...ELLIPSIS }}>{r.cat.headline}</div>
              </div>
              <span data-cat-count className="mono" style={{ flex: 'none', width: 70, fontSize: 11, color: 'var(--fg-3)' }}>
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
            {form?.id === r.cat.id && (
              <div style={PANEL}>
                <CatFormCard form={form} onField={onField} onSubmit={onSubmit} onCancel={onCancelForm} />
              </div>
            )}
            {del?.id === r.cat.id && (
              <div style={PANEL}>
                <CatConfirm cats={cats} posts={posts} del={del} onMoveTo={onMoveTo} onCancel={onCancelDel} onConfirm={onConfirmDel} />
              </div>
            )}
          </div>
        ))}
        {form?.id === null && (
          <div style={{ padding: 16 }}>
            <CatFormCard form={form} onField={onField} onSubmit={onSubmit} onCancel={onCancelForm} />
          </div>
        )}
      </div>
    </>
  )
}
