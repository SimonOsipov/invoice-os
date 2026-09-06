import { test, expect } from '@playwright/test'
import { APP_URL, FIRM_PERSONA, INHOUSE_PERSONA } from './targets'
import { resolveTarget } from '../targets'
import { collectErrors } from '../personaSession'
import { PERSONAS, PERSONA_IDS, DESTINATION_ENV, type PersonaId } from '../personas'
import { login, createEntity, createInvoice, PERSONAS as API_PERSONAS } from '../api/client'
import { freshTin } from '../api/fixtures'
import { approvalRun404Dropper } from './consoleGate'

// The public marketing landing page — sign-out's redirect target. Imported from the
// BASE e2e/targets.ts, not this directory's ./targets: topology/targets.ts re-exports
// only GATEWAY_URL/APP_URL/TENANTS/FIRM_PERSONA/VALIDATION_EXPECTED, so it has no
// LANDING_URL. Pattern mirrors e2e/smoke/apps.ts:21 (Decision [signout-asserts-landing-redirect]).
const LANDING_URL = resolveTarget('LANDING_URL')

// The expected arrival base per persona, computed once at module scope — same idiom as
// LANDING_URL above. This is the ONLY use of resolveTarget/DESTINATION_ENV in the walk below:
// to compute what the navigation SHOULD land on, never to navigate there directly (AC #2).
const EXPECTED_BASE: Record<PersonaId, string> = Object.fromEntries(
  PERSONA_IDS.map((id) => [id, resolveTarget(DESTINATION_ENV[PERSONAS[id].destination])]),
) as Record<PersonaId, string>

// M2-14 deliverable (1): the live browser round trip. On the gateway-wired dev build,
// picking a persona mints a JWT via the gateway (/auth/login) and reads GET
// /api/tenancy/v1/me before revealing the workspace — the first real authenticated fetch
// resolving a tenant under RLS. This proves that whole path end to end, in a real browser.
test('deployed app: persona mock-login renders the backend-verified tenant identity', async ({ page }) => {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })

  // Arrive the way the landing page hands off: ?persona=<id>. The app no longer offers a
  // picker of its own — landing is the single sign-in front door — so this deep link IS
  // the sign-in. With VITE_GATEWAY_URL baked into this build it triggers the real round
  // trip (mint → /me) rather than the pure client-side mock.
  const url = `${APP_URL}?persona=${FIRM_PERSONA.param}`
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()

  // The VERIFIED marker (a sidebar span titled "Tenant verified via /v1/me") renders ONLY
  // in the verified branch — when /me resolved the tenant against the live backend. It is
  // the discriminator this test hinges on: the static firm fallback shows the SAME
  // "OKAFOR & PARTNERS" label, so the marker — not the text — is what proves the round
  // trip resolved a backend identity and not the org-label fallback. Auto-waits for the
  // async sign-in to complete.
  const verifiedMarker = page.locator('[title="Tenant verified via /v1/me"]')
  await expect(verifiedMarker).toBeAttached()

  // Corroborate: the backend-resolved tenant name renders (uppercased) in the sidebar.
  await expect(page.locator('aside.pf-sidebar')).toContainText(FIRM_PERSONA.tenantName.toUpperCase())

  // The wired round trip must complete cleanly — a failed round trip degrades to an
  // unverified session (a console.warn, not an error), which would already have failed the
  // marker assertion above; this pins that no hard error fired during load.
  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// M4-14-01 Gap 4 (sign-out redirect): Sidebar.tsx's Sign-out control (aria-label "Sign
// out") calls ctx.signOut -> App.tsx's signOut(): clearSession() then
// window.location.href = landingBase(). The deployed build bakes VITE_LANDING_URL
// (scripts/ci/railway-env.sh:1049), so on dev this is a real cross-app navigation, not
// just a state reset — asserted by waiting for the browser to land on LANDING_URL
// (Decision [signout-asserts-landing-redirect]).
test('deployed app: sign-out redirects to the landing page', async ({ page }) => {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })

  // Sign in via the landing hand-off and wait for the /me round trip (same discriminator
  // as the identity test above) — Sign out needs an authed session to exercise the real
  // App.tsx signOut() path rather than an already-unauthenticated redirect.
  const url = `${APP_URL}?persona=${FIRM_PERSONA.param}`
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()

  await page.getByRole('button', { name: 'Sign out' }).click()
  await page.waitForURL((url) => url.href.startsWith(LANDING_URL))

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Regression (persona-switch): the landing page is a DIFFERENT origin from the app, so
// picking a profile there cannot clear this origin's stored session — the ?persona= hand-off
// is the entire signal that identity changed. It used to LOSE to a stored session, so
// reaching landing without the in-app Sign out (Back button, a second tab, a bookmark) and
// choosing the other accountant silently reopened the PREVIOUS one's workspace, tenant label
// and all. Asserted on a deployed build because the swap only shows up across a real page
// load with real localStorage.
test('deployed app: a persona hand-off switches identity over a live stored session', async ({ page }) => {
  await page.goto(`${APP_URL}?persona=${FIRM_PERSONA.param}`)
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
  await expect(page.locator('aside.pf-sidebar')).toContainText(FIRM_PERSONA.tenantName.toUpperCase())

  // Arrive again exactly as landing hands off, WITHOUT signing out — the firm session is
  // still stored on this origin, which is the whole point of the regression.
  await page.goto(`${APP_URL}?persona=${INHOUSE_PERSONA.param}`)
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()

  const sidebar = page.locator('aside.pf-sidebar')
  await expect(sidebar).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())
  // The positive assertion alone would pass while BOTH identities render; the bug's
  // signature was the firm tenant surviving the switch.
  await expect(sidebar).not.toContainText(FIRM_PERSONA.tenantName.toUpperCase())
})

// Regression (one-shot hand-off): ?persona= is a sign-in hand-off, not a standing
// credential. It used to survive in the address bar, so after Sign out the back button
// returned to the `?persona=firm` entry and walked straight back into the workspace with no
// OTP — a logout that did not log out. It is now stripped (replaceState) the moment it is
// consumed, leaving a bare, sessionless app URL behind the sign-out redirect.
//
// This pins the strip itself rather than driving the back button: a bfcache restore would
// make the navigation assertion answer "did Chromium reuse the page?" instead of "is the
// param gone?" — and the param is the actual defect.
test('deployed app: the ?persona= hand-off is consumed and removed from the URL', async ({ page }) => {
  await page.goto(`${APP_URL}?persona=${FIRM_PERSONA.param}`)
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()

  await expect
    .poll(() => new URL(page.url()).searchParams.has('persona'), {
      message: `?persona= survived the hand-off at ${page.url()} — the back button would re-sign-in`,
    })
    .toBe(false)
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
// restored the view -- the same trap the strip test above (:111-113) already records for
// a query param. So every step asserts the URL AND the rendered panel, never one alone.
test("deployed app: Back walks the workspace's own history instead of leaving it", async ({ page }) => {
  const errors = collectErrors(page)

  const url = `${APP_URL}?persona=${FIRM_PERSONA.param}`
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
  await expect(page.getByText('COMPLIANCE OVERVIEW', { exact: true })).toBeVisible()

  const nav = page.locator('aside.pf-sidebar nav.pf-nav-list')
  await nav.getByRole('button', { name: 'Invoices' }).click()
  await expect(page, 'nav to Invoices did not update the URL').toHaveURL(/\/invoices$/)
  await expect(page.getByTestId('invoices-list')).toBeVisible()

  await nav.getByRole('button', { name: 'Audit' }).click()
  await expect(page, 'nav to Audit did not update the URL').toHaveURL(/\/audit$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()

  await nav.getByRole('button', { name: 'Settings' }).click()
  await expect(page, 'nav to Settings did not update the URL').toHaveURL(/\/settings$/)
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

  const url = `${APP_URL}?persona=${FIRM_PERSONA.param}`
  await page.goto(url)
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()

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

  const landingRes = await page.goto(LANDING_URL)
  expect(landingRes, `no response from ${LANDING_URL}`).toBeTruthy()
  expect(landingRes!.ok(), `${LANDING_URL} returned HTTP ${landingRes!.status()}`).toBeTruthy()

  const url = `${APP_URL}?persona=${FIRM_PERSONA.param}`
  await page.goto(url)
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()

  // Landing is the only history entry before the workspace, so a Back landing there
  // proves boot replaced that entry rather than pushing a new one.
  await page.goBack()
  await page.waitForURL((u) => u.href.startsWith(LANDING_URL), { timeout: 20_000 })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ROUTE-02-08: a Back journey through a REAL invoice detail. The journey's first history
// entry must be the landing page itself (Decision [back-lives-in-auth-spec]) -- a walk
// starting at page.goto(APP_URL) would already BE entry one and prove nothing about Back
// leaving the workspace, the exact trap ROUTE-01's own deploy-gate red hit on this AC shape.
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

  const landingRes = await page.goto(LANDING_URL)
  expect(landingRes, `no response from ${LANDING_URL}`).toBeTruthy()
  expect(landingRes!.ok(), `${LANDING_URL} returned HTTP ${landingRes!.status()}`).toBeTruthy()

  const url = `${APP_URL}?persona=${FIRM_PERSONA.param}`
  await page.goto(url)
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()

  // Own entity + invoice per test, over the API, before touching the UI -- same discipline
  // invoice-surfaces.spec.ts and ROUTE-02-07's cold-boot specs already use.
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

// The Caddyfile:20-24 try_files fallback has served this since M1-06; nothing in the
// suite has ever cold-booted a top-level path until now.
test('deployed app: a top-level path is a working deep link', async ({ page }) => {
  const errors = collectErrors(page)

  const url = `${APP_URL}/audit?persona=${FIRM_PERSONA.param}`
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()

  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()
  await expect(page, 'the deep link did not settle on /audit').toHaveURL(/\/audit$/)

  await expect
    .poll(() => new URL(page.url()).searchParams.has('persona'), {
      message: `?persona= survived the deep link at ${page.url()}`,
    })
    .toBe(false)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// The ONLY oracle for this story's load-bearing premise: sessionStorage written on the APP
// origin survives a same-tab hard navigation to the LANDING origin and back. sessionStorage is
// keyed by (top-level browsing context, origin), so no unit test can observe it — jsdom never
// leaves the origin. Decision [sessionstorage-survives-the-round-trip].
//
// Drives the REAL SignInModal rather than a constructed ?persona= URL: a constructed URL skips
// the landing round trip, which IS the premise under test.
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

  // No response assertion: the front door navigates away during load. 'a visit with no session
  // redirects to the landing page' above establishes that `goto` onto a bouncing app URL
  // settles cleanly, and 'a top-level path is a working deep link' proves Caddy serves /audit.
  await page.goto(`${APP_URL}/audit`)
  await page.waitForURL((url) => url.href.startsWith(LANDING_URL), { timeout: 20_000 })

  // Same modal drive as the parametrised walk below. 'Explore the platform' renders TWICE on
  // the landing page (header + hero), so the banner scope is required, not stylistic.
  await page.getByRole('banner').getByRole('button', { name: 'Explore the platform' }).click()
  await expect(page.getByRole('dialog', { name: 'Sign in' })).toBeVisible()
  await page.locator(`[data-persona="${FIRM_PERSONA.param}"]`).click()

  // Six separate fill() calls, for the same reason as the walk below: setDigit() moves DOM
  // focus itself on every keystroke, which would race a keyboard-driven approach.
  const digits = '481920'.split('')
  for (let i = 0; i < digits.length; i++) {
    await page.locator(`#si-otp-${i}`).fill(digits[i])
  }
  // verify() delays the navigation by 1100ms (SignInModal.tsx's redirectTimer). Absorbed by the
  // auto-waiting assertions below — never add a fixed wait here.
  await page.getByRole('button', { name: 'Verify & continue' }).click()

  // The hand-off lands on the app ROOT (destUrl carries no path), so arriving on /audit can
  // only have come from the restored destination.
  // 30s, not the file's 15s default: this assertion alone absorbs SignInModal's 1100ms
  // redirectTimer, a cross-origin hard navigation, a cold SPA boot and the /v1/me round trip
  // that mints the marker.
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached({ timeout: 30_000 })
  // The two below keep the default budget on purpose: the marker already gated on a mounted
  // workspace, and AuditView is a static import (App.tsx), so no further fetch precedes the h1.
  await expect(page.getByRole('heading', { level: 1, name: 'Audit log', exact: true })).toBeVisible()
  await expect(page, 'the restored destination did not settle on /audit').toHaveURL(/\/audit$/)

  // The dashboard's eyebrow div, not a heading — the dashboard's h1 is the dynamic client name.
  // Non-vacuous: the two assertions above already gated on a MOUNTED workspace. Its positive
  // control ships in this same file and run — "Back walks the workspace's own history" asserts
  // this exact locator IS visible on the deployed dashboard.
  await expect(page.getByText('COMPLIANCE OVERVIEW', { exact: true })).not.toBeVisible()

  await expect
    .poll(() => new URL(page.url()).searchParams.has('persona'), {
      message: `?persona= survived the restored deep link at ${page.url()}`,
    })
    .toBe(false)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
// This walk drives the REAL SignInModal (open -> pick a persona -> type the OTP -> Verify),
// never e2e/personas.ts#signInUrl's constructed URL. signInUrl cannot catch three ways the
// two sides can silently diverge:
//  (a) the persona->destination table is duplicated (frontend/landing/src/auth.ts's
//      LandingPersona.target vs e2e/personas.ts#PERSONAS[id].destination) and nothing
//      compares the two mappings;
//  (b) the bases resolve from different variables at different times — landing bakes
//      import.meta.env.VITE_*_URL into its build image, this suite reads process.env.*_URL
//      at CI run time;
//  (c) unset behaviour is opposite — destUrl() returns null and verify() silently no-ops,
//      while signInUrl() throws.
for (const id of PERSONA_IDS) {
  const persona = PERSONAS[id]
  test(`deployed ${persona.destination}: the ${id} persona reaches its destination through the sign-in modal`, async ({ page }) => {
    const errors: string[] = []
    page.on('console', (msg) => {
      if (msg.type() === 'error') errors.push(msg.text())
    })
    page.on('pageerror', (err) => {
      errors.push(`pageerror: ${err.message}`)
    })

    await page.goto(LANDING_URL)
    await page.getByRole('banner').getByRole('button', { name: 'Explore the platform' }).click()
    await expect(page.getByRole('dialog', { name: 'Sign in' })).toBeVisible()

    await page.locator(`[data-persona="${id}"]`).click()

    // Six separate fill() calls, not pressSequentially/keyboard.type: setDigit() moves DOM
    // focus to the next box itself on every keystroke, which would race a keyboard-driven
    // approach. fill() re-resolves each box by id regardless of where focus currently sits.
    const digits = '481920'.split('')
    for (let i = 0; i < digits.length; i++) {
      await page.locator(`#si-otp-${i}`).fill(digits[i])
    }

    // Every destination strips ?persona= on arrival (each app's effect commented "Drop the
    // consumed ?persona="), so the wire value is only observable on the outbound navigation
    // request — armed BEFORE the click.
    const base = EXPECTED_BASE[id]
    const [navRequest] = await Promise.all([
      page.waitForRequest((r) => r.isNavigationRequest() && r.url().startsWith(base)),
      page.getByRole('button', { name: 'Verify & continue' }).click(),
    ])
    expect(new URL(navRequest.url()).searchParams.get('persona'), `navigation request did not carry ?persona=${id}`).toBe(id)

    if (persona.destination === 'app') {
      await expect(page.locator('aside.pf-sidebar')).toContainText(persona.tenantName!.toUpperCase())
    } else if (persona.destination === 'ops') {
      await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
    } else {
      await expect(page.getByRole('heading', { name: 'Submissions ops' })).toBeVisible()
    }

    expect(errors, `console errors on the ${persona.destination} arrival:\n${errors.join('\n')}`).toEqual([])
  })
}
