import type { CSSProperties } from 'react'
import { BrandMark } from '../icons'
import { GLOBE_ICON, NAV_ITEMS, SIGN_OUT_ICON } from '../data'
import { landingBase } from '../auth'
import { clearSupportSession } from '../session'
import type { Screen } from '../types'

type Props = {
  screen: Screen
  onNavigate: (s: Screen) => void
  deadLetterCount: number
}

const navBtnStyle = (active: boolean): CSSProperties => ({
  display: 'flex',
  alignItems: 'center',
  gap: 11,
  width: '100%',
  border: 0,
  cursor: 'pointer',
  borderRadius: 'var(--radius-sm)',
  padding: '9px 10px',
  textAlign: 'left',
  fontFamily: 'var(--font-sans)',
  fontSize: 13.5,
  fontWeight: active ? 600 : 500,
  background: active ? 'var(--bg-3)' : 'transparent',
  color: active ? 'var(--fg-1)' : 'var(--fg-2)',
  position: 'relative',
})

export function Sidebar({ screen, onNavigate, deadLetterCount }: Props) {
  return (
    <aside
      className="ops-sidebar"
      style={{ width: 244, flex: 'none', background: 'var(--bg-2)', borderRight: '1px solid var(--line-1)', display: 'flex', flexDirection: 'column' }}
    >
      <div style={{ padding: '16px 16px 14px', borderBottom: '1px solid var(--line-1)' }}>
        <a href="#" style={{ display: 'flex', alignItems: 'center', gap: 9, color: 'var(--fg-1)', marginBottom: 14 }}>
          <BrandMark size={20} />
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
          style={{ display: 'flex', alignItems: 'center', gap: 10, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', padding: '8px 10px' }}
        >
          <span
            style={{ flex: 'none', width: 28, height: 28, borderRadius: 'var(--radius-sm)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center' }}
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
        <div className="label ops-nav-label" style={{ padding: '6px 8px 8px' }}>
          Operations
        </div>
        {NAV_ITEMS.map((n) => {
          const active = screen === n.key
          // proto:821-822 — two badges: the live dead-letter count (red) and the
          // learned-rules inbox (amber). Both are derived, never hardcoded here.
          const badge = n.key === 'submissions' && deadLetterCount ? String(deadLetterCount) : n.key === 'rules' ? '3' : ''
          const badgeRed = n.key === 'submissions'
          return (
            <button key={n.key} type="button" onClick={() => onNavigate(n.key)} className="ops-nav" style={navBtnStyle(active)}>
              <span style={{ position: 'absolute', left: 0, top: 7, bottom: 7, width: 2, borderRadius: 'var(--radius-xs)', background: active ? 'var(--action)' : 'transparent' }} />
              <span style={{ color: active ? 'var(--action)' : 'var(--fg-3)', display: 'inline-flex' }}>{n.glyph}</span>
              <span className="ops-nav-label" style={{ flex: 1 }}>
                {n.label}
              </span>
              {badge && (
                <span
                  className="mono ops-nav-label"
                  style={{
                    fontSize: 10,
                    fontWeight: 700,
                    background: badgeRed ? 'var(--status-red-bg)' : 'var(--action-tint)',
                    color: badgeRed ? 'var(--status-red-text)' : 'var(--action)',
                    borderRadius: 99,
                    padding: '1px 6px',
                  }}
                >
                  {badge}
                </span>
              )}
            </button>
          )
        })}
        {/* APP backpressure meter (proto:69-78). The developer console's equivalent slot
            holds a request quota; here it is the shared Access Point rate budget, which is
            a cross-tenant fact by construction. */}
        <div className="ops-hide-narrow" style={{ marginTop: 'auto', padding: '12px 8px 4px' }}>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-input)', padding: '11px 12px', background: 'var(--bg-1)' }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
              <span className="label">APP backpressure</span>
              <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: 'var(--status-amber-text)' }}>
                82%
              </span>
            </div>
            <div style={{ height: 5, background: 'var(--bg-3)', borderRadius: 'var(--radius-sm)', overflow: 'hidden' }}>
              <div style={{ width: '82%', height: '100%', background: 'var(--status-amber-text)', borderRadius: 'var(--radius-sm)' }} />
            </div>
            <div className="mono" style={{ fontSize: 9.5, color: 'var(--fg-3)', marginTop: 7, letterSpacing: '0.03em' }}>
              82 / 100 req·s · backoff on
            </div>
          </div>
        </div>
      </nav>

      <div style={{ padding: 12, borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 10 }}>
        <span
          style={{ flex: 'none', width: 30, height: 30, borderRadius: 99, background: 'var(--slate-800)', color: 'var(--text-on-dark)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 600 }}
        >
          EI
        </span>
        <div className="ops-nav-label" style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 13, fontWeight: 500 }}>Emeka Iroha</div>
          <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
            SUPPORT ENGINEER · L2
          </div>
        </div>
        {/* Sign out, replacing the prototype's decorative gear (proto:86) — the same call
            the app and the developer console already made. Clears the stored session
            FIRST: navigating alone would leave it behind and let the next visitor back in.
            landingBase() is null on the standalone showcase build; never navigate to
            `null`, which stringifies to "null". */}
        <button
          type="button"
          onClick={() => {
            clearSupportSession()
            const dest = landingBase()
            if (dest) window.location.href = dest
            else window.location.reload()
          }}
          className="ops-btn ops-hide-narrow"
          aria-label="Sign out"
          title="Sign out"
          style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: 28, height: 28, padding: 0, border: 0, borderRadius: 'var(--radius-sm)', background: 'transparent', color: 'var(--fg-3)', cursor: 'pointer' }}
        >
          {SIGN_OUT_ICON}
        </button>
      </div>
    </aside>
  )
}
