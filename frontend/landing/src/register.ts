// Landing registration client.
import { ApiError, apiFetch, gatewayBase } from '@invoice-os/api-client/client'

import { MARKETING_CONSENT_TEXT } from './components/MarketingConsent'
import { EMAIL_RE } from './components/demoForm'
import { signInConfigured } from './signIn'

// Copy of the isFreeMail guard literal in internal/gateway/register.go; e2e/registrationCopy.test.ts pins them equal.
export const FREE_MAIL_REFUSED = 'a business email address is required; personal email providers are not accepted'

const CLOSED = 'Registration is not open yet.'
const THROTTLED = 'Too many attempts. Try again in a minute.'
const UNAVAILABLE = 'Registration is unavailable right now. Try again shortly.'
const NAME_MAX = 200

export type RegisterKind = 'firm' | 'in_house'

export type RegisterValues = {
  email: string
  password: string
  displayName: string
  workspaceName: string
  kind: RegisterKind | ''
  marketing: boolean
}

export type RegisterErrors = Partial<Record<keyof RegisterValues, string>>

export type RegisterOutcome = { field: 'email'; message: string } | { form: string }

export function registrationOpen(): boolean {
  return import.meta.env.VITE_REGISTRATION_OPEN === 'true' && signInConfigured()
}

// Names count in code points, as the gateway counts runes.
function nameError(name: string, empty: string): string | undefined {
  const n = [...name.trim()].length
  if (n === 0) return empty
  if (n > NAME_MAX) return `Use ${NAME_MAX} characters or fewer.`
  return undefined
}

export function validateRegisterForm(v: RegisterValues): RegisterErrors {
  const errors: RegisterErrors = {}
  const email = v.email.trim()
  if (!email) errors.email = 'Enter your work email.'
  else if (!EMAIL_RE.test(email)) errors.email = 'Enter a valid work email address.'
  // The password is never trimmed: surrounding spaces are part of it.
  if (!v.password) errors.password = 'Choose a password.'
  const display = nameError(v.displayName, 'Enter your name.')
  if (display) errors.displayName = display
  const workspace = nameError(v.workspaceName, 'Enter your company or workspace name.')
  if (workspace) errors.workspaceName = workspace
  if (!v.kind) errors.kind = 'Choose how this workspace files invoices.'
  return errors
}

export async function registerAccount(v: RegisterValues): Promise<void> {
  const base = gatewayBase()
  if (!base) throw new ApiError('malformed', 'gateway not configured')
  await apiFetch<unknown>(`${base}/auth/register`, {
    method: 'POST',
    body: {
      email: v.email.trim(),
      password: v.password,
      display_name: v.displayName.trim(),
      workspace_name: v.workspaceName.trim(),
      kind: v.kind,
      ...(v.marketing && { marketing_consent_text: MARKETING_CONSENT_TEXT }),
    },
  })
}

export function registerOutcome(err: unknown): RegisterOutcome {
  if (err instanceof ApiError && err.kind === 'http') {
    if (err.status === 400) {
      return err.message === FREE_MAIL_REFUSED ? { field: 'email', message: FREE_MAIL_REFUSED } : { form: err.message.trim() || UNAVAILABLE }
    }
    if (err.status === 503) return { form: CLOSED }
    if (err.status === 429) return { form: THROTTLED }
  }
  return { form: UNAVAILABLE }
}
