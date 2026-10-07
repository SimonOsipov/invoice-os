// The browser half of ctl login. Every e2e helper loads inside a function, after login set the URL variables.
import { chmodSync, readFileSync } from 'node:fs'

import { chromium, type Browser, type BrowserContext, type Page } from '@playwright/test'

import { CtlError } from './main'
import type { Account, AccountKey } from './login'
import type { EnvResult } from './railway'

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
      await passFrontDoor(page, account, '/')
      await DESTINATION_READY.app(page, READY_TIMEOUT)
      const role = await browserRole(page, account.tenantId as string)
      await save(context, statePath)
      return { role }
    }
    const { seedStaffStorage, consoleUrl } = await import('../staffSession')
    await seedStaffStorage(page, dest, account as Required<Pick<Account, 'email' | 'password' | 'userId'>>)
    const url = consoleUrl(dest)
    const res = await page.goto(url)
    if (!res?.ok()) throw new CtlError(`${url} answered ${res ? `HTTP ${res.status()}` : 'nothing'}`, 'The console is not serving; check its deploy.', 1)
    await DESTINATION_READY[dest](page, READY_TIMEOUT)
    await save(context, statePath)
    return {}
  })
}

const APP_SESSION_KEY = 'invoice-os.session'
const MIN_LIFE_S = 60

// The access token a saved state holds for one origin, or undefined for any unreadable shape.
function savedToken(statePath: string, origin: string, key: string): string | undefined {
  try {
    const state = JSON.parse(readFileSync(statePath, 'utf8')) as { origins?: { origin: string; localStorage: { name: string; value: string }[] }[] }
    const raw = state.origins?.find((o) => o.origin === origin)?.localStorage.find((i) => i.name === key)?.value
    const token = raw === undefined ? undefined : (JSON.parse(raw) as { token?: unknown }).token
    return typeof token === 'string' ? token : undefined
  } catch {
    return undefined
  }
}

// A saved state is reused only on a live access token. Booting the page would present the saved refresh token to GoTrue
// (a console always renews at boot), and a token the agent's live session already rotated revokes that session.
// Any failure reads as stale: the caller signs in again, which mints a new session.
export async function checkSavedState(key: AccountKey, account: Account, urls: Urls, statePath: string): Promise<{ ok: boolean; role?: string }> {
  const dest = destinationOf(key)
  const storageKey = dest === 'app' ? APP_SESSION_KEY : (await import('../staffSession')).CONSOLE_SESSION_KEY[dest]
  const token = savedToken(statePath, new URL(rootUrl(dest, urls)).origin, storageKey)
  const claims = token === undefined ? undefined : claimsOf(token)
  if (!claims || typeof claims.exp !== 'number' || claims.exp - Date.now() / 1000 < MIN_LIFE_S) return { ok: false }
  if (dest !== 'app') return (claims.app_metadata as { staff?: unknown } | undefined)?.staff === true ? { ok: true } : { ok: false }
  try {
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
