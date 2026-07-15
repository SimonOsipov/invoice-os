// The Platform app's bare sign-in (M2-13, "deliberately minimal UI"). It gates the
// workspace: pick a seeded persona and App runs the real round trip (mint a JWT via the
// gateway, GET /api/tenancy/v1/me) before revealing the app. The full landing →
// persona-picker → OTP flow lives on the landing page (task-21) and deep-links here
// with ?persona=<id>, which auto-drives this same sign-in.

import { BrandMark } from '../icons'
import { APP_PERSONAS, type Persona, type PersonaId } from '../auth'

const ORDER: PersonaId[] = ['firm', 'inhouse']

function Chevron() {
  return (
    <svg width={16} height={16} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round">
      <path d="m9 18 6-6-6-6" />
    </svg>
  )
}

function Spinner() {
  return (
    <span
      className="si-spin"
      style={{ width: 15, height: 15, border: '2px solid var(--line-2)', borderTopColor: 'var(--accent)', borderRadius: 99, display: 'inline-block' }}
    />
  )
}

export function SignIn({ signingIn, onPick }: { signingIn: PersonaId | null; onPick: (p: Persona) => void }) {
  const busy = signingIn !== null
  return (
    <div
      className="if-v2"
      style={{ minHeight: '100vh', background: 'var(--bg-1)', fontFamily: 'var(--font-sans)', color: 'var(--fg-1)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}
    >
      <style>{`
        @keyframes siSpin { to { transform: rotate(360deg); } }
        .si-spin { animation: siSpin 0.7s linear infinite; }
        .si-persona { transition: border-color 120ms ease-out, background 120ms ease-out, transform 90ms; }
        .si-persona:not(:disabled):hover { border-color: var(--accent); background: var(--bg-1); }
        .si-persona:not(:disabled):active { transform: translateY(1px); }
        .si-persona:disabled { cursor: default; opacity: 0.6; }
      `}</style>

      <div style={{ width: '100%', maxWidth: 452, background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 10, boxShadow: '0 32px 64px -24px rgba(20,23,26,0.42)', overflow: 'hidden' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 9, padding: '16px 18px', borderBottom: '1px solid var(--line-1)' }}>
          <BrandMark size={20} />
          <span style={{ fontWeight: 600, fontSize: 14, letterSpacing: '-0.02em' }}>FiscalBridge</span>
          <span className="mono" style={{ fontSize: 9, fontWeight: 500, letterSpacing: '0.08em', color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 3, padding: '1px 4px' }}>
            AFRICA
          </span>
        </div>

        <div style={{ padding: '22px 20px 20px' }}>
          <div className="label" style={{ marginBottom: 8 }}>/ SIGN IN</div>
          <h1 style={{ fontSize: 20, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 6px' }}>Choose an account</h1>
          <p style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--fg-2)', margin: 0 }}>
            Pick a demo profile to open its workspace. Signing in resolves the tenant against the live backend when one is configured.
          </p>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 18 }}>
            {ORDER.map((id) => {
              const p = APP_PERSONAS[id]
              const isThis = signingIn === id
              return (
                <button
                  key={id}
                  className="si-persona"
                  disabled={busy}
                  onClick={() => onPick(p)}
                  style={{ display: 'flex', alignItems: 'center', gap: 12, width: '100%', textAlign: 'left', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 8, padding: '12px 13px', cursor: 'pointer', fontFamily: 'var(--font-sans)' }}
                >
                  <span style={{ flex: 'none', width: 38, height: 38, borderRadius: 7, background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', fontSize: 13, fontWeight: 700 }}>
                    {p.initials}
                  </span>
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: 14, fontWeight: 600, color: 'var(--fg-1)' }}>{p.name}</span>
                    <span style={{ display: 'block', fontSize: 12, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                      {p.title} · {p.org}
                    </span>
                    <span className="mono" style={{ display: 'inline-flex', alignItems: 'center', gap: 5, marginTop: 7, fontSize: 9, fontWeight: 600, letterSpacing: '0.06em', color: 'var(--accent)', background: 'var(--accent-tint)', borderRadius: 3, padding: '2px 6px' }}>
                      {p.access}
                    </span>
                  </span>
                  <span style={{ flex: 'none', color: 'var(--fg-3)', display: 'inline-flex' }}>{isThis ? <Spinner /> : <Chevron />}</span>
                </button>
              )
            })}
          </div>

          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginTop: 18, paddingTop: 16, borderTop: '1px solid var(--line-1)' }}>
            <span style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>Mock sign-in · swapped for Supabase at M8</span>
            <span className="mono" style={{ fontSize: 10, letterSpacing: '0.06em', color: 'var(--fg-3)' }}>SSO · OAUTH2</span>
          </div>
        </div>
      </div>
    </div>
  )
}

// Loading splash shown while a landing deep-link (?persona=) auto-sign-in is in flight
// (App.tsx render gate). It replaces the interactive persona picker for that window so
// the landing → app hand-off shows a neutral "signing you in" state instead of flashing
// the "Choose an account" card before the mint → /me round trip resolves. Same card
// chrome as SignIn so the two never visually jump.
export function SignInLoading({ persona }: { persona: Persona }) {
  return (
    <div
      className="if-v2"
      style={{ minHeight: '100vh', background: 'var(--bg-1)', fontFamily: 'var(--font-sans)', color: 'var(--fg-1)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}
    >
      <style>{`
        @keyframes siSpin { to { transform: rotate(360deg); } }
        .si-spin { animation: siSpin 0.7s linear infinite; }
      `}</style>

      <div style={{ width: '100%', maxWidth: 452, background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 10, boxShadow: '0 32px 64px -24px rgba(20,23,26,0.42)', overflow: 'hidden' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 9, padding: '16px 18px', borderBottom: '1px solid var(--line-1)' }}>
          <BrandMark size={20} />
          <span style={{ fontWeight: 600, fontSize: 14, letterSpacing: '-0.02em' }}>FiscalBridge</span>
          <span className="mono" style={{ fontSize: 9, fontWeight: 500, letterSpacing: '0.08em', color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 3, padding: '1px 4px' }}>
            AFRICA
          </span>
        </div>

        <div style={{ padding: '44px 20px', display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 14 }}>
          <Spinner />
          <div style={{ fontSize: 14, fontWeight: 500, color: 'var(--fg-2)' }}>Signing in as {persona.name}…</div>
          <div style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>Resolving your workspace with the backend.</div>
        </div>
      </div>
    </div>
  )
}
