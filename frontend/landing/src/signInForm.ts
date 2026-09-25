// Pure validator for the landing sign-in form (AUTH-05 D7).
import { EMAIL_RE } from './components/demoForm'

export type SignInFormValues = {
  email: string
  password: string
}

export type SignInFormErrors = {
  email?: string
  password?: string
}

// The password is never trimmed: surrounding spaces are part of it.
export function validateSignInForm(v: SignInFormValues): SignInFormErrors {
  const errors: SignInFormErrors = {}
  const email = v.email.trim()

  if (!email) errors.email = 'Enter your work email.'
  else if (!EMAIL_RE.test(email)) errors.email = 'Enter a valid work email address.'

  if (!v.password) errors.password = 'Enter your password.'

  return errors
}
