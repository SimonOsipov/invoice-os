import { useState, type CSSProperties } from 'react'
import { BrandMark, Icon } from '../icons'
import { CHEV_DOWN_ICON, DEV_ORGS, NAV_ITEMS, TICK_ICON } from '../data'
import { signOut } from '../auth'
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

export function Sidebar({ screen, onNavigate, deadLetterCount }: Props) {
  // Local: with one org the selection changes nothing downstream. Lift it when a second org does.
  const [switcherOpen, setSwitcherOpen] = useState(false)
  const [orgId, setOrgId] = useState(DEV_ORGS[0].id)
  const activeOrg = DEV_ORGS.find((o) => o.id === orgId) ?? DEV_ORGS[0]

  return (
    <aside
      className="ops-sidebar asc-dark"
      style={{ width: 244, flex: 'none', background: 'var(--surface)', borderRight: '1px solid var(--surface-panel-border)', display: 'flex', flexDirection: 'column' }}
    >
      <div style={{ padding: '16px 16px 14px', borderBottom: '1px solid var(--line-1)' }}>
        <a href="#" style={{ display: 'flex', alignItems: 'center', gap: 9, color: 'var(--fg-1)', marginBottom: 14 }}>
          <BrandMark />
          <span className="ops-nav-label" style={{ fontWeight: 700, fontSize: 15, letterSpacing: '-0.02em' }}>
            ASComply
          </span>
          <span
            className="mono ops-nav-label"
            style={{ fontSize: 9, fontWeight: 600, letterSpacing: '0.07em', color: 'var(--action)', border: '1px solid var(--action)', borderRadius: 'var(--radius-sm)', padding: '1px 5px' }}
          >
            OPS
          </span>
        </a>
        {/* Same control as the Platform app's company switcher; the menu opens with one org. */}
        <div className="ops-hide-narrow" style={{ position: 'relative' }}>
          <button
            type="button"
            onClick={() => setSwitcherOpen((v) => !v)}
            aria-expanded={switcherOpen}
            aria-haspopup="menu"
            className="ops-btn"
            style={{
              width: '100%',
              display: 'flex',
              alignItems: 'center',
              gap: 10,
              background: 'var(--bg-1)',
              border: `1px solid ${switcherOpen ? 'var(--action)' : 'var(--line-2)'}`,
              borderRadius: 'var(--radius-btn)',
              padding: '8px 10px',
              cursor: 'pointer',
              textAlign: 'left',
              color: 'var(--fg-1)',
            }}
          >
            <span style={{ flex: 'none', width: 28, height: 28, borderRadius: 'var(--radius-sm)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 700 }}>
              {activeOrg.initials}
            </span>
            <span style={{ flex: 1, minWidth: 0 }}>
              <span style={{ display: 'block', fontSize: 13, fontWeight: 600, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{activeOrg.name}</span>
              <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                {activeOrg.meta}
              </span>
            </span>
            <span style={{ flex: 'none', color: 'var(--fg-3)', display: 'inline-flex', transform: switcherOpen ? 'rotate(180deg)' : 'rotate(0deg)', transition: 'transform 160ms' }}>{CHEV_DOWN_ICON}</span>
          </button>
          {switcherOpen && (
            <div
              role="menu"
              className="asc-light"
              style={{ position: 'absolute', top: 'calc(100% + 6px)', left: 0, right: 0, zIndex: 60, background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', boxShadow: 'var(--shadow-card)', overflow: 'hidden', animation: 'opsPop 140ms ease-out' }}
            >
              <div className="label" style={{ padding: '10px 12px 6px' }}>
                Switch organisation
              </div>
              {DEV_ORGS.map((o) => {
                const active = o.id === activeOrg.id
                return (
                  <button
                    key={o.id}
                    type="button"
                    role="menuitem"
                    onClick={() => {
                      setOrgId(o.id)
                      setSwitcherOpen(false)
                    }}
                    className="ops-menu-item"
                    style={{ width: '100%', display: 'flex', alignItems: 'center', gap: 10, border: 0, background: active ? 'var(--bg-3)' : 'transparent', cursor: 'pointer', textAlign: 'left', padding: '9px 12px', color: 'var(--fg-1)' }}
                  >
                    <span style={{ flex: 'none', width: 26, height: 26, borderRadius: 'var(--radius-sm)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', fontSize: 10, fontWeight: 700 }}>
                      {o.initials}
                    </span>
                    <span style={{ flex: 1, minWidth: 0 }}>
                      <span style={{ display: 'block', fontSize: 13, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{o.name}</span>
                      <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)' }}>
                        {o.meta}
                      </span>
                    </span>
                    <span style={{ flex: 'none', color: 'var(--action)', display: 'inline-flex' }}>{active ? TICK_ICON : null}</span>
                  </button>
                )
              })}
            </div>
          )}
        </div>
      </div>

      <nav style={{ flex: 1, padding: '12px 10px', display: 'flex', flexDirection: 'column', gap: 2 }}>
        <div className="label ops-nav-label" style={{ padding: '6px 8px 8px', color: 'var(--eyebrow-on-dark)' }}>
          Console
        </div>
        {NAV_ITEMS.map((n) => {
          const active = screen === n.key
          const badge = n.key === 'submissions' && deadLetterCount ? String(deadLetterCount) : ''
          return (
            <button key={n.key} type="button" onClick={() => onNavigate(n.key)} className="ops-nav" style={navBtnStyle(active)}>
              <span style={{ position: 'absolute', left: 0, top: 7, bottom: 7, width: 2, borderRadius: 2, background: active ? 'var(--action)' : 'transparent' }} />
              <span style={{ color: active ? 'var(--action)' : 'var(--fg-3)', display: 'inline-flex' }}>{n.glyph}</span>
              <span className="ops-nav-label" style={{ flex: 1 }}>
                {n.label}
              </span>
              {badge && (
                <span
                  className="mono ops-nav-label"
                  style={{ fontSize: 10, fontWeight: 700, background: 'var(--status-red-bg)', color: 'var(--status-red-text)', borderRadius: 'var(--radius-sm)', padding: '1px 6px' }}
                >
                  {badge}
                </span>
              )}
            </button>
          )
        })}
        {/* monthly request quota (prototype line 66–75) */}
        <div className="ops-hide-narrow" style={{ marginTop: 'auto', padding: '12px 8px 4px' }}>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '11px 12px', background: 'var(--bg-1)' }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
              <span className="label" style={{ color: 'var(--eyebrow-on-dark)' }}>Requests this month</span>
              <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: 'var(--status-amber-text)' }}>
                120%
              </span>
            </div>
            <div style={{ height: 5, background: 'var(--bg-3)', borderRadius: 2, overflow: 'hidden' }}>
              <div style={{ width: '100%', height: '100%', background: 'var(--status-amber-text)' }} />
            </div>
            <div className="mono" style={{ fontSize: 9.5, color: 'var(--fg-3)', marginTop: 7, letterSpacing: '0.03em' }}>
              48.2K / 40K included · 8.2K over
            </div>
          </div>
        </div>
      </nav>

      <div style={{ padding: 12, borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 10 }}>
        <span
          style={{ flex: 'none', width: 30, height: 30, borderRadius: '50%', background: 'var(--slate-800)', color: 'var(--surface-foreground)', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 600 }}
        >
          AO
        </span>
        <div className="ops-nav-label" style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 13, fontWeight: 500 }}>Amara Okafor</div>
          <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
            OPS · ADMIN
          </div>
        </div>
        {/* Sign out: clears the session, then leaves (signOutConsole). */}
        <button
          onClick={signOut}
          className="ops-btn ops-hide-narrow"
          aria-label="Sign out"
          title="Sign out"
          style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: 28, height: 28, padding: 0, border: 0, borderRadius: 'var(--radius-btn)', background: 'transparent', color: 'var(--fg-3)', cursor: 'pointer' }}
        >
          <Icon paths={['M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4', 'M16 17l5-5-5-5', 'M21 12H9']} size={16} />
        </button>
      </div>
    </aside>
  )
}
