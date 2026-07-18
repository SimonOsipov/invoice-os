import { CRUMB_BY_SCREEN, SEARCH_ICON, SHIELD_ICON } from '../data'
import { Icon } from '../icons'
import type { Env, Screen } from '../types'

type Props = {
  screen: Screen
  env: Env
  onSetEnv: (e: Env) => void
}

const SANDBOX_ICON = (
  <Icon paths={['M9 3h6M10 3v6.5L5.5 17a2 2 0 0 0 1.8 3h9.4a2 2 0 0 0 1.8-3L14 9.5V3', 'M7.5 14h9']} size={15} />
)

export function TopBar({ screen, env, onSetEnv }: Props) {
  const sandbox = env === 'sandbox'

  const seg = (active: boolean, kind: 'sandbox' | 'live') => ({
    bg: active ? (kind === 'live' ? 'var(--status-green-text)' : 'var(--status-amber-text)') : 'transparent',
    color: active ? '#fff' : 'var(--fg-3)',
    dot: active ? '#fff' : kind === 'live' ? 'var(--status-green-text)' : 'var(--status-amber-text)',
  })
  const sbx = seg(sandbox, 'sandbox')
  const liv = seg(!sandbox, 'live')

  const envBanner = sandbox
    ? {
        bg: 'var(--status-amber-bg)',
        border: 'var(--status-amber-border)',
        text: 'var(--status-amber-text)',
        icon: SANDBOX_ICON,
        msg: 'Sandbox — test keys against the simulated FIRS adapter. Nothing here is transmitted to the tax authority.',
        tag: 'TEST DATA · sk_test',
      }
    : {
        bg: 'var(--accent-tint)',
        border: 'var(--teal-200)',
        text: 'var(--accent-soft)',
        icon: SHIELD_ICON,
        msg: 'Live — production keys. Submissions are transmitted to FIRS/MBS and return legally-valid clearance evidence.',
        tag: 'PRODUCTION · sk_live',
      }

  return (
    <>
      <header
        style={{
          flex: 'none',
          height: 56,
          borderBottom: '1px solid var(--line-1)',
          background: 'rgba(247,249,250,0.82)',
          backdropFilter: 'blur(12px)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '0 22px',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>
            ZEPHYR PAY
          </span>
          <span style={{ color: 'var(--line-3)' }}>/</span>
          <span style={{ fontSize: 14, fontWeight: 600 }}>{CRUMB_BY_SCREEN[screen]}</span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <div
            className="ops-header-search"
            style={{ display: 'flex', alignItems: 'center', gap: 8, height: 34, padding: '0 12px', border: '1px solid var(--line-2)', borderRadius: 6, background: 'var(--bg-2)', width: 380 }}
          >
            <span style={{ color: 'var(--fg-3)' }}>{SEARCH_ICON}</span>
            <span style={{ fontSize: 13, color: 'var(--fg-4)', whiteSpace: 'nowrap' }}>Search invoice # · job ID · IRN · evidence hash</span>
            <span className="mono" style={{ marginLeft: 'auto', fontSize: 10, color: 'var(--fg-4)', border: '1px solid var(--line-2)', borderRadius: 3, padding: '1px 5px' }}>
              ⌘K
            </span>
          </div>
          {/* Sandbox / Live switch */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 0, background: 'var(--bg-2)', border: `1px solid ${sandbox ? 'var(--status-amber-border)' : 'var(--status-green-border)'}`, borderRadius: 8, padding: 3 }}>
            <button
              type="button"
              onClick={() => onSetEnv('sandbox')}
              className="ops-btn"
              style={{
                border: 0,
                cursor: 'pointer',
                height: 30,
                padding: '0 14px',
                borderRadius: 6,
                fontFamily: 'var(--font-mono)',
                fontSize: 10.5,
                fontWeight: 700,
                letterSpacing: '0.05em',
                display: 'inline-flex',
                alignItems: 'center',
                gap: 6,
                background: sbx.bg,
                color: sbx.color,
              }}
            >
              <span style={{ width: 6, height: 6, borderRadius: 99, background: sbx.dot }} />
              SANDBOX
            </button>
            <button
              type="button"
              onClick={() => onSetEnv('live')}
              className="ops-btn"
              style={{
                border: 0,
                cursor: 'pointer',
                height: 30,
                padding: '0 14px',
                borderRadius: 6,
                fontFamily: 'var(--font-mono)',
                fontSize: 10.5,
                fontWeight: 700,
                letterSpacing: '0.05em',
                display: 'inline-flex',
                alignItems: 'center',
                gap: 6,
                background: liv.bg,
                color: liv.color,
              }}
            >
              <span style={{ width: 6, height: 6, borderRadius: 99, background: liv.dot }} />
              LIVE
            </button>
          </div>
        </div>
      </header>

      {/* environment banner */}
      <div style={{ flex: 'none', background: envBanner.bg, borderBottom: `1px solid ${envBanner.border}`, padding: '7px 22px', display: 'flex', alignItems: 'center', gap: 9 }}>
        <span style={{ color: envBanner.text, flex: 'none', display: 'inline-flex' }}>{envBanner.icon}</span>
        <span style={{ fontSize: 12.5, color: envBanner.text, fontWeight: 500 }}>{envBanner.msg}</span>
        <span className="mono" style={{ marginLeft: 'auto', fontSize: 10, color: envBanner.text, opacity: 0.85, letterSpacing: '0.05em' }}>
          {envBanner.tag}
        </span>
      </div>
    </>
  )
}
