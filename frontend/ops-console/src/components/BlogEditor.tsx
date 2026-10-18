import type { ChangeEvent, CSSProperties, ReactNode } from 'react'
import { ADD_TYPES, authorOptions, badgeOf, blockCount, blockView, categoryOptions, coverCss, DEMOS, dekCount, NOTE_KINDS, pinHelp, saveState, type BlockType, type Draft, type DraftBlock, type EditorState } from '../blog'
import type { Author, Block, Category } from '../blogApi'
import { G } from './BlogList'

type Props = {
  editor: EditorState
  cats: Category[]
  authors: Author[]
  coverUrl: string | null
  onField: (patch: Partial<Draft>) => void
  onCover: (file: File | null) => void
  onBack: () => void
  onAdd: (type: BlockType) => void
  onMove: (i: number, d: number) => void
  onRemove: (i: number) => void
  onPatch: (i: number, patch: Partial<Block>) => void
  actions?: ReactNode
  preview?: ReactNode
}

const LABEL: CSSProperties = { fontSize: 10, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--muted-foreground)' }
const COLUMN: CSSProperties = { display: 'flex', flexDirection: 'column', gap: 6 }
const ERROR: CSSProperties = { fontSize: 12, fontWeight: 600, color: 'var(--destructive)' }
const field = (bad: boolean): CSSProperties => ({
  width: '100%',
  fontFamily: 'var(--font-sans)',
  fontSize: 13.5,
  color: 'var(--fg-1)',
  background: 'var(--bg-2)',
  borderRadius: 6,
  outline: 'none',
  border: `1px solid ${bad ? 'var(--destructive)' : 'var(--input)'}`,
})
const HINT: CSSProperties = { fontSize: 11.5, color: 'var(--fg-3)' }
const TEXTAREA: CSSProperties = { ...field(false), padding: '10px 12px', resize: 'vertical', lineHeight: 1.6 }
const ICON_BTN: CSSProperties = { width: 26, height: 26, border: 0, borderRadius: 5, background: 'transparent', display: 'grid', placeItems: 'center', cursor: 'pointer' }
const SEGMENT: CSSProperties = { display: 'flex', padding: 3, gap: 2, background: 'var(--sage)', border: '1px solid var(--sage-card-border)', borderRadius: 6 }
const GHOST_SM: CSSProperties = { height: 32, padding: '0 12px', fontSize: 12.5 }

export function BlogEditor({ editor, cats, authors, coverUrl, onField, onCover, onBack, onAdd, onMove, onRemove, onPatch, actions, preview }: Props) {
  const { draft, errors } = editor
  const badge = badgeOf(draft.status)
  const save = saveState(editor.dirty, draft.id)
  const dek = dekCount(draft.summary)
  const hasCover = draft.cover.kind !== 'none'
  const pick = (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0] ?? null
    e.target.value = ''
    onCover(file)
  }
  return (
    <div data-blog-editor style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <div
        style={{ flex: 'none', display: 'flex', alignItems: 'center', gap: 12, padding: '10px 22px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)', flexWrap: 'wrap', minHeight: 56 }}
      >
        <button
          type="button"
          onClick={onBack}
          className="blog-back"
          style={{ display: 'inline-flex', alignItems: 'center', gap: 6, border: 0, background: 'none', cursor: 'pointer', padding: 0, fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 700, color: 'var(--link)' }}
        >
          <G name="chevron-left" size={15} />
          All posts
        </button>
        <span style={{ width: 1, height: 20, background: 'var(--line-2)' }} />
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: badge.bg, border: `1px solid ${badge.border}`, borderRadius: 4, padding: '2px 7px' }}>
          <span style={{ width: 6, height: 6, borderRadius: '50%', background: badge.text }} />
          <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: badge.text, letterSpacing: '0.03em' }}>
            {badge.label}
          </span>
        </span>
        <span className="mono" style={{ fontSize: 11, color: save.color }}>
          {save.label}
        </span>
        <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>{actions}</div>
      </div>

      <div style={{ flex: 1, minHeight: 0, display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1fr)' }}>
        <div data-bscroll="1" style={{ overflowY: 'auto', padding: '22px 24px 64px', display: 'flex', flexDirection: 'column', gap: 18, background: 'var(--bg-1)' }}>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 6, background: 'var(--bg-2)', padding: '18px 18px 20px', display: 'flex', flexDirection: 'column', gap: 16 }}>
            <div className="label">Post details</div>
            <label style={COLUMN}>
              <span style={LABEL}>Title</span>
              <input
                value={draft.title}
                onChange={(e) => onField({ title: e.target.value })}
                placeholder="What structured e-invoicing changes for…"
                style={{ ...field(!!errors.title), height: 46, padding: '0 12px', fontSize: 18, fontWeight: 700, letterSpacing: '-0.02em' }}
              />
              {errors.title && <span style={ERROR}>{errors.title}</span>}
            </label>
            <label style={COLUMN}>
              <span style={{ display: 'flex', justifyContent: 'space-between' }}>
                <span style={LABEL}>Summary</span>
                <span className="mono" style={{ fontSize: 10, color: dek.over220 ? 'var(--destructive)' : 'var(--fg-3)' }}>
                  {dek.text}
                </span>
              </span>
              <textarea
                value={draft.summary}
                onChange={(e) => onField({ summary: e.target.value })}
                rows={2}
                placeholder="One or two sentences. Shown on cards and under the title."
                style={{ ...field(!!errors.summary), padding: '10px 12px', resize: 'vertical', lineHeight: 1.5 }}
              />
              {errors.summary && <span style={ERROR}>{errors.summary}</span>}
            </label>
            <div data-blog-fields style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))', gap: 12 }}>
              <label style={COLUMN}>
                <span style={LABEL}>Category</span>
                <select
                  value={draft.category_id ?? ''}
                  onChange={(e) => onField({ category_id: e.target.value || null })}
                  style={{ ...field(!!errors.category), height: 38, padding: '0 10px', cursor: 'pointer' }}
                >
                  {categoryOptions(cats, draft).map((o) => (
                    <option key={o.value} value={o.value}>
                      {o.label}
                    </option>
                  ))}
                </select>
                {errors.category && <span style={ERROR}>{errors.category}</span>}
              </label>
              <label style={COLUMN}>
                <span style={LABEL}>Author</span>
                <select
                  value={draft.author_id ?? ''}
                  onChange={(e) => onField({ author_id: e.target.value || null })}
                  style={{ ...field(!!errors.author), height: 38, padding: '0 10px', cursor: 'pointer' }}
                >
                  {authorOptions(authors, draft).map((o) => (
                    <option key={o.value} value={o.value}>
                      {o.label}
                    </option>
                  ))}
                </select>
                {errors.author && <span style={ERROR}>{errors.author}</span>}
              </label>
              <label style={COLUMN}>
                <span style={LABEL}>Publish date</span>
                <input
                  type="date"
                  value={draft.publish_date ?? ''}
                  onChange={(e) => onField({ publish_date: e.target.value || null })}
                  style={{ ...field(!!errors.date), height: 38, padding: '0 10px' }}
                />
                {errors.date && <span style={ERROR}>{errors.date}</span>}
              </label>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              <span style={LABEL}>Cover image</span>
              {hasCover ? (
                <div style={{ display: 'flex', alignItems: 'center', gap: 14 }}>
                  <div
                    role="img"
                    aria-label="Cover image"
                    style={{ width: 160, height: 90, borderRadius: 6, border: '1px solid var(--line-1)', backgroundImage: coverCss(draft.cover, coverUrl), backgroundSize: 'cover', backgroundPosition: 'center' }}
                  />
                  <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                    <label className="v2-btn v2-btn-ghost" style={{ ...GHOST_SM, cursor: 'pointer' }}>
                      Replace
                      <input type="file" accept="image/*" onChange={pick} style={{ display: 'none' }} />
                    </label>
                    <button type="button" onClick={() => onField({ cover: { kind: 'none' } })} className="v2-btn v2-btn-ghost" style={{ ...GHOST_SM, color: 'var(--destructive)' }}>
                      Remove
                    </button>
                  </div>
                </div>
              ) : (
                <label
                  className="blog-drop"
                  style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '14px 16px', border: '1px dashed var(--line-3)', borderRadius: 6, background: 'var(--bg-1)', cursor: 'pointer' }}
                >
                  <span style={{ flex: 'none', width: 36, height: 36, borderRadius: 6, display: 'grid', placeItems: 'center', background: 'var(--sage)', color: 'var(--primary)' }}>
                    <G name="image" size={18} />
                  </span>
                  <span style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                    <span style={{ fontSize: 13, fontWeight: 700, color: 'var(--fg-1)' }}>Upload a cover image</span>
                    <span style={{ fontSize: 12, color: 'var(--fg-3)' }}>JPG or PNG, landscape. Without one, the category placeholder is used.</span>
                  </span>
                  <input type="file" accept="image/*" onChange={pick} style={{ display: 'none' }} />
                </label>
              )}
            </div>
            <button
              type="button"
              role="switch"
              aria-checked={draft.featured}
              onClick={() => onField({ featured: !draft.featured })}
              style={{ display: 'flex', alignItems: 'center', gap: 12, textAlign: 'left', border: '1px solid var(--line-1)', borderRadius: 6, background: 'var(--bg-1)', padding: '12px 14px', cursor: 'pointer', fontFamily: 'var(--font-sans)' }}
            >
              <span style={{ flex: 'none', width: 38, height: 22, borderRadius: 11, background: draft.featured ? 'var(--primary)' : 'var(--input)', position: 'relative', transition: 'background 150ms' }}>
                <span
                  style={{
                    position: 'absolute',
                    top: 3,
                    left: 3,
                    width: 16,
                    height: 16,
                    borderRadius: '50%',
                    background: 'var(--primary-foreground)',
                    transform: `translateX(${draft.featured ? 16 : 0}px)`,
                    transition: 'transform 150ms var(--ease-out)',
                  }}
                />
              </span>
              <span style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                <span style={{ fontSize: 13, fontWeight: 700, color: 'var(--fg-1)' }}>Pin as featured post</span>
                <span style={{ fontSize: 12, color: 'var(--fg-3)' }}>{pinHelp(draft.featured)}</span>
              </span>
            </button>
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
              <span className="label">Content</span>
              <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)' }}>
                {blockCount(draft.body.length)}
              </span>
            </div>
            {errors.body && <span style={ERROR}>{errors.body}</span>}
            {draft.body.map((b, i) => (
              <BlockCard key={b.u} block={b} i={i} n={draft.body.length} onMove={onMove} onRemove={onRemove} onPatch={onPatch} />
            ))}
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', padding: '12px 14px', border: '1px dashed var(--line-3)', borderRadius: 6 }}>
              <span className="label" style={{ marginRight: 6 }}>
                Add block
              </span>
              {ADD_TYPES.map((t) => (
                <button key={t.type} type="button" onClick={() => onAdd(t.type)} className="v2-btn v2-btn-ghost" style={{ height: 30, padding: '0 10px', fontSize: 12.5, gap: 5, background: 'var(--bg-2)' }}>
                  <G name="plus" size={13} />
                  {t.label}
                </button>
              ))}
            </div>
          </div>
        </div>

        <div data-bscroll="1" style={{ overflowY: 'auto', background: 'var(--background)', borderLeft: '1px solid var(--line-1)', display: 'flex', flexDirection: 'column' }}>
          {preview}
        </div>
      </div>
    </div>
  )
}

type CardProps = {
  block: DraftBlock
  i: number
  n: number
  onMove: Props['onMove']
  onRemove: Props['onRemove']
  onPatch: Props['onPatch']
}

function BlockCard({ block: b, i, n, onMove, onRemove, onPatch }: CardProps) {
  const v = blockView(b, i, n)
  const text = (rows: number, placeholder: string, big = true) => (
    <textarea
      value={b.text ?? ''}
      onChange={(e) => onPatch(i, { text: e.target.value })}
      rows={rows}
      placeholder={placeholder}
      style={{ ...TEXTAREA, fontSize: big ? 14 : 13.5 }}
    />
  )
  return (
    <div style={{ border: '1px solid var(--line-1)', borderRadius: 6, background: 'var(--bg-2)', overflow: 'hidden', animation: 'opsPop 200ms var(--ease-out)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 10px 8px 14px', background: 'var(--bg-3)', borderBottom: '1px solid var(--line-1)' }}>
        <span className="mono" style={{ fontSize: 10, color: 'var(--fg-4)' }}>
          {v.num}
        </span>
        <span className="mono" style={{ fontSize: 10.5, fontWeight: 700, letterSpacing: '0.06em', color: 'var(--fg-2)' }}>
          {v.typeLabel}
        </span>
        <div style={{ marginLeft: 'auto', display: 'flex', gap: 2 }}>
          <button type="button" aria-label="Move up" onClick={() => onMove(i, -1)} className="blog-icon-btn" style={{ ...ICON_BTN, color: 'var(--fg-3)', opacity: v.upOpacity }}>
            <G name="arrow-up" size={14} />
          </button>
          <button type="button" aria-label="Move down" onClick={() => onMove(i, 1)} className="blog-icon-btn" style={{ ...ICON_BTN, color: 'var(--fg-3)', opacity: v.downOpacity }}>
            <G name="arrow-down" size={14} />
          </button>
          <button type="button" aria-label="Remove block" onClick={() => onRemove(i)} className="blog-icon-btn blog-icon-btn-danger" style={{ ...ICON_BTN, color: 'var(--destructive)' }}>
            <G name="trash-2" size={14} />
          </button>
        </div>
      </div>
      <div style={{ padding: '12px 14px 14px', display: 'flex', flexDirection: 'column', gap: 10 }}>
        {b.type === 'p' && text(4, 'Write a paragraph…')}
        {b.type === 'h' && (
          <>
            <input
              value={b.text ?? ''}
              onChange={(e) => onPatch(i, { text: e.target.value })}
              placeholder="Section heading"
              style={{ ...field(false), height: 40, padding: '0 12px', fontSize: 16, fontWeight: 700, letterSpacing: '-0.02em' }}
            />
            <span style={HINT}>Headings are listed in the article's table of contents.</span>
          </>
        )}
        {b.type === 'ul' && (
          <>
            <textarea
              value={(b.items ?? []).join('\n')}
              onChange={(e) => onPatch(i, { items: e.target.value.split('\n') })}
              rows={4}
              placeholder="One item per line"
              style={{ ...TEXTAREA, fontSize: 14 }}
            />
            <span style={HINT}>One item per line. Each item gets a check mark.</span>
          </>
        )}
        {b.type === 'note' && (
          <>
            <div style={{ ...SEGMENT, alignSelf: 'flex-start' }}>
              {NOTE_KINDS.map((k) => {
                const on = (b.kind || 'regulation') === k.kind
                return (
                  <button
                    key={k.kind}
                    type="button"
                    onClick={() => onPatch(i, { kind: k.kind })}
                    style={{ height: 28, padding: '0 12px', border: 0, borderRadius: 4, cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 12, fontWeight: 700, background: on ? 'var(--primary)' : 'transparent', color: on ? 'var(--primary-foreground)' : 'var(--tab-active-text)' }}
                  >
                    {k.label}
                  </button>
                )
              })}
            </div>
            <input value={b.title ?? ''} onChange={(e) => onPatch(i, { title: e.target.value })} placeholder="Callout title" style={{ ...field(false), height: 38, padding: '0 12px', fontWeight: 700 }} />
            {text(2, 'Callout text', false)}
          </>
        )}
        {b.type === 'quote' && (
          <>
            {text(2, 'Quote, without quotation marks')}
            <input value={b.who ?? ''} onChange={(e) => onPatch(i, { who: e.target.value })} placeholder="Name, role, company" style={{ ...field(false), height: 38, padding: '0 12px' }} />
          </>
        )}
        {b.type === 'demo' && (
          <>
            <select value={b.demo ?? 'import'} onChange={(e) => onPatch(i, { demo: e.target.value })} style={{ ...field(false), height: 38, padding: '0 10px', cursor: 'pointer' }}>
              {DEMOS.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.title}
                </option>
              ))}
            </select>
            <span style={HINT}>Plays the Feature Library demo inside the article.</span>
          </>
        )}
      </div>
    </div>
  )
}
