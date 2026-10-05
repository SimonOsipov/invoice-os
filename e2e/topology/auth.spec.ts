import { test, expect, type BrowserContext, type Frame, type Page, type Request, type Response } from '@playwright/test'
import { APP_URL, FIRM_PERSONA, GATEWAY_URL, INHOUSE_PERSONA, TENANTS } from './targets'
import { resolveTarget } from '../targets'
import { DESTINATION_READY, VERIFIED, browserToken, collectErrors, expectInWorkspace, isHandoffNavigation, sidebarRoster, signInAs, signInAtFrontDoor, submitSignIn } from '../personaSession'
import { CONSOLE_SESSION_KEY, consoleUrl, seedStaffSession, type ConsoleTarget } from '../staffSession'
import { ensureMember } from '../realAccounts'
import {
  contactsMe,
  login,
  createEntity,
  createInvoice,
  createImportBatch,
  claimsOf,
  exchangeCode,
  listEntities,
  mintSignInState,
  provisionRealAccount,
  provisionStaffAccount,
  rawFetch,
  signInForCode,
  PERSONAS as API_PERSONAS,
  type Me,
  type RealAccount,
  type TenantKind,
} from '../api/client'
import { freshTin } from '../api/fixtures'
import { seedConsent } from '../smoke/landingConsent'
import { approvalRun404Dropper, expectedStatusDropper, type Dropper } from './consoleGate'
import { assertPageDoesNotScrollSideways, enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS } from './layout'

// The public marketing landing page — sign-out's redirect target. Imported from the
// BASE e2e/targets.ts, not this directory's ./targets: topology/targets.ts re-exports
// only GATEWAY_URL/APP_URL/TENANTS/FIRM_PERSONA/VALIDATION_EXPECTED, so it has no
// LANDING_URL. Pattern mirrors e2e/smoke/apps.ts:21 (Decision [signout-asserts-landing-redirect]).
const LANDING_URL = resolveTarget('LANDING_URL')

// The live browser round trip: a real sign-in through the landing form mints a session whose
// /v1/me read resolves the tenant under RLS before the workspace renders.
test('deployed app: a real sign-in into 1111 renders the backend-verified tenant identity', async ({ page }) => {
  const errors = collectErrors(page)

  await signInAs(page, 'firm')

  // The marker renders only once /v1/me resolved; the static label alone would pass on the fallback.
  await expect(page.locator(VERIFIED)).toBeAttached()
  await expect(page.locator('aside.pf-sidebar')).toContainText(FIRM_PERSONA.tenantName.toUpperCase())

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Sign-out clears the session and navigates to landingBase() (Decision [signout-asserts-landing-redirect]).
test('deployed app: sign-out redirects to the landing page', async ({ page }) => {
  const errors = collectErrors(page)

  await signInAs(page, 'firm')

  await page.getByRole('button', { name: 'Sign out' }).click()
  await page.waitForURL((url) => url.href.startsWith(LANDING_URL))

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// A live hand-off session wins over a later sign-in from landing (docs/identity-provider.md,
// "Precedence in the app"): a second account only takes the tab once the first session is gone.
test('deployed app: a second real sign-in replaces the session only after the first one is gone', async ({ page }) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  const inhouse = await ensureMember(TENANTS.b.id, 'in_house')

  await signInAs(page, 'firm')
  await expect(page.locator('aside.pf-sidebar')).toContainText(FIRM_PERSONA.tenantName.toUpperCase())
  const firmToken = await browserToken(page)

  // Sign in as 2222 from landing without signing out: ?auth=start bounces over the live session
  // and keeps it, then the hand-off code is ignored.
  await page.goto(`${APP_URL}/?auth=start`)
  await page.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })
  const dialog = page.getByRole('dialog', { name: 'Platform login' })
  await expect(dialog.getByLabel('Work email', { exact: true })).toBeVisible()
  await Promise.all([
    page.waitForRequest((r) => r.isNavigationRequest() && isHandoffNavigation(r.url())),
    submitSignIn(page, inhouse.email, inhouse.password),
  ])
  await expectInWorkspace(page, { workspaceName: FIRM_PERSONA.tenantName })
  expect(await browserToken(page), 'a live session was replaced by a sign-in from landing').toBe(firmToken)

  // Once the stored session is gone, a real sign-in as the 2222 member takes the tab.
  await signInAs(page, 'inhouse')
  const sidebar = page.locator('aside.pf-sidebar')
  await expect(sidebar).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())
  // The positive assertion alone would pass while both identities render.
  await expect(sidebar).not.toContainText(FIRM_PERSONA.tenantName.toUpperCase())
  expect(await browserToken(page), 'the 2222 session carries the 1111 token').not.toBe(firmToken)

  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

// The one case that proves the param is inert: with a live session it neither replaces it nor mints.
test('deployed app: a ?persona=firm visit with a live session keeps that session and mints nothing', async ({ page }) => {
  await signInAs(page, 'firm')
  const before = await browserToken(page)

  const seen: string[] = []
  const logins: string[] = []
  page.on('request', (r) => {
    seen.push(r.url())
    if (new URL(r.url()).pathname === '/auth/login') logins.push(r.method())
  })
  await page.goto(`${APP_URL}?persona=firm`)
  await expect(page.locator(VERIFIED)).toBeAttached()
  await expect(page.locator('aside.pf-sidebar')).toContainText(FIRM_PERSONA.tenantName.toUpperCase())

  expect(await browserToken(page), 'the visit replaced the stored session token').toBe(before)
  // Positive control: the listener saw the visit itself, so an empty `logins` is not blindness.
  expect(seen.some((u) => u.includes('persona=firm')), 'the request listener missed the visit').toBe(true)
  expect(logins, 'the visit minted a session through /auth/login').toEqual([])
})

// The single front door. The app used to answer a sessionless visit with a persona picker
// of its own — a SECOND place to sign in, on a different origin from the landing page's.
// It now sends that visit to the landing page instead.
//
// The picker still exists for exactly one deployment shape: a standalone showcase build
// with no VITE_LANDING_URL, which would otherwise be a dead end. Deployed builds always
// bake that variable, so on this fleet the redirect is unconditional — which is what makes
// it assertable here.
test('deployed app: a visit with no session redirects to the landing page', async ({ page }) => {
  await page.goto(APP_URL)
  await page.waitForURL((url) => url.href.startsWith(LANDING_URL), { timeout: 20_000 })

  expect(page.url(), `expected a redirect from ${APP_URL} to ${LANDING_URL}`).toContain(LANDING_URL)
})

// ROUTE-01-08: the only spec that presses Back/Forward. Lives in the sign-in capability
// file it depends on (docs/e2e-convention.md: organize by capability, not by date).
//
// goBack() alone would only prove Chromium reused a bfcached page, not that the router
// restored the view. So every step asserts the URL AND the rendered panel, never one alone.
test("deployed app: Back walks the workspace's own history instead of leaving it", async ({ page }) => {
  const errors = collectErrors(page)

  await signInAs(page, 'firm')
  await expect(page.getByText('COMPLIANCE OVERVIEW', { exact: true })).toBeVisible()

  const nav = page.locator('aside.pf-sidebar nav.pf-nav-list')
  await nav.getByRole('button', { name: 'Invoices' }).click()
  await expect(page, 'nav to Invoices did not update the URL').toHaveURL(/\/invoices$/)
  await expect(page.getByTestId('invoices-list')).toBeVisible()

  await nav.getByRole('button', { name: 'Audit' }).click()
  await expect(page, 'nav to Audit did not update the URL').toHaveURL(/\/audit$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()

  await nav.getByRole('button', { name: 'Settings' }).click()
  await expect(page, 'nav to Settings did not update the URL').toHaveURL(/\/settings\/members$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Settings', exact: true })).toBeVisible()

  await page.goBack()
  await expect(page, 'first Back did not restore /audit').toHaveURL(/\/audit$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()

  await page.goBack()
  await expect(page, 'second Back did not restore /invoices').toHaveURL(/\/invoices$/)
  await expect(page.getByTestId('invoices-list')).toBeVisible()

  await page.goBack()
  // dashboard serialises to bare `/`; an exact-href match, not a loose /\/$/ regex that
  // would pass on any trailing-slash path.
  await expect(page, 'third Back did not restore /').toHaveURL(new URL('/', APP_URL).href)
  await expect(page.getByText('COMPLIANCE OVERVIEW', { exact: true })).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('deployed app: Forward re-applies the view Back left', async ({ page }) => {
  const errors = collectErrors(page)

  await signInAs(page, 'firm')

  const nav = page.locator('aside.pf-sidebar nav.pf-nav-list')
  await nav.getByRole('button', { name: 'Invoices' }).click()
  await expect(page).toHaveURL(/\/invoices$/)

  await nav.getByRole('button', { name: 'Audit' }).click()
  await expect(page).toHaveURL(/\/audit$/)

  await page.goBack()
  await expect(page, 'Back did not restore /invoices').toHaveURL(/\/invoices$/)
  await expect(page.getByTestId('invoices-list')).toBeVisible()

  await page.goForward()
  await expect(page, 'Forward did not re-apply /audit').toHaveURL(/\/audit$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test("deployed app: Back from the session's first screen leaves for the landing page", async ({ page }) => {
  const errors = collectErrors(page)

  // The sign-in itself passes through landing, so landing is the only history entry before
  // the workspace: a Back landing there proves boot replaced the hand-off entry, not pushed.
  await signInAs(page, 'firm')

  await page.goBack()
  await page.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ROUTE-02-08: a Back journey through a REAL invoice detail. The sign-in passes through
// landing, so landing is the entry behind the workspace (Decision [back-lives-in-auth-spec]).
//
// Local console gate, not the shared collectErrors() above: opening a real invoice detail
// fires getInvoiceApprovalRun on mount, which 404s for an invoice with no approval run yet
// (consoleGate.ts). The three pre-existing Back/Forward tests never open a detail page, so
// their shared collectErrors() is left untouched.
test("deployed app: Back from an invoice detail returns to the list, not the landing page", async ({ page }) => {
  const errors: string[] = []
  const drop = approvalRun404Dropper(page)
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (drop(msg.text(), msg.location().url)) return
    errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })

  // Fixtures FIRST: the workspace reads its portfolio once at mount, so an entity created
  // after boot never reaches the company switcher.
  const token = await login(API_PERSONAS.A)
  const entity = await createEntity(token, { name: `ROUTE-02 back journey ${Date.now()}`, tin: freshTin() })
  const invoiceNumber = `INV-ROUTE02-BACK-${Date.now()}`
  await createInvoice(token, {
    entity_id: entity.id,
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  })

  await signInAs(page, 'firm')

  // Invoices is a CLIENT-scoped surface: the list is filtered to ctx.active.entityId,
  // which signing in leaves at whatever clients[0] resolves to (portfolio's ORDER BY name
  // ASC) -- never this fresh entity. Without this the row click below times out.
  // Same two locators invoice-surfaces.spec.ts's selectEntity uses; not imported, because
  // pulling one spec's module graph into another registers its tests twice.
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entity.name }).click()

  const nav = page.locator('aside.pf-sidebar nav.pf-nav-list')
  await nav.getByRole('button', { name: /Invoices/ }).click()
  await expect(page, 'nav to Invoices did not update the URL').toHaveURL(/\/invoices$/)

  const list = page.getByTestId('invoices-list')
  await expect(list).toBeVisible()
  await list.getByText(invoiceNumber, { exact: true }).click()
  await expect(page, 'the row click did not settle on /invoices/<uuid>').toHaveURL(/\/invoices\/[0-9a-f-]{36}$/)
  await expect(page.getByTestId('invoice-detail')).toBeVisible()

  await page.goBack()
  await expect(page, 'Back from the detail did not restore /invoices').toHaveURL(/\/invoices$/)
  await expect(page.getByTestId('invoices-list')).toBeVisible()
  // Proves Back stayed IN the workspace rather than walking past this entry to the landing
  // page -- the URL regex above alone can't distinguish "restored /invoices" from
  // "coincidentally matches /invoices on some other origin".
  expect(page.url().startsWith(APP_URL), `expected ${page.url()} to stay on ${APP_URL}`).toBe(true)

  await page.goBack()
  // dashboard serialises to bare `/`; an exact-href match, not a loose /\/$/ regex that
  // would pass on any trailing-slash path (mirrors the third-Back assertion above).
  await expect(page, 'second Back did not restore /').toHaveURL(new URL('/', APP_URL).href)
  await expect(page.getByText('COMPLIANCE OVERVIEW', { exact: true })).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// The Caddyfile `try_files` fallback serves a top-level path; a stored session cold-boots it.
test('deployed app: a top-level path is a working deep link', async ({ page }) => {
  const errors = collectErrors(page)

  await signInAs(page, 'firm')
  const url = `${APP_URL}/audit`
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()

  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()
  await expect(page, 'the deep link did not settle on /audit').toHaveURL(/\/audit$/)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Moved from invoice-surfaces.spec.ts unchanged; the locals stand in for that file's helpers.
{
const PERSONAS = API_PERSONAS

function collectErrors(page: Page): string[] {
  const errors: string[] = []
  const drop = approvalRun404Dropper(page)
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (drop(msg.text(), msg.location().url)) return
    errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })
  return errors
}

function cleanInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

// ROUTE-02-07 (X-1/X-2): a top-level /invoices/<uuid> deep link cold-boots the detail panel
// directly on a stored session.
test('deployed app: /invoices/<uuid> is a working deep link', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `ROUTE-02 cold boot ${Date.now()}`, tin: freshTin() })
  const invoiceNumber = `INV-ROUTE02-CB-${Date.now()}`
  const inv = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(invoiceNumber) })

  await signInAs(page, 'firm')
  const url = `${APP_URL}/invoices/${inv.id}`
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()

  await expect(page.getByTestId('invoice-detail'), 'the cold boot must render this invoice, not the empty state').toContainText(
    invoiceNumber,
  )

  await expect(page, 'the deep link did not settle on /invoices/<uuid>').toHaveURL(new RegExp(`/invoices/${inv.id}$`))

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
}

// The tab is a PATH SEGMENT (lib/route.ts's routeUrl), so a cold boot on a stored session restores it.
test('deployed app: a settings tab is a working deep link', async ({ page }) => {
  const errors = collectErrors(page)

  await signInAs(page, 'firm')
  const url = `${APP_URL}/settings/roles`
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()

  await expect(page.getByTestId('roles-grid'), 'the deep link must open the Roles tab').toBeVisible()
  // The discriminator: SettingsView renders one panel at a time, so a boot that fell back to
  // the default tab shows this instead of the grid above.
  await expect(page.getByTestId('members-table'), 'the boot fell back to the default Members tab').toHaveCount(0)
  await expect(page, 'the deep link did not settle on /settings/roles').toHaveURL(/\/settings\/roles$/)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// The ONLY oracle for this story's load-bearing premise: sessionStorage written on the APP
// origin survives a same-tab hard navigation to the LANDING origin and back. sessionStorage is
// keyed by (top-level browsing context, origin), so no unit test can observe it — jsdom never
// leaves the origin. Decision [sessionstorage-survives-the-round-trip].
//
// Signs in through the REAL landing form: skipping the landing round trip would skip the premise under test.
//
// No goBack()/goForward() here, deliberately. The journey makes three document navigations
// (/audit -> landing -> the app hand-off) and two replaceState rewrites, so the entry behind
// the arrival is the LANDING page, not /audit. The journey needs no history depth and must not
// claim any.
test('deployed app: a signed-out deep link returns to its destination after sign-in', async ({ page }) => {
  // The explicit budgets below already sum to 50s, which does not fit the config's 60s default
  // envelope once the initial goto is cold too.
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  // signInAs bounces the signed-out /audit visit through landing and the form, and waits for the marker.
  await signInAs(page, 'firm', { path: '/audit' })

  // The hand-off lands on the app ROOT (it carries no path), so arriving on /audit can only
  // have come from the restored destination. AuditView is a static import (App.tsx), so no
  // further fetch precedes the h1.
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()
  await expect(page, 'the restored destination did not settle on /audit').toHaveURL(/\/audit$/)

  // The dashboard's eyebrow div, not a heading — the dashboard's h1 is the dynamic client name.
  // Non-vacuous: the two assertions above already gated on a MOUNTED workspace. Its positive
  // control ships in this same file and run — "Back walks the workspace's own history" asserts
  // this exact locator IS visible on the deployed dashboard.
  await expect(page.getByText('COMPLIANCE OVERVIEW', { exact: true })).not.toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// The FILTER half of the journey above: same front door, same budgets, but the destination
// owns a query. A well-formed UUID matching nothing needs no fixture and proves the restore
// and the honest filtered-empty state in one pass.
//
// The empty-by-filter arm needs the firm tenant's own log to be non-empty (auditScreenState,
// lib/auditView.ts); audit.spec.ts's audit_exportIsDisabledWhenNothingMatches pins the same
// premise on the same tenant.
test('deployed app: a signed-out deep link returns to its FILTER after sign-in', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const unknownInvoice = crypto.randomUUID()

  // The destination's ?invoice= query rides through the front door and comes back with the restore.
  await signInAs(page, 'firm', { path: `/audit?invoice=${unknownInvoice}` })

  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()
  await expect(page, 'the restored destination did not carry its ?invoice= filter back').toHaveURL(
    new RegExp(`/audit\\?invoice=${unknownInvoice}$`),
  )

  // A URL boot resolves no invoice NUMBER (App.tsx seeds it null), so the pill reads the bare
  // form -- which is also what tells a restored URL filter from an in-app hand-off.
  await expect(page.getByTestId('audit-pill-invoice'), 'the restored filter must render as a pill').toHaveText(/^One invoice/)
  await expect(
    page.getByTestId('audit-empty-by-filter'),
    'an unknown invoice id must land the filtered-empty state, not the new-workspace one',
  ).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
// The console's front door: landing with a sign-in state and the console that asked.
// The landing strips both on mount, so the answer is awaited, not the URL.
const consoleFrontDoor = (page: Page, target: ConsoleTarget) =>
  page.waitForResponse(
    (r) => {
      if (!r.request().isNavigationRequest() || !r.url().startsWith(LANDING_URL)) return false
      const q = new URL(r.url()).searchParams
      return q.has('state') && q.get('console') === target
    },
    { timeout: 20_000 },
  )

// The dialog offers no persona. A console takes a staff session, and that journey is covered by
// 'deployed consoles: a staff session signs in through landing' below.
test('deployed landing: the sign-in dialog offers the form and no persona', async ({ page }) => {
  const errors = collectErrors(page)

  // ?auth=start bounces to landing with a sign-in state, which opens the dialog on its form.
  await page.goto(`${APP_URL}/?auth=start`)
  await page.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })
  const dialog = page.getByRole('dialog', { name: 'Platform login' })
  await expect(dialog).toBeVisible()

  // Positive control first: an absence check beside an unrendered form passes vacuously.
  await expect(dialog.getByRole('heading', { name: 'Sign in to your workspace' })).toBeVisible()
  await expect(dialog.getByLabel('Work email', { exact: true })).toBeVisible()
  await expect(dialog.getByRole('button', { name: 'Sign in →', exact: true })).toBeVisible()
  await expect(dialog.getByRole('button', { name: 'Create an account', exact: true })).toBeVisible()
  await expect(dialog.locator('[data-persona]')).toHaveCount(0)
  await expect(dialog.getByTestId('persona-picker')).toHaveCount(0)
  await expect(dialog.getByText('Choose an account')).toHaveCount(0)

  expect(errors, `console errors on the landing page:\n${errors.join('\n')}`).toEqual([])
})

// ROUTE-06-06 AC-1: the plain top-level views ROUTE-06-05's popstate sweep leaves with no
// coverage (dashboard/audit/settings/extraction/detail already have their own deep-link
// specs above). One test looping all 8 paths in-process — docs/e2e-convention.md forbids
// a test() per screen.
test('deployed app: every top-level path cold-boots to its own screen', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const paths: { path: string; heading: string | null }[] = [
    { path: '/invoices', heading: 'Invoices' },
    { path: '/approvals', heading: 'Approvals' },
    { path: '/rules', heading: 'Rules' },
    { path: '/customers', heading: 'Customers & vendors' },
    { path: '/reports', heading: 'Reports & analytics' },
    { path: '/workflows', heading: 'Approval policies' },
    { path: '/clients', heading: 'Client portfolio' },
    { path: '/create', heading: null }, // no h1/testid — the text check below stands in
  ]

  // Each visit below is a cold boot on the stored session.
  await signInAs(page, 'firm', { path: paths[0].path })

  for (const { path, heading } of paths) {
    const url = `${APP_URL}${path}`
    const res = await page.goto(url)
    expect(res, `no response from ${url}`).toBeTruthy()
    expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()

    // URL alone would pass on a Chromium bfcache reuse, so every path asserts both the URL
    // AND a landmark from that screen's DOM.
    await expect(page, `${path} did not settle on its own path`).toHaveURL(new RegExp(`${path}$`))

    if (heading != null) {
      await expect(
        page.getByRole('heading', { level: 1, name: heading, exact: true }),
        `${path} did not render its "${heading}" landmark`,
      ).toBeVisible()
    } else {
      // CreateForm's own title span reads "New invoice · <client>" (a middle dot); the
      // header bar's persistent CTA button is bare "New invoice" with no dot, so this
      // substring is discriminating between the two.
      await expect(page.getByText('New invoice ·'), `${path} did not render its CreateForm title`).toBeVisible()
    }
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ROUTE-06-06 AC-2: the review path, using a batch id minted through
// createImportBatch (e2e/api/client.ts) in this test -- never predicted (the "an id is
// only ever learned, never predicted" rule). No e2e/api/client.ts export minted a batch
// before this subtask; the shape is the same two multipart POSTs contract-import.spec.ts
// and import.spec.ts already drive locally.
test('deployed app: a review path cold-boots to the review surface', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(API_PERSONAS.A)
  const entity = await createEntity(token, { name: `ROUTE-06 review cold-boot ${Date.now()}`, tin: freshTin() })
  const batchId = await createImportBatch(token, entity.id, `INV-ROUTE06-REVIEW-${Date.now()}`)

  await signInAs(page, 'firm', { path: `/imports/${batchId}/review` })

  await expect(page, 'the review deep link did not keep its batch id').toHaveURL(new RegExp(`/imports/${batchId}/review$`))
  await expect(page.getByText(`BATCH ${batchId}`), 'the review surface did not render its batch header').toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ROUTE-06-07: the deployed proof for the popstate clamp (App.tsx:588-593). A company
// switch replaces the CURRENT history entry with the new identity but leaves older entries
// stamped with the old one, so Back walking PAST the switch must scrub the stale drill-down
// rather than resurface the previous company's invoice.
//
// Landing-first, per Decision [back-lives-in-auth-spec] (:243-246) -- starting at
// page.goto(APP_URL) would already be history entry one and prove nothing about Back. The
// row click is by TEXT (:306), never index: a positional click can fire before
// ctx.active.entityId resolves, which would stamp the entry with a null id and leave the
// clamp permanently unable to fire. Navigates to Audit (not back to Invoices) before the
// switch, so the later Forward assertion lands on a screen the clamp's own Back could not
// have produced by coincidence.
//
// Reuses the invoice-detail 404 gate (:251-261, not the shared collectErrors above): this
// journey also opens a fresh invoice's detail page, which fires the same unavoidable
// approval-run 404 on mount.
test("deployed app: Back past a company switch cannot resume the previous company's invoice", async ({ page }) => {
  const errors: string[] = []
  const drop = approvalRun404Dropper(page)
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (drop(msg.text(), msg.location().url)) return
    errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })

  // Fixtures first, before any navigation, so landing stays history entry one. Filed under
  // the PORTFOLIO's own first-sorted active entity (ORDER BY name ASC) -- the same one
  // sign-in leaves ctx.active on -- rather than a freshly created entity, which is not
  // guaranteed to sort first and would never reach the invoices list.
  const token = await login(API_PERSONAS.A)
  const { entities } = await listEntities(token, { status: 'active' })
  expect(entities.length, 'the firm seed must have at least one active entity to file under').toBeGreaterThan(0)
  const entity = entities[0]
  const invoiceNumber = `INV-ROUTE06-SWITCHBACK-${Date.now()}`
  await createInvoice(token, {
    entity_id: entity.id,
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  })

  await signInAs(page, 'firm')

  const nav = page.locator('aside.pf-sidebar nav.pf-nav-list')
  await nav.getByRole('button', { name: 'Invoices' }).click()
  await expect(page, 'nav to Invoices did not update the URL').toHaveURL(/\/invoices$/)

  const list = page.getByTestId('invoices-list')
  await expect(list).toBeVisible()
  await list.getByText(invoiceNumber, { exact: true }).click()
  await expect(page, 'the row click did not settle on /invoices/<uuid>').toHaveURL(/\/invoices\/[0-9a-f-]{36}$/)
  await expect(page.getByTestId('invoice-detail')).toBeVisible()

  await nav.getByRole('button', { name: 'Audit' }).click()
  await expect(page, 'nav to Audit did not update the URL').toHaveURL(/\/audit$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()

  // The switch, verbatim idiom from workflows.spec.ts:365-399: positional target, identity
  // asserted on the TIN line. beforeTin is captured so a no-op nth(1) -- picking the
  // already-active client -- fails loudly here instead of silently defeating the clamp below.
  const switcher = page.getByTestId('company-switcher')
  const switcherName = switcher.locator('span > span:not(.mono)')
  const switcherTin = switcher.locator('span.mono')
  await expect(switcherTin, 'the switcher must be on a REAL client before the baseline is taken').toHaveText(/^TIN \d/)
  const beforeTin = (await switcherTin.innerText()).trim()

  await switcher.click()
  const options = page.getByTestId('company-switcher-option')
  await expect(options.first()).toBeVisible()
  expect(
    await options.count(),
    'the firm seed must offer >=2 active clients to switch between (db/seed.dev.sql seeds 8)',
  ).toBeGreaterThanOrEqual(2)

  const target = options.nth(1)
  const targetName = (await target.locator('span > span:not(.mono)').innerText()).trim()
  await target.click()
  await expect(switcherName, 'the switcher must now show the client that was clicked').toHaveText(targetName)
  await expect(switcherTin, 'the switch was a no-op -- the TIN line did not move off the previous client').not.toHaveText(beforeTin)

  await page.goBack()
  // Same client the switch landed on -- this entry was restamped in place, not scrubbed --
  // so no clamp fires here.
  await expect(page, 'first Back did not restore /audit').toHaveURL(/\/audit$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true }), 'first Back did not rebuild the Audit screen').toBeVisible()

  await page.goBack()
  // The older drill-down entry still carries the PREVIOUS client's id; live identity has
  // moved on, so the clamp (App.tsx:588-593) must fire and collapse it to the list.
  await expect(page, 'second Back did not collapse the stale drill-down to /invoices').toHaveURL(/\/invoices$/)
  await expect(list, 'the invoices list must render, not a blank panel, after the clamp').toBeVisible()
  await expect(
    page.getByText(invoiceNumber, { exact: true }),
    "the previous client's invoice must not be on screen after the clamp",
  ).not.toBeVisible()

  await page.goForward()
  await expect(page, 'Forward did not re-apply /audit').toHaveURL(/\/audit$/)
  await expect(
    page.getByRole('heading', { level: 1, name: 'Audit log', exact: true }),
    'Forward did not rebuild the Audit screen',
  ).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// The real sign-in hand-off, driven through the landing form against a fresh GoTrue
// account with its own workspace (forks auto-confirm).
const SESSION_KEY = 'invoice-os.session'
const JWT_IN_URL = /eyJ[\w-]+\.[\w-]+\./
// internal/gateway/handoff.go HandoffTTL.
const HANDOFF_TTL_MS = 60_000
// frontend/landing/src/App.tsx SIGN_IN_OUTCOMES and src/signIn.ts INCORRECT.
const HANDOFF_FAILED = "We couldn't open your workspace. Sign in again."
const INCORRECT = 'Email or password is incorrect.'
// internal/gateway/signin.go: the exchange refusal.
const INVALID_CODE = 'invalid or expired code'

function recordUrls(page: Page): string[] {
  const urls: string[] = []
  page.on('framenavigated', (frame) => urls.push(frame.url()))
  page.on('request', (req) => urls.push(req.url()))
  return urls
}

// collectErrors, minus the listed deliberate non-2xx answers (consoleGate.ts).
function gatedErrors(page: Page, drops: Dropper[]): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (drops.some((drop) => drop(msg.text(), msg.location().url))) return
    errors.push(msg.text())
  })
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`))
  return errors
}

// Origin and path only: a failure message must not print the leaked secret.
function leakingUrls(urls: string[], ...secrets: string[]): string[] {
  return urls
    .filter((u) => JWT_IN_URL.test(u) || secrets.some((secret) => u.includes(secret)))
    .map((u) => {
      const at = new URL(u)
      return at.origin + at.pathname.replace(/eyJ[\w.-]*/g, '<jwt>')
    })
}

// The app origin's stored session in this context, or null.
async function storedSession(context: BrowserContext): Promise<string | null> {
  const { origins } = await context.storageState()
  const app = origins.find((o) => o.origin === new URL(APP_URL).origin)
  return app?.localStorage.find((e) => e.name === SESSION_KEY)?.value ?? null
}

test('deployed app: a real sign-in from the front door returns to its destination with no token in any URL', async ({ page, browser }) => {
  test.setTimeout(180_000)
  const account = await provisionRealAccount('handoff-door')
  const errors = collectErrors(page)
  const urls = recordUrls(page)

  await page.goto(`${APP_URL}/audit`)
  await page.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })
  await expect
    .poll(() => new URL(page.url()).searchParams.has('state'), { message: `landing kept ?state= at ${page.url()}` })
    .toBe(false)

  await page.getByRole('banner').getByRole('button', { name: 'Platform login' }).click()
  await expect(page.getByRole('dialog', { name: 'Platform login' })).toBeVisible()
  const [handoffNav] = await Promise.all([
    page.waitForRequest((r) => r.isNavigationRequest() && isHandoffNavigation(r.url())),
    submitSignIn(page, account.email, account.password),
  ])

  await expectInWorkspace(page, account)
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()
  await expect(page, 'the restored destination did not settle on /audit').toHaveURL(/\/audit$/)
  expect(new URL(page.url()).searchParams.has('handoff'), '?handoff= survived the redemption').toBe(false)

  const raw = await page.evaluate((key) => localStorage.getItem(key), SESSION_KEY)
  const session = JSON.parse(raw ?? 'null') as { token?: string; handoff?: boolean } | null
  expect(session?.handoff, 'the stored session is not marked as a hand-off').toBe(true)
  // Positive control for storedSession, which the CSRF journey reads only for absence.
  expect(await storedSession(page.context()), 'storedSession missed the app session').toBe(raw)
  const token = session!.token!
  expect(JWT_IN_URL.test(token), 'the stored token is not a JWT').toBe(true)
  expect(urls.length, 'no URLs were recorded').toBeGreaterThan(0)
  expect(leakingUrls(urls, token), 'the token or a JWT appeared in these URLs').toEqual([])

  // The code's own state, read off the app -> landing navigation: a right-state second redemption.
  const code = new URL(handoffNav.url()).searchParams.get('handoff')!
  const landingWithState = urls.find((u) => u.startsWith(LANDING_URL) && new URL(u).searchParams.has('state'))
  expect(landingWithState, 'no landing navigation carried ?state=').toBeDefined()
  const state = new URL(landingWithState!).searchParams.get('state')!
  const spent = await rawFetch('/auth/exchange', { method: 'POST', body: { code, state } })
  expect([spent.status, spent.body], 'the redeemed code, again').toEqual([400, { error: INVALID_CODE }])

  await page.reload()
  await expectInWorkspace(page, account)

  const restarted = await browser.newContext({ storageState: await page.context().storageState() })
  try {
    const next = await restarted.newPage()
    const nextErrors = collectErrors(next)
    await next.goto(APP_URL)
    await expectInWorkspace(next, account)
    expect(nextErrors, `console errors after the restart:\n${nextErrors.join('\n')}`).toEqual([])
  } finally {
    await restarted.close()
  }

  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

test('deployed app: a real sign-in from a direct landing visit bounces for a state, then signs in', async ({ page }) => {
  test.setTimeout(180_000)
  const account = await provisionRealAccount('handoff-direct')
  const errors = gatedErrors(page, [expectedStatusDropper(page, 401, /\/auth\/sign-in$/)])
  const urls = recordUrls(page)

  await page.goto(LANDING_URL)
  await page.getByRole('banner').getByRole('button', { name: 'Platform login' }).click()
  const dialog = page.getByRole('dialog', { name: 'Platform login' })
  await expect(dialog.getByRole('button', { name: 'Continue with email', exact: true })).toBeVisible()
  await expect(dialog.getByLabel('Work email', { exact: true }), 'a stateless landing must not offer the form').toHaveCount(0)

  // Armed before the click: app ?auth=start, then back to landing with signin=ready.
  await Promise.all([
    page.waitForRequest((r) => r.isNavigationRequest() && r.url().startsWith(APP_URL) && new URL(r.url()).searchParams.get('auth') === 'start'),
    page.waitForRequest((r) => r.isNavigationRequest() && r.url().startsWith(LANDING_URL) && new URL(r.url()).searchParams.get('signin') === 'ready'),
    dialog.getByRole('button', { name: 'Continue with email', exact: true }).click(),
  ])
  await expect(dialog.getByLabel('Work email', { exact: true })).toBeVisible()

  await submitSignIn(page, account.email, `${account.password}x`)
  await expect(dialog.getByRole('alert')).toContainText(INCORRECT)

  await Promise.all([
    page.waitForRequest((r) => r.isNavigationRequest() && isHandoffNavigation(r.url())),
    submitSignIn(page, account.email, account.password),
  ])
  await expectInWorkspace(page, account)
  expect(new URL(page.url()).searchParams.has('handoff'), '?handoff= survived the redemption').toBe(false)
  expect(urls.length, 'no URLs were recorded').toBeGreaterThan(0)
  expect(leakingUrls(urls), 'a JWT appeared in these URLs').toEqual([])

  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

// A code redeems only with the state its own tab minted on the app origin.
test('deployed app: a hand-off code minted in another browser signs no tab in', async ({ browser }) => {
  test.setTimeout(180_000)
  const account = await provisionRealAccount('handoff-csrf')
  const attackerState = mintSignInState()
  const exchangeUrl = `${GATEWAY_URL}/auth/exchange`

  await test.step('a tab holding no state makes no exchange call', async () => {
    const c1 = await signInForCode(account.email, account.password, attackerState)
    const context = await browser.newContext()
    try {
      const victim = await context.newPage()
      const errors = collectErrors(victim)
      const exchanges: string[] = []
      victim.on('request', (r) => {
        if (r.url() === exchangeUrl) exchanges.push(r.method())
      })

      await victim.goto(`${APP_URL}/?handoff=${c1}`)
      await victim.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })
      await expect(victim.getByRole('dialog', { name: 'Platform login' })).toContainText(HANDOFF_FAILED)
      expect(exchanges, 'the victim tab called /auth/exchange').toEqual([])
      expect(await storedSession(context), 'the victim tab stored a session').toBeNull()
      expect(errors, `console errors in the victim tab:\n${errors.join('\n')}`).toEqual([])
    } finally {
      await context.close()
    }
    // Positive control: the code was live, so only the missing state stopped it.
    expect(await exchangeCode(c1, attackerState)).toMatch(JWT_IN_URL)
  })

  await test.step("a tab holding its own state is refused, and the attacker's code is spent", async () => {
    const mintedAt = Date.now()
    const c2 = await signInForCode(account.email, account.password, attackerState)
    const context = await browser.newContext()
    try {
      const victim = await context.newPage()
      const errors = gatedErrors(victim, [expectedStatusDropper(victim, 400, /\/auth\/exchange$/)])

      await victim.goto(APP_URL)
      await victim.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })

      const [exchange] = await Promise.all([
        victim.waitForResponse((r) => r.url() === exchangeUrl && r.request().method() === 'POST'),
        victim.goto(`${APP_URL}/?handoff=${c2}`),
      ])
      expect(exchange.status(), 'the exchange with the victim tab state').toBe(400)
      // An expired code answers the same 400, which would prove nothing about the state.
      expect(Date.now() - mintedAt, 'the code could have expired before the victim tab redeemed it').toBeLessThan(HANDOFF_TTL_MS)
      await victim.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })
      await expect(victim.getByRole('dialog', { name: 'Platform login' })).toContainText(HANDOFF_FAILED)
      expect(await storedSession(context), 'the victim tab stored a session').toBeNull()
      expect(errors, `console errors in the victim tab:\n${errors.join('\n')}`).toEqual([])
    } finally {
      await context.close()
    }
    const spent = await rawFetch('/auth/exchange', { method: 'POST', body: { code: c2, state: attackerState } })
    expect([spent.status, spent.body], "the attacker's code after a wrong-state redemption").toEqual([400, { error: INVALID_CODE }])
  })
})

// The first screen of a fresh workspace: the add-company task.
async function expectAddCompanyTask(page: Page, h1: string): Promise<void> {
  await expect(page.getByTestId('add-company-task')).toBeVisible()
  await expect(page.getByRole('heading', { level: 1, name: h1, exact: true })).toBeVisible()
  await expect(page.getByText('COMPLIANCE OVERVIEW', { exact: true })).not.toBeVisible()
}

test('deployed app: a real in-house workspace has a Company tab and no Clients nav item', async ({ page }) => {
  test.setTimeout(180_000)
  const account = await provisionRealAccount('mode-inhouse', 'in_house')
  const errors = collectErrors(page)

  await signInAtFrontDoor(page, account, '/')
  await expectAddCompanyTask(page, 'Add your company')
  await expect.poll(() => sidebarRoster(page), { message: 'in-house sidebar roster' }).toContain('Settings')
  expect(await sidebarRoster(page), 'the in-house sidebar').not.toContain('Clients')

  await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: 'Settings' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Settings', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Company', exact: true }).click()
  await expect(page.getByText('Your company', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add company' })).toBeVisible()
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

test('deployed app: a real firm workspace has the Clients portfolio and no Company tab', async ({ page }) => {
  test.setTimeout(180_000)
  const account = await provisionRealAccount('mode-firm', 'firm')
  const errors = collectErrors(page)

  await signInAtFrontDoor(page, account, '/')
  await expectAddCompanyTask(page, 'Add your first client')
  await expect.poll(() => sidebarRoster(page), { message: 'firm sidebar roster' }).toContain('Clients')

  const nav = page.locator('aside.pf-sidebar nav.pf-nav-list')
  await nav.getByRole('button', { name: 'Clients' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Client portfolio', exact: true })).toBeVisible()

  await nav.getByRole('button', { name: 'Settings' }).click()
  await expect(page.getByRole('button', { name: 'Members', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Company', exact: true })).toHaveCount(0)
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

// frontend/app/src/lib/entityForm.ts TIN_HINT
const TIN_HINT = 'Use your 12-digit FIRS TIN. A 10-digit JTB TIN is accepted, but invoices filed under it fail the supplier TIN check.'
// internal/portfolio/tin.go: TINShapeMessage, TINLengthMessage (%d = 5), TINChecksumMessage
const TIN_SHAPE_REFUSAL = 'A TIN is digits only. Only the 12-digit FIRS TIN takes a hyphen, after the 8th digit: ########-####.'
const TIN_LENGTH_REFUSAL_5 = 'A TIN has 10 digits (JTB) or 12 digits (FIRS). This one has 5.'
const TIN_CHECKSUM_REFUSAL =
  "This TIN's last digit is a check digit, and it does not match the other digits. Check the number on the tax certificate."

const IMPORT_HEADER = 'Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price'

function sidebarNav(page: Page) {
  return page.locator('aside.pf-sidebar nav.pf-nav-list')
}

// New invoice → pick a one-row CSV → Read columns, stopping on the Map step.
async function openMapStep(page: Page, tag: string, amber: { title: string; shown: boolean }): Promise<void> {
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  // Control: the upload step is on screen, so an absent panel is not an unrendered step.
  await expect(page.getByRole('button', { name: 'Read columns' })).toBeVisible()
  await expect(page.getByText(amber.title, { exact: true })).toHaveCount(amber.shown ? 1 : 0)
  const csv = `${IMPORT_HEADER}\n${tag}-${Date.now()},2026-01-01,12345678-0001,Buyer Ltd,NGN,1000.00,75.00,1075.00,Consulting,1,1000.00\n`
  await page.locator('input[type="file"]#pf-import-file').setInputFiles({ name: `${tag}.csv`, mimeType: 'text/csv', buffer: Buffer.from(csv, 'utf8') })
  const preview = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await preview
  await expect(page.getByText('Map fields to columns ·', { exact: false })).toBeVisible({ timeout: 30_000 })
}

// Chip then column, no `exact: true` on the chip (import-wizard.spec.ts explains). Never clicks Import.
async function placeInvoiceNumberAndExpectImportEnabled(page: Page): Promise<void> {
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await expect(page.getByRole('button', { name: /^Import \d+ rows$/ })).toBeEnabled()
}

// CF2: each absence has its visible control in the same step.
async function expectNoDemoActivity(page: Page): Promise<void> {
  await expect(page.getByText('COMPLIANCE OVERVIEW', { exact: true })).toBeVisible()
  const tile = page.getByText('Recent activity', { exact: true }).locator('xpath=ancestor::div[2]')
  await expect(tile.getByText('No activity to show')).toBeVisible()
  await expect(tile.getByText('SAMPLE')).toHaveCount(0)
  await expect(tile.getByText('INV-2026-00481')).toHaveCount(0)
}

test('deployed app: a new in-house workspace lands on Add your company, and adding it opens the import', async ({ page }) => {
  test.setTimeout(240_000)
  const account = await provisionRealAccount('add-co-inhouse', 'in_house')
  const errors = gatedErrors(page, [expectedStatusDropper(page, 400, /\/api\/portfolio\/v1\/entities$/)])
  let refused = 0
  page.on('response', (res) => {
    if (res.status() === 400 && res.request().method() === 'POST' && /\/api\/portfolio\/v1\/entities$/.test(res.url())) refused += 1
  })

  await signInAtFrontDoor(page, account, '/')
  await expectAddCompanyTask(page, 'Add your company')

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await expect(page.getByText('Add your company before you file', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Add your company →' }).click()
  await expect(page).toHaveURL(/\/settings\/company$/)
  await expect(page.getByText('Your company', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add company' })).toBeVisible()
  await page.getByRole('button', { name: 'Add company' }).click()

  const dialog = page.getByRole('dialog', { name: 'Add company' })
  await expect(dialog.getByText(TIN_HINT, { exact: true })).toBeVisible()
  const name = `Add Co ${Date.now()}`
  const tin = dialog.getByPlaceholder('########-####')
  await dialog.getByRole('textbox').first().fill(name)
  const refusals: Array<[string, string]> = [
    ['BADTIN', TIN_SHAPE_REFUSAL],
    ['12345', TIN_LENGTH_REFUSAL_5],
    ['1234567890', TIN_CHECKSUM_REFUSAL],
  ]
  for (const [i, [value, reason]] of refusals.entries()) {
    await tin.fill(value)
    await dialog.getByRole('button', { name: 'Add company' }).click()
    await expect(dialog.getByText(reason, { exact: true }), `refusal of ${value}`).toBeVisible()
    for (const [, other] of refusals.filter((_, j) => j !== i)) {
      await expect(dialog.getByText(other, { exact: true })).toHaveCount(0)
    }
  }
  await expect(dialog).toBeVisible()

  await tin.fill(freshTin())
  await dialog.getByRole('button', { name: 'Add company' }).click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByText(name, { exact: true }).first()).toBeVisible()

  await sidebarNav(page).getByRole('button', { name: 'Overview' }).click()
  await expectNoDemoActivity(page)
  const chip = page.getByTestId('company-chip')
  await expect(chip).toContainText('WORKSPACE')
  await expect(chip.getByText('ERP', { exact: true })).toHaveCount(0)

  await sidebarNav(page).getByRole('button', { name: 'Settings' }).click()
  await page.getByRole('button', { name: 'ERP connectors', exact: true }).click()
  await expect(page.getByText('0 / 6 CONNECTED')).toBeVisible()
  await expect(page.getByText('Synced 2 min ago')).toHaveCount(0)
  await page.getByRole('button', { name: 'API & webhooks', exact: true }).click()
  await expect(page.getByText('No webhooks yet')).toBeVisible()
  await expect(page.getByText(/honeywell\.ng/)).toHaveCount(0)
  await page.getByRole('button', { name: 'Signing & certificates', exact: true }).click()
  await expect(page.getByText('No signing certificate yet')).toBeVisible()
  await expect(page.getByText('O=Okafor & Partners')).toHaveCount(0)

  await openMapStep(page, 'ADDCO-INH', { title: 'Add your company before you file', shown: false })
  await placeInvoiceNumberAndExpectImportEnabled(page)

  expect(refused, 'refused POSTs the gate may drop').toBe(3)
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

test('deployed app: a new firm lands on Add your first client, and the import step it gates opens once a client is added', async ({ page }) => {
  test.setTimeout(240_000)
  const account = await provisionRealAccount('add-co-firm', 'firm')
  const errors = collectErrors(page)

  await signInAtFrontDoor(page, account, '/')
  await expectAddCompanyTask(page, 'Add your first client')

  await openMapStep(page, 'ADDCO-FIRM', { title: 'Add a client before you file', shown: true })
  await expect(page.getByRole('button', { name: 'Filing needs a linked entity' })).toBeDisabled()

  await sidebarNav(page).getByRole('button', { name: 'Overview' }).click()
  await expectAddCompanyTask(page, 'Add your first client')
  await page.getByTestId('add-company-task').getByRole('button').click()
  const dialog = page.getByRole('dialog', { name: 'Add client' })
  await dialog.getByRole('textbox').first().fill(`Add Client ${Date.now()}`)
  await dialog.getByPlaceholder('########-####').fill(freshTin())
  await dialog.getByRole('button', { name: 'Add client' }).click()
  await expect(page.getByTestId('add-company-task')).toHaveCount(0)
  await expectNoDemoActivity(page)

  await sidebarNav(page).getByRole('button', { name: 'Rules' }).click()
  await expect(page.getByText('No custom rules yet — the golden ruleset alone is running.', { exact: true })).toBeVisible()
  await expect(page.getByText('No suggestions to show.', { exact: true })).toBeVisible()
  await expect(page.getByText('Derived from')).toHaveCount(0)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page.getByRole('button', { name: 'Skip — enter manually' }).click()
  await expect(page.getByText('New invoice ·', { exact: false })).toBeVisible()
  // Input values are not text: toHaveValue, never getByText.
  await expect(page.getByPlaceholder('INV-0000-00000')).toHaveValue('')
  await expect(page.getByText('Buyer name', { exact: true }).locator('xpath=following-sibling::input')).toHaveValue('')
  await expect(page.getByPlaceholder('Description').first()).toHaveValue('')

  await openMapStep(page, 'ADDCO-FIRM', { title: 'Add a client before you file', shown: false })
  await placeInvoiceNumberAndExpectImportEnabled(page)

  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

test('deployed app: a real sign-in names the account holder on the identity card', async ({ page, context }) => {
  test.setTimeout(180_000)
  const account = await provisionRealAccount('card-name', undefined, 'Ada Nwosu')
  const errors = collectErrors(page)

  await signInAtFrontDoor(page, account, '/')
  const name = page.getByTestId('persona-name')
  // Read once, no retry: the card already names the person when the badge appears.
  expect(await name.textContent(), 'the card name when the verified badge attached').toBe('Ada Nwosu')
  await expect(page.getByTestId('persona-initials')).toHaveText('AN')
  await expect(page.locator('aside.pf-sidebar')).not.toContainText('Chinedu Okafor')

  const stored = JSON.parse((await storedSession(context)) ?? 'null') as { me: Me | null } | null
  expect(stored?.me?.user.display_name, 'the stored me.user.display_name').toBe('Ada Nwosu')
  expect(stored?.me?.user.email, 'the stored me.user.email').toBe(account.email)

  // Control for the 197-character journey: a short name is not clipped.
  const short = await name.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
  expect(short.scrollWidth, 'the short name is clipped').toBeLessThanOrEqual(short.clientWidth)

  await page.reload()
  await expect(page.locator(VERIFIED)).toBeAttached({ timeout: 30_000 })
  expect(page.url().startsWith(LANDING_URL), 'the reload went back to landing').toBe(false)
  await expect(name).toHaveText('Ada Nwosu')
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

// internal/tenancy/tenancy.go maxNameChars is 200; 6 x 32 + 5 spaces = 197.
const LONG_NAME = 'Oluwaseyifunmi Adebanjo-Ogunleye '.repeat(6).trim()
// One unbroken 200-char word: the add-company subtitle must wrap it, not scroll the page.
const LONG_WORKSPACE = 'W'.repeat(200)

test('deployed app: a 197-character name stays inside the identity card at every wide width', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  expect(LONG_NAME.length).toBe(197)
  const account = await provisionRealAccount('card-long', undefined, LONG_NAME, LONG_WORKSPACE)
  const errors = collectErrors(page)

  await signInAtFrontDoor(page, account, '/')
  const aside = page.locator('aside.pf-sidebar')
  const main = page.locator('main.pf-main')
  const name = page.getByTestId('persona-name')
  const trigger = page.getByTestId('identity-card')
  const signOut = aside.getByRole('button', { name: 'Sign out' })
  await expect(page.getByTestId('add-company-task')).toBeVisible({ timeout: 30_000 })
  await expect(main.getByText(LONG_WORKSPACE, { exact: false })).toBeVisible()

  const readings: { width: number; scrollWidth: number; clientWidth: number }[] = []
  for (const width of WIDE_WIDTHS) {
    await page.setViewportSize({ width, height: 1080 })
    await settleAnimations(aside, trigger, signOut)
    const [asideBox, mainBox, triggerBox, signOutBox] = await Promise.all([aside.boundingBox(), main.boundingBox(), trigger.boundingBox(), signOut.boundingBox()])
    if (!asideBox || !mainBox || !triggerBox || !signOutBox) throw new Error(`aside, main, trigger or Sign out rendered no box at ${width}px`)

    expect(await name.textContent(), `the card name at ${width}px`).toBe(LONG_NAME)
    const reading = await name.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
    expect(reading.scrollWidth, `the name is not clipped by its own box at ${width}px (${JSON.stringify(reading)})`).toBeGreaterThan(reading.clientWidth)
    expect(enclosesRect(asideBox, triggerBox, 1), `the trigger leaves the aside at ${width}px (${JSON.stringify({ asideBox, triggerBox })})`).toBe(true)
    expect(enclosesRect(asideBox, signOutBox, 1), `Sign out leaves the aside at ${width}px (${JSON.stringify({ asideBox, signOutBox })})`).toBe(true)
    expect(rectsOverlap(signOutBox, triggerBox), `Sign out overlaps the trigger at ${width}px`).toBe(false)
    expect(rectsOverlap(asideBox, mainBox), `the aside overlaps main at ${width}px`).toBe(false)
    await assertPageDoesNotScrollSideways(page, `long name at ${width}px`)
    readings.push({ width, ...reading })
  }
  expect(readings.length, 'widths measured').toBe(WIDE_WIDTHS.length)
  await testInfo.attach('name-readings', { body: JSON.stringify(readings, null, 2), contentType: 'application/json' })
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

interface StoredRenewal {
  token: string
  refresh_token: string
  received_at: number
}

async function storedRenewal(page: Page): Promise<StoredRenewal> {
  const raw = await page.evaluate((key) => localStorage.getItem(key), SESSION_KEY)
  return JSON.parse(raw ?? 'null') as StoredRenewal
}

// Moves received_at back past GOTRUE_JWT_EXP (3600 s, docs/identity-provider.md), whatever the page clock reads.
async function ageStoredSession(page: Page, patch: Partial<StoredRenewal> = {}): Promise<void> {
  await page.evaluate(
    ({ key, patch, ageMs }) => {
      const s = JSON.parse(localStorage.getItem(key)!)
      localStorage.setItem(key, JSON.stringify({ ...s, ...patch, received_at: s.received_at - ageMs }))
    },
    { key: SESSION_KEY, patch, ageMs: 61 * 60_000 },
  )
}

// Gateway requests in send order. The browser's CORS preflights are not application requests.
function recordGatewayRequests(page: Page): Request[] {
  const sent: Request[] = []
  page.on('request', (req) => {
    if (req.url().startsWith(GATEWAY_URL) && req.method() !== 'OPTIONS') sent.push(req)
  })
  return sent
}

// sent.length at the main frame's next commit: the old document can send nothing after it.
function markNextCommit(page: Page, sent: Request[]): () => number {
  let mark = -1
  const onCommit = (frame: Frame) => {
    if (frame !== page.mainFrame()) return
    mark = sent.length
    page.off('framenavigated', onCommit)
  }
  page.on('framenavigated', onCommit)
  return () => {
    expect(mark, 'the reload never committed').toBeGreaterThanOrEqual(0)
    return mark
  }
}

const REFRESH_URL = `${GATEWAY_URL}/auth/refresh`
// frontend/app/src/components/SignIn.tsx SignInLoading, with no persona.
const OPENING = 'Opening your workspace…'
const isApi = (r: Request) => r.url().startsWith(`${GATEWAY_URL}/api/`)
const requestLine = (r: Request) => `${r.method()} ${new URL(r.url()).pathname}`

test("deployed app: a real session renews itself past the access token's lifetime", async ({ page }) => {
  test.setTimeout(180_000)
  const account = await provisionRealAccount('renewal')
  await page.clock.install()
  const errors = collectErrors(page)
  const urls = recordUrls(page)
  const sent = recordGatewayRequests(page)
  const statuses: string[] = []
  page.on('response', (res) => statuses.push(`${res.status()} ${res.request().method()} ${res.url().split('?')[0]}`))

  await signInAtFrontDoor(page, account, '/')
  const first = await storedRenewal(page)
  expect(JWT_IN_URL.test(first.token), 'the stored token is not a JWT').toBe(true)
  expect(first.refresh_token, 'the hand-off stored no refresh token').toBeTruthy()

  await test.step('past the lifetime by the browser clock, the next request renews first', async () => {
    // Settle the dashboard's loads, so "after the jump" holds only requests the jump caused.
    await page.waitForLoadState('networkidle')
    const refreshed = page.waitForResponse((r) => r.url() === REFRESH_URL && r.request().method() === 'POST')
    const mark = sent.length
    // Past GOTRUE_JWT_EXP (3600 s). useLiveRefresh's interval may fire here and send the first request itself.
    await page.clock.fastForward('01:05:00')
    await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: 'Audit' }).click()
    expect((await refreshed).status(), 'the renewal answer').toBe(200)
    await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()
    await expect.poll(() => sent.slice(mark).filter(isApi).length, { message: 'no data request after the renewal' }).toBeGreaterThan(0)

    const after = sent.slice(mark)
    expect(requestLine(after[0]), 'the first gateway request after the jump').toBe('POST /auth/refresh')
    await expect.poll(async () => (await storedRenewal(page)).token, { message: 'the stored token did not change' }).not.toBe(first.token)
    const renewed = await storedRenewal(page)
    expect(renewed.refresh_token, 'the stored refresh token did not rotate').not.toBe(first.refresh_token)
    for (const req of after.filter(isApi)) {
      expect(await req.headerValue('authorization'), `${requestLine(req)} did not carry the renewed token`).toBe(`Bearer ${renewed.token}`)
    }
    await expect(page.locator(VERIFIED)).toBeAttached()
  })

  const second = await storedRenewal(page)
  let third = second

  await test.step('a due stored session renews before the workspace mounts', async () => {
    await ageStoredSession(page)
    // Held while the page is read, so "before the workspace mounts" is observed, not inferred.
    let atRefresh: { opening: boolean; verified: number } | null = null
    await page.route(REFRESH_URL, async (route) => {
      try {
        if (atRefresh === null && route.request().method() === 'POST') {
          atRefresh = { opening: await page.getByText(OPENING, { exact: true }).isVisible(), verified: await page.locator(VERIFIED).count() }
        }
      } finally {
        await route.continue()
      }
    })
    const markAt = markNextCommit(page, sent)
    await page.reload()
    await expectInWorkspace(page, account)
    await page.unroute(REFRESH_URL)
    const mark = markAt()
    expect(atRefresh, 'the page while the boot renewal was out').toEqual({ opening: true, verified: 0 })
    await expect.poll(() => sent.slice(mark).filter(isApi).length, { message: 'no data request after the reload' }).toBeGreaterThan(0)

    const after = sent.slice(mark)
    expect(requestLine(after[0]), 'the first gateway request after the reload').toBe('POST /auth/refresh')
    third = await storedRenewal(page)
    expect(third.token, 'the boot renewal did not change the stored token').not.toBe(second.token)
    for (const req of after.filter(isApi)) {
      expect(await req.headerValue('authorization'), `${requestLine(req)} did not carry the boot-renewed token`).toBe(`Bearer ${third.token}`)
    }
  })

  expect(third.refresh_token, 'the boot renewal did not rotate the refresh token').not.toBe(second.refresh_token)
  expect(statuses.filter((s) => s.startsWith('401 ')), 'a response in the journey was 401').toEqual([])
  expect(
    leakingUrls(urls, first.token, first.refresh_token, second.token, second.refresh_token, third.token, third.refresh_token),
    'a token, a refresh token or a JWT appeared in these URLs',
  ).toEqual([])
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

test('deployed app: a refused renewal returns to landing and keeps the destination', async ({ page }) => {
  test.setTimeout(180_000)
  const account = await provisionRealAccount('renewal-refused')
  const errors = gatedErrors(page, [expectedStatusDropper(page, 401, /\/auth\/refresh$/)])
  const urls = recordUrls(page)
  const sent = recordGatewayRequests(page)

  await signInAtFrontDoor(page, account, '/audit')
  await expect(page, 'the first sign-in did not settle on /audit').toHaveURL(/\/audit$/)
  const signedIn = await storedRenewal(page)

  // An unknown refresh token of GoTrue's length: refused as refresh_token_not_found.
  await ageStoredSession(page, { refresh_token: 'aaaaaaaaaaaa' })
  const urlMark = urls.length
  const markAt = markNextCommit(page, sent)
  const refusal = page.waitForResponse((r) => r.url() === REFRESH_URL && r.request().method() === 'POST')
  // Commit only: the refusal navigates to landing, which may beat the reload's load event.
  await page.reload({ waitUntil: 'commit' })
  expect((await refusal).status(), 'the refused renewal').toBe(401)
  await page.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })
  expect(sent.slice(markAt()).map(requestLine), 'the reloaded app sent more than the refused renewal').toEqual(['POST /auth/refresh'])
  expect(
    urls.slice(urlMark).some((u) => u.startsWith(LANDING_URL) && new URL(u).searchParams.has('state')),
    'the refusal reached landing without a sign-in state',
  ).toBe(true)
  expect(await storedSession(page.context()), 'the app origin kept a stored session').toBeNull()

  await page.getByRole('banner').getByRole('button', { name: 'Platform login' }).click()
  await Promise.all([
    page.waitForRequest((r) => r.isNavigationRequest() && isHandoffNavigation(r.url())),
    submitSignIn(page, account.email, account.password),
  ])
  await expectInWorkspace(page, account)
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()
  await expect(page, 'the destination was not restored after the new sign-in').toHaveURL(/\/audit$/)
  const again = await storedRenewal(page)

  expect(
    leakingUrls(urls, signedIn.token, signedIn.refresh_token, again.token, again.refresh_token),
    'a token, a refresh token or a JWT appeared in these URLs',
  ).toEqual([])
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

const SIGN_OUT_URL = `${GATEWAY_URL}/auth/sign-out`
// internal/gateway/refresh.go RefreshHandler.
const INVALID_REFRESH = 'invalid or expired refresh token'
const isApiResponse = (r: Response) => isApi(r.request()) && r.request().method() !== 'OPTIONS'

// The landing strips ?state= on mount, so the URL shows it only briefly; wait for the response instead.
const frontDoor = (page: Page) =>
  page.waitForResponse((r) => r.request().isNavigationRequest() && r.url().startsWith(LANDING_URL) && new URL(r.url()).searchParams.has('state'), {
    timeout: 20_000,
  })

// Two contexts are two devices: global sign-out ends both, and a record copied before it replays into nothing.
test('deployed app: signing out on one device ends the session on every device', async ({ browser }) => {
  test.setTimeout(300_000)
  const account = await provisionRealAccount('sign-out-everywhere')
  const appOrigin = new URL(APP_URL).origin
  const contexts: BrowserContext[] = []
  const open = async () => {
    const context = await browser.newContext()
    contexts.push(context)
    const page = await context.newPage()
    return { context, page, errors: gatedErrors(page, [expectedStatusDropper(page, 401, /\/api\//)]), urls: recordUrls(page) }
  }

  try {
    const a = await open()
    const b = await open()
    await signInAtFrontDoor(a.page, account, '/')
    await signInAtFrontDoor(b.page, account, '/audit')
    await expect(b.page, 'B did not settle on /audit').toHaveURL(/\/audit$/)
    const copiedA = await storedSession(a.context)
    expect(copiedA, 'A stored no session').not.toBeNull()
    const recordA = JSON.parse(copiedA!) as StoredRenewal
    const recordB = await storedRenewal(b.page)

    await test.step('A signs out: the server revokes before A leaves', async () => {
      // The front door's second navigation aborts the first (D10), and Playwright times only answered requests,
      // so both sides come from CDP on Chromium's monotonic clock.
      const cdp = await a.context.newCDPSession(a.page)
      await cdp.send('Network.enable')
      const signOutPosts = new Set<string>()
      let answeredAt: number | undefined
      const leaving: { url: string; at: number }[] = []
      cdp.on('Network.requestWillBeSent', (e) => {
        if (e.request.url === SIGN_OUT_URL && e.request.method === 'POST') signOutPosts.add(e.requestId)
        if (e.type === 'Document' && e.request.url.startsWith(LANDING_URL)) leaving.push({ url: e.request.url, at: e.timestamp })
      })
      cdp.on('Network.responseReceived', (e) => {
        const t = e.response.timing
        if (signOutPosts.has(e.requestId)) answeredAt = t && t.receiveHeadersEnd > 0 ? t.requestTime + t.receiveHeadersEnd / 1000 : 0
      })
      const signedOut = a.page.waitForResponse((r) => r.url() === SIGN_OUT_URL && r.request().method() === 'POST', { timeout: 20_000 })
      const aFrontDoor = frontDoor(a.page)
      await a.page.getByRole('button', { name: 'Sign out' }).click()
      expect((await signedOut).status(), 'the sign-out answer').toBe(204)
      expect((await aFrontDoor).status(), "A's front door answer").toBe(200)
      await expect.poll(() => leaving.length, { message: 'A did not leave in two landing navigations', timeout: 20_000 }).toBeGreaterThanOrEqual(2)
      expect(
        leaving.map((l) => l.url.slice(LANDING_URL.length).replace(/^\/$/, '')),
        'A left by other than bare landing, then the front door (D10)',
      ).toEqual(['', expect.stringMatching(/^\/\?state=[A-Za-z0-9_-]{43}$/)])
      await expect.poll(() => answeredAt, { message: 'the sign-out answer carries no timing', timeout: 20_000 }).toBeGreaterThan(0)
      expect(answeredAt!, 'the sign-out answer arrived after A began leaving').toBeLessThan(leaving[0].at)
      expect(await storedSession(a.context), 'A kept a stored session').toBeNull()
      await cdp.detach()
    })

    await test.step("B's next request is refused and B returns to landing", async () => {
      const refused = b.page.waitForResponse(isApiResponse)
      const bFrontDoor = frontDoor(b.page)
      await b.page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: 'Invoices' }).click()
      expect((await refused).status(), "B's first /api/ answer after the sign-out").toBe(401)
      expect((await bFrontDoor).status(), "B's front door answer").toBe(200)
      await expect(b.page.getByRole('banner').getByRole('button', { name: 'Platform login' })).toBeVisible()
      expect(await storedSession(b.context), 'B kept a stored session').toBeNull()

      const renewed = await rawFetch('/auth/refresh', { method: 'POST', body: { refresh_token: recordB.refresh_token } })
      expect([renewed.status, renewed.body], "B's refresh token after the sign-out").toEqual([401, { error: INVALID_REFRESH }])
    })

    await test.step('B signs in again and lands on /, not the old destination', async () => {
      await b.page.getByRole('banner').getByRole('button', { name: 'Platform login' }).click()
      await Promise.all([
        b.page.waitForRequest((r) => r.isNavigationRequest() && isHandoffNavigation(r.url())),
        submitSignIn(b.page, account.email, account.password),
      ])
      await expectInWorkspace(b.page, account)
      await expect
        .poll(() => new URL(b.page.url()).pathname, { message: 'B did not land on /' })
        .toBe('/')
    })
    const againB = await storedRenewal(b.page)

    const c = await open()
    await test.step("A's copied record replayed in a fresh context mounts, is refused and ends", async () => {
      await c.page.addInitScript(
        ({ origin, key, record }) => {
          if (location.origin !== origin || sessionStorage.getItem('e2e.replayed')) return
          sessionStorage.setItem('e2e.replayed', '1')
          localStorage.setItem(key, record)
        },
        { origin: appOrigin, key: SESSION_KEY, record: copiedA! },
      )
      // Every /api/ request waits for one look at the page, so no 401 can navigate away first.
      let firstLook: Promise<[number, string | null]> | undefined
      await c.page.route(`${GATEWAY_URL}/api/**`, async (route) => {
        const req = route.request()
        try {
          if (req.method() !== 'OPTIONS') {
            firstLook ??= Promise.all([c.page.locator('aside.pf-sidebar').count(), req.headerValue('authorization')])
            await firstLook
          }
        } finally {
          await route.continue()
        }
      })
      const refused = c.page.waitForResponse(isApiResponse)
      // Commit only: the 401 navigates to landing, which may beat the load event.
      const cFrontDoor = frontDoor(c.page)
      await c.page.goto(APP_URL, { waitUntil: 'commit' })
      expect((await refused).status(), "C's first /api/ answer").toBe(401)
      expect((await cFrontDoor).status(), "C's front door answer").toBe(200)
      await c.page.unroute(`${GATEWAY_URL}/api/**`)
      expect(await firstLook, 'the shell and the Authorization header at the first /api/ request').toEqual([1, `Bearer ${recordA.token}`])
      expect(await storedSession(c.context), 'C kept a stored session').toBeNull()
    })

    for (const [name, urls] of [['A', a.urls], ['B', b.urls], ['C', c.urls]] as const) {
      expect(urls.length, `no URLs were recorded in ${name}`).toBeGreaterThan(0)
    }
    expect(
      leakingUrls([...a.urls, ...b.urls, ...c.urls], recordA.token, recordA.refresh_token, recordB.token, recordB.refresh_token, againB.token, againB.refresh_token),
      'a token, a refresh token or a JWT appeared in these URLs',
    ).toEqual([])
    for (const [name, errors] of [['A', a.errors], ['B', b.errors], ['C', c.errors]] as const) {
      expect(errors, `console errors in ${name}:\n${errors.join('\n')}`).toEqual([])
    }
  } finally {
    await Promise.all(contexts.map((context) => context.close()))
  }
})

// Both consoles take a staff session. Each journey drives landing's real form against real GoTrue accounts.
const CONSOLE_TARGETS: readonly ConsoleTarget[] = ['ops', 'support']
const NOT_STAFF = 'This account cannot open the ASComply consoles.'

const isConsoleHandoff = (url: string, target: ConsoleTarget) => url.startsWith(consoleUrl(target)) && new URL(url).searchParams.has('handoff')
const isRefresh = (r: Request | Response) => r.url() === REFRESH_URL
const refreshAnswer = (page: Page) => page.waitForResponse((r) => isRefresh(r) && r.request().method() === 'POST')

// The console origin's stored session in this context, or null.
async function consoleRecord(context: BrowserContext, target: ConsoleTarget): Promise<{ v: number; token: string; refresh_token: string } | null> {
  const { origins } = await context.storageState()
  const entry = origins.find((o) => o.origin === new URL(consoleUrl(target)).origin)?.localStorage.find((e) => e.name === CONSOLE_SESSION_KEY[target])
  return entry ? JSON.parse(entry.value) : null
}

// Writes a hand-made record into the console origin before any page script runs.
async function seedRawConsoleRecord(context: BrowserContext, target: ConsoleTarget, value: string): Promise<void> {
  await context.addInitScript(
    ({ origin, key, record }) => {
      if (location.origin !== origin || sessionStorage.getItem('e2e.raw-seeded')) return
      sessionStorage.setItem('e2e.raw-seeded', '1')
      localStorage.setItem(key, record)
    },
    { origin: new URL(consoleUrl(target)).origin, key: CONSOLE_SESSION_KEY[target], record: value },
  )
}

// A console visit with no usable session: the console leaves for landing at once, so wait on the commit.
async function visitConsole(page: Page, target: ConsoleTarget): Promise<void> {
  const door = consoleFrontDoor(page, target)
  await page.goto(consoleUrl(target), { waitUntil: 'commit' })
  expect((await door).status(), `the ${target} front door answer`).toBe(200)
  await page.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })
}

async function signInThroughLanding(page: Page, target: ConsoleTarget, account: { email: string; password: string }): Promise<Request> {
  await page.getByRole('banner').getByRole('button', { name: 'Platform login' }).click({ timeout: 15_000 })
  const [handoff] = await Promise.all([
    page.waitForRequest((r) => r.isNavigationRequest() && isConsoleHandoff(r.url(), target)),
    submitSignIn(page, account.email, account.password),
  ])
  return handoff
}

test('deployed consoles: a staff session signs in through landing, opens each console and survives a reload, with no token in any URL', async ({ browser }) => {
  test.setTimeout(240_000)
  const account = await provisionStaffAccount('console-door')

  for (const target of CONSOLE_TARGETS) {
    await test.step(`${target}-console`, async () => {
      const context = await browser.newContext()
      try {
        const page = await context.newPage()
        const errors = gatedErrors(page, [])
        const urls = recordUrls(page)

        await visitConsole(page, target)
        const handoff = await signInThroughLanding(page, target, account)
        expect(new URL(handoff.url()).searchParams.get('handoff'), 'the hand-off code').toMatch(/^[A-Za-z0-9_-]{43}$/)
        await DESTINATION_READY[target](page)
        expect(new URL(page.url()).searchParams.has('handoff'), '?handoff= survived the redemption').toBe(false)

        const stored = await consoleRecord(context, target)
        expect(stored, `${target} stored no session`).not.toBeNull()
        expect(stored!.v, 'the stored record version').toBe(2)
        expect(JWT_IN_URL.test(stored!.token), 'the stored token is not a JWT').toBe(true)
        expect((claimsOf(stored!.token).app_metadata as { staff?: unknown }).staff, 'staff on the stored token').toBe(true)

        await page.reload()
        await DESTINATION_READY[target](page)
        const renewed = await consoleRecord(context, target)
        expect(renewed, `${target} lost its session on reload`).not.toBeNull()

        expect(urls.length, 'no URLs were recorded').toBeGreaterThan(0)
        expect(
          leakingUrls(urls, stored!.token, stored!.refresh_token, renewed!.token, renewed!.refresh_token),
          'a token, a refresh token or a JWT appeared in these URLs',
        ).toEqual([])
        expect(errors, `console errors on the ${target} journey:\n${errors.join('\n')}`).toEqual([])
      } finally {
        await context.close()
      }
    })
  }
})

test("deployed consoles: a customer's real session opens neither console and is told why", async ({ browser }) => {
  test.setTimeout(240_000)
  const account = await provisionRealAccount('console-customer')

  for (const target of CONSOLE_TARGETS) {
    await test.step(`${target}-console`, async () => {
      const context = await browser.newContext()
      try {
        const page = await context.newPage()
        const errors = gatedErrors(page, [])
        const urls = recordUrls(page)

        await visitConsole(page, target)
        // Armed before the sign-in: the console redeems the code, finds no staff claim, and leaves.
        const notStaff = page.waitForResponse(
          (r) => {
            if (!r.request().isNavigationRequest() || !r.url().startsWith(LANDING_URL)) return false
            const q = new URL(r.url()).searchParams
            return q.get('signin') === 'not-staff' && q.get('console') === target && q.has('state')
          },
          { timeout: 30_000 },
        )
        await signInThroughLanding(page, target, account)
        expect((await notStaff).status(), `the ${target} not-staff landing answer`).toBe(200)
        await page.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })
        await expect(page.getByRole('dialog', { name: 'Platform login' }).getByRole('alert')).toContainText(NOT_STAFF)

        expect(await consoleRecord(context, target), `${target} kept a session for a customer`).toBeNull()
        expect(urls.length, 'no URLs were recorded').toBeGreaterThan(0)
        expect(leakingUrls(urls), 'a JWT appeared in these URLs').toEqual([])
        expect(errors, `console errors on the ${target} journey:\n${errors.join('\n')}`).toEqual([])
      } finally {
        await context.close()
      }
    })
  }
})

const b64u = (value: unknown) => Buffer.from(JSON.stringify(value)).toString('base64url')

test('deployed consoles: a hand-written or forged record opens nothing and ends on landing', async ({ browser }) => {
  test.setTimeout(240_000)
  const forgedToken = `${b64u({ alg: 'none', typ: 'JWT' })}.${b64u({ sub: crypto.randomUUID(), app_metadata: { staff: true }, exp: Math.floor(Date.now() / 1000) + 3600 })}.`
  const records = {
    v1: JSON.stringify({ v: 1, operator: { id: 'amara', name: 'Amara Okafor' } }),
    forged: JSON.stringify({ v: 2, token: forgedToken, refresh_token: 'aaaaaaaaaaaa' }),
  }

  for (const target of CONSOLE_TARGETS) {
    for (const [kind, record] of Object.entries(records)) {
      await test.step(`${target}-console, ${kind} record`, async () => {
        const context = await browser.newContext()
        try {
          const page = await context.newPage()
          // Only the forged record reaches /auth/refresh, and only that answer is expected.
          const errors = gatedErrors(page, kind === 'forged' ? [expectedStatusDropper(page, 401, /\/auth\/refresh$/)] : [])
          const urls = recordUrls(page)
          const warnings: string[] = []
          page.on('console', (msg) => {
            if (msg.type() === 'warning') warnings.push(msg.text())
          })
          await seedRawConsoleRecord(context, target, record)
          const refused = kind === 'forged' ? refreshAnswer(page) : null

          await visitConsole(page, target)
          if (refused) {
            expect((await refused).status(), `the ${target} refresh of the forged record`).toBe(401)
            expect(await consoleRecord(context, target), `${target} kept the forged record`).toBeNull()
          } else {
            // A v1 record is ignored, never sent to the gateway. The warning proves the console read it.
            await expect
              .poll(() => warnings.some((w) => w.includes(`ignoring unusable stored session at "${CONSOLE_SESSION_KEY[target]}"`)), { message: `the ${target} console never read the v1 record` })
              .toBe(true)
            expect(urls.filter((u) => u === REFRESH_URL), `${target} sent the v1 record to /auth/refresh`).toEqual([])
          }
          expect(urls.length, 'no URLs were recorded').toBeGreaterThan(0)
          expect(errors, `console errors on the ${target} ${kind} load:\n${errors.join('\n')}`).toEqual([])
        } finally {
          await context.close()
        }
      })
    }
  }
})

test('deployed consoles: a console load renews the stored session and stays in the console', async ({ browser }) => {
  test.setTimeout(240_000)
  const account = await provisionStaffAccount('console-renew')

  for (const target of CONSOLE_TARGETS) {
    await test.step(`${target}-console`, async () => {
      const context = await browser.newContext()
      try {
        const page = await context.newPage()
        const errors = gatedErrors(page, [])
        const urls = recordUrls(page)
        const seeded = await seedStaffSession(page, target, account)
        const first = await consoleRecord(context, target)
        expect(first, `${target} stored no session after the first load`).not.toBeNull()
        // The refresh token always rotates; two access tokens minted in one second can be identical.
        expect(first!.refresh_token, 'the first load did not renew the seeded pair').not.toBe(seeded.refresh_token)

        const renewal = refreshAnswer(page)
        await page.reload()
        expect((await renewal).status(), `the ${target} renewal on reload`).toBe(200)
        await DESTINATION_READY[target](page)
        expect(page.url().startsWith(consoleUrl(target)), `the reload left ${page.url()}`).toBe(true)

        const second = await consoleRecord(context, target)
        expect(second, `${target} stored no session after the reload`).not.toBeNull()
        expect(second!.refresh_token, 'the reload did not change the stored pair').not.toBe(first!.refresh_token)

        expect(urls.length, 'no URLs were recorded').toBeGreaterThan(0)
        expect(
          leakingUrls(urls, seeded.token, seeded.refresh_token, first!.token, first!.refresh_token, second!.token, second!.refresh_token),
          'a token, a refresh token or a JWT appeared in these URLs',
        ).toEqual([])
        expect(errors, `console errors on the ${target} journey:\n${errors.join('\n')}`).toEqual([])
      } finally {
        await context.close()
      }
    })
  }
})

test('deployed consoles: signing out of the Support Console ends the Ops Console session', async ({ browser }) => {
  test.setTimeout(240_000)
  const account = await provisionStaffAccount('console-sign-out')
  const context = await browser.newContext()
  try {
    const ops = await context.newPage()
    const support = await context.newPage()
    const opsErrors = gatedErrors(ops, [expectedStatusDropper(ops, 401, /\/auth\/refresh$/)])
    const supportErrors = gatedErrors(support, [])
    const opsUrls = recordUrls(ops)
    const supportUrls = recordUrls(support)
    const seededOps = await seedStaffSession(ops, 'ops', account)
    const seededSupport = await seedStaffSession(support, 'support', account)
    const opsBefore = await consoleRecord(context, 'ops')
    expect(opsBefore, 'the Ops Console stored no session').not.toBeNull()
    expect(await consoleRecord(context, 'support'), 'the Support Console stored no session').not.toBeNull()

    const signedOut = support.waitForResponse((r) => r.url() === SIGN_OUT_URL && r.request().method() === 'POST', { timeout: 20_000 })
    await support.getByRole('button', { name: 'Sign out' }).click()
    expect((await signedOut).status(), 'the sign-out answer').toBe(204)
    await support.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })
    expect(await consoleRecord(context, 'support'), 'the Support Console kept a session').toBeNull()

    // Armed before the reload: the refused renewal and the front door both come from the one load.
    const refused = refreshAnswer(ops)
    const door = consoleFrontDoor(ops, 'ops')
    await ops.reload({ waitUntil: 'commit' })
    expect((await refused).status(), 'the Ops Console renewal after the sign-out').toBe(401)
    expect((await door).status(), "the Ops Console's front door answer").toBe(200)
    await ops.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })
    expect(await consoleRecord(context, 'ops'), 'the Ops Console kept a session').toBeNull()

    expect(opsUrls.length + supportUrls.length, 'no URLs were recorded').toBeGreaterThan(0)
    expect(
      leakingUrls([...opsUrls, ...supportUrls], seededOps.token, seededOps.refresh_token, seededSupport.token, seededSupport.refresh_token, opsBefore!.token, opsBefore!.refresh_token),
      'a token, a refresh token or a JWT appeared in these URLs',
    ).toEqual([])
    expect(opsErrors, `console errors in the Ops Console:\n${opsErrors.join('\n')}`).toEqual([])
    expect(supportErrors, `console errors in the Support Console:\n${supportErrors.join('\n')}`).toEqual([])
  } finally {
    await context.close()
  }
})

// frontend/landing/src/register.ts FREE_MAIL_REFUSED, from internal/gateway/register.go's isFreeMail guard.
const FREE_MAIL_REFUSED = 'a business email address is required; personal email providers are not accepted'
// frontend/landing/src/components/RegisterModal.tsx KINDS: the radio labels, by tenants.kind value.
const KIND_LABEL: Record<TenantKind, string> = {
  firm: 'For clients — an accounting or tax firm',
  in_house: 'For our own company — in-house',
}
const CREATE = 'Create an account'

// Opens the registration window from the header, fills it, submits, and ends on "Check your email".
// A firstEmail is submitted first: the window must refuse it inline and keep every other field.
async function registerThroughLanding(page: Page, account: RealAccount, kind: TenantKind, firstEmail?: string, marketing = false): Promise<void> {
  await seedConsent(page, false)
  await page.goto(LANDING_URL)
  await page.getByRole('banner').getByRole('button', { name: CREATE }).click()
  const dialog = page.getByRole('dialog', { name: CREATE })
  await expect(dialog).toBeVisible()
  const email = dialog.getByLabel('Work email', { exact: true })
  const submit = dialog.getByRole('button', { name: 'Create account →' })

  await email.fill(firstEmail ?? account.email)
  await dialog.getByLabel('Password', { exact: true }).fill(account.password)
  await dialog.getByLabel('Your name', { exact: true }).fill(account.displayName)
  await dialog.getByLabel('Workspace name', { exact: true }).fill(account.workspaceName)
  await dialog.getByRole('radio', { name: KIND_LABEL[kind] }).check()
  if (marketing) await dialog.getByRole('checkbox').check()

  if (firstEmail !== undefined) {
    await submit.click()
    const refusal = dialog.getByRole('alert')
    await expect(refusal).toHaveCount(1)
    await expect(refusal).toHaveText(FREE_MAIL_REFUSED)
    await expect(email).toHaveAttribute('aria-invalid', 'true')
    await expect(dialog.getByLabel('Password', { exact: true })).toHaveValue(account.password)
    await expect(dialog.getByLabel('Your name', { exact: true })).toHaveValue(account.displayName)
    await expect(dialog.getByLabel('Workspace name', { exact: true })).toHaveValue(account.workspaceName)
    await expect(dialog.getByRole('radio', { name: KIND_LABEL[kind] })).toBeChecked()
    await email.fill(account.email)
  }

  await submit.click()
  await expect(dialog.getByRole('heading', { name: 'Check your email', exact: true })).toBeVisible({ timeout: 30_000 })
  await expect(dialog).toContainText(account.email)
}

async function expectNoDialog(page: Page): Promise<void> {
  await expect(page.getByRole('dialog')).toHaveCount(0)
}

test('deployed journey: a stranger registers through the landing and lands in a workspace of each kind', async ({ page, browser }) => {
  // Two real sign-ins at the 180 s one-sign-in budget of the journeys above, plus three registrations and two 30 s contact polls.
  test.setTimeout(360_000)
  // The free-mail refusal and the first sign-in's /me before provisioning are deliberate 4xx, which Chromium logs as console errors.
  const errors = gatedErrors(page, [
    expectedStatusDropper(page, 400, /\/auth\/register$/),
    expectedStatusDropper(page, 403, /\/api\/tenancy\/v1\/me$/),
  ])
  let meForbidden = 0
  let workspacesCreated = 0
  page.on('response', (res) => {
    const url = res.url().split('?')[0]
    if (res.status() === 403 && url.endsWith('/api/tenancy/v1/me')) meForbidden += 1
    if (res.status() === 201 && res.request().method() === 'POST' && url.endsWith('/api/tenancy/v1/workspaces')) workspacesCreated += 1
  })
  const firm = freshRegistration('firm')
  const inHouse = freshRegistration('in_house')

  for (const { kind, account, path } of [
    { kind: 'firm', account: firm, path: '/' },
    { kind: 'in_house', account: inHouse, path: '/clients' },
  ] as const) {
    await test.step(`${kind}: registers through the landing window`, async () => {
      // The firm pass submits a free-mail address first; the in-house pass registers directly.
      await registerThroughLanding(page, account, kind, kind === 'firm' ? `${crypto.randomUUID()}@gmail.com` : undefined, kind === 'firm')
    })

    await test.step(`${kind}: the emailed link's landing shows the failed and the verified notice`, async () => {
      // Step 2 of the verify half is a stand-in, and the failed-link half is the only real one:
      // a bogus token makes the deployed gateway answer 303 to ?verify=failed (real).
      // `?verified=1` below is COPY-ONLY: the test types the query itself, so it proves the notice
      // text and that no dialog opens, not that the gateway verified anything. The verified redirect
      // is proven in CI by TestIdP_EmailedLinkVerifiesThenSignInSucceeds.
      await page.goto(`${GATEWAY_URL}/auth/verify?token=bogus-${crypto.randomUUID()}&type=signup`)
      await page.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })
      await expect(page.getByRole('status').filter({ hasText: 'That link did not work' })).toBeVisible()
      await expect.poll(() => new URL(page.url()).searchParams.has('verify'), { message: 'the landing strips ?verify' }).toBe(false)
      await expectNoDialog(page)

      await page.goto(`${LANDING_URL}/?verified=1`)
      await expect(page.getByRole('status').filter({ hasText: 'Your email address is verified' })).toBeVisible()
      await expectNoDialog(page)
    })

    await test.step(`${kind}: signs in and lands in the workspace it registered`, async () => {
      const urls = recordUrls(page)
      await signInAtFrontDoor(page, account, path)
      expect(urls.length, 'recorded navigations').toBeGreaterThan(0)
      expect(urls.filter((u) => u.includes('signin=no-workspace')), 'a navigation carried signin=no-workspace').toEqual([])
      await expect(page.getByTestId('persona-name')).toHaveText(account.displayName)

      if (kind === 'firm') {
        await expectAddCompanyTask(page, 'Add your first client')
        await expect.poll(() => sidebarRoster(page), { message: 'firm sidebar roster' }).toContain('Clients')
      } else {
        // The in-house workspace closes /clients: the app settles on the dashboard, with no portfolio.
        // The task comes first: only the dashboard renders it, so the URL below is read after the mount settled.
        await expectAddCompanyTask(page, 'Add your company')
        await expect(page).toHaveURL(new URL('/', APP_URL).href)
        await expect(page.getByRole('heading', { level: 1, name: 'Client portfolio', exact: true })).toHaveCount(0)
        await expect.poll(() => sidebarRoster(page), { message: 'in-house sidebar roster' }).toContain('Settings')
        expect(await sidebarRoster(page), 'the in-house sidebar').not.toContain('Clients')
      }
    })

    await test.step(`${kind}: the gateway hand-off made a registered contact, eligible only if ticked`, async () => {
      const session = JSON.parse((await page.evaluate((key) => localStorage.getItem(key), SESSION_KEY)) ?? 'null') as { token?: string } | null
      expect(session?.token, 'no app session token after landing in the workspace').toBeTruthy()
      // The hand-off retries at 0 s, 5 s and 35 s; 30 s covers the first two.
      await expect
        .poll(() => contactsMe(session!.token!), { timeout: 30_000, message: 'no contact row for the registrant' })
        .not.toBeNull()
      const row = (await contactsMe(session!.token!))!
      expect(row.marketing_eligible, `${kind}: marketing_eligible`).toBe(kind === 'firm')
      expect(row.tags, `${kind}: tags`).toEqual(['registered'])
      expect(row.mode, `${kind}: contact mode`).toBe('fake')
    })

    // The next kind starts signed out: a stored session would skip the front door.
    await page.evaluate(() => localStorage.clear())
  }

  await test.step('a confirmed address registered again answers the same Check your email', async () => {
    const context = await browser.newContext()
    try {
      const repeat = await context.newPage()
      const repeatErrors = collectErrors(repeat)
      await registerThroughLanding(repeat, firm, 'firm')
      expect(repeatErrors, `console errors on the repeat registration:\n${repeatErrors.join('\n')}`).toEqual([])
    } finally {
      await context.close()
    }
  })

  expect(meForbidden, 'one /me 403 per kind before provisioning').toBe(2)
  expect(workspacesCreated, 'one workspace created per kind').toBe(2)
  expect(errors, `console errors on the journey:\n${errors.join('\n')}`).toEqual([])
})

function freshRegistration(kind: TenantKind): RealAccount {
  const id = crypto.randomUUID()
  return {
    email: `reg-${kind}-${id}@example.com`,
    password: id.slice(0, 16),
    displayName: `Reg ${kind === 'firm' ? 'Firm' : 'House'} ${id.slice(0, 6)}`,
    workspaceName: `Reg ${kind} ${id.slice(0, 8)}`,
  }
}
