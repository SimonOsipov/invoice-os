// Versioned localStorage consent gate. LAND-05 renders the notice and calls
// writeConsent; this module only owns the storage contract.

export const CONSENT_STORAGE_KEY = 'asc_consent'
export const CONSENT_VERSION = 1

/** No stored record ⇒ this answer. Stays one literal, never a branch: consent.test.ts pins both. */
export const CONSENT_DEFAULT_ANALYTICS: boolean = false

export type ConsentRecord = { analytics: boolean; ts: string; v: number }
export type ConsentStore = Pick<Storage, 'getItem' | 'setItem'>

// Resolving the global throws outright when storage is disabled by policy.
function defaultStore(): ConsentStore | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

/** `null` means "no usable stored choice" — the caller applies the default. */
export function parseConsent(raw: string | null): ConsentRecord | null {
  if (!raw) return null

  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }

  // `JSON.parse('null')` yields null, which is typeof 'object'.
  if (typeof parsed !== 'object' || parsed === null) return null

  const candidate = parsed as Record<string, unknown>
  if (candidate.v !== CONSENT_VERSION) return null
  if (typeof candidate.analytics !== 'boolean') return null

  // Rebuilt, never the parsed object, so unknown stored keys cannot leak through.
  return {
    analytics: candidate.analytics,
    ts: typeof candidate.ts === 'string' ? candidate.ts : '',
    v: CONSENT_VERSION,
  }
}

// The try wraps the getItem CALL, not a presence check: under native Node the global
// is present but its methods throw. Covered by "a present-but-unusable localStorage
// is not an error on either path".
export function readConsent(store: ConsentStore | null = defaultStore()): ConsentRecord | null {
  if (!store) return null

  let raw: string | null
  try {
    raw = store.getItem(CONSENT_STORAGE_KEY)
  } catch {
    return null
  }
  return parseConsent(raw)
}

// setItem fails where getItem succeeds (quota, private mode), so the write path
// carries its own guard. The record is built first and returned either way — a
// caller cannot tell a persisted write from an in-memory one.
export function writeConsent(
  analytics: boolean,
  store: ConsentStore | null = defaultStore(),
  now: Date = new Date(),
): ConsentRecord {
  const record: ConsentRecord = { analytics, ts: now.toISOString(), v: CONSENT_VERSION }
  try {
    store?.setItem(CONSENT_STORAGE_KEY, JSON.stringify(record))
  } catch {
    // Storage is best-effort.
  }
  return record
}

export function analyticsAllowed(record: ConsentRecord | null): boolean {
  return record ? record.analytics : CONSENT_DEFAULT_ANALYTICS
}
