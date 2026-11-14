import { test as base, expect, type Page } from '@playwright/test'

import { mintSignInState, provisionStaffAccount, signInForCode, type StaffAccount } from './api/client'
import { DESTINATION_ENV } from './personas'
import { DESTINATION_READY } from './personaSession'
import { resolveTarget } from './targets'

export type ConsoleTarget = 'ops' | 'support'

// Each console's own localStorage key (frontend/<console>/src/auth.ts SESSION_KEY).
export const CONSOLE_SESSION_KEY: Record<ConsoleTarget, string> = {
  ops: 'invoice-os.ops-session',
  support: 'invoice-os.support-session',
}

export const consoleUrl = (target: ConsoleTarget): string => resolveTarget(DESTINATION_ENV[target])

// One staff account per Playwright worker (the smoke sweep opens the real Rules and Tenants screens). No smoke spec signs it out, so the parallel sessions do not interact.
export const test = base.extend<object, { staffAccount: StaffAccount }>({
  staffAccount: [
    async ({}, use) => {
      await use(await provisionStaffAccount('staff-smoke'))
    },
    { scope: 'worker', timeout: 60_000 },
  ],
})

// Signs in through the real hand-off: the init script arms the sign-in state, the page redeems the code,
// and the gateway sets the HttpOnly refresh cookie. Returns the exchanged access token.
// `ready: false` skips the destination-ready wait for a landing that is not the default screen.
export async function openWithHandoff(page: Page, target: 'app' | ConsoleTarget, account: Pick<StaffAccount, 'email' | 'password'>, opts: { ready?: boolean } = {}): Promise<{ token: string }> {
  const base = target === 'app' ? resolveTarget('APP_URL') : consoleUrl(target)
  const state = mintSignInState()
  const code = await signInForCode(account.email, account.password, state)
  await page.context().addInitScript(
    ({ origin, value }) => {
      if (location.origin !== origin || sessionStorage.getItem('e2e.handoff-armed')) return
      sessionStorage.setItem('e2e.handoff-armed', '1')
      sessionStorage.setItem('invoice-os.signInState', value)
    },
    { origin: new URL(base).origin, value: JSON.stringify({ v: 1, s: state, at: Date.now() }) },
  )
  const exchanged = page.waitForResponse((r) => r.url().endsWith('/auth/exchange') && r.request().method() === 'POST')
  await page.goto(`${base}/?handoff=${code}`)
  const { access_token } = (await (await exchanged).json()) as { access_token: string }
  if (opts.ready !== false) await DESTINATION_READY[target](page)
  return { token: access_token }
}

// Opens the console on a seeded real staff session and waits until it has drawn.
// The response is asserted ok() first, so an HTTP failure reports as itself.
export async function seedStaffSession(page: Page, target: ConsoleTarget, account: StaffAccount): Promise<{ token: string }> {
  const seeded = await openWithHandoff(page, target, account)
  const url = consoleUrl(target)
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()
  await DESTINATION_READY[target](page)
  return seeded
}
