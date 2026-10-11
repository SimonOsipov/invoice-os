// The forgot view of the sign-in window: asks the gateway to mail a reset link.
import { useState } from 'react'
import type { FormEvent } from 'react'
import { RESET_SENT, requestPasswordReset } from '../passwordReset'
import { validateSignInForm } from '../signInForm'
import { DEMO_FORM_CSS } from './DemoLeadForm'
import { Button } from './ds/Button'
import { Alert, FIELD_STYLE, ResendNotice, SPINNER_STYLE } from './SignInForm'
import { useResend } from './useResend'

const ID = 'fp-form'

export function ForgotPasswordForm({ onBack }: { onBack: () => void }) {
  const [email, setEmail] = useState('')
  const [error, setError] = useState<string>()
  const { resending, note, resend } = useResend(undefined, requestPasswordReset)

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (resending) return
    // Only the email rule applies here; the password is a placeholder.
    const { email: invalid } = validateSignInForm({ email, password: 'x' })
    setError(invalid)
    if (invalid) {
      document.getElementById(`${ID}-email`)?.focus()
      return
    }
    await resend(email.trim())
  }

  return (
    <form noValidate onSubmit={handleSubmit}>
      <style>{DEMO_FORM_CSS}</style>
      <p className="t-body-sm" style={{ margin: '0 0 12px', lineHeight: 1.55 }}>Enter your work email. If it has an account, we will send a link to choose a new password.</p>
      <label htmlFor={`${ID}-email`} className="label" style={{ display: 'block', marginBottom: 6 }}>Work email</label>
      <input
        id={`${ID}-email`}
        type="email"
        className={'dm-input' + (error ? ' dm-err' : '')}
        value={email}
        onChange={(e) => {
          setEmail(e.target.value)
          setError(undefined)
        }}
        placeholder="you@company.com"
        autoComplete="email"
        aria-required="true"
        aria-invalid={Boolean(error)}
        aria-describedby={error ? `${ID}-email-error` : undefined}
        disabled={resending}
        style={FIELD_STYLE}
      />
      {error && <Alert id={`${ID}-email-error`} text={error} />}
      <button type="submit" disabled={resending} className="ds-btn ds-btn--primary ds-btn--md" style={{ width: '100%', marginTop: 18 }}>
        {resending ? (
          <>
            <span style={SPINNER_STYLE} />
            Sending…
          </>
        ) : (
          'Send reset link'
        )}
      </button>
      <ResendNotice note={note} email={email.trim()} text={RESET_SENT} peach />
      <div style={{ marginTop: 14 }}>
        <Button variant="text" type="button" onClick={onBack} disabled={resending} style={{ fontSize: 13 }}>
          Back to sign in
        </Button>
      </div>
    </form>
  )
}
