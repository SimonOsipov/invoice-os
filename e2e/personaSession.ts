// e2e/personaSession.ts — the Playwright-driving layer for the persona axis (PERSONA-01-01,
// Backlog task-270). Split out of e2e/personas.ts because personas.ts must stay importable
// from e2e/personas.test.ts, which runs under vitest in `node` and would break if the pure
// registry pulled in Playwright.

import { expect, type Page, type Request } from '@playwright/test'

import { DESTINATION_ENV, type Destination, type PersonaId } from './personas'
import { resolveTarget } from './targets'

// Each destination's own proof that it actually drew for a signed-in persona — not that the
// shell HTML was served. All three are verified rendered:
//   app     -> the green dot the sidebar's user card renders ONLY once /v1/me has resolved
//              (Sidebar.tsx's identity card), i.e. the
//              backend round trip completed, not just a mount.
//   ops     -> the default Overview screen's h1 (ops-console/src/components/Overview.tsx:154)
//   support -> the default Submissions ops h1 (support-console/src/components/Submissions.tsx:48)
// `timeout` undefined keeps the runner's configured timeout.
export const DESTINATION_READY: Record<Destination, (page: Page, timeout?: number) => Promise<void>> = {
  app: async (page, timeout) => {
    await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached({ timeout })
  },
  ops: async (page, timeout) => {
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible({ timeout })
  },
  support: async (page, timeout) => {
    await expect(page.getByRole('heading', { level: 1, name: 'Submissions ops' })).toBeVisible({ timeout })
  },
}

export const VERIFIED = '[title="Tenant verified via /v1/me"]'
const SESSION_KEY = 'invoice-os.session'

export function isHandoffNavigation(url: string): boolean {
  return url.startsWith(resolveTarget('APP_URL')) && new URL(url).searchParams.has('handoff')
}

// "Sign in" names the header button, the window and (with " →") the submit; scope plus exact keeps them apart.
export const signInDialog = (page: Page) => page.getByRole('dialog', { name: 'Sign in', exact: true })
export const headerSignIn = (page: Page) => page.getByRole('banner').getByRole('button', { name: 'Sign in', exact: true })

export async function submitSignIn(page: Page, email: string, password: string): Promise<void> {
  const dialog = signInDialog(page)
  await dialog.getByLabel('Work email', { exact: true }).fill(email)
  await dialog.getByLabel('Password', { exact: true }).fill(password)
  await dialog.getByRole('button', { name: 'Sign in →', exact: true }).click()
}

export async function expectInWorkspace(page: Page, account: { workspaceName: string }): Promise<void> {
  await expect(page.locator(VERIFIED)).toBeAttached({ timeout: 30_000 })
  await expect(page.locator('aside.pf-sidebar')).toContainText(account.workspaceName.toUpperCase())
}

// App path -> landing front door -> "Sign in" -> hand-off navigation back to the app.
// A stored session is rehydrated at boot and suppresses the front-door bounce (App.tsx resolveBootSession,
// the `activeSession` guard of the bounce effect), so a page already on the app drops it first.
// ceiling: a page parked on another origin keeps its stored session, sign out there first.
export async function passFrontDoor(page: Page, account: { email: string; password: string }, path: string): Promise<void> {
  if (new URL(page.url(), 'about:blank').origin === new URL(resolveTarget('APP_URL')).origin) {
    await page.evaluate((key) => localStorage.removeItem(key), SESSION_KEY)
  }
  await page.goto(`${resolveTarget('APP_URL')}${path}`)
  await page.waitForURL((u) => u.href.startsWith(resolveTarget('LANDING_URL')), { timeout: 20_000 })
  await headerSignIn(page).click()
  await Promise.all([
    page.waitForRequest((r) => r.isNavigationRequest() && isHandoffNavigation(r.url())),
    submitSignIn(page, account.email, account.password),
  ])
}

export async function signInAtFrontDoor(page: Page, account: { email: string; password: string; workspaceName: string }, path: string): Promise<void> {
  await passFrontDoor(page, account, path)
  await expectInWorkspace(page, account)
}

// Sign in as the e2e member of a tenant (realAccounts.ts) through the landing form, then wait for the app to draw.
// `tenantId` defaults to the seeded 1111 (firm) / 2222 (inhouse); `path` is where the app lands.
// Postcondition: the stored session is a hand-off session for the tenant, and no main-frame navigation carried `persona=` (the app ignores it).
// ceiling: about two extra SPA loads per test, revisit with per-worker storageState above +3 min per unit.
export async function signInAs(page: Page, id: PersonaId, opts: { tenantId?: string; path?: string } = {}): Promise<void> {
  if (id !== 'firm' && id !== 'inhouse') throw new Error(`signInAs: persona "${id}" has no e2e member`)
  const { TENANTS } = await import('./topology/targets')
  const { ensureMember } = await import('./realAccounts')
  const tenantId = opts.tenantId ?? TENANTS[id === 'firm' ? 'a' : 'b'].id
  const member = await ensureMember(tenantId, id === 'firm' ? 'firm' : 'in_house')

  const personaNavs: string[] = []
  const onRequest = (r: Request): void => {
    if (r.isNavigationRequest() && r.frame() === page.mainFrame() && new URL(r.url()).searchParams.has('persona')) personaNavs.push(r.url())
  }
  page.on('request', onRequest)
  try {
    await passFrontDoor(page, member, opts.path ?? '/')
    await DESTINATION_READY.app(page)
  } finally {
    page.off('request', onRequest)
  }

  expect(personaNavs, 'signInAs navigated with ?persona=, which the real door never carries').toEqual([])
  await expectHandoffSession(page, tenantId)
}

// The stored session is a hand-off session bound to `tenantId`.
export async function expectHandoffSession(page: Page, tenantId: string): Promise<void> {
  const raw = await page.evaluate((key) => localStorage.getItem(key), SESSION_KEY)
  expect(raw, 'no stored session after sign-in').not.toBeNull()
  const session = JSON.parse(raw!) as { handoff?: boolean; me?: { tenant?: { id?: string } } }
  expect(session.handoff, 'the stored session is not a hand-off session').toBe(true)
  expect(session.me?.tenant?.id, `the session is bound to another tenant than ${tenantId}`).toBe(tenantId)
}

// The browser session's own access token, for live reads compared with what the page shows.
export async function browserToken(page: Page): Promise<string> {
  const raw = await page.evaluate((key) => localStorage.getItem(key), SESSION_KEY)
  const token = raw === null ? undefined : (JSON.parse(raw) as { token?: string }).token
  if (!token) throw new Error('browserToken: no stored session token, sign in first')
  return token
}

// The refusal half of the axis: visit a destination with `?persona=<id>` and assert it bounces
// back to the landing page. The param is no credential at any of the three destinations: no
// session, so the SPA navigates to landingBase() (the console's StaffGate does it after an
// async boot), which is why one helper covers all of them.
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
