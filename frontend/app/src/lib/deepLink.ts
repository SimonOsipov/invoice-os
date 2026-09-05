// sessionStorage-backed deep-link capture. Mirrors lib/session.ts's namespaced-key,
// versioned-blob, warn-never-error conventions, scoped to a tab instead of the browser.

export const DEEP_LINK_KEY = 'invoice-os.deepLink'
export const DEEP_LINK_SCHEMA_VERSION = 1
export const DEEP_LINK_TTL_MS = 10 * 60_000

// Shared by capture (what may be written) and read (what a stored path must still look
// like): app-relative, not the bare root, not protocol-relative.
function isCapturablePath(path: string): boolean {
  return path !== '/' && path.startsWith('/') && !path.startsWith('//')
}

// Silent no-op for a non-capturable path. try/catch guards real-browser failure modes —
// quota exceeded, disabled storage in a sandboxed iframe, Safari private mode.
export function captureDestination(pathname: string, now: number = Date.now()): void {
  if (!isCapturablePath(pathname)) {
    return
  }
  try {
    sessionStorage.setItem(
      DEEP_LINK_KEY,
      JSON.stringify({ v: DEEP_LINK_SCHEMA_VERSION, path: pathname, at: now }),
    )
  } catch (e) {
    console.warn(`[deepLink] failed to store destination at "${DEEP_LINK_KEY}":`, e)
  }
}

// Pure read — never removes the key. Absent and expired are normal and warn nothing;
// every other rejection (corrupt JSON, bad shape, a clock moved backwards) warns once.
export function readDestination(now: number = Date.now()): string | null {
  try {
    const raw = sessionStorage.getItem(DEEP_LINK_KEY)
    if (raw == null) {
      return null
    }
    const parsed = JSON.parse(raw)
    if (
      parsed != null &&
      parsed.v === DEEP_LINK_SCHEMA_VERSION &&
      typeof parsed.path === 'string' &&
      isCapturablePath(parsed.path) &&
      typeof parsed.at === 'number' &&
      !Number.isNaN(parsed.at)
    ) {
      if (parsed.at > now) {
        console.warn(`[deepLink] ignoring destination at "${DEEP_LINK_KEY}" with a future timestamp`)
        return null
      }
      return now - parsed.at > DEEP_LINK_TTL_MS ? null : parsed.path
    }
    console.warn(`[deepLink] ignoring corrupt destination at "${DEEP_LINK_KEY}"`)
    return null
  } catch (e) {
    console.warn(`[deepLink] failed to read destination at "${DEEP_LINK_KEY}":`, e)
    return null
  }
}

// Removes the captured destination. Never throws.
export function clearDestination(): void {
  try {
    sessionStorage.removeItem(DEEP_LINK_KEY)
  } catch (e) {
    console.warn(`[deepLink] failed to clear destination at "${DEEP_LINK_KEY}":`, e)
  }
}
