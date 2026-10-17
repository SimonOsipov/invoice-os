import type { ChangeEvent, CSSProperties, ReactNode } from 'react'
import { authorOptions, badgeOf, categoryOptions, coverCss, dekCount, pinHelp, saveState, type Draft, type EditorState } from '../blog'
import type { Author, Category } from '../blogApi'
import { G } from './BlogList'

type Props = {
  editor: EditorState
  cats: Category[]
  authors: Author[]
  coverUrl: string | null
  onField: (patch: Partial<Draft>) => void
  onCover: (file: File | null) => void
  onBack: () => void
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
const GHOST_SM: CSSProperties = { height: 32, padding: '0 12px', fontSize: 12.5 }

export function BlogEditor({ editor, cats, authors, coverUrl, onField, onCover, onBack, actions, preview }: Props) {
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
        </div>

        <div data-bscroll="1" style={{ overflowY: 'auto', background: 'var(--background)', borderLeft: '1px solid var(--line-1)', display: 'flex', flexDirection: 'column' }}>
          {preview}
        </div>
      </div>
    </div>
  )
}
