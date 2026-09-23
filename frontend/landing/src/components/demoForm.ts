// Pure data and helpers for the Book-a-Demo form, kept out of DemoLeadForm.tsx so
// validation and the success-copy derivation are testable without rendering React.
//
// `consent` is REQUIRED: DEFAULT_FORM below always carries a real `false`, so no
// caller reaches validateDemoForm's fail-closed branch by omitting the key.

export type DemoFormValues = {
  name: string
  email: string
  company: string
  consent: boolean
}

export type DemoFormErrors = {
  name?: string
  email?: string
  company?: string
  consent?: string
}

export const ROLE_OPTIONS = ['Owner / Partner', 'Finance or Accounting lead', 'Tax / Compliance', 'Developer / IT', 'Other']
export const VOLUME_OPTIONS = ['under 1k', '1k–10k', '10k–100k', '100k+']

// The four mandate turnover bands, in the regulator's enforcement order.
export const TAXPAYER_SIZE_OPTIONS = [
  'Large ₦5bn+',
  'Medium ₦1bn–₦5bn',
  'Small ₦50m–₦1bn',
  'Below ₦50m',
] as const
export const DEFAULT_TAXPAYER_SIZE = 'Medium ₦1bn–₦5bn'

// The EXACT sentence the visitor is shown beside the consent checkbox AND the exact
// string sent as legalConsentOptions.consent.text. One constant, two consumers —
// that is the mechanism that makes "the exact wording the visitor was shown" true
// rather than a promise.
export const CONSENT_TEXT =
  'I agree to ASComply Africa storing and processing my details so a compliance specialist can contact me about this demo request.'

export const DEFAULT_FORM = {
  name: '',
  email: '',
  company: '',
  role: 'Finance or Accounting lead',
  size: DEFAULT_TAXPAYER_SIZE,
  volume: '1k–10k',
  consent: false,
}

export type DemoFormState = typeof DEFAULT_FORM
// Every key except the one boolean — setField carries strings, setConsent the box.
export type DemoFieldKey = Exclude<keyof DemoFormState, 'consent'>
export type DemoStep = 'form' | 'submitting' | 'success' | 'error'

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

// Only the three required fields validate — Role/Taxpayer size/Monthly invoices
// never block submit. An unchecked consent adds its own error key and must never
// mask the name/email/company errors.
export function validateDemoForm(v: DemoFormValues): DemoFormErrors {
  const errors: DemoFormErrors = {}

  if (!v.name.trim()) errors.name = 'Enter your full name.'

  if (!v.email.trim()) errors.email = 'Enter your work email.'
  else if (!EMAIL_RE.test(v.email.trim())) errors.email = 'Enter a valid work email address.'

  if (!v.company.trim()) errors.company = 'Enter your company name.'

  // Fails closed: an absent `consent` is treated exactly like an unchecked one.
  if (!v.consent) errors.consent = 'Please confirm you agree before we can contact you.'

  return errors
}

// Trims, collapses runs of internal whitespace; the first token is firstName, the
// remainder (joined by single spaces) is lastName. Both '' for an empty/
// whitespace-only input.
export function splitName(name: string): { firstName: string; lastName: string } {
  const tokens = name.trim().split(/\s+/).filter(Boolean)
  return { firstName: tokens[0] ?? '', lastName: tokens.slice(1).join(' ') }
}

// First whitespace token of a trimmed name, e.g. "Ada Okafor" -> "Ada".
// Falls back to "there" when the name is empty/whitespace-only.
export function firstNameOf(name: string): string {
  return splitName(name).firstName || 'there'
}
