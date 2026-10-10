import { test as base, expect, type Page } from '@playwright/test'

import { provisionStaffAccount, signInSession, type StaffAccount } from './api/client'
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

// One rules-role staff account per Playwright worker (the smoke sweep opens the real Rules screen). No smoke spec signs it out, so the parallel sessions do not interact.
export const test = base.extend<object, { staffAccount: StaffAccount }>({
  staffAccount: [
    async ({}, use) => {
      await use(await provisionStaffAccount('staff-smoke', undefined, { rulesRole: true }))
    },
    { scope: 'worker', timeout: 60_000 },
  ],
})

// Writes a fresh real staff pair into the console origin before any page script runs.
// The sessionStorage flag keeps a reload from overwriting the pair the console renewed.
export async function seedStaffStorage(page: Page, target: ConsoleTarget, account: StaffAccount): Promise<{ token: string; refresh_token: string }> {
  const pair = await signInSession(account.email, account.password)
  const record = JSON.stringify({ v: 2, token: pair.access_token, refresh_token: pair.refresh_token })
  await page.context().addInitScript(
    ({ origin, key, value }) => {
      if (location.origin !== origin || sessionStorage.getItem('e2e.staff-seeded')) return
      sessionStorage.setItem('e2e.staff-seeded', '1')
      localStorage.setItem(key, value)
    },
    { origin: new URL(consoleUrl(target)).origin, key: CONSOLE_SESSION_KEY[target], value: record },
  )
  return { token: pair.access_token, refresh_token: pair.refresh_token }
}

// Opens the console on a seeded real staff session and waits until it has drawn.
// The response is asserted ok() first, so an HTTP failure reports as itself.
export async function seedStaffSession(page: Page, target: ConsoleTarget, account: StaffAccount): Promise<{ token: string; refresh_token: string }> {
  const seeded = await seedStaffStorage(page, target, account)
  const url = consoleUrl(target)
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()
  await DESTINATION_READY[target](page)
  return seeded
}
