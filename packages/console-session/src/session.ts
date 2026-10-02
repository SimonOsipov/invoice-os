export interface ConsoleSession {
  token: string
  refreshToken: string
}

export const CONSOLE_SESSION_SCHEMA_VERSION = 2

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

const nonEmpty = (v: unknown): v is string => typeof v === 'string' && v !== ''

// Null for absent; null plus one console.warn for anything that is not a v2 record.
export function parseStoredConsoleSession(raw: string | null, key: string): ConsoleSession | null {
  if (raw == null) {
    return null
  }
  try {
    const p = JSON.parse(raw) as { v?: unknown; token?: unknown; refresh_token?: unknown } | null
    if (p != null && p.v === CONSOLE_SESSION_SCHEMA_VERSION && nonEmpty(p.token) && nonEmpty(p.refresh_token)) {
      return { token: p.token, refreshToken: p.refresh_token }
    }
    console.warn(`[console-session] ignoring unusable stored session at "${key}"`)
    return null
  } catch (e) {
    console.warn(`[console-session] failed to parse stored session at "${key}":`, e)
    return null
  }
}

export function loadConsoleSession(key: string): ConsoleSession | null {
  try {
    return parseStoredConsoleSession(localStorage.getItem(key), key)
  } catch (e) {
    console.warn(`[console-session] failed to read stored session at "${key}":`, e)
    return null
  }
}

export function saveConsoleSession(key: string, s: ConsoleSession): void {
  try {
    localStorage.setItem(key, JSON.stringify({ v: CONSOLE_SESSION_SCHEMA_VERSION, token: s.token, refresh_token: s.refreshToken }))
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
