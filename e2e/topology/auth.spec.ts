import { test, expect } from '@playwright/test'
import { APP_URL, FIRM_PERSONA } from './targets'
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

  const res = await page.goto(APP_URL)
  expect(res, `no response from ${APP_URL}`).toBeTruthy()
  expect(res!.ok(), `${APP_URL} returned HTTP ${res!.status()}`).toBeTruthy()

  // Pick the firm persona to run the sign-in. With VITE_GATEWAY_URL baked into this build,
  // that triggers the real round trip (mint → /me) rather than the pure client-side mock.
  await page.getByRole('button', { name: new RegExp(FIRM_PERSONA.buttonName) }).click()

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

  const res = await page.goto(APP_URL)
  expect(res, `no response from ${APP_URL}`).toBeTruthy()
  expect(res!.ok(), `${APP_URL} returned HTTP ${res!.status()}`).toBeTruthy()

  // Sign in as the firm persona and wait for the /me round trip (same discriminator as
  // the identity test above) — Sign out needs an authed session to exercise the real
  // App.tsx signOut() path rather than the unauthenticated persona-picker.
  await page.getByRole('button', { name: new RegExp(FIRM_PERSONA.buttonName) }).click()
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()

  await page.getByRole('button', { name: 'Sign out' }).click()
  await page.waitForURL((url) => url.href.startsWith(LANDING_URL))

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
