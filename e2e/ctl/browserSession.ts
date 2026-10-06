// The browser half of ctl login. Every e2e helper loads inside a function, after login set the URL variables (D30).
import { chmodSync } from 'node:fs'

import { chromium, type Browser, type BrowserContext, type Page } from '@playwright/test'

import { CtlError } from './main'
import { raceReady, type Account, type AccountKey } from './login'
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

// Any failure of the saved state, including a failed check after it drew, reads as stale: the caller signs in again.
export async function checkSavedState(key: AccountKey, account: Account, urls: Urls, statePath: string): Promise<{ ok: boolean; role?: string }> {
  const dest = destinationOf(key)
  const { DESTINATION_READY } = await import('../personaSession')
  const landing = new URL(urls.LANDING_URL as string).origin
  return withContext(statePath, async (context, page) => {
    await page.goto(rootUrl(dest, urls))
    const verdict = await raceReady(DESTINATION_READY[dest](page, READY_TIMEOUT), page.waitForURL((u) => u.origin === landing, { timeout: READY_TIMEOUT }))
    if (verdict === 'stale') return { ok: false }
    try {
      const role = dest === 'app' ? await browserRole(page, account.tenantId as string) : undefined
      await save(context, statePath)
      return { ok: true, role }
    } catch {
      return { ok: false }
    }
  })
}
