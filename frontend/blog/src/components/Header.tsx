import { Icon } from '../icons'
import type { Crumb } from '../model'

type HeaderProps = {
  crumbs: Crumb[]
  q: string
  onQ: (q: string) => void
}

export function Header({ crumbs, q, onQ }: HeaderProps) {
  return (
    <header
      id="blog-header"
      style={{
        position: 'sticky',
        top: 0,
        zIndex: 20,
        minHeight: 64,
        display: 'flex',
        flexWrap: 'wrap',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: '8px 24px',
        padding: '8px 40px',
        background: 'var(--header-bg)',
        backdropFilter: 'blur(18px)',
        WebkitBackdropFilter: 'blur(18px)',
        borderBottom: '1px solid var(--header-border)',
      }}
    >
      <nav
        aria-label="Breadcrumb"
        style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 6, minWidth: 0 }}
      >
        {crumbs.map((k, i) => {
          const last = i === crumbs.length - 1
          return (
            <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
              <button
                type="button"
                className="blog-crumb"
                onClick={k.go}
                style={{
                  background: 'none',
                  border: 0,
                  padding: '4px 2px',
                  cursor: 'pointer',
                  fontSize: 13,
                  fontWeight: last ? 700 : 600,
                  color: last ? 'var(--ink)' : 'var(--muted-foreground)',
                  maxWidth: 340,
                  whiteSpace: 'nowrap',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                }}
              >
                {k.label}
              </button>
              {!last && (
                <span style={{ color: 'var(--input)', display: 'inline-flex' }}>
                  <Icon name="chevron-right" size={14} />
                </span>
              )}
            </div>
          )
        })}
      </nav>
      <label
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          height: 38,
          width: 300,
          maxWidth: '100%',
          padding: '0 12px',
          background: 'var(--card)',
          border: '1px solid var(--input)',
          borderRadius: 7,
          color: 'var(--muted-foreground)',
        }}
      >
        <Icon name="search" size={15} />
        <input
          aria-label="Search posts"
          placeholder="Search posts"
          value={q}
          onChange={(e) => onQ(e.target.value)}
          style={{
            flex: 1,
            minWidth: 0,
            border: 0,
            outline: 0,
            background: 'transparent',
            fontSize: '13.5px',
            color: 'var(--ink)',
          }}
        />
        {q.trim() !== '' && (
          <button
            type="button"
            className="blog-clear"
            aria-label="Clear search"
            onClick={() => onQ('')}
            style={{
              background: 'none',
              border: 0,
              padding: 2,
              cursor: 'pointer',
              color: 'var(--muted-foreground)',
              display: 'inline-flex',
            }}
          >
            <Icon name="x" size={14} />
          </button>
        )}
      </label>
    </header>
  )
}
