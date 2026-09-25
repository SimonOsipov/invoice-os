// Landing email/password sign-in (AUTH-05 D7, D25). Posts to the gateway, then hands the code to the app.
import { useEffect, useState } from 'react'
import type { CSSProperties, FormEvent } from 'react'
import { DEMO_FORM_CSS, Glyph, WARN_PATHS } from './DemoLeadForm'
import { handoffUrl, signInErrorMessage, signInWithPassword, startUrl } from '../signIn'
import { validateSignInForm, type SignInFormErrors } from '../signInForm'

const ID = 'si-form'

const FIELD_STYLE: CSSProperties = { width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', padding: '0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)' }
const ALERT_STYLE: CSSProperties = { display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--status-red-text)' }
const BUTTON_STYLE: CSSProperties = { width: '100%', justifyContent: 'center', height: 44, cursor: 'pointer', gap: 9 }

function Alert({ id, text }: { id?: string; text: string }) {
  return (
    <div id={id} role="alert" style={ALERT_STYLE}>
      <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {text}
    </div>
  )
}

export function SignInForm({ state, initialError }: { state: string | null; initialError?: string }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<SignInFormErrors>({})
  const [formError, setFormError] = useState(initialError)
  const [submitting, setSubmitting] = useState(false)

  // A back/forward-cache restore would otherwise show the page frozen mid-submit.
  useEffect(() => {
    const onShow = (e: PageTransitionEvent) => {
      if (!e.persisted) return
      setSubmitting(false)
      setPassword('')
      setFormError(undefined)
    }
    window.addEventListener('pageshow', onShow)
    return () => window.removeEventListener('pageshow', onShow)
  }, [])

  // No state in memory: the app mints one and bounces back with ?state= (D25 step 3).
  if (!state) {
    return (
      <div>
        <style>{DEMO_FORM_CSS}</style>
        <button
          type="button"
          onClick={() => {
            const url = startUrl()
            if (url) window.location.href = url
          }}
          className="v2-btn v2-btn-primary"
          style={BUTTON_STYLE}
        >
          Continue with email
        </button>
        {formError && <Alert text={formError} />}
      </div>
    )
  }

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (submitting || !state) return
    const next = validateSignInForm({ email, password })
    setErrors(next)
    if (next.email || next.password) {
      document.getElementById(`${ID}-${next.email ? 'email' : 'password'}`)?.focus()
      return
    }
    setFormError(undefined)
    setSubmitting(true)
    try {
      const url = handoffUrl(await signInWithPassword(email.trim(), password, state))
      if (url) {
        window.location.href = url
        return
      }
    } catch (err) {
      setFormError(signInErrorMessage(err))
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
      <button type="submit" disabled={submitting} className="v2-btn v2-btn-primary" style={{ ...BUTTON_STYLE, marginTop: 18 }}>
        {submitting ? (
          <>
            <span style={{ width: 15, height: 15, border: '2px solid color-mix(in oklch, var(--text-on-dark) 40%, transparent)', borderTopColor: 'var(--text-on-dark)', borderRadius: 99, animation: 'dmSpin 0.7s linear infinite' }} />
            Checking…
          </>
        ) : (
          'Sign in →'
        )}
      </button>
      {formError && <Alert text={formError} />}
    </form>
  )
}
