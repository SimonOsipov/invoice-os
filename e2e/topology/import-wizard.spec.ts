// M4-08-07 (task-176), Mode A (RALPH Stage 2.5): the deployed e2e specs for the
// import wizard's UI-driven surface -- the FINAL subtask of M4-08 (Core AC7: a
// 500-invoice CSV completes end to end through the wizard on deployed dev; also
// confirms AC1/AC2/AC3/AC4/AC6 on the real build).
//
// There is no way to run these locally: this project's "No Local Server" policy
// forbids a dev server, and the ephemeral per-PR Railway environment these specs
// drive does not exist until this PR is marked ready for review. This subtask's
// local oracle is exactly `pnpm -r typecheck` + `playwright test --list` collection
// under playwright.topology.config.ts -- no config or workflow edit is needed
// (testDir './topology' + testMatch '**/*.spec.ts' picks this file up automatically,
// run by dev-env.yml's `e2e` job, step "Topology (app verified-login + cross-tenant
// isolation)" -> `pnpm --filter @invoice-os/e2e test:topology`). The first REAL run
// is that deploy gate, not this authoring pass.
//
// Drives the UI, not the API -- e2e/api/import.spec.ts and perf.spec.ts already gate
// the same 500-invoice path server-side and are left untouched (and remain the API-
// level oracle for AC1/AC2's literal wall-clock budget). Same firm-persona sign-in
// idiom as topology.spec.ts (same file, same verified-tenant discriminator), extended
// with an entity created via e2e/api/client.ts's createEntity + freshTin() BEFORE
// page.goto -- CreateUpload loads entities on mount, so an entity created after load
// would not appear without a reload. Each test still needs its OWN entity rather than
// reusing a seeded one: no-duplicate-invoice-number is scoped per entity, so a shared
// target would make a retry (or the second test) collide on fixed invoice numbers.
//
// NOTE (merged from main, M4-22-03): db/seed.dev.sql now seeds 27 curated
// business_entities into THIS persona's tenant (1111...), where it previously seeded
// zero. Harmless here and deliberately not compensated for: selectOption matches our
// own uniquely-named entity by label, freshTin()'s pid-seeded range cannot collide
// with the curated 10012345-0001..10278901-0027 literals, and listEntities requests
// ?limit=200 (frontend/app/src/lib/portfolio.ts:73) against 27+1 rows, so our entity
// cannot fall off the picker's page.
//
// URLs are gateway-prefixed: the SPA calls POST {base}/api/invoice/v1/imports and
// .../imports/preview, NOT /v1/imports -- every waitForResponse predicate below
// matches on that prefixed path, never a bare /v1/... one.
//
// fullyParallel:true (playwright.topology.config.ts), retries:1 in CI: each test
// below creates its OWN fresh entity (own freshTin()) and its own page/sign-in, so no
// two tests contend for one entity, and a retry's fixed invoice numbers never collide
// across attempts -- no-duplicate-invoice-number is scoped per entity
// (internal/importer/service.go's msgDuplicateInvoiceNumber). The one exception is
// E2E-05/E2E-09 below, which the Implementation Plan requires share ONE page/session
// (the F6 hijack this guards against is session-scoped) -- both live in a single
// test with two labelled assertion blocks rather than two tests.
//
// No production code changes, no data-testid (grep -rn 'data-testid' frontend/
// packages/ e2e/ -> zero, repo-wide) -- every selector below is role/exact-text,
// matching the convention every existing spec in this package already uses.
import { test, expect, type Page } from '@playwright/test'
import { login, createEntity, PERSONAS } from '../api/client'
import { freshTin } from '../api/fixtures'
import { APP_URL, FIRM_PERSONA } from './targets'
import { buildMixedCsv, buildPerfCsv, PERF_HEADER, statValue } from '../importFixtures'

// MixedImportResponse: the subset of POST /v1/imports's success body (internal/
// importer/handlers.go's importResponse) E2E-05 reads to get a REAL invoice UUID to
// click through with, independent of the DOM. invoice_id is optional on the wire
// (absent on dry-run; this is a real import, so it is always populated here --
// perf.spec.ts's own proven recipe, re-verified against
// internal/importer/service.go:951).
interface MixedImportResponse {
  invoice_violations: {
    invoice_number: string
    invoice_id?: string
    violations: { rule_key: string }[]
  }[]
}

// collectErrors()/signInFirm(): the console/pageerror collection + firm-persona
// sign-in idiom topology.spec.ts uses verbatim (E2E-07's gate), extracted here
// because this file needs it three times. The verified marker
// (`[title="Tenant verified via /v1/me"]`) is the only proof the
// /api/tenancy/v1/me round trip resolved against the live backend -- never proceed
// before it, the classic cold-fleet flake.
function collectErrors(page: Page): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })
  return errors
}

async function signInFirm(page: Page): Promise<void> {
  const res = await page.goto(APP_URL)
  expect(res, `no response from ${APP_URL}`).toBeTruthy()
  expect(res!.ok(), `${APP_URL} returned HTTP ${res!.status()}`).toBeTruthy()
  await page.getByRole('button', { name: new RegExp(FIRM_PERSONA.buttonName) }).click()
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
}

test('E2E-01/02/03/06/07 (Core AC7, FLOW-05): 500-invoice CSV completes through the UI on deployed dev', async ({ page }, testInfo) => {
  // Sign-in, nav, TWO full uploads of the same bytes ([preview-stateless] --
  // preview then import), and render, on a possibly cold 11-service fleet.
  // import.spec.ts already spends 120s on the API-only path with a 60s budget; this
  // UI path needs more headroom.
  test.setTimeout(240_000)

  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M4-08 UI ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  // Guard: a build with no VITE_GATEWAY_URL renders "No gateway configured..." and
  // no <select> at all -- fail with an attributable message, not a bare timeout.
  const select = page.locator('select')
  await expect(select, 'entity picker <select> not found -- check VITE_GATEWAY_URL is configured for this deployed build').toBeVisible({
    timeout: 30_000,
  })

  const readColumnsBtn = page.getByRole('button', { name: 'Read columns' })

  // E2E-03: Read columns stays disabled until BOTH an entity and a file are chosen.
  await expect(readColumnsBtn, 'disabled with neither an entity nor a file selected').toBeDisabled()

  await select.selectOption({ label: entity.name })
  await expect(readColumnsBtn, 'disabled with only an entity selected').toBeDisabled()

  const fileInput = page.locator('input[type="file"][accept=".csv,.xlsx"]')
  await fileInput.setInputFiles({ name: 'ui-perf.csv', mimeType: 'text/csv', buffer: Buffer.from(buildPerfCsv(), 'utf8') })
  await expect(readColumnsBtn, 'enabled only once both an entity and a file are chosen').toBeEnabled()

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  const previewT0 = Date.now()
  await readColumnsBtn.click()
  await previewResp
  const previewMs = Date.now() - previewT0

  // E2E-02: the Map step's rendered headers are exactly the server-echoed columns,
  // in order, including the space in "Invoice No". CreateMapping.tsx's column
  // header cell is the ONLY <div class="mono"> under <main> at this step (letters
  // and sample values are <span class="mono">), so this locator resolves to exactly
  // the header row, proving the columns come from previewColumns (server-fed), not
  // a hardcoded/browser-parsed list.
  const headerCells = page.locator('main div.mono')
  await expect(headerCells).toHaveText(PERF_HEADER.split(','))

  // Map invoice_number by click-to-place (D3 -- no <select>, drag is flaky under
  // Playwright): arm the chip, then click the target column.
  //
  // Do NOT add `exact: true` to the chip locators. CreateMapping's palette button
  // carries textTransform:'uppercase', and Chromium APPLIES CSS text-transform when
  // computing the accessible name -- so the real name is "INVOICE_NUMBER*", not
  // "invoice_number*". Playwright's `name` match is case-insensitive substring by
  // default, which is what makes this work; `exact: true` makes it case-sensitive
  // AND whole-string, and it can then never match (proven on deployed dev: the
  // 240s timeout at this line was exactly this). The trailing `*` comes from a
  // separate <span> for `required`, so a substring match also sidesteps any
  // accessible-name concatenation question.
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()

  const importBtn = page.getByRole('button', { name: /^Import \d+ rows$/ })
  await expect(importBtn).toBeEnabled()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 220_000 },
  )
  const importT0 = Date.now()
  await importBtn.click()

  // E2E-06 (redesigned per D5, FLOW-06's non-bar half): an honest in-flight
  // indicator appears, and no invented PARSE_LABELS stage list renders on the
  // import path. Deliberately does NOT assert the determinate bar -- §7 point 1:
  // App.tsx seeds uploadPhase total:0, which uploadPercent maps to null (the
  // indeterminate spinner), and IMPAPI-08 makes zero progress events legal, so a
  // real N% bar is not deterministically observable on a live run. Hosted on this
  // 500-invoice test specifically because the window between click and response is
  // seconds here (milliseconds on the small mixed fixture below, which is why that
  // test does not also assert this).
  await expect(page.getByText('Working…', { exact: true })).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText('Scanning line rows', { exact: true })).toHaveCount(0)
  await expect(page.getByText('Detecting delimiter & encoding', { exact: true })).toHaveCount(0)

  await importResp
  const wireMs = Date.now() - importT0
  await expect(page.getByText('Ready invoices', { exact: true })).toBeVisible({ timeout: 60_000 })
  const renderedMs = Date.now() - importT0

  // Evidence base for a future xhr.timeout decision (§5) -- assert NOTHING about
  // these numbers, no AC specifies a UI duration and a guessed budget would
  // spuriously fail on a cold fleet.
  console.log(`IMP-UI-PERF (deployed): 500 inv / 1500 rows -- preview ${previewMs}ms, import wire ${wireMs}ms, report rendered ${renderedMs}ms`)
  testInfo.annotations.push({ type: 'imp-ui-perf', description: `preview=${previewMs}ms wire=${wireMs}ms rendered=${renderedMs}ms` })

  // E2E-01 (Core AC7), restated per D2 -- there is no standalone rows_total tile; it
  // appears only inside "Rows valid" as `${rows_valid} / ${rows_total}`.
  await expect(statValue(page, 'Rows valid')).toHaveText('1500 / 1500')
  await expect(statValue(page, 'Ready invoices')).toHaveText('500')
  await expect(statValue(page, 'Quarantined')).toHaveText('0')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('E2E-04/05/09 (RPT-09, [click-through-honest-placeholder], [detail-target-exclusive]/F6): mixed fixture renders two distinct report sections; the violation row opens the real invoice id; a normal invoice afterwards renders the real detail view', async ({
  page,
}) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M4-08 UI Mixed ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  const select = page.locator('select')
  await expect(select, 'entity picker <select> not found -- check VITE_GATEWAY_URL is configured for this deployed build').toBeVisible({
    timeout: 30_000,
  })
  await select.selectOption({ label: entity.name })

  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'ui-mixed.csv', mimeType: 'text/csv', buffer: Buffer.from(buildMixedCsv(), 'utf8') })

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

  // Map invoice_number AND subtotal by click-to-place. subtotal has no entry in
  // lib/mapping.ts's ALIAS table, so auto-recognize never places it -- and it MUST
  // be mapped for this fixture's clean/violating split to be real rather than a
  // uniform tax_math data-fault on every invoice (see importFixtures.ts's
  // buildMixedCsv doc comment for the verified reasoning).
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  const resp = await importResp
  const body = (await resp.json()) as MixedImportResponse

  // E2E-04 + RPT-09: both channels render as two DISTINCT sections, proven by
  // TEXT-OFFSET ORDERING (§4) -- section titles are plain <div>s with no heading
  // role, no testid and no count, so containment cannot be asserted by role. This
  // genuinely falsifies a merged, reordered, or single-section rendering.
  await expect(page.getByText('Rule violations', { exact: true })).toBeVisible()
  const reportText = await page.locator('main').innerText()
  const iStructTitle = reportText.indexOf('Structural row errors')
  const iStructMsg = reportText.indexOf('rows disagree on issue_date')
  const iViolTitle = reportText.indexOf('Rule violations')
  const iViolKey = reportText.indexOf('vat-standard-rate')
  expect(iStructTitle, 'Structural row errors section missing').toBeGreaterThanOrEqual(0)
  expect(iStructMsg, 'structural error message ("rows disagree on issue_date") missing').toBeGreaterThanOrEqual(0)
  expect(iViolTitle, 'Rule violations section missing').toBeGreaterThanOrEqual(0)
  expect(iViolKey, 'vat-standard-rate violation missing').toBeGreaterThanOrEqual(0)
  expect(iStructTitle, 'the structural section must render before its own message').toBeLessThan(iStructMsg)
  expect(iStructMsg, 'the structural section must render entirely before the violations section').toBeLessThan(iViolTitle)
  expect(iViolTitle, 'the violations section must render before its own rule key').toBeLessThan(iViolKey)

  // The clean invoice is a provable SUBSET witness: it must appear in NEITHER
  // section, proving "Rule violations" is not just echoing the whole report.
  await expect(page.getByText('INV-UI-MIX-CLEAN')).toHaveCount(0)

  // E2E-05: click the violation row and land on the placeholder carrying the REAL
  // invoice id -- read from the import response body, independent of the DOM.
  const violateEntry = body.invoice_violations.find((iv) => iv.invoice_number === 'INV-UI-MIX-VIOLATE')
  expect(
    violateEntry,
    'expected an invoice_violations entry for INV-UI-MIX-VIOLATE -- if this fails, check whether vat-standard-rate was disabled by an out-of-order suite run (api -> topology -> demo)',
  ).toBeTruthy()
  expect(violateEntry!.violations.map((v) => v.rule_key)).toEqual(['vat-standard-rate'])
  const violateId = violateEntry!.invoice_id
  expect(violateId, 'invoice_violations[].invoice_id must be populated on a REAL import').toBeTruthy()

  await page.getByText('INV-UI-MIX-VIOLATE', { exact: true }).click()

  await expect(page.getByRole('heading', { name: 'Imported invoice', level: 1 })).toBeVisible()
  // [click-through-honest-placeholder] -- the ONLY guard on InvoiceDetail's
  // early-branch ordering (QA proved by mutation that no node spec observes it: all
  // 263 node specs stayed green when the branch was moved below the invList
  // fallback). main div.mono is the ONLY <div class="mono"> on this placeholder
  // (the sidebar's own div.mono is excluded by the `main` scope).
  await expect(page.locator('main div.mono')).toHaveText(violateId!)

  // E2E-09 (the F6 regression guard, [detail-target-exclusive]): click back through
  // to Invoices and open a NORMAL invoice -- the real detail view must render, not
  // the placeholder. Proves importedInvoiceId is cleared at selectInvoice, not just
  // set once and left to hijack the detail view for the rest of the session.
  await page.getByRole('button', { name: '← All invoices' }).click()
  await page.locator('.pf-list-row').first().click()

  await expect(page.getByRole('heading', { name: 'Imported invoice', level: 1 })).toHaveCount(0)
  await expect(page.getByText('This invoice was created by the import', { exact: false })).toHaveCount(0)
  await expect(page.getByText('Audit trail', { exact: true })).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('E2E-10/E2E-08 (FLOW-07, [scanline-stays-on-doc-path]/F4): the wizard header flips between the 3-step import path and the 5-step document path, and the single-document PDF path still runs unchanged', async ({
  page,
}) => {
  const errors = collectErrors(page)

  await signInFirm(page)

  const newInvoiceBtn = page.locator('header').getByRole('button', { name: 'New invoice' })
  await newInvoiceBtn.click()

  // E2E-10 (discharges J1's deferred layout half; confirming oracle for FLOW-07).
  // Bare 'upload' step with nothing chosen renders the 3-step IMPORT_STEPS strip
  // (Import/Map/Report).
  await expect(page.getByText('Report', { exact: true }), '3-step IMPORT_STEPS strip expected with nothing chosen').toBeVisible()
  await expect(page.getByText('Build', { exact: true })).toHaveCount(0)

  // Selecting the PDF sample flips to the 5-step WIZARD_STEPS strip
  // (Import/Map/Build/Validate/Approve).
  await page.getByText('lagos-freight-INV-0482.pdf', { exact: true }).click()
  await expect(page.getByText('Build', { exact: true }), '5-step WIZARD_STEPS strip expected once a sample file is picked').toBeVisible()
  await expect(page.getByText('Report', { exact: true })).toHaveCount(0)

  // Also choosing a spreadsheet file flips back to the 3-step strip -- a chosen
  // import file always wins over a stale sample selection (wizardHeader's FLOW-13
  // tie-break, lib/importFlow.ts).
  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'e2e10.csv', mimeType: 'text/csv', buffer: Buffer.from('Invoice No,Issue Date\nX,2026-01-01', 'utf8') })
  await expect(page.getByText('Report', { exact: true }), 'a chosen import file must win the strip back to 3 steps').toBeVisible()
  await expect(page.getByText('Build', { exact: true })).toHaveCount(0)

  // Reset to a clean wizard before driving E2E-08 -- the header's "New invoice" CTA
  // (openCreate) unconditionally clears uploadFile/importFile/mapping/etc, so the
  // probe above's stray spreadsheet selection cannot leak into the document path
  // below (D7: no file upload is involved in the document path at all).
  await newInvoiceBtn.click()

  // E2E-08 ([scanline-stays-on-doc-path] / F4 regression guard): the fenced
  // single-document path must still run byte-for-byte the same upload -> parsing ->
  // form -> validate -> results flow M4-08-06 could only prove unchanged by git-diff
  // and code review. This is the only REAL oracle for that claim.
  await page.getByText('lagos-freight-INV-0482.pdf', { exact: true }).click()
  await page.getByRole('button', { name: 'Upload & parse' }).click()

  await expect(page.getByText('Parsing lagos-freight-INV-0482.pdf…', { exact: true })).toBeVisible()
  await expect(page.getByText(/\d+% PARSED/)).toBeVisible()

  // Auto-advances to 'form' (~1.3s) -- wait for the destination state (the
  // pre-fill banner), never an intermediate scanline frame.
  await expect(
    page.getByText('Pre-filled from lagos-freight-INV-0482.pdf — review and edit below.', { exact: true }),
  ).toBeVisible({ timeout: 15_000 })

  await page.getByRole('button', { name: 'Run validation' }).click()
  await expect(page.getByText('Validating against MBS rules…', { exact: true })).toBeVisible()
  await expect(page.getByText(/\d+% COMPLETE/)).toBeVisible()

  // Auto-advances to 'results' (~1.8s) -- one of the three verdict literals plus a
  // /16 score, whichever this deterministic mock draft resolves to (which one is
  // not the point; that a verdict renders at all, unchanged, is).
  await expect(page.getByText(/Not compliant yet|Review warnings|Compliant — ready to approve/)).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText(/\d+\/16/)).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
