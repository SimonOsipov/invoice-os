import type { Category } from '../data'
import { Icon } from '../icons'

type NotFoundProps = { categories: Category[]; onCategory: (id: string) => void }

export function NotFound({ categories, onCategory }: NotFoundProps) {
  return (
    <section
      className="blog-px"
      style={{ padding: '56px 40px 80px', maxWidth: 900, display: 'flex', flexDirection: 'column', gap: 32 }}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <div className="t-eyebrow">Not found</div>
        <h2 className="t-h2" style={{ margin: 0, fontSize: 36 }}>This page is not on the blog.</h2>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <p className="t-body" style={{ margin: 0 }}>Browse a category, or go back to all posts.</p>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          {categories.map((c) => (
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
    </section>
  )
}
