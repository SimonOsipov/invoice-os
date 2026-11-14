// borrowed: the access token comes from the app and cannot be renewed here.
// legacyRefreshToken: read from a pre-v3 record, to be adopted into the cookie by the next renewal; never saved.
export interface ConsoleSession {
  token: string
  borrowed: boolean
  legacyRefreshToken?: string
}

export const CONSOLE_SESSION_SCHEMA_VERSION = 3

// Unverified read of a three-part JWT's payload; null for anything else.
export function decodeJwtPayload(token: string | null): Record<string, unknown> | null {
  const parts = token?.split('.')
  if (parts?.length !== 3 || !parts[1]) {
    return null
  }
  try {
    const b64 = parts[1].replace(/-/g, '+').replace(/_/g, '/')
    const claims: unknown = JSON.parse(atob(b64.padEnd(Math.ceil(b64.length / 4) * 4, '=')))
    return claims !== null && typeof claims === 'object' && !Array.isArray(claims) ? (claims as Record<string, unknown>) : null
  } catch {
    return null
  }
}

// Display only: the gateway decides access.
export function isStaffToken(token: string | null): boolean {
  const meta = decodeJwtPayload(token)?.app_metadata
  return meta !== null && typeof meta === 'object' && !Array.isArray(meta) && (meta as { staff?: unknown }).staff === true
}

export const nonEmpty = (v: unknown): v is string => typeof v === 'string' && v !== ''

const unusable = (key: string): null => {
  console.warn(`[console-session] ignoring unusable stored session at "${key}"`)
  return null
}

// Null for absent; null plus one console.warn for anything that is not a v3 record.
export function parseStoredConsoleSession(raw: string | null, key: string): ConsoleSession | null {
  if (raw == null) {
    return null
  }
  try {
    const p = JSON.parse(raw) as { v?: unknown; token?: unknown; from?: unknown } | null
    if (p != null && p.v === CONSOLE_SESSION_SCHEMA_VERSION && nonEmpty(p.token) && Object.keys(p).every((k) => ['v', 'token', 'from'].includes(k))) {
      if (!('from' in p)) return { token: p.token, borrowed: false }
      if (p.from === 'app') return { token: p.token, borrowed: true }
    }
    return unusable(key)
  } catch (e) {
    console.warn(`[console-session] failed to parse stored session at "${key}":`, e)
    return null
  }
}

// ceiling: v2 records are read until every console has renewed once; the landing record keeps its refresh token
// in storage until the adoption refresh answers 200 (the boot rewrites it then).
function readLegacy(raw: string, key: string): ConsoleSession | null | undefined {
  const p = JSON.parse(raw) as { v?: unknown; token?: unknown; refresh_token?: unknown; from?: unknown } | null
  if (p == null || !nonEmpty(p.token)) return undefined
  if (p.v === 2) {
    if (nonEmpty(p.refresh_token) && !('from' in p)) return { token: p.token, borrowed: false, legacyRefreshToken: p.refresh_token }
    if (!('refresh_token' in p) && p.from === 'app') {
      const s = { token: p.token, borrowed: true }
      saveConsoleSession(key, s)
      return s
    }
  }
  if (p.v === CONSOLE_SESSION_SCHEMA_VERSION && 'refresh_token' in p) {
    clearConsoleSession(key)
    return unusable(key)
  }
  return undefined
}

export function loadConsoleSession(key: string): ConsoleSession | null {
  try {
    const raw = localStorage.getItem(key)
    if (raw != null) {
      try {
        const legacy = readLegacy(raw, key)
        if (legacy !== undefined) return legacy
      } catch {
        // not JSON: the strict parse below warns once
      }
    }
    return parseStoredConsoleSession(raw, key)
  } catch (e) {
    console.warn(`[console-session] failed to read stored session at "${key}":`, e)
    return null
  }
}

// Writes the access token only; a refresh token never reaches storage.
export function saveConsoleSession(key: string, s: ConsoleSession): void {
  try {
    localStorage.setItem(key, JSON.stringify({ v: CONSOLE_SESSION_SCHEMA_VERSION, token: s.token, ...(s.borrowed ? { from: 'app' } : {}) }))
  } catch (e) {
    console.warn(`[console-session] failed to store session at "${key}":`, e)
  }
}

export function clearConsoleSession(key: string): void {
  try {
    localStorage.removeItem(key)
  } catch (e) {
    console.warn(`[console-session] failed to clear stored session at "${key}":`, e)
  }
}
