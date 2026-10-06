// Landing sign-in: the email/password form, or the unavailable copy when the gateway is unset.

import { useEffect } from 'react'
import { SIGN_IN_UNAVAILABLE, signInConfigured, type ConsoleTarget } from '../signIn'
import { SignInForm } from './SignInForm'
import { Eyebrow } from './ds/Eyebrow'
import { MODAL_CHROME_CSS, MODAL_SCRIM_STYLE, ModalHeader, modalCardStyle } from './modalChrome'

export const HEADING_STYLE = { fontSize: 22, letterSpacing: '-0.03em', fontWeight: 700, color: 'var(--ink)' } as const

const NO_STATE = () => null

export function SignInModal({ onClose, heldState = NO_STATE, initialError, consoleTarget, onCreateAccount }: { onClose: () => void; heldState?: () => string | null; initialError?: string; consoleTarget?: ConsoleTarget; onCreateAccount?: () => void; initialView?: 'sign-in' | 'forgot' }) {
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

  return (
    <div
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label="Platform login"
      style={MODAL_SCRIM_STYLE}
    >
      <style>{MODAL_CHROME_CSS}</style>

      <div
        onClick={(e) => e.stopPropagation()}
        style={modalCardStyle(452)}
      >
        <ModalHeader onClose={onClose} padX={18} />

        <div style={{ padding: '22px 20px 20px' }}>
          <div style={{ marginBottom: 14 }}>
            <Eyebrow>PLATFORM LOGIN</Eyebrow>
          </div>
          <h3 style={{ ...HEADING_STYLE, margin: '0 0 16px' }}>Sign in to your workspace</h3>
          {signInConfigured() ? (
            <>
              <SignInForm heldState={heldState} initialError={initialError} consoleTarget={consoleTarget} />
              {onCreateAccount && (
                <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginTop: 18, paddingTop: 16, borderTop: '1px solid var(--border)', fontSize: 13, color: 'var(--muted-foreground)' }}>
                  New to ASComply?
                  <button type="button" className="a-link" onClick={onCreateAccount} style={{ fontSize: 13 }}>Create an account</button>
                </div>
              )}
            </>
          ) : (
            <p className="t-body-sm" style={{ margin: 0, lineHeight: 1.55 }}>{SIGN_IN_UNAVAILABLE}</p>
          )}
        </div>
      </div>
    </div>
  )
}
