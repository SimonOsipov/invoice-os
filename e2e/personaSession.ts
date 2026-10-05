// e2e/personaSession.ts — the Playwright-driving layer for the persona axis (PERSONA-01-01,
// Backlog task-270). Split out of e2e/personas.ts because personas.ts must stay importable
// from e2e/personas.test.ts, which runs under vitest in `node` and would break if the pure
// registry pulled in Playwright.

import { expect, type Page } from '@playwright/test'

import { DESTINATION_ENV, PERSONAS, signInUrl, type Destination, type PersonaId } from './personas'
import { resolveTarget } from './targets'

// Each destination's own proof that it actually drew for a signed-in persona — not that the
// shell HTML was served. All three are verified rendered:
//   app     -> the green dot the sidebar's user card renders ONLY once /v1/me has resolved
//              (Sidebar.tsx flag-off, PersonaFooter.tsx's marker row flag-on), i.e. the
//              backend round trip completed, not just a mount.
//   ops     -> the default Overview screen's h1 (ops-console/src/components/Overview.tsx:154)
//   support -> the default Submissions ops h1 (support-console/src/components/Submissions.tsx:48)
export const DESTINATION_READY: Record<Destination, (page: Page) => Promise<void>> = {
  app: async (page) => {
    await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
  },
  ops: async (page) => {
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
  },
  support: async (page) => {
    await expect(page.getByRole('heading', { level: 1, name: 'Submissions ops' })).toBeVisible()
  },
}

// Sign in as an app persona through the landing hand-off and wait until its destination has
// drawn. The landing page is the only front door, so `?persona=` IS the sign-in. A console
// takes a staff session instead (staffSession.ts). The response is asserted ok() BEFORE the
// discriminator so an HTTP failure reports as itself, not as a selector timeout.
// `tenantId` signs in to a shard tenant: the session is seeded first and asserted after.
// `path` is where the app lands.
export async function signInAs(page: Page, id: PersonaId, opts: { tenantId?: string; path?: string } = {}): Promise<void> {
  const { tenantId, path } = opts
  let url = signInUrl(id, path)
  if (tenantId !== undefined) {
    if (id !== 'firm' && id !== 'inhouse') throw new Error(`signInAs: persona "${id}" has no shard tenant`)
    const { TENANTS } = await import('./topology/targets')
    await (await import('./topology/shardSession')).seedShardSession(page, id, { ...TENANTS[id === 'firm' ? 'a' : 'b'], id: tenantId })
    // The seeded in-house session is the sign-in; its `?persona=` would re-mint seeded tenant 2222.
    if (id === 'inhouse') url = url.split('?')[0]
  }
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()
  await DESTINATION_READY[PERSONAS[id].destination](page)
  if (tenantId !== undefined) await (await import('./topology/shardSession')).assertShardSession(page, tenantId)
}

// The refusal half of the axis: hand a destination a persona it does not admit and assert it
// bounces back to the landing page. All three gates refuse the same way — no session, so the
// SPA navigates to landingBase() (the console's StaffGate does it after an async boot) —
// which is why one helper covers all of them.
//
// Builds the URL from DESTINATION_ENV rather than signInUrl(), because the whole point is to
// pair a persona with a destination that is NOT its own.
export async function expectRefused(page: Page, id: PersonaId, destination: Destination): Promise<void> {
  const url = `${resolveTarget(DESTINATION_ENV[destination])}?persona=${id}`
  const landingUrl = resolveTarget('LANDING_URL')

  await page.goto(url)
  await page.waitForURL((u) => u.href.startsWith(landingUrl), { timeout: 20_000 })

  expect(page.url(), `expected ${url} to refuse persona "${id}" and redirect to ${landingUrl}`).toContain(landingUrl)
}

// The app sidebar's nav labels, in render order. Scoped to the <nav> (Sidebar.tsx:212) so it
// picks up neither the company-switcher button in the header div above it (:143) nor the
// group-label divs (:216), both of which a bare `button` or text sweep would catch.
//
// Badge stripping: a nav button renders `<label span><badge span?>`, and the label span is
// unclassed, so nth-child on it is brittle. Badges are always numeric (String(...) at :84 and
// :88) and no nav label ends in a digit, so trimming a trailing run of digits off the
// button's text is both sufficient and stable.
export async function sidebarRoster(page: Page): Promise<string[]> {
  const labels = await page.locator('aside.pf-sidebar nav.pf-nav-list button.pf-nav').allTextContents()
  return labels.map((t) => t.replace(/\d+$/, '').trim())
}

// Console errors + uncaught exceptions, for specs that assert a persona's surfaces draw
// clean. Attach before navigating so load-time errors are captured.
//
export function collectErrors(page: Page): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })
  return errors
}
