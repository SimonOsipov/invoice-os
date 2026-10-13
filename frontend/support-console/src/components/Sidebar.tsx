import type { CSSProperties } from 'react'
import { BrandMark, GLOBE_ICON, SIGN_OUT_ICON } from '../icons'
import { NAV_ITEMS } from '../nav'
import { signOut } from '../auth'
import type { Screen } from '../types'

type Props = {
  screen: Screen
  onNavigate: (s: Screen) => void
}

const navBtnStyle = (active: boolean): CSSProperties => ({
  display: 'flex',
  alignItems: 'center',
  gap: 11,
  width: '100%',
  border: 0,
  cursor: 'pointer',
  borderRadius: 'var(--radius-md)',
  padding: '9px 10px',
  textAlign: 'left',
  fontFamily: 'var(--font-sans)',
  fontSize: 13.5,
  fontWeight: active ? 600 : 500,
  background: active ? 'var(--bg-3)' : 'transparent',
  color: active ? 'var(--fg-1)' : 'var(--fg-2)',
  position: 'relative',
})

export function Sidebar({ screen, onNavigate }: Props) {
  return (
    <aside
      className="ops-sidebar asc-dark"
      style={{ width: 244, flex: 'none', background: 'var(--surface)', borderRight: '1px solid var(--surface-panel-border)', display: 'flex', flexDirection: 'column' }}
    >
      <div style={{ padding: '16px 16px 14px', borderBottom: '1px solid var(--line-1)' }}>
        <a href="#" style={{ display: 'flex', alignItems: 'center', gap: 9, color: 'var(--fg-1)', marginBottom: 14 }}>
          <BrandMark />
          <span className="ops-nav-label" style={{ fontWeight: 600, fontSize: 15, letterSpacing: '-0.02em' }}>
            ASComply
          </span>
          <span
            className="mono ops-nav-label"
            style={{ fontSize: 9, fontWeight: 600, letterSpacing: '0.07em', color: 'var(--action)', border: '1px solid var(--action)', borderRadius: 'var(--radius-sm)', padding: '1px 5px' }}
          >
            SUPPORT
          </span>
        </a>
        {/* Cross-tenant indicator (proto:51). Same card chrome as the Platform app's
            workspace card and the developer console's org switcher — one component across
            four apps — but deliberately NOT a control: there is nothing to switch, because
            this console is never scoped to a tenant. The globe and the mono caption carry
            that difference, not a different card.

            The prototype tinted the whole card and filled the tile solid; that made the
            same slot read as a different component in each app, which is the drift this
            resolves. Scope is asserted by the environment banner (TopBar), which is amber
            in sandbox and RED in live — the sidebar does not need to shout it too. */}
        <div
          className="ops-hide-narrow"
          style={{ display: 'flex', alignItems: 'center', gap: 10, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', padding: '8px 10px' }}
        >
          <span
            style={{ flex: 'none', width: 28, height: 28, borderRadius: 'var(--radius-md)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center' }}
          >
            {GLOBE_ICON}
          </span>
          <span style={{ flex: 1, minWidth: 0 }}>
            <span style={{ display: 'block', fontSize: 13, fontWeight: 600, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>All tenants</span>
            <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              CROSS-TENANT VIEW
            </span>
          </span>
        </div>
      </div>

      <nav style={{ flex: 1, padding: '12px 10px', display: 'flex', flexDirection: 'column', gap: 2 }}>
        <div className="label ops-nav-label" style={{ padding: '6px 8px 8px', color: 'var(--eyebrow-on-dark)' }}>
          Operations
        </div>
        {NAV_ITEMS.map((n) => {
          const active = screen === n.key
          return (
            <button key={n.key} type="button" onClick={() => onNavigate(n.key)} className="ops-nav" style={navBtnStyle(active)}>
              <span style={{ position: 'absolute', left: 0, top: 7, bottom: 7, width: 2, borderRadius: 2, background: active ? 'var(--action)' : 'transparent' }} />
              <span style={{ color: active ? 'var(--action)' : 'var(--fg-3)', display: 'inline-flex' }}>{n.glyph}</span>
              <span className="ops-nav-label" style={{ flex: 1 }}>
                {n.label}
              </span>
            </button>
          )
        })}
      </nav>

      <div className="ops-hide-narrow" style={{ padding: 12, borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 10 }}>
        {/* Sign out: clears the session, then leaves (signOutConsole). */}
        <button
          type="button"
          onClick={signOut}
          className="ops-btn ops-hide-narrow"
          aria-label="Sign out"
          title="Sign out"
          style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: 28, height: 28, padding: 0, border: 0, borderRadius: 'var(--radius-btn)', background: 'transparent', color: 'var(--fg-3)', cursor: 'pointer' }}
        >
          {SIGN_OUT_ICON}
        </button>
      </div>
    </aside>
  )
}
