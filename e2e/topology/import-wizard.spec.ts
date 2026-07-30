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
// zero. Harmless here and deliberately not compensated for: selectEntity() (the
// workspace-switcher helper, [import-upload-unify] -- CreateUpload's own in-page
// entity <select> is gone) matches our own uniquely-named entity by label,
// freshTin()'s pid-seeded range cannot collide with the curated
// 10012345-0001..10278901-0027 literals, and listEntities requests ?limit=200
// (frontend/app/src/lib/portfolio.ts:73) against 27+1 rows, so our entity cannot
// fall off the switcher's page.
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
// No production code changes at authoring time, and no data-testid -- every selector
// below was role/exact-text, matching the convention every existing spec in this
// package already used. STALE as of the persona-handoff-fix regression fix
// ([entity-id-restored]): that story's own steps 1-2 added data-testid="company-switcher"
// / "company-switcher-option" / "invoices-list" to the app (Sidebar.tsx/
// InvoicesList.tsx), and this file's own selectEntity() helper (a copy of
// invoice-surfaces.spec.ts's, see its doc comment) now uses them -- role/exact-text is
// no longer the exclusive idiom in this file, just the original one.
import { test, expect, type Page } from '@playwright/test'
import { login, createEntity, PERSONAS } from '../api/client'
import { freshTin } from '../api/fixtures'
import { APP_URL, FIRM_PERSONA, INHOUSE_PERSONA } from './targets'
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

async function signInPersona(page: Page, param: string): Promise<void> {
  // The landing page is the single sign-in front door, so the app has no picker to click
  // on a deployed build; ?persona= IS the sign-in, exactly as landing destUrl() hands off.
  const url = `${APP_URL}?persona=${param}`
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
}

async function signInFirm(page: Page): Promise<void> {
  await signInPersona(page, FIRM_PERSONA.param)
}

// The in-house counterpart, added by [inhouse-can-start]. Every functional spec in this
// package drove the FIRM persona only -- in-house appeared in exactly one assertion in
// the whole browser suite (auth.spec.ts's identity-switch regression, which reads the
// sidebar tenant label and never navigates further). That is precisely why an in-house
// accountant could reach the New-invoice wizard and find its first step replaced by a
// dead end without a single test going red.
async function signInInhouse(page: Page): Promise<void> {
  await signInPersona(page, INHOUSE_PERSONA.param)
}

// selectEntity(): a second copy of invoice-surfaces.spec.ts's own helper of the same
// name (this package's established convention for small Page-driving helpers -- see
// this file's own collectErrors/signInFirm doc comment above: "no spec file in this
// package exports its own helpers today, so this is a third copy, not a new seam").
// Needed here by the persona-handoff-fix regression fix ([entity-id-restored]):
// Invoices is a CLIENT-scoped surface now (listInvoices' own `entity_id` param,
// server-side), so a test that drives it must make ITS OWN fixture entity the active
// workspace switcher selection first -- signInFirm() alone leaves the switcher on
// whatever `clients[0]` resolves to (portfolio's List `ORDER BY name ASC, id ASC`),
// never this test's own Date.now()-suffixed entity. Sidebar.tsx:
// data-testid="company-switcher" (the toggle button) / "company-switcher-option"
// (each row in the open dropdown).
async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
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
  // [import-upload-unify] CreateUpload no longer renders its own entity <select> --
  // entityId now mirrors the active workspace switcher selection, so the fresh
  // entity must be made active there BEFORE "New invoice" opens (this is also now
  // the fail-fast point: selectEntity times out attributably if the entity never
  // reached the switcher, e.g. no gateway configured).
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  const readColumnsBtn = page.getByRole('button', { name: 'Read columns' })

  // E2E-03: Read columns stays disabled until a file is chosen -- the entity is
  // already fixed to the active workspace selection on mount, so file choice is now
  // the only remaining gate (canReadColumns still checks both; there is just no
  // longer a UI path to a file-only, entity-empty state).
  await expect(readColumnsBtn, 'disabled with no file selected').toBeDisabled()

  const fileInput = page.locator('input[type="file"][accept=".csv,.xlsx"]')
  await fileInput.setInputFiles({ name: 'ui-perf.csv', mimeType: 'text/csv', buffer: Buffer.from(buildPerfCsv(), 'utf8') })
  await expect(readColumnsBtn, 'enabled once a file is chosen').toBeEnabled()

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
  // indicator appears. Deliberately does NOT assert the determinate bar -- §7 point 1:
  // App.tsx seeds uploadPhase total:0, which uploadPercent maps to null (the
  // indeterminate spinner), and IMPAPI-08 makes zero progress events legal, so a
  // real N% bar is not deterministically observable on a live run. Hosted on this
  // 500-invoice test specifically because the window between click and response is
  // seconds here (milliseconds on the small mixed fixture below, which is why that
  // test does not also assert this).
  //
  // The companion "no invented PARSE_LABELS stage list renders here" absence
  // assertions are GONE, not repointed (INVCR-01-01, plan D-01b): PARSE_LABELS was
  // deleted with the document mock, so those two strings now exist in no code path at
  // all and the assertions could never fail again. INVCR-01-05 introduces the honest
  // server-stage list and owns asserting it positively.
  await expect(page.getByText('Working…', { exact: true })).toBeVisible({ timeout: 60_000 })

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
  // Invoices is CLIENT-scoped now ([entity-id-restored]) -- make `entity` the active
  // workspace switcher selection BEFORE anything else, so E2E-09's "← All invoices"
  // step below actually shows ITS two imported rows, not whichever entity the
  // switcher defaults to (see selectEntity's own doc comment).
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  // [import-upload-unify] no more in-page entity <select> -- selectEntity() above
  // (workspace switcher) already made `entity` the active selection CreateUpload's
  // entityId mirrors, so there is nothing left to pick here.
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

  // E2E-05: click the violation row and land on the LIVE detail (M4-09-05) for the
  // REAL invoice id -- read from the import response body, independent of the DOM.
  // [click-through-honest-placeholder]'s inert M4-08 placeholder is GONE
  // (InvoiceDetail.tsx's `target.kind === 'imported'` branch now mounts
  // LiveInvoiceDetail instead); the early-branch ordering that decision guarded
  // still applies to whichever component sits behind it.
  const violateEntry = body.invoice_violations.find((iv) => iv.invoice_number === 'INV-UI-MIX-VIOLATE')
  expect(
    violateEntry,
    'expected an invoice_violations entry for INV-UI-MIX-VIOLATE -- if this fails, check whether vat-standard-rate was disabled by an out-of-order suite run (api -> topology -> demo)',
  ).toBeTruthy()
  expect(violateEntry!.violations.map((v) => v.rule_key)).toEqual(['vat-standard-rate'])
  const violateId = violateEntry!.invoice_id
  expect(violateId, 'invoice_violations[].invoice_id must be populated on a REAL import').toBeTruthy()

  // Proves the click targets the REAL invoice id, not a client-side guess -- a
  // network-level replacement for the retired placeholder's raw-UUID text render:
  // the live detail's own GET request carries violateId in its path.
  const detailResp = page.waitForResponse(
    (r) => r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith(`/api/invoice/v1/invoices/${violateId}`),
  )
  await page.getByText('INV-UI-MIX-VIOLATE', { exact: true }).click()
  await detailResp

  await expect(page.getByTestId('invoice-detail')).toBeVisible()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('INV-UI-MIX-VIOLATE')
  await expect(page.getByTestId('violations-table')).toContainText('vat-standard-rate')

  // E2E-09 (the F6 regression guard, [detail-target-exclusive]): click back through
  // to Invoices and open a DIFFERENT invoice -- the live detail must refresh to
  // THAT invoice's own content, not keep rendering INV-UI-MIX-VIOLATE's. Proves
  // importedInvoiceId tracks the LATEST clicked row rather than hijacking the
  // detail view for the rest of the session. "Audit trail" was the retired mock
  // detail's panel title; M4-09-05's live detail names the equivalent panel
  // "Status history" instead, so its absence here is also proof this is the live
  // surface, never the old mock fallback.
  //
  // [entity-id-restored] regression fix: Invoices is entity-scoped now (server-side,
  // via listInvoices' own `entity_id` param), and `selectEntity` above made `entity`
  // the active workspace selection -- so its scoped list contains EXACTLY the two
  // invoices this run just imported (INV-UI-MIX-STRUCT never became an invoice at all,
  // quarantined structurally per buildMixedCsv's own doc comment). The "different
  // invoice" this test wants is therefore unambiguously INV-UI-MIX-CLEAN, clicked by
  // name -- not a positional `.pf-list-row.first()`, which (before this fix) depended
  // on whichever entity happened to be active by DEFAULT and how the shared,
  // never-reset dev DB's ever-growing invoice set paginated: exactly the CI-caught
  // filter-after-paginate regression this fix closes (the scoped list would render
  // EMPTY whenever the default entity's own invoices fell outside the newest-50
  // tenant-wide window, timing out this very click).
  await page.getByRole('button', { name: '← All invoices' }).click()
  await page.getByTestId('invoices-list').getByText('INV-UI-MIX-CLEAN', { exact: true }).click()

  await expect(page.getByTestId('invoice-detail')).toBeVisible()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('INV-UI-MIX-CLEAN')
  await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('INV-UI-MIX-VIOLATE')
  await expect(page.getByTestId('status-history')).toBeVisible()
  await expect(page.getByText('Audit trail', { exact: true })).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// E2E-10 (FLOW-07, [wizard-steps-split], INVCR-01-04/task-280): the header path
// resolver, re-anchored again. `Build`/`Validate`/`Approve`/`Report` are retired by
// this subtask -- the typed path is now the 2-item `Enter · Review` strip and the
// import path is `Import · Map · Review`, sharing the `Review` label between them.
// The sample-PDF click that used to flip the strip is deleted with the mock, so
// manual entry ('Skip — enter manually' -> skipUpload -> createStep 'form') is still
// the only way to reach a DOCUMENT_ONLY_STEP. No SANDBOX toggle: with the document
// card gone, LIVE and SANDBOX render an identical first step.
//
// A label-presence check alone cannot prove STAGE_OF.form === 0: with a 2-item
// strip, stageIndex 0 and stageIndex 2 (the retired 5-item index) render the SAME
// two labels -- only the highlight differs. So this also asserts computed `color`,
// token-agnostic (`--fg-1` vs `--fg-3`, verified genuinely distinct:
// oklch(16% .03 210) vs oklch(45% .02 210)) rather than a class or token name.
// Verified no exact-text collision for Import/Map/Review/Enter anywhere else on this
// screen: CreateUpload's card title is 'Import invoices · X', CreateMapping's are
// 'Map fields to columns · X' / 'Import N rows' / 'Map invoice number to continue' --
// none is an exact match for the bare word -- and ConnectorDetail.tsx's own 'Review'
// is a different, unmounted view.
//
// E2E-08 is deliberately NOT replaced -- its subject (the sample-PDF parse -> form ->
// validate -> results run) is deleted by an earlier subtask, so nothing is left to guard.
test('E2E-10 (FLOW-07, [wizard-steps-split]): the wizard header resolves the 3-step import path on entry and the 2-step typed path once manual entry is chosen', async ({
  page,
}) => {
  const errors = collectErrors(page)

  await signInFirm(page)
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  // Leg 1 -- createStep 'upload' is NOT in DOCUMENT_ONLY_STEPS, so IMPORT_STEPS
  // (Import/Map/Review) renders; the typed-path-only 'Enter' label is absent.
  await expect(page.getByText('Import', { exact: true }), '3-step IMPORT_STEPS strip expected on entry').toBeVisible({ timeout: 30_000 })
  await expect(page.getByText('Map', { exact: true })).toBeVisible()
  await expect(page.getByText('Review', { exact: true })).toBeVisible()
  await expect(page.getByText('Enter', { exact: true })).toHaveCount(0)

  const colorOf = (t: string) => page.getByText(t, { exact: true }).evaluate((el) => getComputedStyle(el).color)

  // Positive companion: exactly one of the three is lit, at index 0 -- proves the
  // color-comparison technique itself discriminates before leg 2's assertion relies
  // on it. Map (idx 1) and Review (idx 2) are both un-lit, so their colors match;
  // Import (idx 0, current) differs from both.
  expect(await colorOf('Import')).not.toBe(await colorOf('Map'))
  expect(await colorOf('Map')).toBe(await colorOf('Review'))

  // Leg 2 -- 'form' IS in DOCUMENT_ONLY_STEPS, so wizardHeader must return
  // WIZARD_STEPS (Enter/Review) at STAGE_OF.form === 0, and the import-only
  // 'Import'/'Map' labels must disappear.
  await page.getByRole('button', { name: 'Skip — enter manually' }).click()
  await expect(page.getByText('Enter', { exact: true }), '2-step WIZARD_STEPS strip expected on manual entry').toBeVisible()
  await expect(page.getByText('Review', { exact: true })).toBeVisible()
  await expect(page.getByText('Import', { exact: true })).toHaveCount(0)
  await expect(page.getByText('Map', { exact: true })).toHaveCount(0)

  // The label pair alone is indistinguishable from a regressed stageIndex 2: with
  // only two entries (indices 0/1), index 2 matches neither, so BOTH labels would
  // fall to the same muted --fg-3 and this comparison would be equal -- only the
  // correct stageIndex 0 lights Enter and leaves Review muted, producing a genuine
  // color inequality.
  expect(await colorOf('Enter'), 'STAGE_OF.form must be 0').not.toBe(await colorOf('Review'))

  // Not just the strip: the Enter step's BODY rendered underneath it. A resolver
  // that returned the right labels over a blank step router would otherwise pass.
  // Same smallest-match text idiom as :465/:529's 'Import invoices ·'; the header
  // CTA's name is exactly 'New invoice' with no '·', so there is no collision.
  await expect(page.getByText('New invoice ·', { exact: false }), 'the Enter step body rendered').toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// [import-upload-unify] LIVE-mode guards for the unified upload surface. Two things
// this change made true that nothing else asserts: manual entry survives OUTSIDE the
// import card (it used to live in the mock document card's sibling rail -- gating it
// along with that card would have deleted the only from-scratch creation path in
// production), and the dropzone accepts a real drop (setInputFiles everywhere else
// bypasses onDrop entirely).
//
// This test used to carry a third claim -- that the mock document card was sandbox-ONLY
// rather than unconditional. INVCR-01-01 deleted that card outright, so its three
// absence assertions became permanently vacuous (the strings exist in no code path) and
// were removed rather than repointed, per plan D-01b. What remains is the positive fact
// that survives: there is exactly ONE import surface here, and it is real.
test('[import-upload-unify] LIVE: one real import surface, manual entry survives, dropzone accepts a drop', async ({ page }) => {
  const errors = collectErrors(page)

  await signInFirm(page)
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  // The unified card is the only import surface in LIVE. Entity is now static header
  // text, not a <select> -- this doubles as the assertion that it still renders.
  await expect(page.getByText('Import invoices ·', { exact: false })).toBeVisible({ timeout: 30_000 })

  // Wait for the zone itself, not just the card. The dropzone no longer depends on the
  // entities fetch at all -- reading columns needs only the file ([inhouse-can-start]) --
  // but the card still mounts a tick before the surrounding data settles, and dropping
  // before the label exists would fail inside page.evaluate.
  await expect(page.locator('label[for="pf-import-file"]'), 'dropzone renders').toBeVisible({
    timeout: 30_000,
  })

  const readColumnsBtn = page.getByRole('button', { name: 'Read columns' })
  await expect(readColumnsBtn, 'disabled before any file is chosen').toBeDisabled()

  // Synthetic DataTransfer rather than a real mouse drag: this file already records
  // (mapping step, ~L171) that real Playwright drag is flaky here. A dispatched event
  // is deterministic and still exercises the onDrop handler no other test reaches.
  await page.evaluate(() => {
    const label = document.querySelector('label[for="pf-import-file"]')
    if (!label) throw new Error('dropzone label[for="pf-import-file"] not found')
    const dt = new DataTransfer()
    dt.items.add(new File(['invoice_number,subtotal\nDROP-1,100\n'], 'dropped.csv', { type: 'text/csv' }))
    label.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt }))
  })

  await expect(page.getByText('dropped.csv', { exact: true }), 'dropped filename renders inside the zone').toBeVisible()
  await expect(readColumnsBtn, 'enabled once a dropped file lands').toBeEnabled()

  // Manual entry must be reachable in LIVE. skipUpload -> createStep 'form'; the build
  // step's own primary is 'File invoice' (CreateForm), which no earlier step renders. That
  // label replaced 'Run validation' in INVCR-01-03, when the mock 16-check scanline and its
  // approve screen were deleted in favour of one real POST /v1/invoices — the firm persona
  // has a resolved entity, so the gate passes and the primary reads its armed label.
  await page.getByRole('button', { name: 'Skip — enter manually' }).click()
  await expect(page.getByRole('button', { name: 'File invoice' }), 'manual build step reachable in LIVE').toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// [inhouse-can-start] — the regression this whole persona gap produced, asserted on the
// deployed build as the IN-HOUSE accountant.
//
// Both personas must be able to START creating an invoice. The upload surface used to be
// gated on `active.entityId !== null`, and inhouseClient() hardcodes that to null (an
// in-house tenant has no business_entities row and no Clients screen to add one from), so
// step 1 rendered "No linked entity -- import is unavailable here" and the firm alone got
// a dropzone. Reading columns never needed an entity: POST /imports/preview takes the file
// and nothing else, and its handler persists nothing. So the gate moved to the COMMIT,
// which is the step that really does write import_batches.entity_id / invoices.entity_id
// (both NOT NULL).
//
// Deliberately NOT asserted here: that an in-house import can be filed. Persistence for
// entity-less workspaces is a separate story -- this spec pins that the wizard OPENS and
// that the point where it stops is honest and legible, not a dead button.
test('[inhouse-can-start] LIVE: the in-house persona reaches the import dropzone and reads columns; filing is refused in words, not silence', async ({
  page,
}) => {
  const errors = collectErrors(page)

  await signInInhouse(page)
  await expect(page.locator('aside.pf-sidebar')).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await expect(page.getByText('Import invoices ·', { exact: false })).toBeVisible({ timeout: 30_000 })

  // The heart of it: the surface the blocked state used to replace. Paired with an explicit
  // absence check on the old copy, so re-introducing the gate under a different condition
  // still fails here rather than passing on a visible-but-wrong screen.
  await expect(page.locator('label[for="pf-import-file"]'), 'in-house gets the dropzone too').toBeVisible({
    timeout: 30_000,
  })
  await expect(page.getByText('No linked entity', { exact: false })).toHaveCount(0)

  const readColumnsBtn = page.getByRole('button', { name: 'Read columns' })
  await expect(readColumnsBtn, 'disabled before any file is chosen').toBeDisabled()

  // Same synthetic-DataTransfer approach as the firm test above (real drag is flaky here).
  await page.evaluate(() => {
    const label = document.querySelector('label[for="pf-import-file"]')
    if (!label) throw new Error('dropzone label[for="pf-import-file"] not found')
    const dt = new DataTransfer()
    dt.items.add(new File(['invoice_number,subtotal\nINH-1,100\n'], 'inhouse.csv', { type: 'text/csv' }))
    label.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt }))
  })
  await expect(page.getByText('inhouse.csv', { exact: true })).toBeVisible()

  // The gate is gone from the front door: with no entity anywhere in this tenant, Read
  // columns still arms purely on the file. This is the assertion the old contract made
  // impossible -- canReadColumns required an entity, so it could never enable here.
  await expect(readColumnsBtn, 'Read columns arms on the file alone, with no entity').toBeEnabled()

  // And it genuinely works server-side: the preview round-trips without an entity_id.
  await readColumnsBtn.click()
  await expect(page.getByText('Map fields to columns ·', { exact: false }), 'preview succeeded with no entity').toBeVisible({
    timeout: 60_000,
  })

  // Where it must stop, and how. Not a primary that looks armed and swallows the click:
  // named reason + actually disabled, because an in-house user cannot resolve this in-app.
  const commitBtn = page.getByRole('button', { name: 'Filing needs a linked entity' })
  await expect(commitBtn, 'commit names why it cannot file').toBeVisible()
  await expect(commitBtn, 'and is truly disabled, not a silent no-op').toBeDisabled()

  // Manual entry stays reachable for in-house as well — and stops the SAME way the commit
  // step above does. INVCR-01-03 made the manual primary a real POST /v1/invoices, which
  // writes invoices.entity_id (NOT NULL), so with no entity anywhere in this tenant it
  // cannot file either. The refusal reuses the commit step's copy byte-for-byte (one
  // wording, not two that drift) and is gated on the RESOLVED entity, so it is truly
  // disabled rather than an armed button that swallows the click.
  await page.getByRole('button', { name: '← Back to import' }).click()
  await page.getByRole('button', { name: 'Skip — enter manually' }).click()
  const fileBtn = page.getByRole('button', { name: 'Filing needs a linked entity' })
  await expect(fileBtn, 'manual build step reachable for in-house, and names why it cannot file').toBeVisible()
  await expect(fileBtn, 'and is truly disabled, not a silent no-op').toBeDisabled()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
