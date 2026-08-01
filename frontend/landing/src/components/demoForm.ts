// Pure, testable helpers for DemoModal's lead-capture form (task-117.1 / task-117.2,
// extended by LAND-02-01 for HubSpot lead capture — consent + taxpayer-size options
// + the splitName/firstNameOf refactor). Kept separate from the component so
// validation and the success-copy derivation can be reviewed/tested without
// rendering React.
//
// `consent` is REQUIRED (tightened from optional by LAND-02-02, once DemoModal's
// DEFAULT_FORM started carrying `consent: false`): an unchecked box is now always a
// real `false`, never an absent key, so no caller can reach validateDemoForm's
// fail-closed branch by omission alone.

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
