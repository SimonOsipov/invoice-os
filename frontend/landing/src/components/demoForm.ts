// Pure, testable helpers for DemoModal's lead-capture form (task-117.1 / task-117.2,
// extended by LAND-02-01 for HubSpot lead capture — consent + taxpayer-size options
// + the splitName/firstNameOf refactor). Kept separate from the component so
// validation and the success-copy derivation can be reviewed/tested without
// rendering React.
//
// STUB — LAND-02-01 test-spec stage (RED) for the new/changed surface below.
// validateDemoForm, firstNameOf and splitName throw; the real bodies land in the
// paired implementation commit. `consent` is OPTIONAL on DemoFormValues so
// DemoModal.tsx's existing call (which never sets it) keeps compiling —
// DemoModal.tsx is untouched in this subtask (LAND-02-02 wires the checkbox).

export type DemoFormValues = {
  name: string
  email: string
  company: string
  consent?: boolean
}

export type DemoFormErrors = {
  name?: string
  email?: string
  company?: string
  consent?: string
}

// The four mandate turnover bands, in the regulator's enforcement order.
// STUB placeholder values — deliberately wrong so a spec asserting them fails on
// assertion rather than on `undefined is not iterable`.
export const TAXPAYER_SIZE_OPTIONS = ['STUB_TAXPAYER_A', 'STUB_TAXPAYER_B', 'STUB_TAXPAYER_C', 'STUB_TAXPAYER_D'] as const
export const DEFAULT_TAXPAYER_SIZE = 'STUB_TAXPAYER_B'

// The EXACT sentence the visitor is shown beside the consent checkbox AND the
// exact string sent as legalConsentOptions.consent.text. STUB placeholder value —
// deliberately wrong; the real copy lands with the implementation.
export const CONSENT_TEXT = 'STUB: consent copy not implemented yet'

// Only the three required fields validate — Role/Taxpayer size/Monthly invoices
// never block submit. An unchecked consent adds its own error key and must never
// mask the name/email/company errors.
export function validateDemoForm(_v: DemoFormValues): DemoFormErrors {
  throw new Error('not implemented')
}

// Trims, collapses runs of internal whitespace; the first token is firstName, the
// remainder (joined by single spaces) is lastName. Both '' for an empty/
// whitespace-only input.
export function splitName(_name: string): { firstName: string; lastName: string } {
  throw new Error('not implemented')
}

// First whitespace token of a trimmed name, e.g. "Ada Okafor" -> "Ada".
// Falls back to "there" when the name is empty/whitespace-only.
export function firstNameOf(_name: string): string {
  throw new Error('not implemented')
}
