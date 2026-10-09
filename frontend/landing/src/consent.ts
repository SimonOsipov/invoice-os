// Versioned consent record: a cookie on the production hosts, `localStorage` elsewhere.
// The notice calls writeConsent; this module only owns the storage contract.
import { isProductionHost, LIBRARY_HOSTNAMES, SHARED_COOKIE_DOMAIN } from './hubspot'

export const CONSENT_STORAGE_KEY = 'asc_consent'
export const CONSENT_VERSION = 1

/** No stored record ⇒ this answer. Stays one literal, never a branch: consent.test.ts pins both. */
export const CONSENT_DEFAULT_ANALYTICS: boolean = false

export type ConsentRecord = { analytics: boolean; ts: string; v: number }
export type ConsentStore = Pick<Storage, 'getItem' | 'setItem'> & Partial<Pick<Storage, 'removeItem'>>
export type ConsentJar = Pick<Document, 'cookie'>

const COOKIE_MAX_AGE_S = 400 * 24 * 60 * 60 // Chrome's cap, counted from the choice

/** The shared cookie domain for an exact production host, else `null`. */
export function sharedConsentDomain(hostname: string): string | null {
  return isProductionHost(hostname) || isProductionHost(hostname, LIBRARY_HOSTNAMES)
    ? SHARED_COOKIE_DOMAIN
    : null
}

// Resolving the global throws outright when storage is disabled by policy.
function defaultStore(): ConsentStore | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

function defaultJar(): ConsentJar | null {
  try {
    return globalThis.document ?? null
  } catch {
    return null
  }
}

function defaultHostname(): string {
  return globalThis.location?.hostname ?? ''
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

function readLocal(store: ConsentStore | null): ConsentRecord | null {
  if (!store) return null

  let raw: string | null
  try {
    raw = store.getItem(CONSENT_STORAGE_KEY)
  } catch {
    return null
  }
  return parseConsent(raw)
}

function removeLocal(store: ConsentStore | null): void {
  try {
    store?.removeItem?.(CONSENT_STORAGE_KEY)
  } catch {
    // Best-effort.
  }
}

/** `null` means "no usable cookie record". */
export function readConsentCookie(jar: ConsentJar): ConsentRecord | null {
  try {
    for (const pair of jar.cookie.split(';')) {
      const eq = pair.indexOf('=')
      if (eq < 0 || pair.slice(0, eq).trim() !== CONSENT_STORAGE_KEY) continue
      return parseConsent(decodeURIComponent(pair.slice(eq + 1).trim()))
    }
  } catch {
    // A throwing getter or a bad escape is no record.
  }
  return null
}

function writeConsentCookie(record: ConsentRecord, jar: ConsentJar, domain: string): void {
  try {
    jar.cookie =
      `${CONSENT_STORAGE_KEY}=${encodeURIComponent(JSON.stringify(record))}; Domain=${domain}; ` +
      `Path=/; Max-Age=${COOKIE_MAX_AGE_S}; SameSite=Lax; Secure`
  } catch {
    // Cookies are best-effort.
  }
}

function sameRecord(a: ConsentRecord | null, b: ConsentRecord | null): boolean {
  return !!a && !!b && a.analytics === b.analytics && a.ts === b.ts
}

// The try wraps the getItem CALL, not a presence check: under native Node the global
// is present but its methods throw. Covered by "a present-but-unusable localStorage
// is not an error on either path".
export function readConsent(
  store: ConsentStore | null = defaultStore(),
  jar: ConsentJar | null = defaultJar(),
  hostname: string = defaultHostname(),
): ConsentRecord | null {
  const local = readLocal(store)
  const domain = sharedConsentDomain(hostname)
  if (!domain || !jar) return local

  const shared = readConsentCookie(jar)
  // A local record wins only when strictly newer; a tie keeps the cookie.
  const winner = local && (!shared || local.ts > shared.ts) ? local : shared
  if (!winner) return null
  if (winner !== shared) writeConsentCookie(winner, jar, domain) // migrate, never renew
  if (local && sameRecord(readConsentCookie(jar), winner)) removeLocal(store)
  return winner
}

// setItem fails where getItem succeeds (quota, private mode), so the write path
// carries its own guard. The record is built first and returned either way — a
// caller cannot tell a persisted write from an in-memory one.
export function writeConsent(
  analytics: boolean,
  store: ConsentStore | null = defaultStore(),
  now: Date = new Date(),
  jar: ConsentJar | null = defaultJar(),
  hostname: string = defaultHostname(),
): ConsentRecord {
  const record: ConsentRecord = { analytics, ts: now.toISOString(), v: CONSENT_VERSION }
  const domain = sharedConsentDomain(hostname)
  if (domain && jar) {
    writeConsentCookie(record, jar, domain)
    removeLocal(store) // a blocked cookie must not leave an older local answer in charge
  } else {
    try {
      store?.setItem(CONSENT_STORAGE_KEY, JSON.stringify(record))
    } catch {
      // Storage is best-effort.
    }
  }
  return record
}

export function analyticsAllowed(record: ConsentRecord | null): boolean {
  return record ? record.analytics : CONSENT_DEFAULT_ANALYTICS
}
