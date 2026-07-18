import type { CSSProperties } from 'react'
import { BrandMark } from '../icons'
import { GEAR_ICON, NAV_ITEMS } from '../data'
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
  borderRadius: 5,
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
            FiscalBridge
          </span>
          <span
            className="mono ops-nav-label"
            style={{ fontSize: 9, fontWeight: 600, letterSpacing: '0.07em', color: 'var(--accent)', border: '1px solid var(--accent)', borderRadius: 3, padding: '1px 5px' }}
          >
            DEV
          </span>
        </a>
        {/* client org card */}
        <div
          className="ops-hide-narrow"
          style={{ display: 'flex', alignItems: 'center', gap: 9, background: 'var(--accent-tint)', border: '1px solid var(--teal-200)', borderRadius: 6, padding: '8px 10px' }}
        >
          <span
            style={{
              flex: 'none',
              width: 26,
              height: 26,
              borderRadius: 5,
              background: 'var(--accent)',
              color: '#fff',
              display: 'grid',
              placeItems: 'center',
              fontSize: 10,
              fontWeight: 700,
            }}
          >
            ZP
          </span>
          <span style={{ flex: 1, minWidth: 0 }}>
            <span style={{ display: 'block', fontSize: 12, fontWeight: 600, color: 'var(--accent-soft)' }}>Zephyr Pay</span>
            <span className="mono" style={{ display: 'block', fontSize: 9, color: 'var(--accent)', letterSpacing: '0.05em' }}>
              SCALE PLAN · ORG_ZP001
            </span>
          </span>
        </div>
      </div>

      <nav style={{ flex: 1, padding: '12px 10px', display: 'flex', flexDirection: 'column', gap: 2 }}>
        <div className="label ops-nav-label" style={{ padding: '6px 8px 8px' }}>
          Console
        </div>
        {NAV_ITEMS.map((n) => {
          const active = screen === n.key
          const badge = n.key === 'submissions' && deadLetterCount ? String(deadLetterCount) : ''
          return (
            <button key={n.key} type="button" onClick={() => onNavigate(n.key)} className="ops-nav" style={navBtnStyle(active)}>
              <span style={{ position: 'absolute', left: 0, top: 7, bottom: 7, width: 2, borderRadius: 2, background: active ? 'var(--accent)' : 'transparent' }} />
              <span style={{ color: active ? 'var(--accent)' : 'var(--fg-3)', display: 'inline-flex' }}>{n.glyph}</span>
              <span className="ops-nav-label" style={{ flex: 1 }}>
                {n.label}
              </span>
              {badge && (
                <span
                  className="mono ops-nav-label"
                  style={{ fontSize: 10, fontWeight: 700, background: 'var(--status-red-bg)', color: 'var(--status-red-text)', borderRadius: 99, padding: '1px 6px' }}
                >
                  {badge}
                </span>
              )}
            </button>
          )
        })}
        {/* monthly request quota (prototype line 66–75) */}
        <div className="ops-hide-narrow" style={{ marginTop: 'auto', padding: '12px 8px 4px' }}>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 7, padding: '11px 12px', background: 'var(--bg-1)' }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
              <span className="label">Requests this month</span>
              <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: 'var(--status-amber-text)' }}>
                120%
              </span>
            </div>
            <div style={{ height: 5, background: 'var(--bg-3)', borderRadius: 3, overflow: 'hidden' }}>
              <div style={{ width: '100%', height: '100%', background: 'var(--status-amber-text)', borderRadius: 3 }} />
            </div>
            <div className="mono" style={{ fontSize: 9.5, color: 'var(--fg-3)', marginTop: 7, letterSpacing: '0.03em' }}>
              48.2K / 40K included · 8.2K over
            </div>
          </div>
        </div>
      </nav>

      <div style={{ padding: 12, borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 10 }}>
        <span
          style={{ flex: 'none', width: 30, height: 30, borderRadius: 99, background: 'var(--slate-800)', color: '#fff', display: 'grid', placeItems: 'center', fontSize: 11, fontWeight: 600 }}
        >
          AO
        </span>
        <div className="ops-nav-label" style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 13, fontWeight: 500 }}>Amara Okafor</div>
          <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
            DEVELOPER · ADMIN
          </div>
        </div>
        <span className="ops-hide-narrow" style={{ color: 'var(--fg-3)' }}>
          {GEAR_ICON}
        </span>
      </div>
    </aside>
  )
}
