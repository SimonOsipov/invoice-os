import { ALERT_ICON, CRUMB_BY_SCREEN, SANDBOX_ICON, SEARCH_ICON } from '../data'
import type { Env, Screen } from '../types'

type Props = {
  screen: Screen
  env: Env
  onSetEnv: (e: Env) => void
}

// dark-scope --status-green-text; no light-scope token
const LIVE_DOT_ACTIVE = '#8fdcaa'

export function TopBar({ screen, env, onSetEnv }: Props) {
  const sandbox = env === 'sandbox'

  const seg = (active: boolean, kind: 'sandbox' | 'live') => ({
    bg: active ? 'var(--primary)' : 'transparent',
    color: active ? 'var(--primary-foreground)' : 'var(--fg-3)',
    dot: active ? (kind === 'live' ? LIVE_DOT_ACTIVE : 'var(--accent)') : kind === 'live' ? 'var(--status-green-text)' : 'var(--status-amber-text)',
  })
  const sbx = seg(sandbox, 'sandbox')
  const liv = seg(!sandbox, 'live')

  // proto:847. Note this console's LIVE banner is RED, not teal: unlike the tenant apps,
  // every control on these screens acts across every tenant's production traffic, so live
  // mode is a warning state rather than a reassurance. The tag states the scope both ways.
  const envBanner = sandbox
    ? {
        bg: 'var(--status-amber-bg)',
        border: 'var(--status-amber-border)',
        text: 'var(--status-amber-text)',
        icon: SANDBOX_ICON,
        msg: 'Sandbox — operating against simulated clearance. Re-drives affect simulated traffic only. Rule switches are real and apply to every tenant.',
        tag: 'CROSS-TENANT · ALL ENTITIES',
      }
    : {
        bg: 'var(--status-red-bg)',
        border: 'var(--status-red-border)',
        text: 'var(--status-red-text)',
        icon: ALERT_ICON,
        msg: 'LIVE — cross-tenant scope. Re-drives and cancellations reach no production traffic until NRS accreditation. Rule switches are real and audited.',
        tag: 'CROSS-TENANT · PENDING ACCREDITATION',
      }

  return (
    <>
      <header
        style={{
          flex: 'none',
          height: 56,
          borderBottom: '1px solid var(--header-border)',
          background: 'var(--header-bg)',
          backdropFilter: 'blur(var(--header-blur))',
          WebkitBackdropFilter: 'blur(var(--header-blur))',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '0 22px',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>
            SUPPORT
          </span>
          <span style={{ color: 'var(--line-3)' }}>/</span>
          <span style={{ fontSize: 14, fontWeight: 600 }}>{CRUMB_BY_SCREEN[screen]}</span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <div
            className="ops-header-search"
            style={{ display: 'flex', alignItems: 'center', gap: 8, height: 34, padding: '0 12px', border: '1px solid var(--input)', borderRadius: 'var(--radius-btn)', background: 'var(--bg-2)', width: 420 }}
          >
            <span style={{ color: 'var(--fg-3)', display: 'inline-flex' }}>{SEARCH_ICON}</span>
            <span style={{ fontSize: 13, color: 'var(--fg-3)', whiteSpace: 'nowrap' }}>Search IRN · invoice # · TIN · job ID · tenant</span>
            <span className="mono" style={{ marginLeft: 'auto', fontSize: 10, color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', padding: '1px 5px' }}>
              ⌘K
            </span>
          </div>
          {/* Sandbox / Live switch */}
          <div
            style={{ display: 'flex', alignItems: 'center', gap: 2, background: 'var(--sage)', border: `1px solid ${sandbox ? 'var(--status-amber-border)' : 'var(--status-green-border)'}`, borderRadius: 'var(--radius-btn)', padding: 3 }}
          >
            <button
              type="button"
              onClick={() => onSetEnv('sandbox')}
              style={{ border: 0, cursor: 'pointer', height: 30, padding: '0 14px', borderRadius: 'var(--radius-sm)', fontFamily: 'var(--font-mono)', fontSize: 10.5, fontWeight: 700, letterSpacing: '0.05em', display: 'inline-flex', alignItems: 'center', gap: 6, background: sbx.bg, color: sbx.color }}
            >
              <span style={{ width: 6, height: 6, borderRadius: '50%', background: sbx.dot }} />
              SANDBOX
            </button>
            <button
              type="button"
              onClick={() => onSetEnv('live')}
              style={{ border: 0, cursor: 'pointer', height: 30, padding: '0 14px', borderRadius: 'var(--radius-sm)', fontFamily: 'var(--font-mono)', fontSize: 10.5, fontWeight: 700, letterSpacing: '0.05em', display: 'inline-flex', alignItems: 'center', gap: 6, background: liv.bg, color: liv.color }}
            >
              <span style={{ width: 6, height: 6, borderRadius: '50%', background: liv.dot }} />
              LIVE
            </button>
          </div>
        </div>
      </header>

      {/* environment banner */}
      <div style={{ flex: 'none', background: envBanner.bg, borderBottom: `1px solid ${envBanner.border}`, padding: '7px 22px', display: 'flex', alignItems: 'center', gap: 9 }}>
        <span style={{ color: envBanner.text, flex: 'none', display: 'inline-flex' }}>{envBanner.icon}</span>
        <span style={{ fontSize: 12.5, color: envBanner.text, fontWeight: 500 }}>{envBanner.msg}</span>
        <span className="mono" style={{ marginLeft: 'auto', fontSize: 10, color: envBanner.text, whiteSpace: 'nowrap', letterSpacing: '0.05em' }}>
          {envBanner.tag}
        </span>
      </div>
    </>
  )
}
