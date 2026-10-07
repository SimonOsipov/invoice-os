// Landing registration window: the sign-in window's chrome around a form that posts to the gateway.

import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { registerAccount, registerOutcome, validateRegisterForm, type RegisterErrors, type RegisterKind, type RegisterValues } from '../register'
import { DEMO_FORM_CSS } from './DemoLeadForm'
import { Alert, FIELD_STYLE, ResendNotice } from './SignInForm'
import { useResend } from './useResend'
import { HEADING_STYLE } from './SignInModal'
import { MarketingConsent } from './MarketingConsent'
import { Eyebrow } from './ds/Eyebrow'
import { MODAL_CHROME_CSS, MODAL_SCRIM_STYLE, ModalHeader, modalCardStyle } from './modalChrome'

const ID = 'reg'
const KINDS: { value: RegisterKind; label: string }[] = [
  { value: 'firm', label: 'For clients — an accounting or tax firm' },
  { value: 'in_house', label: 'For our own company — in-house' },
]
export const PRODUCT_EMAIL_NOTICE = 'We will email you about your account and the service. This is part of using ASComply Africa.'
const EMPTY: RegisterValues = { email: '', password: '', displayName: '', workspaceName: '', kind: '', marketing: false }
const FIELD_ORDER = ['email', 'password', 'displayName', 'workspaceName'] as const
const FIELD_IDS = { email: `${ID}-email`, password: `${ID}-password`, displayName: `${ID}-name`, workspaceName: `${ID}-workspace` }

export function RegisterModal({ onClose }: { onClose: () => void }) {
  const [values, setValues] = useState<RegisterValues>(EMPTY)
  const [errors, setErrors] = useState<RegisterErrors>({})
  const [formError, setFormError] = useState<string>()
  const [submitting, setSubmitting] = useState(false)
  const [sentTo, setSentTo] = useState<string>()
  const { resending, note, resend } = useResend(sentTo)

  // Close on Escape (never a native dialog).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  function edit(key: keyof RegisterValues, value: string) {
    setValues((prev) => ({ ...prev, [key]: value }))
    setErrors((prev) => ({ ...prev, [key]: undefined }))
  }

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (submitting) return
    const next = validateRegisterForm(values)
    setErrors(next)
    const firstField = FIELD_ORDER.find((k) => next[k])
    if (firstField) {
      document.getElementById(FIELD_IDS[firstField])?.focus()
      return
    }
    if (next.kind) {
      document.getElementById(`${ID}-kind-firm`)?.focus()
      return
    }
    setFormError(undefined)
    setSubmitting(true)
    try {
      await registerAccount(values)
      setSentTo(values.email.trim())
      return
    } catch (err) {
      const outcome = registerOutcome(err)
      if ('field' in outcome) setErrors({ email: outcome.message })
      else setFormError(outcome.form)
    }
    setSubmitting(false)
  }

  function field(key: (typeof FIELD_ORDER)[number], label: string, type: string, autoComplete: string, placeholder?: string) {
    const id = FIELD_IDS[key]
    const error = errors[key]
    return (
      <div>
        <label htmlFor={id} className="label" style={{ display: 'block', marginBottom: 6 }}>{label}</label>
        <input
          id={id}
          type={type}
          className={'dm-input' + (error ? ' dm-err' : '')}
          value={values[key]}
          onChange={(e) => edit(key, e.target.value)}
          placeholder={placeholder}
          autoComplete={autoComplete}
          aria-required="true"
          aria-invalid={Boolean(error)}
          aria-describedby={error ? `${id}-error` : undefined}
          disabled={submitting}
          style={FIELD_STYLE}
        />
        {error && <Alert id={`${id}-error`} text={error} />}
      </div>
    )
  }

  return (
    <div onClick={onClose} role="dialog" aria-modal="true" aria-label="Create an account" style={MODAL_SCRIM_STYLE}>
      <style>{MODAL_CHROME_CSS + DEMO_FORM_CSS}</style>
      <div onClick={(e) => e.stopPropagation()} style={modalCardStyle(452)}>
        <ModalHeader onClose={onClose} padX={18} />
        <div style={{ padding: '22px 20px 20px' }}>
          <div style={{ marginBottom: 14 }}>
            <Eyebrow>CREATE AN ACCOUNT</Eyebrow>
          </div>
          {sentTo !== undefined ? (
            <>
              <h3 style={{ ...HEADING_STYLE, margin: '0 0 10px' }}>Check your email</h3>
              <p className="t-body-sm" style={{ margin: 0, lineHeight: 1.55, overflowWrap: 'anywhere' }}>
                If this address can be registered, a confirmation link is on its way to {sentTo}. Open it, confirm your email, then sign in.
              </p>
              <button type="button" onClick={() => resend()} disabled={resending} className="ds-btn ds-btn--outline ds-btn--md" style={{ width: '100%', marginTop: 18 }}>
                {resending ? 'Sending…' : 'Send the link again'}
              </button>
              <ResendNotice note={note} email={sentTo} />
              <button type="button" onClick={onClose} className="ds-btn ds-btn--outline ds-btn--md" style={{ width: '100%', marginTop: 10 }}>
                Close
              </button>
            </>
          ) : (
            <>
              <h3 style={{ ...HEADING_STYLE, margin: '0 0 16px' }}>Create your workspace</h3>
              <form noValidate onSubmit={handleSubmit}>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
                  {field('email', 'Work email', 'email', 'email', 'you@company.com')}
                  {field('password', 'Password', 'password', 'new-password')}
                  {field('displayName', 'Your name', 'text', 'name')}
                  {field('workspaceName', 'Workspace name', 'text', 'organization')}
                  <fieldset
                    disabled={submitting}
                    aria-describedby={errors.kind ? `${ID}-kind-error` : undefined}
                    style={{ border: 0, margin: 0, padding: 0, minWidth: 0 }}
                  >
                    <legend className="label" style={{ padding: 0, marginBottom: 6 }}>How will this workspace file invoices?</legend>
                    {KINDS.map((k) => (
                      <label key={k.value} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '4px 0', fontSize: 14, color: 'var(--ink)', cursor: 'pointer' }}>
                        <input
                          id={k.value === 'firm' ? `${ID}-kind-firm` : undefined}
                          type="radio"
                          name="reg-kind"
                          value={k.value}
                          checked={values.kind === k.value}
                          onChange={() => edit('kind', k.value)}
                          style={{ accentColor: 'var(--primary)', margin: 0 }}
                        />
                        {k.label}
                      </label>
                    ))}
                    {errors.kind && <Alert id={`${ID}-kind-error`} text={errors.kind} />}
                  </fieldset>
                  <MarketingConsent id={`${ID}-marketing`} checked={values.marketing} onChange={(marketing) => setValues((prev) => ({ ...prev, marketing }))} disabled={submitting} />
                </div>
                <button type="submit" disabled={submitting} className="ds-btn ds-btn--primary ds-btn--md" style={{ width: '100%', marginTop: 18 }}>
                  {submitting ? (
                    <>
                      <span style={{ width: 15, height: 15, border: '2px solid color-mix(in srgb, var(--primary-foreground) 40%, transparent)', borderTopColor: 'var(--primary-foreground)', borderRadius: 'var(--radius-pill)', animation: 'dmSpin 0.7s linear infinite' }} />
                      Creating…
                    </>
                  ) : (
                    'Create account →'
                  )}
                </button>
                {formError && <Alert text={formError} />}
                <p className="t-caption" style={{ textAlign: 'center', margin: '14px 0 0' }}>{PRODUCT_EMAIL_NOTICE}</p>
              </form>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
