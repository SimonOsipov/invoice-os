// HubSpot Forms API integration for the Book-a-Demo lead-capture flow (LAND-02).
// Placed at src/, NOT src/components/, mirroring auth.ts: it is the landing's other
// module that resolves VITE_* and exports a null-when-unset contract. Config
// resolution stays INSIDE function bodies, never at module scope, so `vi.stubEnv`
// can drive it in tests (auth.ts:89-99's resolveBase convention). This module
// imports nothing from src/components/ — CONSENT_TEXT is passed in as an argument.
//
// STUB — LAND-02-01 test-spec stage (RED). Every function below throws; the real
// bodies land in the paired implementation commit. PRODUCTION_HOSTNAMES holds a
// deliberately wrong placeholder so any future spec asserting its value fails on
// assertion rather than on a hardcoded real hostname.

/** The hostnames that ARE the real production landing site. Exact match only. */
export const PRODUCTION_HOSTNAMES: readonly string[] = ['stub-placeholder.invalid']

export type HubSpotTarget = { portalId: string; formGuid: string }

/** Null when either var is unset or blank — mirrors auth.ts's resolveBase contract. */
export function hubspotTarget(): HubSpotTarget | null {
  throw new Error('not implemented')
}

/** Pure. No env, no DOM. Exact, case-insensitive, whitespace-trimmed equality. */
export function isProductionHost(_hostname: string, _allowlist?: readonly string[]): boolean {
  throw new Error('not implemented')
}

/** THE GATE. Returns the target only when the build carries ids AND the browser is on production. */
export function resolveSubmitTarget(_hostname: string): HubSpotTarget | null {
  throw new Error('not implemented')
}

/** EU regional Forms host. The global host fails silently in the browser for an EU portal. */
export function submissionUrl(_t: HubSpotTarget): string {
  throw new Error('not implemented')
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

/** Pure. Maps the seven answers onto contact properties. Emits nothing else, ever. */
export function buildSubmission(_lead: DemoLead, _consentText: string): HubSpotSubmission {
  throw new Error('not implemented')
}

/** POSTs. Resolves on 2xx, REJECTS on anything else. Writes nothing to the console. */
export function submitDemoLead(_t: HubSpotTarget, _lead: DemoLead, _consentText: string): Promise<void> {
  throw new Error('not implemented')
}
