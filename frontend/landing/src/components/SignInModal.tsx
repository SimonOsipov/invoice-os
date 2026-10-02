// Landing sign-in: the email/password form (when configured) above the demo persona picker.
// The form posts to the gateway; a persona pick makes no backend call, the app mints from ?persona=<id>.

import { useEffect } from 'react'
import { LANDING_PERSONAS, destUrl, type LandingPersona } from '../auth'
import { signInConfigured } from '../signIn'
import { GLYPHS, Icon } from '../icons'
import { Eyebrow } from './ds/Eyebrow'
import { SignInForm } from './SignInForm'
import { MODAL_CHROME_CSS, MODAL_SCRIM_STYLE, ModalHeader, modalCardStyle } from './modalChrome'

const SIGN_IN_CSS = `
  .si-persona { transition: filter var(--dur-fast) var(--ease-out); }
  .si-persona:hover { filter: brightness(0.97); }
  .si-persona:focus-visible { outline: 2px solid var(--ring); outline-offset: 2px; }
`

const HEADING_STYLE = { fontSize: 22, letterSpacing: '-0.03em', fontWeight: 700, color: 'var(--ink)' } as const

const NO_STATE = () => null

export function SignInModal({ onClose, heldState = NO_STATE, initialError }: { onClose: () => void; heldState?: () => string | null; initialError?: string }) {
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
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label="Platform login"
      style={MODAL_SCRIM_STYLE}
    >
      <style>{MODAL_CHROME_CSS + SIGN_IN_CSS}</style>

      <div
        onClick={(e) => e.stopPropagation()}
        style={modalCardStyle(452)}
      >
        <ModalHeader onClose={onClose} padX={18} />

        <div style={{ padding: '22px 20px 20px' }}>
          <div style={{ marginBottom: 14 }}>
            <Eyebrow>PLATFORM LOGIN</Eyebrow>
          </div>
          {signInConfigured() && (
            <>
              <h3 style={{ ...HEADING_STYLE, margin: '0 0 16px' }}>Sign in to your workspace</h3>
              <SignInForm heldState={heldState} initialError={initialError} />
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, margin: '22px 0 18px', fontSize: 12, color: 'var(--muted-foreground)' }}>
                <span style={{ flex: 1, height: 1, background: 'var(--border)' }} />
                or explore with a demo profile
                <span style={{ flex: 1, height: 1, background: 'var(--border)' }} />
              </div>
            </>
          )}
          <div data-testid="persona-picker">
            <h3 style={{ ...HEADING_STYLE, margin: '0 0 6px' }}>Choose an account</h3>
            <p className="t-body-sm" style={{ margin: 0, lineHeight: 1.55 }}>Pick a demo profile to continue. Each role opens only the workspace it's allowed to use.</p>
            <div style={{ display: 'grid', gap: 10, marginTop: 18 }}>
              {LANDING_PERSONAS.map((p) => (
                // data-persona: stable selector for the persona-picker test oracle
                <button
                  data-persona={p.id}
                  key={p.id}
                  onClick={() => pickPersona(p)}
                  className="si-persona"
                  style={{ display: 'flex', alignItems: 'center', gap: 12, width: '100%', textAlign: 'left', background: 'var(--card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', padding: '12px 14px', cursor: 'pointer', fontFamily: 'var(--font-sans)' }}
                >
                  <span style={{ flex: 'none', width: 38, height: 38, borderRadius: 'var(--radius-md)', background: p.avBg, color: p.avColor, display: 'grid', placeItems: 'center', fontSize: 13, fontWeight: 700 }}>{p.initials}</span>
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: 14, fontWeight: 700, color: 'var(--ink)' }}>{p.name}</span>
                    <span style={{ display: 'block', fontSize: 12, color: 'var(--muted-foreground)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{p.title} · {p.org}</span>
                    <span className="t-meta" style={{ display: 'inline-block', marginTop: 6, color: 'var(--teal)' }}>{p.access}</span>
                  </span>
                  <span style={{ flex: 'none', color: 'var(--muted-foreground)', display: 'inline-flex' }}>
                    <Icon paths={GLYPHS['chevron-right']} size={16} strokeWidth={2} />
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
