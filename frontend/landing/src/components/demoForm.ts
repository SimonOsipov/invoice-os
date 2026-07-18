// Pure, testable helpers for DemoModal's lead-capture form (task-117.1 / task-117.2).
// Kept separate from the component so validation and the success-copy derivation
// can be reviewed/tested without rendering React.

export type DemoFormValues = {
  name: string
  email: string
  company: string
}

export type DemoFormErrors = {
  name?: string
  email?: string
  company?: string
}

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

// Only the three required fields validate — Role/Taxpayer size/Monthly invoices
// never block submit.
export function validateDemoForm(v: DemoFormValues): DemoFormErrors {
  const errors: DemoFormErrors = {}

  if (!v.name.trim()) errors.name = 'Enter your full name.'

  if (!v.email.trim()) errors.email = 'Enter your work email.'
  else if (!EMAIL_RE.test(v.email.trim())) errors.email = 'Enter a valid work email address.'

  if (!v.company.trim()) errors.company = 'Enter your company name.'

  return errors
}

// First whitespace token of a trimmed name, e.g. "Ada Okafor" -> "Ada".
// Falls back to "there" when the name is empty/whitespace-only.
export function firstNameOf(name: string): string {
  const trimmed = name.trim()
  if (!trimmed) return 'there'
  return trimmed.split(/\s+/)[0]
}
