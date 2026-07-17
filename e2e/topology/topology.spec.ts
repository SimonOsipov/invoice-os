import { test, expect } from '@playwright/test'
import { APP_URL, FIRM_PERSONA, VALIDATION_EXPECTED } from './targets'

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

// M3-09-05 deliverable: the live browser round trip for the validation playground
// (M3-09-04). Loading the "Has violations" preset and clicking Validate drives
// buildInvoicePayload -> validateInvoice -> the live gateway -> the seeded MBS v1
// rule-set (M3-05), rendered through ViolationsTable (M3-09-03). Mirrors the identity
// test above (same error-collection setup, same firm-persona sign-in) but proves the
// validate path end to end instead of /me.
test('deployed app: validation playground round-trips the live engine', async ({ page }) => {
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

  // Sign in as the firm persona and wait for the /me round trip to finish (same
  // discriminator as the test above) before touching the nav — Validate needs an authed
  // gateway session.
  await page.getByRole('button', { name: new RegExp(FIRM_PERSONA.buttonName) }).click()
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()

  await page.getByRole('button', { name: /Validation/ }).click()
  await page.getByRole('button', { name: /Has violations/ }).click()
  await page.getByRole('button', { name: 'Validate' }).click()

  // ViolationsTable renders only once the live POST /v1/validate resolves; Playwright
  // auto-waits for it rather than an arbitrary sleep.
  const table = page.getByRole('table')
  await expect(table).toBeVisible()

  for (const key of VALIDATION_EXPECTED.sampleRuleKeys) {
    await expect(table).toContainText(key)
  }

  // Rule-set version column, scoped to the first violation row rather than a loose
  // "contains '1'" check (which would also match row indices, TINs, etc. elsewhere in
  // the table).
  const firstRow = table.locator('tbody tr').first()
  await expect(firstRow.locator('td').last()).toHaveText(String(VALIDATION_EXPECTED.ruleSetVersion))

  // Every rule in the ACTIVE seeded MBS set is error severity -- v1's 17 base rules
  // (migrations/20260711121327_seed_mbs_v1.sql) and the two line-item rules v2 re-issues
  // (20260716185106_rule_set_v2.sql) alike -- so this claim needs no version label to stay
  // true. severityStyle maps error -> the "Error" pill label (validationApi.ts). Only ONE
  // such pill is required, so this holds even if a later set ever mixes severities.
  await expect(table.getByText('Error', { exact: false }).first()).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
