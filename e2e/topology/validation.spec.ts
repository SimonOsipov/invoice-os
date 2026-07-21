import { test, expect } from '@playwright/test'
import { APP_URL, FIRM_PERSONA, VALIDATION_EXPECTED } from './targets'

// Folded from e2e/demo/day30.spec.ts (M4-14-01 demo retirement) — AC-7 (parked until
// M7): the kill-switch writes a real audit_log row (event validation.rule.disabled,
// payload.key = vat-standard-rate) but audit has no HTTP read surface (write-only —
// internal/audit/audit.go — no gateway route reads it), so this can't be asserted
// without direct DB access (M4-22 [db-op-resolution] removed DB access from the E2E
// suites entirely). This is its own bare `test(...)` — NOT a describe-scope
// `test.skip(true, ...)`, which would skip the whole file instead of just this AC
// (M4-22 [loud-park]) — declared first so it always runs to completion and reports
// Playwright's "skipped" bucket, never the declared-after-a-failure "did not run"
// bucket, regardless of whether the round-trip test below passes or fails.
//
// The API rule kill-switch itself (disabling vat-standard-rate -> validate omits only
// it -> reversible) is NOT recreated here — it is already covered live in
// e2e/api/validation.spec.ts ("kill-switch: disabling vat-standard-rate drops only
// it ..." ~line 170), Decision [kill-switch-already-at-api]. Duplicating it in the
// browser layer would violate the thin-browser-layer convention
// (docs/e2e-convention.md).
test('AC-7 (parked until M7): the kill-switch audit_log row', () => {
  const reason =
    'Parked until M7: requires a queryable audit_log read (auditRowExists). See M4-22 decision [park-shape].'
  // test.skip() throws synchronously, so any console output must precede it.
  console.warn(`[validation] ${reason}`)
  test.skip(true, reason)
})

// M3-09-05 deliverable: the live browser round trip for the validation playground
// (M3-09-04). Loading the "Has violations" preset and clicking Validate drives
// buildInvoicePayload -> validateInvoice -> the live gateway -> the seeded MBS v1
// rule-set (M3-05), rendered through ViolationsTable (M3-09-03). Mirrors the identity
// test in auth.spec.ts (same error-collection setup, same firm-persona sign-in) but
// proves the validate path end to end instead of /me.
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
  // discriminator as auth.spec.ts) before touching the nav — Validate needs an authed
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
