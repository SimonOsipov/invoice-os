import { ApiError } from '@invoice-os/api-client/client'

import { redeemHandoffCode, renewConsoleSession } from './boot'
import { isStaffToken, nonEmpty, type ConsoleSession } from './session'
import { endConsoleSession } from './signOut'
import { BASE64URL_43, mintSignInState } from './state'

// The two production console hosts behind IAP; previews and local builds keep password sign-in.
export const IAP_HOSTS: readonly string[] = ['ops.ascomply.com', 'sup.ascomply.com']
export const isIapHost = (hostname: string): boolean => IAP_HOSTS.includes(hostname)

const IAP_SESSION_URL = '/auth/iap-session'
const CLEAR_LOGIN_COOKIE = '/_gcp_iap/clear_login_cookie'
const TIMEOUT_MS = 15000
// An expiry this soon after a mint is a fault, not a lapse: it stops the reload-mint loop.
export const IAP_FRESH_MS = 60_000

export const signedOutKey = (storageKey: string): string => `${storageKey}:signed-out`
export const iapMintedAtKey = (storageKey: string): string => `${storageKey}:iap-minted-at`
export const iapExpiredFreshKey = (storageKey: string): string => `${storageKey}:iap-expired-fresh`

export function isSignedOut(storageKey: string): boolean {
  try {
    return localStorage.getItem(signedOutKey(storageKey)) !== null
  } catch {
    return false
  }
}

export function markSignedOut(storageKey: string): void {
  try {
    localStorage.setItem(signedOutKey(storageKey), '1')
  } catch (e) {
    console.warn('[console-session] failed to store the signed-out marker:', e)
  }
}

export function clearSignedOut(storageKey: string): void {
  try {
    localStorage.removeItem(signedOutKey(storageKey))
  } catch (e) {
    console.warn('[console-session] failed to clear the signed-out marker:', e)
  }
}

export type IapMint = { kind: 'session'; session: ConsoleSession } | { kind: 'no-staff'; email: string } | { kind: 'failed' } | { kind: 'signed-out' }

const FAILED: IapMint = { kind: 'failed' }

// Wire shape of /auth/iap-session: internal/gateway/iap_session.go. Same-origin (the console host proxies it),
// redirect manual so an IAP login redirect reads as a failure instead of being followed.
async function requestCode(state: string): Promise<IapMint | string> {
  const res = await fetch(IAP_SESSION_URL, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ state }),
    credentials: 'same-origin',
    redirect: 'manual',
    signal: AbortSignal.timeout(TIMEOUT_MS),
  })
  if (res.status === 403) {
    const b = (await res.json()) as { error?: unknown; email?: unknown } | null
    return b?.error === 'no staff account' && nonEmpty(b.email) ? { kind: 'no-staff', email: b.email } : FAILED
  }
  if (!res.ok) return FAILED
  const code = ((await res.json()) as { code?: unknown } | null)?.code
  return typeof code === 'string' && BASE64URL_43.test(code) ? code : FAILED
}

export async function mintFromIap(o: { gateway: string; storageKey: string; now?: number }): Promise<IapMint> {
  const now = o.now ?? Date.now()
  if (isSignedOut(o.storageKey)) return { kind: 'signed-out' }
  try {
    const fresh = iapExpiredFreshKey(o.storageKey)
    if (sessionStorage.getItem(fresh) !== null) {
      sessionStorage.removeItem(fresh)
      return FAILED
    }
  } catch {
    // storage denied: no guard to apply
  }
  try {
    const state = mintSignInState(now)
    const code = await requestCode(state)
    if (typeof code !== 'string') return code
    const session = await redeemHandoffCode(o.gateway, code, state)
    if (!isStaffToken(session.token)) return FAILED
    try {
      sessionStorage.setItem(iapMintedAtKey(o.storageKey), String(now))
    } catch {
      // storage denied: the loop guard is off for this mint
    }
    return { kind: 'session', session }
  } catch {
    return FAILED
  }
}

// Cookie renewal; in IAP mode a refused one (400/401) is replaced by a fresh mint. Rethrows when no session results.
export async function renewStaffSession(o: { gateway: string; storageKey: string; iap: boolean; legacyRefreshToken?: string }): Promise<ConsoleSession> {
  try {
    return await renewConsoleSession(o.gateway, o.legacyRefreshToken)
  } catch (e) {
    if (!o.iap || !(e instanceof ApiError && e.kind === 'http' && (e.status === 400 || e.status === 401))) throw e
    const m = await mintFromIap(o)
    if (m.kind === 'session') return m.session
    throw e
  }
}

// "Use a different Google account": ends the console session like Sign out but writes no signed-out marker,
// so the mint after IAP's account chooser is not refused.
export async function switchGoogleAccount(o: { storageKey: string; gateway: string | null }): Promise<void> {
  await endConsoleSession(o, true)
  window.location.assign(CLEAR_LOGIN_COOKIE)
}
