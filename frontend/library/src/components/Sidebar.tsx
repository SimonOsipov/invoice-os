import { GROUPS } from '../content'
import { trackLibraryDemoOpen } from '../analytics'
import { Icon, type GlyphName } from '../icons'
import type { Route } from '../route'
import type { Feature, Group } from '../types'
import { Button } from './Button'
import { ComingSoonPill } from './ComingSoonPill'
import { Logo } from './Logo'

type SidebarProps = {
  route: Route
  demoHref: string | null
  onHome: () => void
  onGroup: (g: Group) => void
  onFeature: (f: Feature) => void
  onTour: (() => void) | null
  tourLabel?: string
  onCookieChoices: () => void
}

const navButton = {
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
} as const

export function Sidebar({
  route,
  demoHref,
  onHome,
  onGroup,
  onFeature,
  onTour,
  tourLabel = 'Take the tour',
  onCookieChoices,
}: SidebarProps) {
  const home = route.view === 'home'
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
            LIBRARY
          </span>
        </button>
        {onTour !== null && (
          <button
            type="button"
            className="lib-tour"
            onClick={onTour}
            style={{
              position: 'relative',
              zIndex: 61,
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
            <Icon name="play" size={15} />
            <span>{tourLabel}</span>
          </button>
        )}
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
          className="lib-nav"
          onClick={onHome}
          aria-current={home ? 'true' : undefined}
          style={{
            ...navButton,
            fontWeight: 600,
            background: home ? 'var(--surface-panel)' : 'transparent',
            color: home ? 'var(--fg-1)' : 'var(--fg-3)',
          }}
        >
          <Icon name="layout-dashboard" size={17} />
          <span style={{ flex: 1 }}>Overview</span>
        </button>
        <div className="t-meta lib-navlabel" style={{ padding: '16px 10px 8px', color: 'var(--fg-4)', fontSize: 10, letterSpacing: '0.1em' }}>
          FEATURE GROUPS
        </div>
        {GROUPS.map((g) => {
          const open = route.view !== 'home' && route.group.id === g.id
          return (
            <div key={g.id} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              <button
                type="button"
                id={`nav-${g.id}`}
                className="lib-nav"
                aria-current={open ? 'true' : undefined}
                onClick={() => onGroup(g)}
                style={{
                  ...navButton,
                  fontWeight: open ? 700 : 600,
                  background: open ? 'var(--surface-panel)' : 'transparent',
                  color: open ? 'var(--fg-1)' : 'var(--fg-3)',
                }}
              >
                <Icon name={g.icon as GlyphName} size={17} />
                <span style={{ flex: 1 }}>{g.name}</span>
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-4)' }}>
                  {g.feats.length}
                </span>
              </button>
              {open && (
                <div
                  className="lib-subnav"
                  style={{
                    display: 'flex',
                    flexDirection: 'column',
                    gap: 1,
                    margin: '2px 0 6px 20px',
                    paddingLeft: 12,
                    borderLeft: '1px solid var(--line-2)',
                  }}
                >
                  {g.feats.map((f) => {
                    const active = route.view === 'feature' && route.feature.id === f.id
                    return (
                      <button
                        type="button"
                        key={f.id}
                        className="lib-nav"
                        aria-current={active ? 'true' : undefined}
                        onClick={() => onFeature(f)}
                        style={{
                          textAlign: 'left',
                          border: 0,
                          cursor: 'pointer',
                          borderRadius: 5,
                          padding: '6px 9px',
                          fontSize: '12.5px',
                          lineHeight: 1.35,
                          fontWeight: active ? 700 : 500,
                          background: active ? 'var(--on-dark-10)' : 'transparent',
                          color: active ? 'var(--accent)' : 'var(--fg-3)',
                          ...(f.status === 'soon' && {
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'space-between',
                            gap: 8,
                          }),
                        }}
                      >
                        {f.status === 'soon' ? (
                          <>
                            <span style={{ flex: '1 1 auto', minWidth: 0 }}>{f.title}</span>
                            <ComingSoonPill tone="dark" style={{ flex: 'none' }} />
                          </>
                        ) : (
                          f.title
                        )}
                      </button>
                    )
                  })}
                </div>
              )}
            </div>
          )
        })}
      </nav>
      <div style={{ flex: 'none', padding: '14px 16px 16px', borderTop: '1px solid var(--line-1)' }}>
        {demoHref !== null && (
          <Button variant="outlineDark" size="sm" href={demoHref} onClick={trackLibraryDemoOpen} style={{ width: '100%' }}>
            Book the Demo
          </Button>
        )}
        <button
          type="button"
          className="lib-cookie-choices"
          onClick={onCookieChoices}
          style={{
            display: 'block',
            marginTop: demoHref !== null ? 12 : 0,
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
