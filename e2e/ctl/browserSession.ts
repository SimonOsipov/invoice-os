// The browser half of ctl login. Every e2e helper loads inside a function, after login set the URL variables.
import { chmodSync, readFileSync } from 'node:fs'

import { chromium, type Browser, type BrowserContext, type Page } from '@playwright/test'

import { CtlError } from './main'
import type { Account, AccountKey } from './login'
import type { EnvResult } from './envUrls'

type Urls = EnvResult['urls']
type Destination = 'app' | 'ops' | 'support'

const READY_TIMEOUT = 30_000
const DESTINATION: Record<'developer' | 'support', Destination> = { developer: 'ops', support: 'support' }
const rootUrl = (dest: Destination, urls: Urls) => (dest === 'app' ? urls.APP_URL : dest === 'ops' ? urls.OPS_CONSOLE_URL : urls.SUPPORT_CONSOLE_URL) as string
const destinationOf = (key: AccountKey): Destination => (key === 'developer' || key === 'support' ? DESTINATION[key] : 'app')

async function withContext<T>(storageState: string | undefined, run: (context: BrowserContext, page: Page) => Promise<T>): Promise<T> {
  let browser: Browser
  try {
    browser = await chromium.launch()
  } catch (err) {
    throw new CtlError(`Chromium did not start: ${err instanceof Error ? err.message.split('\n')[0] : String(err)}`, 'Run: pnpm --filter @invoice-os/e2e exec playwright install chromium', 1)
  }
  try {
    const context = await browser.newContext(storageState ? { storageState } : {})
    return await run(context, await context.newPage())
  } finally {
    await browser.close()
  }
}

// The tenant id and role the signed-in browser session reads from /v1/me.
async function browserRole(page: Page, tenantId: string): Promise<string> {
  const { expectHandoffSession, browserToken } = await import('../personaSession')
  const { me } = await import('../api/client')
  await expectHandoffSession(page, tenantId)
  const read = await me(await browserToken(page))
  if (read.tenant.id !== tenantId) throw new CtlError(`the browser session is bound to tenant ${read.tenant.id}, expected ${tenantId}`, 'Run login again.', 1)
  return read.user.role
}

// The page's state holds session tokens; keep the file owner-only like accounts.json.
async function save(context: BrowserContext, statePath: string): Promise<void> {
  await context.storageState({ path: statePath })
  chmodSync(statePath, 0o600)
}

export async function signInFresh(key: AccountKey, account: Account, _urls: Urls, statePath: string): Promise<{ role?: string }> {
  const dest = destinationOf(key)
  const { DESTINATION_READY, passFrontDoor } = await import('../personaSession')
  return withContext(undefined, async (context, page) => {
    if (dest === 'app') {
      await passFrontDoor(page, account, '/', { consent: 'none' })
      await DESTINATION_READY.app(page, READY_TIMEOUT)
      const role = await browserRole(page, account.tenantId as string)
      await save(context, statePath)
      return { role }
    }
    const { openWithHandoff } = await import('../staffSession')
    await openWithHandoff(page, dest, account as Required<Pick<Account, 'email' | 'password'>>)
    await save(context, statePath)
    return {}
  })
}

const APP_SESSION_KEY = 'invoice-os.session'
const MIN_LIFE_S = 60
const RENEW_DUE = 0.2 // the app renews past 80% of the token's life

interface SavedState {
  cookies?: { name: string; domain: string }[]
  origins?: { origin: string; localStorage: { name: string; value: string }[] }[]
}

// The access token a saved state holds for one origin, or undefined for any unreadable shape.
function savedToken(state: SavedState, origin: string, key: string): string | undefined {
  try {
    const raw = state.origins?.find((o) => o.origin === origin)?.localStorage.find((i) => i.name === key)?.value
    const token = raw === undefined ? undefined : (JSON.parse(raw) as { token?: unknown }).token
    return typeof token === 'string' ? token : undefined
  } catch {
    return undefined
  }
}

// A saved state is reused only when it holds the audience cookie and loading it sends no refresh:
// a console renews at boot and an app renews when due, and a cookie that the live session rotated revokes the session when presented.
// Any failure reads as stale: the caller signs in again, which mints a new session.
export async function checkSavedState(key: AccountKey, account: Account, urls: Urls, statePath: string): Promise<{ ok: boolean; role?: string }> {
  const dest = destinationOf(key)
  if (dest !== 'app') return { ok: false }
  try {
    const state = JSON.parse(readFileSync(statePath, 'utf8')) as SavedState
    const gateway = new URL(urls.GATEWAY_URL as string).hostname
    if (!state.cookies?.some((c) => c.name === `__Host-rt-${dest}` && c.domain === gateway)) return { ok: false }
    const token = savedToken(state, new URL(rootUrl(dest, urls)).origin, APP_SESSION_KEY)
    const claims = token === undefined ? undefined : claimsOf(token)
    if (!claims || typeof claims.exp !== 'number' || typeof claims.iat !== 'number') return { ok: false }
    const left = claims.exp - Date.now() / 1000
    if (left < MIN_LIFE_S + RENEW_DUE * (claims.exp - claims.iat)) return { ok: false }
    const { me } = await import('../api/client')
    const read = await me(token as string)
    return read.tenant.id === account.tenantId ? { ok: true, role: read.user.role } : { ok: false }
  } catch {
    return { ok: false }
  }
}

function claimsOf(token: string): Record<string, unknown> | undefined {
  try {
    return JSON.parse(Buffer.from(token.split('.')[1] ?? '', 'base64url').toString('utf8')) as Record<string, unknown>
  } catch {
    return undefined
  }
}
