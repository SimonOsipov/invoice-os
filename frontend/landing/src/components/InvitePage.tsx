// The landing accept page for an invite link: one card, no Nav or Footer (a Nav "Sign in" would drop the invite).
import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { ApiError } from '@invoice-os/api-client/client'
import { ROLE_LABELS, inviteSignInUrl, previewInvitation, registerInvitee, type InvitationPreview } from '../invite'
import { registerOutcome, registrationOpen } from '../register'
import { RESET_SENT, requestPasswordReset } from '../passwordReset'
import { DEMO_FORM_CSS } from './DemoLeadForm'
import { PRODUCT_EMAIL_NOTICE } from './RegisterModal'
import { Alert, FIELD_STYLE, ResendNotice, SPINNER_STYLE } from './SignInForm'
import { HEADING_STYLE } from './SignInModal'
import { useResend } from './useResend'
import { Button } from './ds/Button'
import { Eyebrow } from './ds/Eyebrow'
import { MODAL_CHROME_CSS, ModalHeader, modalCardStyle } from './modalChrome'

type View = 'loading' | 'invalid' | 'unavailable' | 'ready' | 'register' | 'sent' | 'exists' | 'unconfirmed'

const WRAP = { overflowWrap: 'anywhere' } as const
const TEXT = { margin: 0, lineHeight: 1.55, ...WRAP } as const
const PASSWORD_ID = 'inv-password'
// internal/gateway/invitation.go: msgAccountExists, msgAccountUnconfirmed.
const ACCOUNT_EXISTS = 'account_exists'
const ACCOUNT_UNCONFIRMED = 'account_unconfirmed'
const isNotFound = (err: unknown) => err instanceof ApiError && err.kind === 'http' && err.status === 404

export function InvitePage({ token }: { token: string | null }) {
  const [view, setView] = useState<View>(token ? 'loading' : 'invalid')
  const [invite, setInvite] = useState<InvitationPreview>()
  const [password, setPassword] = useState('')
  const [passwordError, setPasswordError] = useState<string>()
  const [formError, setFormError] = useState<string>()
  const [submitting, setSubmitting] = useState(false)
  const { resending, note, resend } = useResend(view === 'sent' || view === 'unconfirmed' ? invite?.email : undefined)
  const { resending: resetting, note: resetNote, resend: sendReset } = useResend(view === 'unconfirmed' ? invite?.email : undefined, requestPasswordReset)
  // StrictMode runs the effect twice; the ref keeps it to one request.
  const asked = useRef(false)

  useEffect(() => {
    if (!token || asked.current) return
    asked.current = true
    previewInvitation(token).then(
      (p) => {
        setInvite(p)
        setView(p.account === 'confirmed' ? 'exists' : p.account === 'unconfirmed' ? 'unconfirmed' : 'ready')
      },
      (err) => setView(isNotFound(err) ? 'invalid' : 'unavailable'),
    )
  }, [token])

  const signIn = () => {
    const url = inviteSignInUrl(token)
    if (url) window.location.href = url
  }

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (submitting || !token) return
    if (!password) {
      setPasswordError('Choose a password.')
      document.getElementById(PASSWORD_ID)?.focus()
      return
    }
    setFormError(undefined)
    setSubmitting(true)
    try {
      await registerInvitee(token, password)
      setView('sent')
    } catch (err) {
      const conflict = err instanceof ApiError && err.kind === 'http' && err.status === 409 ? err.message : undefined
      if (isNotFound(err)) setView('invalid')
      else if (conflict === ACCOUNT_EXISTS || conflict === ACCOUNT_UNCONFIRMED) {
        setPassword('')
        setView(conflict === ACCOUNT_EXISTS ? 'exists' : 'unconfirmed')
      } else {
        const outcome = registerOutcome(err)
        setFormError('form' in outcome ? outcome.form : outcome.message)
      }
    }
    setSubmitting(false)
  }

  const signInButton = (
    <button type="button" onClick={signIn} className="ds-btn ds-btn--outline ds-btn--md" style={{ width: '100%', marginTop: 10 }}>
      Sign in
    </button>
  )

  let body
  if (view === 'loading') {
    body = (
      <p role="status" className="t-body-sm" style={TEXT}>
        Checking your invite…
      </p>
    )
  } else if (view === 'invalid') {
    body = (
      <>
        <h3 style={{ ...HEADING_STYLE, margin: '0 0 10px', ...WRAP }}>This invite is no longer valid</h3>
        <p className="t-body-sm" style={TEXT}>Ask your workspace admin for a new one.</p>
      </>
    )
  } else if (view === 'unavailable') {
    body = <p className="t-body-sm" style={TEXT}>We could not load this invite. Reload the page to try again.</p>
  } else if (view === 'ready' && invite) {
    body = (
      <>
        <h3 style={{ ...HEADING_STYLE, margin: '0 0 10px', ...WRAP }}>Join {invite.workspace}</h3>
        <p className="t-body-sm" style={TEXT}>
          {invite.email} is invited to join {invite.workspace} on ASComply as {ROLE_LABELS[invite.role] ?? invite.role}.
        </p>
        {registrationOpen() && (
          <>
            <p className="t-body-sm" style={{ ...TEXT, marginTop: 10 }}>New to ASComply? Create an account with this address. Already have one? Sign in.</p>
            <button type="button" onClick={() => setView('register')} className="ds-btn ds-btn--primary ds-btn--md" style={{ width: '100%', marginTop: 18 }}>
              Create account
            </button>
          </>
        )}
        {signInButton}
      </>
    )
  } else if (view === 'exists' && invite) {
    body = (
      <>
        <p className="t-body-sm" style={TEXT}>
          {invite.email} already has an ASComply account. Sign in to join {invite.workspace} as {ROLE_LABELS[invite.role] ?? invite.role}.
        </p>
        {signInButton}
      </>
    )
  } else if (view === 'unconfirmed' && invite) {
    body = (
      <>
        <p className="t-body-sm" style={TEXT}>
          A confirmation email was already sent to {invite.email}. Open it, then sign in with the password you chose first.
        </p>
        <button type="button" onClick={() => resend()} disabled={resending} className="ds-btn ds-btn--outline ds-btn--md" style={{ width: '100%', marginTop: 18 }}>
          {resending ? 'Sending…' : 'Send it again'}
        </button>
        <ResendNotice note={note} email={invite.email} />
        <div style={{ marginTop: 8 }}>
          <Button variant="text" type="button" onClick={() => sendReset()} disabled={resetting} style={{ fontSize: 13 }}>
            Forgot password?
          </Button>
        </div>
        <ResendNotice note={resetNote} email={invite.email} text={RESET_SENT} peach />
        {signInButton}
      </>
    )
  } else if (view === 'register' && invite) {
    body = (
      <>
        <h3 style={{ ...HEADING_STYLE, margin: '0 0 16px' }}>Create your account</h3>
        <form noValidate onSubmit={handleSubmit}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <div>
              <label htmlFor="inv-email" className="label" style={{ display: 'block', marginBottom: 6 }}>Work email</label>
              <input
                id="inv-email"
                type="email"
                className="dm-input"
                value={invite.email}
                readOnly
                autoComplete="email"
                aria-required="true"
                disabled={submitting}
                style={FIELD_STYLE}
              />
            </div>
            <div>
              <label htmlFor={PASSWORD_ID} className="label" style={{ display: 'block', marginBottom: 6 }}>Password</label>
              <input
                id={PASSWORD_ID}
                type="password"
                className={'dm-input' + (passwordError ? ' dm-err' : '')}
                value={password}
                onChange={(e) => {
                  setPassword(e.target.value)
                  setPasswordError(undefined)
                }}
                autoComplete="new-password"
                aria-required="true"
                aria-invalid={Boolean(passwordError)}
                aria-describedby={passwordError ? `${PASSWORD_ID}-error` : undefined}
                disabled={submitting}
                style={FIELD_STYLE}
              />
              {passwordError && <Alert id={`${PASSWORD_ID}-error`} text={passwordError} />}
            </div>
          </div>
          <button type="submit" disabled={submitting} className="ds-btn ds-btn--primary ds-btn--md" style={{ width: '100%', marginTop: 18 }}>
            {submitting ? (
              <>
                <span style={SPINNER_STYLE} />
                Creating…
              </>
            ) : (
              'Create account →'
            )}
          </button>
          {formError && <Alert text={formError} />}
          <p className="t-caption" style={{ textAlign: 'center', margin: '14px 0 0' }}>{PRODUCT_EMAIL_NOTICE}</p>
          <div style={{ marginTop: 14 }}>
            <Button variant="text" type="button" onClick={() => { setPassword(''); setPasswordError(undefined); setFormError(undefined); setView('ready') }} disabled={submitting} style={{ fontSize: 13 }}>
              Back
            </Button>
          </div>
        </form>
      </>
    )
  } else if (view === 'sent' && invite) {
    body = (
      <>
        <h3 style={{ ...HEADING_STYLE, margin: '0 0 10px' }}>Check your email</h3>
        <p className="t-body-sm" style={TEXT}>
          A confirmation link is on its way to {invite.email}. Open it and confirm your email, then come back to this page and choose Sign in. Already have an account? Sign in now.
        </p>
        <button type="button" onClick={() => resend()} disabled={resending} className="ds-btn ds-btn--outline ds-btn--md" style={{ width: '100%', marginTop: 18 }}>
          {resending ? 'Sending…' : 'Send the link again'}
        </button>
        <ResendNotice note={note} email={invite.email} />
        {signInButton}
      </>
    )
  }

  return (
    <div style={{ minHeight: '100vh', background: 'var(--bg-1)', fontFamily: 'var(--font-sans)', color: 'var(--fg-1)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}>
      <style>{MODAL_CHROME_CSS + DEMO_FORM_CSS}</style>
      <div style={modalCardStyle(452)}>
        <ModalHeader padX={18} />
        <div style={{ padding: '22px 20px 20px' }}>
          {view !== 'loading' && (
            <div style={{ marginBottom: 14 }}>
              <Eyebrow>INVITATION</Eyebrow>
            </div>
          )}
          {body}
        </div>
      </div>
    </div>
  )
}
