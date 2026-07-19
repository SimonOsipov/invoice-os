// M3-11-02 (task-86): the Day-30 wedge demo — ONE serial journey that walks the
// accounting-firm story against the DEPLOYED dev fleet exactly as a firm sees it.
// Steps 1–5 drive the app SPA in a real browser (a single continuous `page`); step 6
// runs headless over the reused api seam (Bearer token) — there is no UI kill-switch
// (Decision D2). Step 7 (the audit_log row the toggle writes) is PARKED, not run at
// all: audit is write-only with no read surface (Decision D5) and this suite no
// longer has direct DB access (M4-22 [db-op-resolution]) — see the standalone
// skipped test below (M4-22-06 [park-shape]). The browser persona "Chinedu Okafor"
// and the headless login(PERSONAS.A) both resolve to the same firm tenant 1111…, so
// the onboarded entity and the toggle belong to one tenant (Decision "demo tenant").
//
// Rule-state safety (Decision D6): the journey kill-switches the shared, un-tenanted
// `vat-standard-rate` rule, so it self-heals it to enabled on entry (re-throwing a
// genuinely unexpected failure so a broken precondition aborts loudly) and restores
// it unconditionally on exit (a never-throwing backstop) + in the toggle's own
// finally. This mirrors e2e/api/validation.spec.ts's D3 protocol
// (toggleEnabledResilient/selfHeal/ensureEnabled), which is module-local there and so
// re-derived compactly here.
//
// Oracle: `test:demo` green in the M2-14 dev-env.yml deploy gate (there is no local
// fleet to run it against; AC-7 is unconditionally parked — M4-22-06 [park-shape]).
import { test, expect } from '@playwright/test'
import { login, validate, toggleRule, PERSONAS, ApiError, type ValidateResult } from '../api/client'
import { badInvoice, BAD_INVOICE_KEYS, freshTin } from '../api/fixtures'
import { APP_URL, FIRM_PERSONA } from '../topology/targets'
import { ACTIVE_RULE_SET_VERSION } from '../rule-set'
import {
  ensurePortfolioSeeded,
  DISABLED_RULE_KEY,
  CLIENTS_NAV,
  VALIDATION_NAV,
} from './fixtures'

// keysOf(): the sorted rule_key set of a ValidateResult. Engine.Evaluate already
// sorts its output (Decision N16); we sort again so the AC-6 exact-set assertion
// doesn't silently depend on that ordering guarantee holding.
function keysOf(result: ValidateResult): string[] {
  return result.violations.map((v) => v.rule_key).sort()
}

// ensureRuleEnabled(): force DISABLED_RULE_KEY back to enabled, tolerating 409
// (already enabled) + one network retry. `throwOnUnexpected` = true is the beforeAll
// self-heal (a genuinely unexpected failure aborts the file loudly rather than
// surfacing as a confusing mid-journey assertion); = false is the afterAll / finally
// restore (a throwing cleanup would mask the real failure AND leave the rule off).
// Playwright runs afterAll even when beforeAll throws, so the self-heal throwing can
// never skip the restore. Mirrors validation.spec.ts's toggleEnabledResilient.
async function ensureRuleEnabled(token: string, throwOnUnexpected: boolean): Promise<void> {
  try {
    await toggleRule(token, DISABLED_RULE_KEY, true)
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) return // already enabled — success
    if (err instanceof ApiError && err.kind === 'network') {
      try {
        await toggleRule(token, DISABLED_RULE_KEY, true)
        return
      } catch (retryErr) {
        if (retryErr instanceof ApiError && retryErr.status === 409) return
        if (throwOnUnexpected && !(retryErr instanceof ApiError && retryErr.kind === 'network')) throw retryErr
        console.error(`ensureRuleEnabled(${DISABLED_RULE_KEY}): retry after network error failed; leaving as-is`, retryErr)
        return
      }
    }
    if (throwOnUnexpected) throw err
    console.error(`ensureRuleEnabled(${DISABLED_RULE_KEY}): unexpected failure; leaving as-is`, err)
  }
}

// disableRule(): toggle DISABLED_RULE_KEY off, tolerating 409 (already disabled —
// e.g. a prior crashed run's leak). Any other failure propagates: this runs mid-test,
// not from cleanup, so a real failure here is an assertion-relevant signal. In the
// happy path beforeAll's self-heal has left the rule enabled, so this is a real
// transition that writes the AC-7 audit row.
async function disableRule(token: string): Promise<void> {
  try {
    await toggleRule(token, DISABLED_RULE_KEY, false)
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) return
    throw err
  }
}

// AC-7 (parked until M7): the kill-switch writes a real audit_log row (event
// validation.rule.disabled, payload.key = vat-standard-rate) but audit has no HTTP
// read surface (write-only — internal/audit/audit.go — no gateway route reads it), so
// this can't be asserted without direct DB access, which M4-22 [db-op-resolution]
// removes from this suite. This is its own bare `test(...)` — NOT a describe-scope
// `test.skip(true, ...)`, which would skip the entire journey below instead of just
// this AC (M4-22 [loud-park]) — declared BEFORE the journey test so it always runs to
// completion and reports Playwright's "skipped" bucket, never the declared-after-a-
// failure "did not run" bucket, regardless of whether the journey passes or fails.
test('AC-7 (parked until M7): the kill-switch audit_log row', () => {
  const reason =
    'Parked until M7: requires a queryable audit_log read (auditRowExists). See M4-22 decision [park-shape].'
  // test.skip() throws synchronously, so any console output must precede it
  // (Playwright's list reporter dumps a test's own console output to stdout, unlike
  // the skip() reason string itself, which only the (unused) JUnit reporter reads).
  console.warn(`[day30] ${reason}`)
  test.skip(true, reason)
})

test.describe.configure({ mode: 'serial' })

test.describe('Day-30 wedge demo (browser journey + API kill-switch + DB audit, over the deployed fleet)', () => {
  let tokenA: string

  test.beforeAll(async () => {
    tokenA = await login(PERSONAS.A)
    // Self-heal the kill-switched rule to enabled BEFORE the journey so AC-5's "it
    // fires" is reliable even after a prior crashed run left it disabled (D6).
    await ensureRuleEnabled(tokenA, true)
    // Provision the AC-2 precondition idempotently through the shipped onboard API:
    // total >= 25 entities with >= 2 archived (>= 1 ACTIVE + >= 1 ARCHIVED pill). On a
    // CI reset the demo tenant starts empty, so this issues ~25–27 create/offboard
    // round-trips against a possibly-cold fleet — the 60s hook budget covers it (D1).
    await ensurePortfolioSeeded(tokenA)
  })

  test.afterAll(async () => {
    // Unconditional never-throws restore — the ultimate backstop even if the test's
    // own finally never ran (a hard crash mid-journey rather than a failed assertion).
    await ensureRuleEnabled(tokenA, false)
  })

  test('login → portfolio → onboard → playground → violations → kill-switch → audit', async ({ page }) => {
    // Collect hard console/page errors across the whole browser journey — the same
    // no-error gate the topology suite pins on the (already proven) login + validate
    // flows (topology.spec.ts:9-16,40). A degraded /me round trip is a console.warn,
    // not an error, so it would already have failed the AC-1 marker below.
    const errors: string[] = []
    page.on('console', (msg) => {
      if (msg.type() === 'error') errors.push(msg.text())
    })
    page.on('pageerror', (err) => {
      errors.push(`pageerror: ${err.message}`)
    })

    // ---- AC-1 — sign in as the firm persona; assert the backend-verified identity.
    // Mirrors topology.spec.ts:17-32 exactly: goto APP_URL, click the "Chinedu Okafor"
    // persona card (SignIn), await the sidebar marker that renders ONLY when /me
    // resolved the tenant against the live backend (Sidebar.tsx:169).
    const res = await page.goto(APP_URL)
    expect(res, `no response from ${APP_URL}`).toBeTruthy()
    expect(res!.ok(), `${APP_URL} returned HTTP ${res!.status()}`).toBeTruthy()

    await page.getByRole('button', { name: new RegExp(FIRM_PERSONA.buttonName) }).click()
    await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()

    // ---- AC-2 — the Clients surface lists >= 25 entities with a realistic status mix.
    // Firm persona → mode 'firm' (App.tsx:46, auth.ts:43), so the "Clients" sidebar nav
    // button is present (Sidebar.tsx:35, NAV_CLIENTS label glyphs.tsx:55) → ClientsView.
    await page.getByRole('button', { name: CLIENTS_NAV }).click()

    // Rows render as `.pf-list-row` (ClientsView.tsx:95) once the ?limit=200 fetch
    // resolves; poll so the assertion waits out the async load + render.
    await expect
      .poll(() => page.locator('.pf-list-row').count(), { timeout: 20_000 })
      .toBeGreaterThanOrEqual(25)

    // Status pills render the uppercase label 'ACTIVE' / 'ARCHIVED' (portfolio.ts:95-96,
    // rendered ClientsView.tsx:111). Both must be present (seeded by D1).
    await expect(page.getByText('ACTIVE', { exact: true }).first()).toBeVisible()
    await expect(page.getByText('ARCHIVED', { exact: true }).first()).toBeVisible()

    // ---- AC-3 — onboard a new client via the Add-client modal; assert it appears.
    // "Add client" trigger button (ClientsView.tsx:62-68) → EntityFormModal
    // (role="dialog", EntityFormModal.tsx:127-129).
    const onboardTin = freshTin()
    const onboardName = `Demo Onboarding ${onboardTin}` // unique per process (freshTin counter)

    await page.getByRole('button', { name: 'Add client' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()

    // The Name field has no label association (its "Name" caption is a plain <div>,
    // EntityFormModal.tsx:154-155), so target the first `.pf-input`; the TIN input is
    // the only one carrying the ########-#### placeholder (EntityFormModal.tsx:166).
    await dialog.locator('.pf-input').first().fill(onboardName)
    await dialog.getByPlaceholder('########-####').fill(onboardTin)
    // Submit button reads "Add client" in create mode (EntityFormModal.tsx:192-193);
    // scoped to the dialog so it doesn't collide with the trigger of the same label.
    await dialog.getByRole('button', { name: 'Add client' }).click()

    // onSuccess refetches the list + closes the modal (ClientsView.tsx:127-130). The new
    // row's name span (ClientsView.tsx:103) is display-only text (not the input value),
    // so getByText matches the list row, not the — now closed — form.
    await expect(page.getByText(onboardName, { exact: true })).toBeVisible()

    // ---- AC-4 — open the validation playground. "Validation" sidebar nav button
    // (Sidebar.tsx:34, NAV_VALIDATION label glyphs.tsx:56) → ValidationView.
    await page.getByRole('button', { name: VALIDATION_NAV }).click()
    await expect(page.getByRole('heading', { name: 'Validation playground' })).toBeVisible()

    // ---- AC-5 — the "Has violations" preset validate returns a table CONTAINING a row
    // for BOTH the bad-TIN and bad-VAT rules, each fully stamped. The preset encodes a
    // bad TIN + wrong VAT math (invoicePayload.ts:127-141) AND fires ~4 other keys, so
    // this is a CONTAINS-BOTH superset check — NOT an exact-set toEqual (Decision D4).
    await page.getByRole('button', { name: 'Has violations' }).click()
    await page.getByRole('button', { name: 'Validate' }).click()

    // ViolationsTable renders a semantic <table> once POST /v1/validate resolves
    // (ValidationView.tsx:155, ViolationsTable.tsx:39). Columns: Severity | Message |
    // Rule key | Path | Rule-set version (ViolationsTable.tsx:42-46).
    const table = page.getByRole('table')
    await expect(table).toBeVisible()

    for (const key of BAD_INVOICE_KEYS) {
      const row = table.locator('tbody tr').filter({ hasText: key })
      await expect(row, `violations table should contain exactly one row for ${key}`).toHaveCount(1)
      // Rule key cell (3rd column, .mono span, ViolationsTable.tsx:61-63) is exactly the key.
      await expect(row.locator('td').nth(2)).toHaveText(key)
      // Message cell (2nd column, ViolationsTable.tsx:60) is a non-empty human message.
      await expect(row.locator('td').nth(1)).not.toBeEmpty()
      // Rule-set version cell (last column, ViolationsTable.tsx:67). The cell is
      // LIVE-DRIVEN, not a constant: ValidationView.tsx:159 feeds it
      // validation.data.rule_set_version straight from the POST /v1/validate response,
      // which 04 stamps from the ACTIVE rule-set (handlers.go:284 -> store.go:106's
      // `WHERE is_active`). So this asserts the cell TRACKS the active rule-set; it must
      // never re-state today's number in prose or as a literal. It resolves through the
      // shared ../rule-set module -- the one place the e2e package names the active
      // version ([e2e-active-version]) -- so a publish moves this in lockstep.
      await expect(row.locator('td').last()).toHaveText(String(ACTIVE_RULE_SET_VERSION))
    }

    // The browser journey (AC-1…AC-5) must have run clean — same convention as topology.
    expect(errors, `console errors during the browser journey:\n${errors.join('\n')}`).toEqual([])

    // ---- AC-6 (API): toggle the rule off, then assert the re-run over the API omits
    // ONLY the disabled key. AC-7 (the audit_log row the toggle writes) is parked — see
    // the standalone skipped test above this describe (M4-22-06 [park-shape]) — this
    // suite no longer touches the DB, so there is no DSN/t0 gate here anymore.
    try {
      await disableRule(tokenA)

      // AC-6 — the re-run over the API omits ONLY the disabled key; the control
      // (supplier-tin-format) still fires. Exact-set delta is safe HERE on the 2-key
      // golden badInvoice (unlike AC-5's ~6-key preset) — proves the engine dropped
      // exactly one rule, not that it went dark (D5, validation.spec.ts:169-186).
      const disabled = await validate(tokenA, badInvoice)
      expect(keysOf(disabled)).toEqual(BAD_INVOICE_KEYS.filter((k) => k !== DISABLED_RULE_KEY))
    } finally {
      // Belt-and-braces restore over afterAll — never throws (D6).
      await ensureRuleEnabled(tokenA, false)
    }
  })
})
