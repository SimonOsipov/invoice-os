import { Icon } from '../icons'
import { trackBlogDemoOpen } from '../analytics'
import { demoHref } from '../links'
import type { BlogData } from '../data'
import { postsOf } from '../model'
import { Button } from './Button'
import { Logo } from './Logo'

type SidebarProps = {
  data: BlogData | null
  homeActive: boolean
  activeCat: string | null
  onHome: () => void
  onCategory: (id: string) => void
  onSubscribe: () => void
  onCookieChoices: () => void
}

export function Sidebar({ data, homeActive, activeCat, onHome, onCategory, onSubscribe, onCookieChoices }: SidebarProps) {
  return (
    <aside
      className="asc-dark"
      style={{
        width: 288,
        flex: 'none',
        background: 'var(--surface)',
        borderRight: '1px solid var(--surface-panel-border)',
        display: 'flex',
        flexDirection: 'column',
      }}
    >
      <div
        style={{
          padding: '18px 16px 16px',
          borderBottom: '1px solid var(--line-1)',
          display: 'flex',
          flexDirection: 'column',
          gap: 16,
        }}
      >
        <button
          type="button"
          onClick={onHome}
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 9,
            color: 'var(--fg-1)',
            background: 'none',
            border: 0,
            padding: 0,
            cursor: 'pointer',
            textAlign: 'left',
          }}
        >
          <Logo />
          <span
            className="mono"
            style={{
              fontSize: 9,
              fontWeight: 600,
              letterSpacing: '0.07em',
              color: 'var(--action)',
              border: '1px solid var(--action)',
              borderRadius: 4,
              padding: '1px 5px',
            }}
          >
            BLOG
          </span>
        </button>
        <button
          type="button"
          className="blog-sub"
          onClick={onSubscribe}
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            gap: 10,
            width: '100%',
            height: 42,
            border: 0,
            borderRadius: 7,
            background: 'var(--accent)',
            color: 'var(--accent-foreground)',
            fontSize: 14,
            fontWeight: 700,
            cursor: 'pointer',
          }}
        >
          <Icon name="mail" size={15} />
          <span>Subscribe</span>
        </button>
      </div>
      <nav
        style={{
          flex: '1 1 0',
          minHeight: 0,
          overflowY: 'auto',
          padding: '12px 10px',
          display: 'flex',
          flexDirection: 'column',
          gap: 2,
        }}
      >
        <button
          type="button"
          className="blog-nav"
          aria-current={homeActive ? 'true' : undefined}
          onClick={onHome}
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 11,
            width: '100%',
            border: 0,
            cursor: 'pointer',
            borderRadius: 6,
            padding: '9px 10px',
            textAlign: 'left',
            fontSize: '13.5px',
            fontWeight: 600,
            background: homeActive ? 'var(--surface-panel)' : 'transparent',
            color: homeActive ? 'var(--fg-1)' : 'var(--fg-3)',
          }}
        >
          <Icon name="layout-dashboard" size={17} />
          <span style={{ flex: 1 }}>All posts</span>
          {data && (
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-4)' }}>
              {data.posts.length}
            </span>
          )}
        </button>
        <div
          className="t-meta blog-navlabel"
          style={{ padding: '16px 10px 8px', color: 'var(--fg-4)', fontSize: 10, letterSpacing: '0.1em' }}
        >
          CATEGORIES
        </div>
        {data?.categories.map((c) => {
          const active = activeCat === c.id
          return (
            <button
              key={c.id}
              type="button"
              className="blog-nav"
              aria-current={active ? 'true' : undefined}
              onClick={() => onCategory(c.id)}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 11,
                width: '100%',
                border: 0,
                cursor: 'pointer',
                borderRadius: 6,
                padding: '9px 10px',
                textAlign: 'left',
                fontSize: '13.5px',
                fontWeight: active ? 700 : 600,
                background: active ? 'var(--surface-panel)' : 'transparent',
                color: active ? 'var(--fg-1)' : 'var(--fg-3)',
              }}
            >
              <Icon name={c.icon} size={17} />
              <span style={{ flex: 1 }}>{c.name}</span>
              <span className="mono" style={{ fontSize: 11, color: 'var(--fg-4)' }}>
                {postsOf(data, c.id).length}
              </span>
            </button>
          )
        })}
      </nav>
      <div style={{ flex: 'none', padding: '14px 16px 16px', borderTop: '1px solid var(--line-1)' }}>
        <Button variant="outlineDark" size="sm" href={demoHref()} onClick={trackBlogDemoOpen} style={{ width: '100%' }}>
          Book the Demo
        </Button>
        <button
          type="button"
          className="blog-cookie-choices"
          onClick={onCookieChoices}
          style={{
            display: 'block',
            marginTop: 12,
            background: 'none',
            border: 0,
            padding: 0,
            cursor: 'pointer',
            fontSize: 13,
            color: 'var(--fg-3)',
          }}
        >
          Cookie choices
        </button>
      </div>
    </aside>
  )
}
