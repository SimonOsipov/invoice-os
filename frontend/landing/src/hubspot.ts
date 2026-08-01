// HubSpot Forms API integration for the Book-a-Demo lead-capture flow (LAND-02).
// Placed at src/, NOT src/components/, mirroring auth.ts: it is the landing's other
// module that resolves VITE_* and exports a null-when-unset contract. Config
// resolution stays INSIDE function bodies, never at module scope, so `vi.stubEnv`
// can drive it in tests (auth.ts:89-99's resolveBase convention). This module
// imports nothing from src/components/ — CONSENT_TEXT is passed in as an argument.

/** The hostnames that ARE the real production landing site. Exact match only. */
export const PRODUCTION_HOSTNAMES: readonly string[] = ['www.ascomply.com']

export type HubSpotTarget = { portalId: string; formGuid: string }

const normaliseHost = (hostname: string): string => hostname.trim().toLowerCase()

/** Null when either var is unset or blank — mirrors auth.ts's resolveBase contract. */
export function hubspotTarget(): HubSpotTarget | null {
  // Read inside the body, never at module scope: a module-scope read is baked at
  // import time and cannot be driven by vi.stubEnv (auth.ts:93-96).
  const portalId = (import.meta.env.VITE_HUBSPOT_PORTAL_ID ?? '').trim()
  const formGuid = (import.meta.env.VITE_HUBSPOT_FORM_GUID ?? '').trim()
  if (!portalId || !formGuid) return null
  return { portalId, formGuid }
}

/** Pure. No env, no DOM. Exact, case-insensitive, whitespace-trimmed equality. */
export function isProductionHost(
  hostname: string,
  allowlist: readonly string[] = PRODUCTION_HOSTNAMES,
): boolean {
  const candidate = normaliseHost(hostname)
  if (!candidate) return false
  // Exact equality ONLY. Never endsWith/includes/startsWith/regex: `includes`
  // would admit www.ascomply.com.attacker.example, `endsWith` the same.
  return allowlist.some((allowed) => normaliseHost(allowed) === candidate)
}

/** THE GATE. Returns the target only when the build carries ids AND the browser is on production. */
export function resolveSubmitTarget(hostname: string): HubSpotTarget | null {
  if (!isProductionHost(hostname)) return null
  return hubspotTarget()
}

/** EU regional Forms host. The global host fails silently in the browser for an EU portal. */
export function submissionUrl(t: HubSpotTarget): string {
  return `https://api-eu1.hsforms.com/submissions/v3/integration/submit/${t.portalId}/${t.formGuid}`
}

export type DemoLead = {
  name: string
  email: string
  company: string
  role: string
  size: string
  volume: string
  consent: boolean
}

export type HubSpotField = { objectTypeId: '0-1'; name: string; value: string }
export type HubSpotSubmission = {
  fields: HubSpotField[]
  legalConsentOptions: {
    consent: { consentToProcess: boolean; text: string; communications: [] }
  }
}

// Local, deliberately duplicated from demoForm.ts's splitName: hubspot.ts imports
// nothing from src/components/ (the wire contract must not depend on form
// semantics), and DemoLead carries the single `name` the visitor typed.
function splitLeadName(name: string): { firstName: string; lastName: string } {
  const tokens = name.trim().split(/\s+/).filter(Boolean)
  return { firstName: tokens[0] ?? '', lastName: tokens.slice(1).join(' ') }
}

/** Pure. Maps the seven answers onto contact properties. Emits nothing else, ever. */
export function buildSubmission(lead: DemoLead, consentText: string): HubSpotSubmission {
  const { firstName, lastName } = splitLeadName(lead.name)
  // A FIXED LITERAL of the seven mapped property names. The payload is built by
  // walking this list, so the function is structurally incapable of emitting an
  // eighth key however many extra properties the caller's object carries.
  const mapped: readonly (readonly [string, string])[] = [
    ['firstname', firstName],
    ['lastname', lastName],
    ['email', lead.email],
    ['company', lead.company],
    ['jobtitle', lead.role],
    ['company_size', lead.size],
    ['monthly_invoice_volume', lead.volume],
  ]

  const fields: HubSpotField[] = []
  for (const [name, raw] of mapped) {
    const value = (raw ?? '').trim()
    // Empty values are DROPPED, not sent blank: a repeat submit from the same
    // email must never blank a value HubSpot already holds.
    if (!value) continue
    fields.push({ objectTypeId: '0-1', name, value })
  }

  // No `context` key — hutk/pageUri/pageName are out of scope for LAND-02.
  // `communications: []` records process-consent only, no marketing subscription.
  return {
    fields,
    legalConsentOptions: {
      consent: { consentToProcess: lead.consent, text: consentText, communications: [] },
    },
  }
}

/** POSTs. Resolves on 2xx, REJECTS on anything else. Writes nothing to the console. */
export async function submitDemoLead(
  t: HubSpotTarget,
  lead: DemoLead,
  consentText: string,
): Promise<void> {
  const res = await fetch(submissionUrl(t), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(buildSubmission(lead, consentText)),
    signal: AbortSignal.timeout(15_000),
  })
  // Status only, NEVER a field value — the rejection message is surfaced nowhere
  // near a log sink, and must not carry the visitor's email or company.
  if (!res.ok) throw new Error('hubspot ' + res.status)
}
