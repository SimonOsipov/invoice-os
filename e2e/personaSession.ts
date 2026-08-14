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
//              (Sidebar.tsx:259), i.e. the backend round trip completed, not just a mount.
//              The same discriminator the four existing signInFirm copies already wait on.
//   ops     -> the default Overview screen's h1 (ops-console/src/components/Overview.tsx:154)
//   support -> the default Submissions ops h1 (support-console/src/components/Submissions.tsx:48)
const DESTINATION_READY: Record<Destination, (page: Page) => Promise<void>> = {
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

// Sign in as a persona through the landing hand-off and wait until its destination has
// actually drawn. The landing page is the single sign-in front door, so no deployed build
// has a picker to click — `?persona=` IS the sign-in, exactly as landing destUrl() hands
// off. The response is asserted ok() BEFORE the discriminator so an HTTP failure reports as
// itself rather than as a selector timeout.
export async function signInAs(page: Page, id: PersonaId): Promise<void> {
  const url = signInUrl(id)
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()
  await DESTINATION_READY[PERSONAS[id].destination](page)
}

// The refusal half of the axis: hand a destination a persona it does not admit and assert it
// bounces back to the landing page. All three gates refuse the same way — no session, so the
// SPA navigates to landingBase() (app/src/App.tsx, ops-console/src/App.tsx:40-44,
// support-console/src/App.tsx:41-45) — which is why one helper covers all of them.
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
// Hoisted here for NEW specs to import. It deliberately does NOT refactor the three existing
// inline copies (portfolio.spec.ts:23, import-wizard.spec.ts:83, invoice-surfaces.spec.ts:35)
// — those files are passing and out of this subtask's scope.
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
