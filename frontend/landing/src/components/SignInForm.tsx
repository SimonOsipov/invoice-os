// Landing email/password sign-in. Posts to the gateway, then returns the code to whichever app asked.
import { useEffect, useState } from 'react'
import type { CSSProperties, FormEvent } from 'react'
import { DEMO_FORM_CSS, Glyph, WARN_PATHS } from './DemoLeadForm'
import { Button } from './ds/Button'
import { RESEND_FAILED, resendSentNotice } from '../register'
import { useResend } from './useResend'
import { bounceToStart, handoffUrl, isUnverified, SIGN_IN_UNAVAILABLE, signInErrorMessage, signInWithPassword, type ConsoleTarget } from '../signIn'
import { validateSignInForm, type SignInFormErrors } from '../signInForm'

const ID = 'si-form'

export const FIELD_STYLE: CSSProperties = { width: '100%', height: 42, background: 'var(--card)', border: '1px solid var(--input)', borderRadius: 'var(--radius)', padding: '0 13px', fontSize: 14, color: 'var(--ink)', fontFamily: 'var(--font-sans)' }
const ALERT_STYLE: CSSProperties = { display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--destructive)' }
const BUTTON_STYLE: CSSProperties = { width: '100%' }
export const SPINNER_STYLE: CSSProperties = { width: 15, height: 15, border: '2px solid color-mix(in srgb, var(--primary-foreground) 40%, transparent)', borderTopColor: 'var(--primary-foreground)', borderRadius: 'var(--radius-pill)', animation: 'dmSpin 0.7s linear infinite' }

export function Alert({ id, text }: { id?: string; text: string }) {
  return (
    <div id={id} role="alert" style={ALERT_STYLE}>
      <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {text}
    </div>
  )
}

// peach: ink on --accent, since peach text on white fails contrast.
const PEACH_NOTICE_STYLE: CSSProperties = { marginTop: 12, marginBottom: 0, padding: '10px 12px', borderRadius: 'var(--radius)', background: 'var(--accent)', color: 'var(--ink)', overflowWrap: 'anywhere' }

// The status region stays mounted while the resend control shows, so screen readers announce the text put into it.
export function ResendNotice({ note, email, text, peach }: { note?: { ok: boolean }; email: string; text?: string; peach?: boolean }) {
  return (
    <>
      <div role="status">
        {note?.ok && <p className="t-body-sm" style={peach ? PEACH_NOTICE_STYLE : { marginTop: 8, marginBottom: 0, overflowWrap: 'anywhere' }}>{text ?? resendSentNotice(email)}</p>}
      </div>
      {note?.ok === false && <Alert text={RESEND_FAILED} />}
    </>
  )
}

// heldState is read at submit: a missing state bounces to the app, and an expired state is never posted.
export function SignInForm({ heldState, initialError, consoleTarget, onForgot }: { heldState: () => string | null; initialError?: string; consoleTarget?: ConsoleTarget; onForgot?: () => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<SignInFormErrors>({})
  const [formError, setFormError] = useState(initialError)
  const [submitting, setSubmitting] = useState(false)
  const [unverified, setUnverified] = useState<string>()
  const { resending, note, resend, reset: resetResend } = useResend(unverified)

  // A back/forward-cache restore would otherwise show the page frozen mid-submit.
  useEffect(() => {
    const onShow = (e: PageTransitionEvent) => {
      if (!e.persisted) return
      setSubmitting(false)
      setPassword('')
      setFormError(undefined)
      setUnverified(undefined)
    }
    window.addEventListener('pageshow', onShow)
    return () => window.removeEventListener('pageshow', onShow)
  }, [])

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (submitting) return
    const next = validateSignInForm({ email, password })
    setErrors(next)
    if (next.email || next.password) {
      document.getElementById(`${ID}-${next.email ? 'email' : 'password'}`)?.focus()
      return
    }
    const live = heldState()
    setFormError(undefined)
    setUnverified(undefined)
    resetResend()
    setSubmitting(true)
    if (!live) {
      if (!(await bounceToStart(consoleTarget))) {
        setFormError(SIGN_IN_UNAVAILABLE)
        setSubmitting(false)
      }
      return
    }
    try {
      const url = handoffUrl(await signInWithPassword(email.trim(), password, live), consoleTarget)
      if (url) {
        window.location.href = url
        return
      }
    } catch (err) {
      setFormError(signInErrorMessage(err))
      if (isUnverified(err)) setUnverified(email.trim())
    }
    setSubmitting(false)
  }

  return (
    <form noValidate onSubmit={handleSubmit}>
      <style>{DEMO_FORM_CSS}</style>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        <div>
          <label htmlFor={`${ID}-email`} className="label" style={{ display: 'block', marginBottom: 6 }}>Work email</label>
          <input
            id={`${ID}-email`}
            type="email"
            className={'dm-input' + (errors.email ? ' dm-err' : '')}
            value={email}
            onChange={(e) => {
              setEmail(e.target.value)
              setErrors((prev) => ({ ...prev, email: undefined }))
            }}
            placeholder="you@company.com"
            autoComplete="email"
            aria-required="true"
            aria-invalid={Boolean(errors.email)}
            aria-describedby={errors.email ? `${ID}-email-error` : undefined}
            disabled={submitting}
            style={FIELD_STYLE}
          />
          {errors.email && <Alert id={`${ID}-email-error`} text={errors.email} />}
        </div>
        <div>
          <label htmlFor={`${ID}-password`} className="label" style={{ display: 'block', marginBottom: 6 }}>Password</label>
          <input
            id={`${ID}-password`}
            type="password"
            className={'dm-input' + (errors.password ? ' dm-err' : '')}
            value={password}
            onChange={(e) => {
              setPassword(e.target.value)
              setErrors((prev) => ({ ...prev, password: undefined }))
            }}
            autoComplete="current-password"
            aria-required="true"
            aria-invalid={Boolean(errors.password)}
            aria-describedby={errors.password ? `${ID}-password-error` : undefined}
            disabled={submitting}
            style={FIELD_STYLE}
          />
          {errors.password && <Alert id={`${ID}-password-error`} text={errors.password} />}
        </div>
      </div>
      {onForgot && (
        <div style={{ marginTop: 8 }}>
          <Button variant="text" type="button" onClick={onForgot} disabled={submitting} style={{ fontSize: 13 }}>
            Forgot password?
          </Button>
        </div>
      )}
      <button type="submit" disabled={submitting} className="ds-btn ds-btn--primary ds-btn--md" style={{ ...BUTTON_STYLE, marginTop: 18 }}>
        {submitting ? (
          <>
            <span style={SPINNER_STYLE} />
            Checking…
          </>
        ) : (
          'Sign in →'
        )}
      </button>
      {formError && <Alert text={formError} />}
      {unverified !== undefined && (
        <>
          <button type="button" onClick={() => resend()} disabled={resending} className="ds-btn ds-btn--outline ds-btn--md" style={{ ...BUTTON_STYLE, marginTop: 12 }}>
            {resending ? 'Sending…' : 'Send the link again'}
          </button>
          <ResendNotice note={note} email={unverified} />
        </>
      )}
    </form>
  )
}
