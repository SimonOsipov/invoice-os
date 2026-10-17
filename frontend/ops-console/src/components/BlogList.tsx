import type { CSSProperties } from 'react'
import { BLOG_HOME, catFilterOptions, listRows, statusChips, subtitle, tabs } from '../blog'
import type { Author, Category, Post } from '../blogApi'
import { GLYPHS, type GlyphName } from '../glyphs'
import { Icon } from '../icons'
import type { BlogFilters } from '../types'

export type BlogState =
  | { status: 'loading' }
  | { status: 'forbidden' }
  | { status: 'error'; message: string }
  | { status: 'ready'; posts: Post[]; cats: Category[]; authors: Author[] }

type Props = {
  state: BlogState
  filters: BlogFilters
  onFilters: (f: BlogFilters) => void
  onRetry: () => void
  onPin: (post: Post) => void
  onNew?: () => void
  onOpen?: (post: Post) => void
}

const GRID = 'minmax(280px, 1fr) 190px 130px 110px 108px 40px'
const ELLIPSIS: CSSProperties = { whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }
const NOTE: CSSProperties = { border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: '14px 16px', fontSize: 13, color: 'var(--fg-3)' }

const G = ({ name, size }: { name: GlyphName; size: number }) => <Icon paths={GLYPHS[name]} size={size} />

function Note({ text, onRetry }: { text: string; onRetry?: () => void }) {
  return (
    <div style={{ ...NOTE, display: 'flex', alignItems: 'center', gap: 12 }}>
      <span>{text}</span>
      {onRetry && (
        <button type="button" onClick={onRetry} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 30 }}>
          Retry
        </button>
      )}
    </div>
  )
}

export function BlogList({ state, filters, onFilters, onRetry, onPin, onNew, onOpen }: Props) {
  const ready = state.status === 'ready' ? state : null
  const set = (patch: Partial<BlogFilters>) => onFilters({ ...filters, ...patch })
  const rows = ready ? listRows(ready.posts, ready.cats, filters) : []
  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24, flexWrap: 'wrap' }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 10 }}>
            07 — CONTENT
          </div>
          <h1 style={{ fontSize: 28, letterSpacing: '-0.04em', margin: 0 }}>Blog</h1>
          <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 6, letterSpacing: '0.03em' }}>
            {ready ? subtitle(ready.posts) : ''}
          </div>
        </div>
        <div style={{ display: 'flex', gap: 10 }}>
          <a
            href={BLOG_HOME}
            target="_blank"
            rel="noopener noreferrer"
            className="v2-btn v2-btn-ghost"
            style={{ height: 36, padding: '0 14px', fontSize: 13, textDecoration: 'none' }}
          >
            <G name="external-link" size={15} />
            View blog
          </a>
          <button type="button" onClick={onNew} className="v2-btn v2-btn-primary" style={{ height: 36, padding: '0 14px', fontSize: 13 }}>
            <G name="plus" size={15} />
            New post
          </button>
        </div>
      </div>
      {state.status === 'loading' && <Note text="Loading posts…" />}
      {state.status === 'forbidden' && <Note text="Your account is not ASComply staff." />}
      {state.status === 'error' && <Note text={state.message} onRetry={onRetry} />}
      {ready && (
        <>
          <div style={{ display: 'flex', gap: 0, borderBottom: '2px solid var(--sage-card-border)', marginBottom: 18 }}>
            {tabs(ready.posts, ready.cats, filters.tab).map((t) => (
              <button
                key={t.key}
                type="button"
                onClick={() => set({ tab: t.key })}
                style={{
                  border: 0,
                  cursor: 'pointer',
                  marginBottom: -2,
                  padding: '10px 16px',
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 8,
                  fontFamily: 'var(--font-sans)',
                  fontSize: 13.5,
                  fontWeight: 700,
                  background: t.active ? 'var(--sage)' : 'transparent',
                  color: t.active ? 'var(--link)' : 'var(--fg-3)',
                  borderBottom: `2px solid ${t.active ? 'var(--link)' : 'transparent'}`,
                  borderRadius: '6px 6px 0 0',
                }}
              >
                <span className="mono" style={{ fontSize: 10, opacity: 0.7 }}>
                  {t.num}
                </span>
                <span>{t.label}</span>
                <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)' }}>
                  {t.count}
                </span>
              </button>
            ))}
          </div>
          {filters.tab === 'posts' && (
            <>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}>
                {statusChips(ready.posts, filters.status).map((c) => (
                  <button
                    key={c.key}
                    type="button"
                    className="ops-chip"
                    onClick={() => set({ status: c.key })}
                    style={{
                      border: `1px solid ${c.active ? 'var(--line-3)' : 'var(--line-1)'}`,
                      background: c.active ? 'var(--bg-3)' : 'var(--bg-2)',
                      color: c.active ? 'var(--fg-1)' : 'var(--fg-3)',
                      borderRadius: 4,
                      height: 30,
                      padding: '0 11px',
                      fontFamily: 'var(--font-mono)',
                      fontSize: 10.5,
                      fontWeight: 600,
                      letterSpacing: '0.04em',
                      display: 'inline-flex',
                      alignItems: 'center',
                      gap: 7,
                    }}
                  >
                    {c.label}
                    <span style={{ fontSize: 10, opacity: 0.7 }}>{c.count}</span>
                  </button>
                ))}
                <select
                  className="ops-input"
                  aria-label="Category"
                  value={filters.cat}
                  onChange={(e) => set({ cat: e.target.value })}
                  style={{ borderRadius: 6, height: 30, cursor: 'pointer' }}
                >
                  {catFilterOptions(ready.cats).map((o) => (
                    <option key={o.id} value={o.id}>
                      {o.name}
                    </option>
                  ))}
                </select>
                <div className="ops-input" style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 9, borderRadius: 6 }}>
                  <span style={{ color: 'var(--fg-3)', display: 'inline-flex' }}>
                    <G name="search" size={15} />
                  </span>
                  <input
                    className="ops-input"
                    aria-label="Search posts"
                    style={{ border: 0, boxShadow: 'none', height: 30, width: 230, padding: 0 }}
                    placeholder="Search titles and summaries…"
                    value={filters.q}
                    onChange={(e) => set({ q: e.target.value })}
                  />
                </div>
              </div>
              <div data-blog-table style={{ border: '1px solid var(--line-1)', borderRadius: 6, overflowX: 'auto', background: 'var(--bg-2)' }}>
                <div
                  style={{ display: 'grid', gridTemplateColumns: GRID, padding: '10px 16px', background: 'var(--bg-3)', borderBottom: '1px solid var(--line-1)', minWidth: 900 }}
                >
                  {['Post', 'Category', 'Author', 'Date', 'Status'].map((l) => (
                    <span key={l} className="label">
                      {l}
                    </span>
                  ))}
                  <span className="label" style={{ textAlign: 'center' }}>
                    Pin
                  </span>
                </div>
                {rows.map((r) => (
                  <div
                    key={r.post.id}
                    className="ops-row"
                    onClick={() => onOpen?.(r.post)}
                    style={{ display: 'grid', gridTemplateColumns: GRID, padding: '12px 16px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: 900 }}
                  >
                    <span style={{ minWidth: 0, paddingRight: 16 }}>
                      <span style={{ display: 'block', fontSize: 13.5, fontWeight: 600, color: 'var(--fg-1)', ...ELLIPSIS }}>{r.title}</span>
                      <span style={{ display: 'block', fontSize: 11.5, color: 'var(--fg-3)', marginTop: 2, ...ELLIPSIS }}>{r.summary}</span>
                    </span>
                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, minWidth: 0, fontSize: 12.5, color: 'var(--fg-2)' }}>
                      <span style={{ flex: 'none', color: 'var(--fg-3)', display: 'inline-flex' }}>
                        <G name={r.catIcon} size={14} />
                      </span>
                      <span style={ELLIPSIS}>{r.catName}</span>
                    </span>
                    <span style={{ fontSize: 12.5, color: 'var(--fg-2)', ...ELLIPSIS }}>{r.author}</span>
                    <span className="mono" style={{ fontSize: 11.5, color: 'var(--fg-2)' }}>
                      {r.date}
                    </span>
                    <span>
                      <span
                        style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: r.badge.bg, border: `1px solid ${r.badge.border}`, borderRadius: 4, padding: '2px 7px' }}
                      >
                        <span style={{ width: 6, height: 6, borderRadius: '50%', background: r.badge.text }} />
                        <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: r.badge.text, letterSpacing: '0.03em' }}>
                          {r.badge.label}
                        </span>
                      </span>
                    </span>
                    <button
                      type="button"
                      title={r.pinTitle}
                      aria-label={r.pinTitle}
                      onClick={(e) => {
                        e.stopPropagation()
                        onPin(r.post)
                      }}
                      style={{
                        justifySelf: 'center',
                        width: 28,
                        height: 28,
                        borderRadius: 6,
                        border: `1px solid ${r.post.featured ? 'var(--primary)' : 'var(--line-2)'}`,
                        background: r.post.featured ? 'var(--primary)' : 'var(--bg-2)',
                        color: r.post.featured ? 'var(--primary-foreground)' : 'var(--fg-4)',
                        display: 'grid',
                        placeItems: 'center',
                        cursor: 'pointer',
                      }}
                    >
                      <G name="pin" size={14} />
                    </button>
                  </div>
                ))}
                {rows.length === 0 && (
                  <div style={{ padding: '40px 16px', textAlign: 'center', display: 'flex', flexDirection: 'column', gap: 6, alignItems: 'center' }}>
                    <span style={{ fontSize: 14, fontWeight: 600 }}>No posts match these filters.</span>
                    <button
                      type="button"
                      onClick={() => set({ q: '', status: 'all', cat: 'all' })}
                      style={{ border: 0, background: 'none', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 700, color: 'var(--link)' }}
                    >
                      Clear filters
                    </button>
                  </div>
                )}
              </div>
              <div style={{ marginTop: 14 }}>
                <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.03em' }}>
                  Pinned post shows first on the blog home · drafts are not public
                </span>
              </div>
            </>
          )}
        </>
      )}
    </div>
  )
}
