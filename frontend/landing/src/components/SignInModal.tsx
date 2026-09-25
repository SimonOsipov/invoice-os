// Landing sign-in: the email/password form (when configured) above the demo persona picker.
// The form posts to the gateway; a persona pick makes no backend call, the app mints from ?persona=<id>.

import { useEffect } from 'react'
import { BrandMark } from '../icons'
import { LANDING_PERSONAS, destUrl, type LandingPersona } from '../auth'
import { signInConfigured } from '../signIn'
import { SignInForm } from './SignInForm'

function Glyph({ d, size = 16, sw = 1.7 }: { d: string | string[]; size?: number; sw?: number }) {
  const paths = Array.isArray(d) ? d : [d]
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={sw} strokeLinecap="round" strokeLinejoin="round">
      {paths.map((p, i) => (
        <path key={i} d={p} />
      ))}
    </svg>
  )
}

export function SignInModal({ onClose, state = null, initialError }: { onClose: () => void; state?: string | null; initialError?: string }) {
  // Close on Escape (never a native dialog).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('keydown', onKey)
    }
  }, [onClose])

  function pickPersona(p: LandingPersona) {
    const dest = destUrl(p)
    // Target SPA URL not configured: stay put rather than navigate to null.
    if (!dest) return
    window.location.href = dest
  }

  return (
    <div
      className="asc-app"
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label="Sign in"
      style={{ position: 'fixed', inset: 0, zIndex: 200, background: 'oklch(16% .03 210 / .44)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24, animation: 'siOvIn 160ms ease-out' }}
    >
      <style>{`
        @keyframes siOvIn { from { opacity: 0; } to { opacity: 1; } }
        @keyframes siCardIn { from { opacity: 0; transform: translateY(10px) scale(0.985); } to { opacity: 1; transform: none; } }
        .si-persona { transition: border-color 120ms ease-out, background 120ms ease-out, transform 90ms; }
        .si-persona:hover { border-color: var(--action); background: var(--bg-1); }
        .si-persona:active { transform: translateY(1px); }
        .si-close { transition: background 120ms ease-out, color 120ms ease-out; }
        .si-close:hover { background: var(--bg-3); color: var(--fg-1); }
      `}</style>

      <div
        onClick={(e) => e.stopPropagation()}
        style={{ width: '100%', maxWidth: 452, background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-lg)', boxShadow: '0 32px 64px -24px oklch(16% .03 210 / .42)', overflow: 'hidden', maxHeight: 'calc(100vh - 48px)', overflowY: 'auto', animation: 'siCardIn 200ms var(--ease-out)' }}
      >
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '16px 18px', borderBottom: '1px solid var(--line-1)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
            <BrandMark size={19} />
            <span style={{ fontWeight: 600, fontSize: 14, letterSpacing: '-0.02em' }}>ASComply</span>
            <span className="mono" style={{ fontSize: 9, fontWeight: 500, letterSpacing: '0.08em', color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', padding: '1px 4px' }}>
              AFRICA
            </span>
          </div>
          <button onClick={onClose} className="si-close" aria-label="Close" style={{ flex: 'none', width: 30, height: 30, borderRadius: 'var(--radius-md)', border: 0, background: 'transparent', color: 'var(--fg-3)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}>
            <Glyph d="M18 6 6 18M6 6l12 12" size={17} sw={1.8} />
          </button>
        </div>

        <div style={{ padding: '22px 20px 20px' }}>
          <div className="eyebrow" style={{ marginBottom: 8 }}>SIGN IN</div>
          {signInConfigured() && (
            <>
              <h3 style={{ fontSize: 20, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 16px' }}>Sign in to your workspace</h3>
              <SignInForm state={state} initialError={initialError} />
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, margin: '22px 0 18px', fontSize: 12, color: 'var(--fg-3)' }}>
                <span style={{ flex: 1, height: 1, background: 'var(--line-1)' }} />
                or explore with a demo profile
                <span style={{ flex: 1, height: 1, background: 'var(--line-1)' }} />
              </div>
            </>
          )}
          <div data-testid="persona-picker">
            <h3 style={{ fontSize: 20, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 6px' }}>Choose an account</h3>
            <p style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--fg-2)', margin: 0 }}>Pick a demo profile to continue. Each role opens only the workspace it's allowed to use.</p>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 18 }}>
              {LANDING_PERSONAS.map((p) => (
                // data-persona: stable selector for the persona-picker test oracle
                <button
                  data-persona={p.id}
                  key={p.id}
                  onClick={() => pickPersona(p)}
                  className="si-persona"
                  style={{ display: 'flex', alignItems: 'center', gap: 12, width: '100%', textAlign: 'left', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-lg)', padding: '12px 13px', cursor: 'pointer', fontFamily: 'var(--font-sans)' }}
                >
                  <span style={{ flex: 'none', width: 38, height: 38, borderRadius: 'var(--radius-md)', background: p.avBg, color: p.avColor, display: 'grid', placeItems: 'center', fontSize: 13, fontWeight: 700 }}>{p.initials}</span>
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: 14, fontWeight: 600, color: 'var(--fg-1)' }}>{p.name}</span>
                    <span style={{ display: 'block', fontSize: 12, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{p.title} · {p.org}</span>
                    <span className="mono" style={{ display: 'inline-flex', alignItems: 'center', gap: 5, marginTop: 7, fontSize: 9, fontWeight: 600, letterSpacing: '0.06em', color: 'var(--action)', background: 'var(--action-tint)', borderRadius: 'var(--radius-sm)', padding: '2px 6px' }}>{p.access}</span>
                  </span>
                  <span style={{ flex: 'none', color: 'var(--fg-3)', display: 'inline-flex' }}>
                    <Glyph d="m9 18 6-6-6-6" size={16} sw={1.8} />
                  </span>
                </button>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
