import type { BlogData } from '../data'
import { Icon } from '../icons'
import { catOf, fmtDate, resultsTitle, searchPosts } from '../model'

type SearchResultsProps = {
  data: BlogData
  q: string
  onPost: (slug: string) => void
  onCategory: (id: string) => void
}

export function SearchResults({ data, q, onPost, onCategory }: SearchResultsProps) {
  const hits = searchPosts(data, q)
  return (
    <section
      className="blog-px"
      style={{ padding: '56px 40px 80px', maxWidth: 900, display: 'flex', flexDirection: 'column', gap: 32 }}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <div className="t-eyebrow">Search</div>
        <h2 className="t-h2" style={{ margin: 0, fontSize: 36 }}>{resultsTitle(hits.length, q)}</h2>
      </div>
      {hits.length > 0 ? (
        <div style={{ display: 'flex', flexDirection: 'column', borderBottom: '1px solid var(--border)' }}>
          {hits.map((p) => (
            <button
              key={p.id}
              type="button"
              className="blog-row"
              onClick={() => onPost(p.id)}
              style={{
                display: 'grid',
                gridTemplateColumns: '110px minmax(0, 1fr)',
                gap: 24,
                alignItems: 'baseline',
                textAlign: 'left',
                cursor: 'pointer',
                padding: '22px 4px',
                background: 'transparent',
                border: 0,
                borderTop: '1px solid var(--border)',
                color: 'var(--ink)',
              }}
            >
              <span className="mono" style={{ fontSize: 12, color: 'var(--muted-foreground)' }}>{fmtDate(p.publish_date)}</span>
              <span style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                <span className="t-step">{catOf(data, p.category_id)?.name ?? ''}</span>
                <span style={{ fontSize: 19, fontWeight: 700, letterSpacing: '-0.02em', lineHeight: 1.3 }}>{p.title}</span>
                <span className="t-body-sm">{p.summary}</span>
              </span>
            </button>
          ))}
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <p className="t-body" style={{ margin: 0 }}>Try a shorter search, or browse a category.</p>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
            {data.categories.map((c) => (
              <button
                key={c.id}
                type="button"
                className="blog-chip"
                onClick={() => onCategory(c.id)}
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 8,
                  height: 36,
                  padding: '0 14px',
                  border: '1px solid var(--border)',
                  borderRadius: 7,
                  background: 'var(--card)',
                  color: 'var(--ink)',
                  fontSize: 13,
                  fontWeight: 600,
                  cursor: 'pointer',
                }}
              >
                <Icon name={c.icon} size={15} />
                <span>{c.name}</span>
              </button>
            ))}
          </div>
        </div>
      )}
    </section>
  )
}
