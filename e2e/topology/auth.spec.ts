import { test, expect } from '@playwright/test'
import { APP_URL, FIRM_PERSONA, INHOUSE_PERSONA } from './targets'
import { resolveTarget } from '../targets'

// The public marketing landing page — sign-out's redirect target. Imported from the
// BASE e2e/targets.ts, not this directory's ./targets: topology/targets.ts re-exports
// only GATEWAY_URL/APP_URL/TENANTS/FIRM_PERSONA/VALIDATION_EXPECTED, so it has no
// LANDING_URL. Pattern mirrors e2e/smoke/apps.ts:21 (Decision [signout-asserts-landing-redirect]).
const LANDING_URL = resolveTarget('LANDING_URL')

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
