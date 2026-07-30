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
// E2E-04/E2E-09 below, which share ONE page/session (the F6 hijack E2E-09 guards
// against is session-scoped) -- both live in a single test with labelled assertion
// blocks rather than two tests. E2E-05 was the third block in that test until
// INVCR-01-09 deleted it with the report row it clicked; see the deletion site.
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
import { buildMixedCsv, buildPerfCsv, PERF_HEADER } from '../importFixtures'

// MixedImportResponse: the subset of POST /v1/imports's success body (internal/
// importer/handlers.go's importResponse) E2E-04 reads to assert that a REAL import
// populates a REAL invoice UUID, independent of the DOM -- the wire-level fact that
// makes subtask 10's row click-through and 16's N=1 route implementable. invoice_id is
// optional on the wire (absent on dry-run; this is a real import, so it is always
// populated here -- perf.spec.ts's own proven recipe, re-verified against
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
  // INVCR-01-05 arm-on-click oracle (AC-14), hosted here because this is the exact
  // state it needs -- Map step, invoice_number unplaced -- and this is the only harness
  // that can reach it at all: the behaviour is a closure inside App.tsx's continueMapping
  // and this frontend's unit suite is node-only with no jsdom.
  //
  // The continue control is NOT disabled in this state (only the no-entity case is), it
  // just used to do nothing at all when clicked. It now ARMS invoice_number. TWICE is the
  // whole point: continueMapping must SET the armed field, not toggle it -- routing it
  // through ctx.armField (which IS a toggle: `a === k ? null : k`) would dis-arm on the
  // second click and reproduce the identical do-nothing click the fix deletes. Only the
  // second assertion can catch that; the first passes either way.
  const continueBtn = page.getByRole('button', { name: 'Map invoice number to continue' })
  const armedNote = page.getByText('invoice_number is armed', { exact: false })
  await continueBtn.click()
  await expect(armedNote, 'first click arms invoice_number instead of swallowing the click').toBeVisible()
  await continueBtn.click()
  await expect(armedNote, 'second click must LEAVE it armed -- a toggle here would dis-arm').toBeVisible()

  // Disarm before the click-to-place sequence below, restoring the exact precondition it
  // has always assumed (nothing armed). The chip click at the next line goes through
  // ctx.armField, which toggles -- from an already-armed state it would clear the field
  // and the column click after it would then assign nothing. This click doubles as proof
  // that armField itself is still a toggle, and is the reason continueMapping cannot be.
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await expect(armedNote, 'the chip click toggles the same field back off').toHaveCount(0)

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
  // Repointed by INVCR-01-05: 'Working…' no longer exists anywhere. The footer spinner
  // it named was deleted along with its determinate-bar sibling, replaced by the
  // ImportProgress card that body-swaps this whole step.
  //
  // An earlier note here promised that INVCR-01-05 would "introduce the honest
  // server-stage list and own asserting it positively". It deliberately does the
  // opposite, and there is nothing left to assert positively, because there is no
  // honest stage list to build:
  //   - Nothing can drive one. POST /v1/imports is synchronous with no job to poll (no
  //     GET/status route on the service, no Flusher or text/event-stream in
  //     internal/importer, no EventSource/ReadableStream in the app). importApi.ts's own
  //     progress contract already says everything after upload.onload is unobservable,
  //     "so any stage label there would be invented".
  //   - A STATIC list would misdescribe the server anyway: internal/importer/service.go
  //     stores rows BEFORE it validates them, and it has a classify/quarantine step -- the
  //     one that produces the whole structural-error channel this very test asserts on
  //     later -- that no plausible four-item list includes.
  // So the card claims nothing it cannot know. What it DOES render is asserted here.
  //
  // The title is the phase-stable anchor (both 'sending' and 'processing' show it); the
  // phase word is asserted as either side of the single transition the transport really
  // observes, since which one is on screen at any instant is a race with the network.
  await expect(page.getByText('Importing ui-perf.csv', { exact: true })).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText(/^(SENDING FILE|SERVER PROCESSING)$/)).toBeVisible()

  // The honest denominator, and the proof it is a denominator rather than a counter:
  // it is the server's OWN preview count of this fixture (500 invoices x 3 line-item
  // rows, 11 header columns), rendered whole and unchanging. There is no numerator to
  // pair it with -- UploadPhase carries bytes, never rows -- so a progress-shaped
  // "N OF 1500" appearing here later would be invented, and this exact-text assertion
  // is what would fail if someone added one.
  await expect(page.getByText('1500 ROWS · 11 COLS', { exact: true })).toBeVisible()

  await importResp
  const wireMs = Date.now() - importT0
  // Re-anchored by INVCR-01-09: the `Stat` tile grid this used to wait on
  // ('Ready invoices') is gone with CreateReport.tsx. The review shell's title is the
  // equivalent arrival signal -- it renders only once all FIVE of its requests resolve
  // (four since INVCR-01-10 added the `queued` pill count to the shell's Promise.all).
  await expect(page.getByRole('heading', { name: '500 invoices imported' })).toBeVisible({ timeout: 60_000 })
  const renderedMs = Date.now() - importT0

  // Evidence base for a future xhr.timeout decision (§5) -- assert NOTHING about
  // these numbers, no AC specifies a UI duration and a guessed budget would
  // spuriously fail on a cold fleet.
  console.log(`IMP-UI-PERF (deployed): 500 inv / 1500 rows -- preview ${previewMs}ms, import wire ${wireMs}ms, report rendered ${renderedMs}ms`)
  testInfo.annotations.push({ type: 'imp-ui-perf', description: `preview=${previewMs}ms wire=${wireMs}ms rendered=${renderedMs}ms` })

  // E2E-01 (Core AC7), REWRITTEN for the review shell (INVCR-01-09). All three facts
  // this asserted survive the deletion of CreateReport's `Stat` grid, in new shapes and
  // from new sources -- so the assertion moves rather than being dropped:
  //   - rows_total    -> the header sub-line's `1500 ROWS READ` (off the batch GET).
  //   - ready count   -> the title's `500 invoices imported`, which is now the LIVE
  //                      pagination.total of the batch's invoices, not the 201 body's
  //                      frozen `ready_invoices` -- a stronger fact, since it proves the
  //                      rows are queryable in the ledger and not merely counted.
  //   - quarantined   -> the amber "Not imported" channel's own tile.
  // `statValue()` (e2e/importFixtures.ts) was deleted with these three call sites: it
  // located a CreateReport `Stat`'s value by an xpath sibling step off a `.label` div,
  // a two-child shape that no longer exists anywhere, and it had no other consumer.
  await expect(page.getByText('1500 ROWS READ', { exact: false })).toBeVisible()
  await expect(page.getByText('0 unreadable rows', { exact: true })).toBeVisible()
  // Positive companion for the tile above: the channel's zero-state copy, which proves
  // the tile is rendering the AT-ZERO branch rather than an amber tile that happens to
  // read 0 -- the channel must stay visible (dashed, greyed) rather than disappear.
  await expect(page.getByText('Every row in the file became part of an invoice.', { exact: true })).toBeVisible()
  // The second tab is OMITTED from the DOM at zero unreadable rows, never merely hidden.
  await expect(page.getByRole('button', { name: /^Unreadable rows \(/ })).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('E2E-04/09 ([detail-target-exclusive]/F6, INVCR-01-09): the mixed fixture separates the two channels by TAB, the structural message is reachable only through Unreadable rows, and Finish leads to a live invoice detail', async ({
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

  // E2E-04, REWRITTEN for the review shell (INVCR-01-09). The old assertion proved the
  // two channels were two DISTINCT SECTIONS by text-offset ordering, because
  // CreateReport stacked them as untitled <div>s on one screen. They are now two TABS,
  // which affords a strictly stronger claim than ordering: the structural message must
  // be reachable ONLY through the Unreadable rows tab, i.e. the channels are separated
  // by NAVIGATION and not merely by vertical position. A merged rendering fails the
  // first leg; a mislabelled one fails the second.
  //
  // The CONTENT half of the old assertion (`vat-standard-rate` under "Rule violations")
  // still has no home HERE. INVCR-01-10 has since built the Invoices tab -- the table
  // with its per-row verdicts, the batch-wide failing-rules rail (where a
  // `vat-standard-rate` pill now genuinely renders) and the pager -- but none of it runs
  // on this branch until the PR leaves draft, and asserting a rail pill from inside
  // E2E-04, whose whole subject is the two channels being separated by NAVIGATION, would
  // blur two claims into one spec. Subtask 16 owns asserting the tab's content on the
  // deployed run.
  const structuralMsg = 'rows disagree on issue_date'
  await expect(page.getByRole('button', { name: /^Invoices \(\d+\)$/ })).toBeVisible({ timeout: 60_000 })
  const unreadableTab = page.getByRole('button', { name: /^Unreadable rows \(\d+\)$/ })
  await expect(unreadableTab, 'the Unreadable rows tab must render for a fixture with a structural failure').toBeVisible()

  // Leg 1: the structural channel is NOT on the default tab.
  await expect(page.getByText(structuralMsg)).toHaveCount(0)

  // Leg 2: it is on the other one, verbatim -- the browser never re-authors the
  // server's own reason string.
  await unreadableTab.click()
  await expect(page.getByText(structuralMsg)).toBeVisible()

  // §7.1's amber "Not imported" channel renders its own NON-ZERO count off the batch's
  // structural errors, independent of the tab body -- the tile, the tab and the footer
  // all read the same expansion count, so a disagreement between them is falsifiable.
  await expect(page.getByText(/^[1-9]\d* unreadable rows$/)).toBeVisible()

  // The clean invoice is a provable SUBSET witness, RE-HOMED (INVCR-01-09) rather than
  // rescoped. Asserted HERE, deliberately after the click above and never before it: the
  // claim is now "a readable invoice never enters the STRUCTURAL channel", i.e. it is
  // absent while the Unreadable rows tab is the open one -- and only one tab body is
  // mounted at a time, so this is a claim about that tab, not about the page.
  //
  // The old version made the same call before any tab existed, where it passed only
  // because 09 lists no invoices anywhere. Subtask 10 puts this very invoice on screen
  // legitimately, under the OTHER tab, at which point the old placement would have gone
  // red for a correct implementation and the obvious "fix" (deleting it) would have lost
  // the invariant. This placement survives 10 unchanged.
  await expect(page.getByText('INV-UI-MIX-CLEAN')).toHaveCount(0)

  // E2E-05 is DELETED here, not rescoped. Its subject was the click-through from a
  // rule-violation row in the import report to the live InvoiceDetail, and the review
  // shell has no such row: the content channel moved off the frozen 201 payload onto
  // live per-invoice verdicts (D4), and 09 rendered no table for them. Two owners, both
  // downstream on this branch:
  //   - subtask 10 owns the invoices-table row click-through, which is the direct
  //     successor of this assertion. NOW BUILT (ReviewInvoicesTab.tsx: a `review-row`
  //     click calls the same ctx.openImportedInvoice this exercised), but still
  //     UNASSERTED -- nothing on this branch runs until the PR leaves draft, so subtask
  //     16 owns proving it on the deployed run.
  //   - subtask 16 owns the N=1 route through the SAME openImportedInvoice/detailTarget
  //     seam this exercised (import a one-invoice file -> land on the real InvoiceDetail,
  //     never the review shell), which is the highest-value uncovered path in 09.
  // Deleting rather than leaving a weakened version is deliberate: a spec that asserts
  // less than its name claims is worse than an absent one.
  //
  // What is NOT deleted is the fact the import response carries a real invoice id at
  // all -- that is a wire-level claim, independent of any DOM affordance, and it is what
  // makes subtask 10's and 16's click-throughs implementable.
  const violateEntry = body.invoice_violations.find((iv) => iv.invoice_number === 'INV-UI-MIX-VIOLATE')
  expect(
    violateEntry,
    'expected an invoice_violations entry for INV-UI-MIX-VIOLATE -- if this fails, check whether vat-standard-rate was disabled by an out-of-order suite run (api -> topology -> demo)',
  ).toBeTruthy()
  expect(violateEntry!.violations.map((v) => v.rule_key)).toEqual(['vat-standard-rate'])
  expect(violateEntry!.invoice_id, 'invoice_violations[].invoice_id must be populated on a REAL import').toBeTruthy()

  // E2E-09 (the F6 regression guard, [detail-target-exclusive]): leave the review
  // screen for Invoices and open one of this batch's invoices -- the live detail must
  // render THAT invoice's own content. "Audit trail" was the retired mock detail's
  // panel title; M4-09-05's live detail names the equivalent panel "Status history"
  // instead, so its absence here is also proof this is the live surface, never the old
  // mock fallback.
  //
  // The exit is "Finish · go to invoices" (INVCR-01-09), not the detail view's
  // "← All invoices": with E2E-05 deleted this test never leaves the review shell, so
  // there is no detail view to come back from. Finish is NAVIGATION ONLY -- the
  // invoices were persisted at import time -- and this click is also the only coverage
  // that the review screen has a working exit at all.
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
  await page.getByRole('button', { name: 'Finish · go to invoices' }).click()
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
//
// INVCR-01-05 moved WHEN that refusal is legible, not whether: the amber panel now states
// it on step 1, instead of letting the user choose a file and map eleven columns before
// meeting it at the commit. The panel is INFORMATIONAL and the distinction is the whole
// point of this test -- "told earlier" and "blocked earlier" look identical in a
// screenshot and are opposites in behaviour. So the assertions below are deliberately
// paired: every claim that the panel is PRESENT is followed by a claim that the control it
// sits next to still WORKS. Restoring the old gate under any new condition fails here.
test('[inhouse-can-start] LIVE: the in-house persona reaches the import dropzone and reads columns; filing is refused in words, not silence', async ({
  page,
}) => {
  const errors = collectErrors(page)

  await signInInhouse(page)
  await expect(page.locator('aside.pf-sidebar')).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await expect(page.getByText('Import invoices ·', { exact: false })).toBeVisible({ timeout: 30_000 })

  // The heart of it: the surface the blocked state used to replace.
  await expect(page.locator('label[for="pf-import-file"]'), 'in-house gets the dropzone too').toBeVisible({
    timeout: 30_000,
  })

  // INVCR-01-05's amber panel, asserted POSITIVELY. What stood here was
  // `getByText('No linked entity').toHaveCount(0)` -- an absence check meant to catch the
  // old blocking empty state coming back. It was already vacuous (that exact string
  // survives only in source comments) and it passed on this screen purely by luck: the new
  // panel says "No linked business entity", which does not contain the substring it looked
  // for. An absence assertion that cannot fail is worse than none, because it reads like
  // coverage. The real guarantee was never "no such words appear" -- it is "the surface
  // still WORKS", and every assertion below this block states that directly.
  //
  // Timeout: unlike the dropzone, this panel waits on the entities fetch to SETTLE. It is
  // gated on `entitiesState` being 'ready'|'empty' as well as a null activeEntity,
  // precisely so a firm user never sees an amber refusal flash while that fetch is in
  // flight -- which means an in-house user sees it only once the fetch has answered.
  await expect(page.getByText('No linked business entity', { exact: true }), 'the refusal is stated up front, on step 1').toBeVisible({
    timeout: 30_000,
  })
  await expect(page.getByText('so there is nothing to file against', { exact: false })).toBeVisible()
  await expect(
    page.getByText('no way to link one from an in-house workspace', { exact: false }),
    'and it says why THIS persona cannot resolve it',
  ).toBeVisible()

  // No dead control. NAV_CLIENTS is in the firm-only sidebar group and EntityFormModal
  // mounts from ClientsView alone, so the firm's "Link a business entity →" CTA would send
  // an in-house user to a screen they have no route to -- the exact dead end this panel
  // exists to replace. In-house gets the sentence and no button.
  await expect(
    page.getByRole('button', { name: 'Link a business entity' }),
    'no CTA to a screen this persona cannot reach',
  ).toHaveCount(0)

  // And the panel is INFORMATIONAL, not a gate: it is added to the card, it replaces
  // nothing and disables nothing. This is the regression guard proper -- if the panel ever
  // becomes blocking, this test fails here and at every assertion after it.
  await expect(page.getByText('Manual entry has the same requirement', { exact: false }), 'the skip hint tells the truth about manual entry too').toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Skip — enter manually' }),
    'manual entry stays enabled -- it names its own reason one screen later',
  ).toBeEnabled()

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
