// Stub for RED specs (ROUTE-05-01) — Stage 3 fills in the bodies. Mirrors lib/session.ts's
// namespaced-key, versioned-blob, warn-never-error conventions for sessionStorage.

export const DEEP_LINK_KEY = 'invoice-os.deepLink'
export const DEEP_LINK_SCHEMA_VERSION = 1
export const DEEP_LINK_TTL_MS = 10 * 60_000

export function captureDestination(pathname: string, now: number = Date.now()): void {}

export function readDestination(now: number = Date.now()): string | null {
  return null
}

export function clearDestination(): void {}
