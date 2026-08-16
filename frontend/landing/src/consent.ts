// Versioned localStorage consent gate (LAND-03-01). LAND-05 renders the notice and
// calls writeConsent; this module only owns the storage contract.

export const CONSENT_STORAGE_KEY = 'asc_consent'
export const CONSENT_VERSION = 1

/** No stored record ⇒ this answer. LAND-05 flips this one literal to `false`. */
export const CONSENT_DEFAULT_ANALYTICS: boolean = true

export type ConsentRecord = { analytics: boolean; ts: string; v: number }
export type ConsentStore = Pick<Storage, 'getItem' | 'setItem'>

export function parseConsent(_raw: string | null): ConsentRecord | null {
  throw new Error('not implemented')
}

export function readConsent(_store?: ConsentStore | null): ConsentRecord | null {
  throw new Error('not implemented')
}

export function writeConsent(
  _analytics: boolean,
  _store?: ConsentStore | null,
  _now?: Date,
): ConsentRecord {
  throw new Error('not implemented')
}

export function analyticsAllowed(_record: ConsentRecord | null): boolean {
  throw new Error('not implemented')
}
