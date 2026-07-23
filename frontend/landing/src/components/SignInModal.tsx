// The full sign-in experience (task-21), ported faithfully from the FiscalBridge Africa
// sign-in prototype: a two-step modal — choose an account (persona picker), then verify
// a 6-digit OTP — that routes each role to only the workspace it may open. The OTP is
// client-side theater (the demo code is fixed); the real JWT mint + /v1/me round trip
// happens in the destination Platform app on arrival (M2-13), which this deep-links via
// ?persona=<id>. No backend call is made here.

import { useEffect, useRef, useState } from 'react'
import { BrandMark } from '../icons'
import { DEMO_CODE, LANDING_PERSONAS, destUrl, maskedEmail, type LandingPersona } from '../auth'

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

export function SignInModal({ onClose }: { onClose: () => void }) {
  const [step, setStep] = useState<'persona' | 'otp'>('persona')
  const [persona, setPersona] = useState<LandingPersona | null>(null)
  const [code, setCode] = useState<string[]>(['', '', '', '', '', ''])
  const [otpError, setOtpError] = useState(false)
  const [loading, setLoading] = useState(false)
  const [forgotNote, setForgotNote] = useState(false)
  const redirectTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Close on Escape (never a native dialog); clear any pending redirect on unmount.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('keydown', onKey)
      if (redirectTimer.current) clearTimeout(redirectTimer.current)
    }
  }, [onClose])

  function pickPersona(p: LandingPersona) {
    setPersona(p)
    setStep('otp')
    setCode(['', '', '', '', '', ''])
    setOtpError(false)
    setLoading(false)
    setTimeout(() => document.getElementById('si-otp-0')?.focus(), 60)
  }

  function setDigit(i: number, raw: string) {
    const v = (raw || '').replace(/\D/g, '').slice(-1)
    setCode((prev) => {
      const next = [...prev]
      next[i] = v
      return next
    })
    setOtpError(false)
    if (v && i < 5) document.getElementById('si-otp-' + (i + 1))?.focus()
  }

  function otpKey(i: number, e: React.KeyboardEvent) {
    if (e.key === 'Backspace' && !code[i] && i > 0) {
      document.getElementById('si-otp-' + (i - 1))?.focus()
    } else if (e.key === 'Enter') {
      verify()
    }
  }

  function verify() {
    if (loading || !persona) return
    if (code.join('') === DEMO_CODE) {
      const dest = destUrl(persona)
      // No-gateway path (M4-21): the target SPA's VITE_* URL isn't configured — stay put
      // rather than navigate to null (destUrl no longer defaults to a hardcoded deploy).
      if (!dest) return
      setLoading(true)
      redirectTimer.current = setTimeout(() => {
        window.location.href = dest
      }, 1100)
    } else {
      setOtpError(true)
      setCode(['', '', '', '', '', ''])
      setTimeout(() => document.getElementById('si-otp-0')?.focus(), 60)
    }
  }

  return (
    <div
      className="if-v2"
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label="Sign in"
      style={{ position: 'fixed', inset: 0, zIndex: 200, background: 'rgba(20,23,26,0.44)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24, animation: 'siOvIn 160ms ease-out' }}
    >
      <style>{`
        @keyframes siOvIn { from { opacity: 0; } to { opacity: 1; } }
        @keyframes siCardIn { from { opacity: 0; transform: translateY(10px) scale(0.985); } to { opacity: 1; transform: none; } }
        @keyframes siSpin { to { transform: rotate(360deg); } }
        @keyframes siShake { 10%,90%{transform:translateX(-1px);} 20%,80%{transform:translateX(2px);} 30%,50%,70%{transform:translateX(-5px);} 40%,60%{transform:translateX(5px);} }
        .si-shake { animation: siShake 400ms ease-in-out; }
        .si-persona { transition: border-color 120ms ease-out, background 120ms ease-out, transform 90ms; }
        .si-persona:hover { border-color: var(--accent); background: var(--bg-1); }
        .si-persona:active { transform: translateY(1px); }
        .si-otp { transition: border-color 120ms, box-shadow 120ms; }
        .si-otp:focus { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-glow); outline: none; }
        .si-close { transition: background 120ms ease-out, color 120ms ease-out; }
        .si-close:hover { background: var(--bg-3); color: var(--fg-1); }
        .si-link { transition: color 120ms ease-out; cursor: pointer; }
        .si-link:hover { color: var(--accent); }
      `}</style>

      <div
        onClick={(e) => e.stopPropagation()}
        style={{ width: '100%', maxWidth: 452, background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-xl)', boxShadow: '0 32px 64px -24px rgba(20,23,26,0.42)', overflow: 'hidden', animation: 'siCardIn 200ms var(--ease-out)' }}
      >
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '16px 18px', borderBottom: '1px solid var(--line-1)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
            <BrandMark size={19} />
            <span style={{ fontWeight: 600, fontSize: 14, letterSpacing: '-0.02em' }}>FiscalBridge</span>
            <span className="mono" style={{ fontSize: 9, fontWeight: 500, letterSpacing: '0.08em', color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', padding: '1px 4px' }}>
              AFRICA
            </span>
          </div>
          <button onClick={onClose} className="si-close" aria-label="Close" style={{ flex: 'none', width: 30, height: 30, borderRadius: 'var(--radius-lg)', border: 0, background: 'transparent', color: 'var(--fg-3)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}>
            <Glyph d="M18 6 6 18M6 6l12 12" size={17} sw={1.8} />
          </button>
        </div>

        {step === 'persona' && (
          <div style={{ padding: '22px 20px 20px' }}>
            <div className="label" style={{ marginBottom: 8 }}>/ SIGN IN</div>
            <h3 style={{ fontSize: 20, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 6px' }}>Choose an account</h3>
            <p style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--fg-2)', margin: 0 }}>Pick a demo profile to continue. Each role opens only the workspace it's allowed to use.</p>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 18 }}>
              {LANDING_PERSONAS.map((p) => (
                <button
                  key={p.id}
                  onClick={() => pickPersona(p)}
                  className="si-persona"
                  style={{ display: 'flex', alignItems: 'center', gap: 12, width: '100%', textAlign: 'left', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-xl)', padding: '12px 13px', cursor: 'pointer', fontFamily: 'var(--font-sans)' }}
                >
                  <span style={{ flex: 'none', width: 38, height: 38, borderRadius: 'var(--radius-lg)', background: p.avBg, color: p.avColor, display: 'grid', placeItems: 'center', fontSize: 13, fontWeight: 700 }}>{p.initials}</span>
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: 14, fontWeight: 600, color: 'var(--fg-1)' }}>{p.name}</span>
                    <span style={{ display: 'block', fontSize: 12, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{p.title} · {p.org}</span>
                    <span className="mono" style={{ display: 'inline-flex', alignItems: 'center', gap: 5, marginTop: 7, fontSize: 9, fontWeight: 600, letterSpacing: '0.06em', color: 'var(--accent)', background: 'var(--accent-tint)', borderRadius: 'var(--radius-sm)', padding: '2px 6px' }}>{p.access}</span>
                  </span>
                  <span style={{ flex: 'none', color: 'var(--fg-3)', display: 'inline-flex' }}>
                    <Glyph d="m9 18 6-6-6-6" size={16} sw={1.8} />
                  </span>
                </button>
              ))}
            </div>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginTop: 18, paddingTop: 16, borderTop: '1px solid var(--line-1)' }}>
              <a href="#" onClick={(e) => { e.preventDefault(); setForgotNote((v) => !v) }} className="si-link" style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>Forgot password?</a>
              <span className="mono" style={{ fontSize: 10, letterSpacing: '0.06em', color: 'var(--fg-3)' }}>SSO · OAUTH2</span>
            </div>
            {forgotNote && (
              <div style={{ marginTop: 12, fontSize: 12, lineHeight: 1.5, color: 'var(--fg-3)', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-lg)', padding: '10px 12px' }}>
                Password reset is disabled in this demo — choose one of the profiles above to sign in.
              </div>
            )}
          </div>
        )}

        {step === 'otp' && persona && (
          <div style={{ padding: '20px 20px 22px' }}>
            <button onClick={() => { setStep('persona'); setOtpError(false) }} className="si-link" style={{ display: 'inline-flex', alignItems: 'center', gap: 5, border: 0, background: 'none', color: 'var(--fg-3)', fontFamily: 'var(--font-sans)', fontSize: 12, cursor: 'pointer', padding: 0, marginBottom: 15 }}>
              <Glyph d="m15 18-6-6 6-6" size={15} sw={1.8} /> Back to accounts
            </button>
            <div style={{ display: 'flex', alignItems: 'center', gap: 11, marginBottom: 18 }}>
              <span style={{ flex: 'none', width: 40, height: 40, borderRadius: 'var(--radius-lg)', background: persona.avBg, color: persona.avColor, display: 'grid', placeItems: 'center', fontSize: 14, fontWeight: 700 }}>{persona.initials}</span>
              <div style={{ minWidth: 0 }}>
                <div style={{ fontSize: 14, fontWeight: 600 }}>{persona.name}</div>
                <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{maskedEmail(persona.email)}</div>
              </div>
            </div>
            <h3 style={{ fontSize: 19, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 6px' }}>Verify it's you</h3>
            <p style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--fg-2)', margin: 0 }}>Enter the 6-digit code we sent to your email to open the {persona.destLabel}.</p>
            <div className={otpError ? 'si-shake' : ''} style={{ display: 'flex', gap: 8, marginTop: 18 }}>
              {code.map((v, i) => (
                <input
                  key={i}
                  id={'si-otp-' + i}
                  className="si-otp"
                  value={v}
                  onChange={(e) => setDigit(i, e.target.value)}
                  onKeyDown={(e) => otpKey(i, e)}
                  inputMode="numeric"
                  maxLength={1}
                  autoComplete="off"
                  style={{ width: '100%', height: 54, textAlign: 'center', fontFamily: 'var(--font-mono)', fontSize: 22, fontWeight: 600, color: 'var(--fg-1)', background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-xl)', padding: 0 }}
                />
              ))}
            </div>
            {otpError && (
              <div style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 12, fontSize: 12.5, color: 'var(--status-red-text)' }}>
                <Glyph d={['M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z', 'M12 9v4', 'M12 17h.01']} size={15} sw={1.7} />
                That code doesn't match. Use the demo code {DEMO_CODE}.
              </div>
            )}
            <button onClick={verify} className="v2-btn v2-btn-primary" style={{ width: '100%', justifyContent: 'center', height: 44, marginTop: 18, cursor: 'pointer', gap: 9 }}>
              {loading ? (
                <>
                  <span style={{ width: 15, height: 15, border: '2px solid rgba(255,255,255,0.4)', borderTopColor: '#fff', borderRadius: 99, animation: 'siSpin 0.7s linear infinite' }} />
                  Signing in…
                </>
              ) : (
                'Verify & continue'
              )}
            </button>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginTop: 14 }}>
              <span className="mono" style={{ fontSize: 10, letterSpacing: '0.05em', color: 'var(--accent)', background: 'var(--accent-tint)', borderRadius: 'var(--radius-md)', padding: '4px 8px' }}>DEMO CODE · {DEMO_CODE}</span>
              <a href="#" onClick={(e) => e.preventDefault()} className="si-link" style={{ fontSize: 12, color: 'var(--fg-3)' }}>Resend code</a>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
