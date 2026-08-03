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
// fullyParallel:false / workers:1 (playwright.topology.config.ts:21-22 -- conformed to
// the convention's "one browser, serial" rule by M4-14-01, with its own rationale at
// :18-20; this comment said fullyParallel:true until PERSONA-01 corrected it), retries:1
// in CI: each test below creates its OWN fresh entity (own freshTin()) and its own
// page/sign-in, so no two tests contend for one entity -- which still matters under
// workers:1, because the entity is what keeps a RETRY of a test from colliding with its
// own earlier attempt's fixed invoice numbers -- no-duplicate-invoice-number is scoped
// per entity
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
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

import { test, expect, type Page, type Request } from '@playwright/test'
import { login, createEntity, PERSONAS } from '../api/client'
import { freshTin } from '../api/fixtures'
import { APP_URL, FIRM_PERSONA, INHOUSE_PERSONA } from './targets'
import { buildHeaderOnlyCsv, buildMixedCsv, buildPerfCsv, buildSingleInvoiceCsv, PERF_HEADER } from '../importFixtures'

// BULK-01-08 (task-312) fixtures -- COMMITTED files, referenced by path, deliberately
// NEVER regenerated in memory the way importFixtures.ts's builders are. This is a
// declared, one-story deviation from that convention (Core AC 7 requires spreadsheets
// that are literally committed to the repo), and it is safe ONLY under two conditions
// that hold for every test below:
//   1. Every test mints its OWN fresh entity via createEntity + freshTin() before
//      page.goto (same discipline as every other test in this file).
//   2. no-duplicate-invoice-number (internal/importer/service.go) is scoped PER ENTITY.
// Together, a static invoice number baked into a committed file (e.g. BULK-LAG-001)
// cannot collide across separate runs of this suite, and -- critically -- cannot
// collide across a CI retry of the SAME test either, because a retry mints its own new
// entity via the same freshTin() call. Do NOT "fix" these fixtures back into an
// in-memory generator: that would silently reintroduce the cross-run collision risk
// this file header exists to rule out.
//
// Path resolution follows e2e/package.test.ts:11 and e2e/personas.test.ts:10-12 (also
// e2e/api/no-db-access.test.ts): e2e/ is an ESM package ("type":"module",
// "module":"ESNext"), so `__dirname` does not exist, and setInputFiles(relativeString)
// resolves against `process.cwd()` -- which is `e2e/` under CI's `pnpm --filter`
// invocation but the repo root for a developer running from there. import.meta.url,
// computed once at module scope, is the only cwd-independent anchor.
const BULK_FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '../fixtures/bulk')
const SHARED_LAYOUT_LAGOS = join(BULK_FIXTURES, 'shared-layout-lagos.csv')
const SHARED_LAYOUT_ABUJA = join(BULK_FIXTURES, 'shared-layout-abuja.csv')
const LAYOUT_A_TILL = join(BULK_FIXTURES, 'layout-a-till.csv')
const LAYOUT_B_TERMINAL = join(BULK_FIXTURES, 'layout-b-terminal.csv')
const PARTIAL_FIRST = join(BULK_FIXTURES, 'partial-first.csv')
const PARTIAL_DUPE = join(BULK_FIXTURES, 'partial-dupe.csv')

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

// requestBody(): the multipart body Chromium recorded, for the ONE spec below that
// inspects a request rather than a response. postDataBuffer first (postData() decodes
// as text and is null for some bodies); '' when nothing was recorded, so an assertion
// over it fails loudly instead of passing vacuously.
function requestBody(req: Request): string {
  return req.postDataBuffer()?.toString('utf8') ?? req.postData() ?? ''
}

test('E2E-01/02/03/06/07 (Core AC7, FLOW-05): 500-invoice CSV completes through the UI on deployed dev', async ({ page }, testInfo) => {
  // Sign-in, nav, ONE upload ([upload-once] -- preview stores the bytes and the
  // import that follows sends only the id), and render, on a possibly cold 11-service fleet.
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
  // BULK-01-05 (task-308) turned the single-file card into a per-file LIST
  // (ImportProgress.tsx, lib/importRun.ts's runFileRows) -- these three assertions are
  // invalidated by that rewrite itself, not by a later multi-file subtask (BULK-01-08
  // adds the genuinely multi-file journeys; this run is still exactly one file). The
  // title is now phase-stable across the WHOLE run rather than naming one file (states
  // the run's size, "Importing 1 file" here); the filename moved onto its own row
  // alongside the phase word, asserted separately so a rename of either one fails on
  // its own line.
  await expect(page.getByText('Importing 1 file', { exact: true })).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText('ui-perf.csv', { exact: true })).toBeVisible()
  // The phase word is asserted as either side of the single transition the transport
  // really observes, since which one is on screen at any instant is a race with the
  // network.
  await expect(page.getByText(/^(SENDING FILE|SERVER PROCESSING)$/)).toBeVisible()

  // The old "1500 ROWS · 11 COLS" denominator is GONE, not renamed: lib/importRun.ts's
  // RunFileRow (the per-file list's own type, BULK-01-05) deliberately carries nothing
  // beyond a filename for a queued/sending/processing row -- a run's files can come
  // from DIFFERENT mapping groups with different preview facts, so there is no single
  // row-count/column-count left to state honestly at this per-file layer once the list
  // generalizes past one file. The equivalent server-confirmed fact reappears per
  // file, AFTER it settles, as that row's own ready-invoice count.

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
  // server's own reason string. TWO matches, not one, and that is the fixture's own
  // doc comment (importFixtures.ts, buildMixedCsv): INV-UI-MIX-STRUCT is two rows that
  // both disagree on issue_date, so the "Why it could not be read" table renders the
  // identical reason once per row (row 2 and row 3) -- a bare `getByText` would throw a
  // strict-mode violation on the legitimate second match rather than proving anything
  // about the message being re-authored.
  await unreadableTab.click()
  await expect(page.getByText(structuralMsg)).toHaveCount(2)

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
  //   - subtask 10 owned the invoices-table row's disclosure, but NOT as a
  //     click-through: task-290 ([navigate-vs-expand]) built a whole-row click as the
  //     row-EXPANSION toggle (§7.3's fix loop) instead, never a navigation to
  //     InvoiceDetail -- ctx.openImportedInvoice stays wired everywhere else
  //     (InvoicesList.tsx, the N=1 route below) but is no longer what a review-row click
  //     does. Proven on the deployed run by subtask 16's own INVCR-E2E-1 (the row-switch
  //     expand/fix/re-validate loop) above.
  //   - subtask 16 owns the N=1 route through the SAME openImportedInvoice/detailTarget
  //     seam this exercised (import a one-invoice file -> land on the real InvoiceDetail,
  //     never the review shell), which is the highest-value uncovered path in 09 --
  //     INVCR-E2E-2 above.
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

// [inhouse-can-file] — task-304 (INVCR-01-19) REPLACES [inhouse-can-start] outright
// (AC-7), not a patch of its assertions. That test's whole premise was that in-house
// COULD open the wizard and read columns but every filing path was a legible dead end
// ("filing is refused in words, not silence") -- true only because in-house's `active`
// memo was hardcoded to a null-entityId placeholder and the tenant had zero
// business_entities rows to resolve instead. Both are now false: db/seed.dev.sql seeds
// the in-house tenant exactly ONE entity (AC-1), and App.tsx's `active` memo resolves it
// through the SAME entitiesList path as firm, no persona special-case (AC-2,
// resolveActiveClient in lib/clients.ts). So the premise INVERTS: this test proves
// in-house FILES successfully, both by import and manually, mirroring the firm paths
// (E2E-01 above for import; CreateForm's manual round trip, same as
// [import-upload-unify] proves is reachable in LIVE) rather than asserting a refusal.
//
// What SURVIVES from the old test, restated rather than deleted: the amber "No linked
// business entity" panel and its 'Filing needs a linked entity' refusal are still real,
// still INFORMATIONAL-never-blocking code (CreateUpload.tsx's computeNoEntity,
// lib/importFlow.ts) -- they just no longer fire for THIS persona, because this persona
// no longer has anything to refuse. AC-6 keeps that contract alive for its other
// legitimate case (a FIRM workspace whose active entity has been archived out of the
// roster) -- but every persona this e2e suite's fixtures can sign in as now legitimately
// has at least one entity, so a genuinely-zero-entity workspace is no longer reachable
// through ANY browser spec here. lib/importFlow.test.ts's computeNoEntity specs
// (FLOW-15..17) are the surviving proof that the predicate itself still fires correctly
// for that case -- see that file for why this is the honest place for it to live now.
test('[inhouse-can-file] LIVE: the in-house persona resolves its seeded entity and files an invoice, both by import and manually', async ({
  page,
}) => {
  const errors = collectErrors(page)

  await signInInhouse(page)
  await expect(page.locator('aside.pf-sidebar')).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await expect(page.getByText('Import invoices ·', { exact: false })).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('label[for="pf-import-file"]'), 'in-house gets the dropzone too').toBeVisible({
    timeout: 30_000,
  })

  // Inverts the old test's positive assertion of this exact panel (AC-7/8:662-669): with
  // a real entity resolved, the amber refusal — and its in-house-only sentence,
  // CreateUpload.tsx's now-deleted "There is no way to link one from an in-house
  // workspace yet" — never renders at all, at any point in the fetch's lifecycle
  // (computeNoEntity's guard 1 keeps it off during 'idle'/'loading' too, and a resolved
  // entity keeps it off once 'ready'). No absence check ever proves a flash CAN'T have
  // happened a moment earlier — the real proof is everything below: Read columns arming,
  // the Map step's commit control, and the import itself all succeeding on this
  // workspace's real resolved entity.
  await expect(page.getByText('No linked business entity', { exact: true }), 'no refusal — in-house has a resolved entity now').toHaveCount(0)

  const invoiceNumber = `INH-IMP-${Date.now()}`
  const readColumnsBtn = page.getByRole('button', { name: 'Read columns' })
  await expect(readColumnsBtn, 'disabled before any file is chosen').toBeDisabled()

  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'inhouse.csv', mimeType: 'text/csv', buffer: Buffer.from(`Invoice No,Subtotal\n${invoiceNumber},100\n`, 'utf8') })
  await expect(readColumnsBtn, 'arms on the file alone').toBeEnabled()

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await readColumnsBtn.click()
  await previewResp
  await expect(page.getByText('Map fields to columns ·', { exact: false })).toBeVisible({ timeout: 30_000 })

  // Inverts :715-717: the commit control names the REAL action now, never the refusal
  // [inhouse-can-start] pinned.
  await expect(
    page.getByRole('button', { name: 'Filing needs a linked entity' }),
    'the refusal is gone from the Map step — in-house has an entity now',
  ).toHaveCount(0)

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()

  const importBtn = page.getByRole('button', { name: /^Import \d+ rows$/ })
  await expect(importBtn, 'armed once invoice_number is placed — the SAME control that used to be the disabled refusal').toBeEnabled()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await importBtn.click()
  await importResp

  // Core AC 8: exactly one ready invoice routes straight to its own detail screen — the
  // affirmation IS the server's own row rendering, the same real-filing proof the firm
  // path gets (E2E-01 above), now available to in-house too.
  await expect(page.getByTestId('invoice-detail'), 'the import actually filed a real invoice, not a refusal').toBeVisible({ timeout: 30_000 })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(invoiceNumber)

  // And manual entry — the OTHER path [inhouse-can-start] proved was also a dead end
  // (:725-729). Re-opens the wizard fresh via the header CTA rather than navigating back
  // through the just-filed import's state.
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page.getByRole('button', { name: 'Skip — enter manually' }).click()

  const fileBtn = page.getByRole('button', { name: 'File invoice' })
  await expect(fileBtn, 'manual build step is armed for in-house now, not refused').toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Filing needs a linked entity' }),
    'the manual refusal is gone too',
  ).toHaveCount(0)

  // A fresh number: the default draft seeds a FIXED literal (lib/clients.ts's
  // defaultDraft), and this suite reruns against a persistent, never-reset dev DB — a
  // second run under the literal would 409 on (tenant_id, entity_id, invoice_number).
  const manualNumber = `INH-MAN-${Date.now()}`
  await page.getByPlaceholder('INV-0000-00000').fill(manualNumber)
  await expect(fileBtn).toBeEnabled()

  const createResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices'),
    { timeout: 30_000 },
  )
  await fileBtn.click()
  await createResp

  await expect(page.getByTestId('invoice-detail'), 'manual filing succeeded too, for in-house').toBeVisible({ timeout: 30_000 })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(manualNumber)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// INVCR-01-16 (task-292) -- Core AC 11's closing subtask. The five tests below discharge
// the story's rewritten ACs 1/2/4/6/7 against a deployed build (AC-3/AC-5/AC-9/AC-10 live
// in invoice-surfaces.spec.ts; AC-5's own in-house happy path is [inhouse-can-file] above
// -- task-304 -- NOT duplicated here, this subtask only verifies it runs green).
//
// ReviewRow.tsx's row (`data-testid="review-row"`) and its row-expansion panel
// (`data-testid="review-row-expansion"`, and everything inside it) are SIBLINGS in the
// DOM, not parent/child (ReviewRow.tsx returns `<>{row}{expanded && <ExpandedFixPanel/>}
// </>`) -- so every panel-internal testid below (`review-fix-card`/`review-fix-input`/
// `review-fix-save`/`review-revalidate`/`review-row-passing`/`review-keep-reason`/
// `review-keep`/`review-kept-banner`) is queried PAGE-SCOPED, never scoped off a row
// locator. This is safe because ReviewInvoicesTab.tsx's `expandedId` is singular -- at
// most one row's panel exists in the DOM at any moment -- unlike `review-verdict`/
// `review-select`, which genuinely are children of the row's own grid div and so ARE
// scoped off the row locator throughout.

// AC-1: the full firm N>1 loop, driven for real for the first time -- import a mixed
// batch, filter the review table by a failing RULE (the rail, not a pill), expand a row,
// fix the field the server flagged, re-validate, watch the verdict pill move, select the
// now-eligible set, confirm, and prove the row badges only move once a REAL re-fetch
// lands (never derived off the submit response -- bulkOutcome's own documented
// discipline, lib/reviewBatch.ts: batch_submit.go's duplicate-request branch hard-codes a
// known-wrong status on a skipped item, M5-11).
//
// Also closes the coverage gap task-290's own Implementation Notes flagged and this
// subtask's own notes repeat: switching the row-expansion slot between two rows must not
// leak one row's fetched detail/draft into the other. Verified by CODE READING only until
// now -- vitest here is environment:'node' (no jsdom/RTL), so there is no unit oracle for
// this, and this is the first time it runs against a live DOM at all.
test('INVCR-E2E-1 firm: mixed import -> filter by rule -> expand -> fix -> re-validate -> select -> submit, badges from a re-fetch', async ({
  page,
}) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `INVCR-01-16 loop ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'e2e-loop.csv', mimeType: 'text/csv', buffer: Buffer.from(buildMixedCsv(), 'utf8') })

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

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
  const body = (await resp.json()) as { id: string }
  const batchId = body.id

  // Lands on the review shell -- 2 ready invoices (CLEAN and VIOLATE; STRUCT never
  // becomes an invoice at all), both §7.1 channels rendered.
  await expect(page.getByRole('heading', { name: '2 invoices imported' })).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText('1 valid', { exact: true })).toBeVisible()
  await expect(page.getByText('1 failed a rule', { exact: true })).toBeVisible()
  await expect(page.getByText(/^[1-9]\d* unreadable rows$/)).toBeVisible()

  const violateRow = page.getByTestId('review-row').filter({ hasText: 'INV-UI-MIX-VIOLATE' })
  const cleanRow = page.getByTestId('review-row').filter({ hasText: 'INV-UI-MIX-CLEAN' })
  await expect(violateRow).toBeVisible()
  await expect(cleanRow).toBeVisible()

  // Row-switch non-leak (coverage gap): expand VIOLATE, note its (wrong) VAT value,
  // switch to CLEAN, and back -- neither panel may show the other's data.
  const fixCard = page.getByTestId('review-fix-card')
  const fixInput = page.getByTestId('review-fix-input')
  const rowPassing = page.getByTestId('review-row-passing')

  await violateRow.click()
  await expect(fixCard).toContainText('vat-standard-rate')
  await expect(fixInput).toHaveValue('0.00')

  await cleanRow.click()
  await expect(rowPassing, "CLEAN never inherits VIOLATE's fix card").toBeVisible()
  await expect(fixCard).toHaveCount(0)

  await violateRow.click()
  await expect(rowPassing).toHaveCount(0)
  await expect(fixInput, 'switching back re-fetches VIOLATE fresh, not a ghost of CLEAN').toHaveValue('0.00')

  // Filter by the failing RULE (the rail, not the "Needs a fix" pill) -- narrows the
  // server-paged table to VIOLATE alone.
  const railPill = page.getByTestId('review-rail-pill').filter({ hasText: 'vat-standard-rate' })
  await expect(railPill).toBeVisible()
  await railPill.click()
  await expect(cleanRow).toHaveCount(0)
  await expect(violateRow).toBeVisible()

  // Fix the field the server flagged, save.
  await expect(fixInput).toHaveValue('0.00')
  await fixInput.fill('75.00')
  await page.getByTestId('review-fix-save').click()
  await expect(fixInput, 'the panel re-fetches the saved value').toHaveValue('75.00')

  // Re-validate -- the verdict pill moves. ReviewInvoicesTab is entirely server-filtered
  // ([filters-are-server-side], its own file header) -- the fixed invoice no longer
  // matches vat-standard-rate, so the refetch this triggers (onChanged -> page.run())
  // drops it out of THIS filtered view the instant it lands, taking its expanded panel
  // with it. Proven explicitly, not assumed, before un-filtering: asserting
  // `rowPassing` visible here (while still filtered) would be asserting a fact about a
  // row that just left the DOM.
  await page.getByTestId('review-revalidate').click()
  await expect(violateRow, 'the fixed row drops out of the still-active rule filter').toHaveCount(0)

  // Toggle the SAME rule filter back off -- the invoice no longer matches it, so this is
  // also what makes the moved verdict (and the still-expanded row's passing panel)
  // observable again. `expandedId` (ReviewInvoicesTab.tsx) is untouched by the filter
  // round-trip, so the SAME row re-expands rather than starting collapsed.
  await railPill.click()
  await expect(cleanRow).toBeVisible()
  await expect(violateRow).toBeVisible()
  await expect(rowPassing, 'clean after the fix').toBeVisible()
  await expect(violateRow.getByTestId('review-verdict'), 'the verdict pill moved to VALIDATED').toContainText('VALIDATED')
  await expect(violateRow.getByTestId('review-verdict')).not.toContainText('RULES FAILED')

  // Select the (now fully) eligible subset and submit.
  await page.getByTestId('review-select-all').click()
  await expect(page.getByTestId('review-bulk-submit')).toContainText('Submit 2 for transmission')
  await page.getByTestId('review-bulk-submit').click()
  await expect(page.getByTestId('review-bulk-confirm')).toContainText('Yes, send 2 now')

  const submitResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices/submissions'),
  )
  // The re-fetch this whole AC pins as the badge source -- registered BEFORE the click so
  // it cannot be satisfied by a request that already landed.
  const refetchResp = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices') &&
      new URL(r.url()).searchParams.get('import_batch_id') === batchId,
  )
  await page.getByTestId('review-bulk-confirm').click()
  await submitResp
  await refetchResp

  await expect(
    violateRow.getByTestId('review-verdict'),
    'the badge only moves once the re-fetch lands, never off the submit response',
  ).toContainText('QUEUED')
  await expect(cleanRow.getByTestId('review-verdict')).toContainText('QUEUED')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// AC-2: Core AC 8's N=1 route, proven on the deployed build for the first time -- a
// single-invoice CSV lands directly on the real InvoiceDetail, asserted by PRESENCE of
// its own data-testid and status-history, never by the review table's absence alone
// ([E2E-must-not] #4).
test('INVCR-E2E-2 firm: a single-invoice CSV lands on the real invoice detail, never a one-row review grid', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `INVCR-01-16 single ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  const invoiceNumber = `INV-E2E-SINGLE-${Date.now()}`
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'single.csv', mimeType: 'text/csv', buffer: Buffer.from(buildSingleInvoiceCsv(invoiceNumber), 'utf8') })

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await importResp

  await expect(page.getByTestId('invoice-detail'), 'N=1 routes to the real detail').toBeVisible({ timeout: 30_000 })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(invoiceNumber)
  await expect(page.getByTestId('status-history'), 'the real detail carries a status-history panel').toBeVisible()
  await expect(page.getByTestId('review-table')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// AC-4: a header-only file (a real spreadsheet, real bytes, zero data rows) is refused
// honestly -- §7.5's "Nothing was imported" with three zeroed tiles, and no Map-step
// retry offered from the rejected screen (only re-upload or manual entry).
test('INVCR-E2E-4 firm: a header-only file is refused honestly, with no Map-step retry offered', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `INVCR-01-16 rejected ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'header-only.csv', mimeType: 'text/csv', buffer: Buffer.from(buildHeaderOnlyCsv(), 'utf8') })

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

  // A header-only file still echoes real columns (PRV-07) -- invoice_number is placed by
  // hand exactly as every other fixture in this file, at 0 rows.
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  const importBtn = page.getByRole('button', { name: 'Import 0 rows' })
  await expect(importBtn).toBeEnabled()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await importBtn.click()
  await importResp

  await expect(page.getByText('Nothing was imported', { exact: true }), "§7.5's rejected-file state").toBeVisible({ timeout: 30_000 })
  await expect(page.getByText('Invoices created', { exact: true })).toBeVisible()
  await expect(page.getByText('Rows stored', { exact: true })).toBeVisible()
  await expect(page.getByText('Rows quarantined', { exact: true })).toBeVisible()
  // Three ZEROED tiles -- a header-only file creates nothing, stores nothing, quarantines
  // nothing (it fails before any per-row classification runs at all).
  await expect(page.getByText('0', { exact: true })).toHaveCount(3)

  // No Map-step retry from here: the only two recovery actions are re-upload and manual
  // entry (ctx.restartImport / ctx.skipUpload) -- never a "go back and remap" affordance.
  await expect(page.getByRole('button', { name: 'Choose another file' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Enter one invoice instead' })).toBeVisible()
  await expect(page.getByText('Map fields to columns', { exact: false })).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// AC-6: the review screen is addressable and REVISITABLE (D4) -- a reload on
// `#review/<batchId>` restores it with BOTH channels re-derived from a fresh GET, never
// from memory the tab already held.
test('INVCR-E2E-6 the review screen survives a reload -- the deep link re-derives both channels', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `INVCR-01-16 deep-link ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'e2e-deep-link.csv', mimeType: 'text/csv', buffer: Buffer.from(buildMixedCsv(), 'utf8') })

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await importResp

  await expect(page.getByRole('heading', { name: '2 invoices imported' })).toBeVisible({ timeout: 60_000 })
  await expect(page).toHaveURL(/#review\//)

  // Registered BEFORE reload: D4's whole point is that a revisit re-derives from the
  // server rather than reading a frozen import-time payload, so a genuine GET must fire
  // for the batch AND the invoices list, not merely a client-side re-render of memory
  // this page already held.
  const batchRefetch = page.waitForResponse(
    (r) => r.request().method() === 'GET' && /\/api\/invoice\/v1\/imports\/[0-9a-fA-F-]+$/.test(new URL(r.url()).pathname),
  )
  const invoicesRefetch = page.waitForResponse(
    (r) => r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices'),
  )
  await page.reload()
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
  await batchRefetch
  await invoicesRefetch

  await expect(page.getByRole('heading', { name: '2 invoices imported' })).toBeVisible({ timeout: 30_000 })
  await expect(page.getByText('1 valid', { exact: true })).toBeVisible()
  await expect(page.getByText('1 failed a rule', { exact: true })).toBeVisible()
  await expect(page.getByTestId('review-table')).toBeVisible()
  await expect(page.getByTestId('review-row').filter({ hasText: 'INV-UI-MIX-VIOLATE' })).toBeVisible()
  await expect(page.getByTestId('review-row').filter({ hasText: 'INV-UI-MIX-CLEAN' })).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// AC-7: keeping a failing invoice as-is drops it out of "Needs a fix" but the row -- and
// its checkbox -- stay on screen, DISABLED, never absent (task-291's own QA finding; the
// plan's older "checkbox is absent" wording is wrong and the rewritten AC corrects it).
test('INVCR-E2E-7 kept-as-is drops out of Needs a fix and stays present-but-disabled, never absent', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `INVCR-01-16 keep ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'e2e-keep.csv', mimeType: 'text/csv', buffer: Buffer.from(buildMixedCsv(), 'utf8') })

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await importResp
  await expect(page.getByRole('heading', { name: '2 invoices imported' })).toBeVisible({ timeout: 60_000 })

  const violateRow = page.getByTestId('review-row').filter({ hasText: 'INV-UI-MIX-VIOLATE' })
  const cleanRow = page.getByTestId('review-row').filter({ hasText: 'INV-UI-MIX-CLEAN' })
  await expect(violateRow).toBeVisible()

  await violateRow.click()
  const keepReason = page.getByTestId('review-keep-reason')
  await expect(keepReason).toBeVisible()
  await keepReason.fill('Buyer confirmed the VAT discrepancy is intentional; will not recur.')
  await page.getByTestId('review-keep').click()

  // The pill flips, and the reason surfaces verbatim (KEEP-3's own live proof).
  await expect(violateRow.getByTestId('review-verdict')).toContainText('KEPT')
  await expect(page.getByTestId('review-kept-banner')).toContainText('Buyer confirmed the VAT discrepancy is intentional')

  // It drops out of "Needs a fix" ...
  await page.getByTestId('review-filter-pill').filter({ hasText: 'Needs a fix' }).click()
  await expect(page.getByTestId('review-row').filter({ hasText: 'INV-UI-MIX-VIOLATE' })).toHaveCount(0)

  // ... but stays PRESENT and DISABLED on "All", never absent.
  await page.getByTestId('review-filter-pill').filter({ hasText: 'All' }).click()
  await expect(violateRow).toBeVisible()
  await expect(violateRow.getByTestId('review-select'), 'the checkbox renders -- it is not absent').toBeVisible()
  await expect(
    violateRow.getByTestId('review-select'),
    'but it is disabled -- a kept row stays non-selectable (isRowSelectable)',
  ).toBeDisabled()

  // select-all excludes it (status stays non-validated even though kept) and still picks
  // up the still-eligible CLEAN row.
  await page.getByTestId('review-select-all').click()
  await expect(cleanRow.getByTestId('review-select')).toBeChecked()
  await expect(violateRow.getByTestId('review-select')).not.toBeChecked()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// BULK-01-08 (task-312) -- the FINAL subtask of BULK-01 (multi-file import), and the
// deployed oracle for Core AC 7: the user made it an explicit acceptance criterion that
// multi-file import is covered by e2e specs run against spreadsheets COMMITTED to the
// repo (the BULK_FIXTURES constants above), across three shapes -- a shared column
// layout, two different layouts, and a partial cross-file failure. Also serves as the
// deployed oracle for Core AC 1-6 (BULK-01-01 .. BULK-01-07), the same role INVCR-01-16
// played for M4-08/INVCR-01 above.
//
// Same "cannot run locally" oracle as every other Mode-A block in this file: this
// package's "No Local Server" policy plus the ephemeral per-PR Railway environment mean
// `pnpm --filter @invoice-os/e2e typecheck` + `playwright test --list` (topology config)
// collecting these three tests is the whole local oracle. The first REAL run is the
// deploy gate on this PR, once it leaves draft.
//
// Only `invoice_number` is ever click-mapped below, in every group, on every fixture --
// it is the ONLY field canSubmitMapping (lib/mapping.ts) gates the commit on, and none of
// the three shapes below asserts anything about rule violations or verdict pills, so
// there is no reason to also hand-map `subtotal` the way E2E-04's buildMixedCsv fixture
// needs to (that fixture's whole point is a clean/violating SPLIT this one does not
// care about). Leaving every other canonical field unmapped is the documented,
// non-blocking "optional fields unmapped" path (CreateMapping.tsx's own mapNote branch).

// BULK-E2E-01 (BULK-E2E-01a..01d/01e/01f): a shared-layout multi-file run.
test('BULK-E2E-01 (Core AC 1/2/3): shared-layout multi-file run -- select, cap-refuse, remove in order, map once, one review screen for both files', async ({
  page,
}) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BULK-01 shared ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  const fileInput = page.locator('input[type="file"][accept=".csv,.xlsx"]')
  const fileRow = (name: string) => page.locator('ul li').filter({ hasText: name })
  const fileNames = page.locator('ul li div.mono')

  // BULK-E2E-01a: several files really are selected -- both filenames listed after one
  // setInputFiles([lagos, abuja]) call.
  await fileInput.setInputFiles([SHARED_LAYOUT_LAGOS, SHARED_LAYOUT_ABUJA])
  await expect(fileRow('shared-layout-lagos.csv'), 'both filenames listed after one multi-file pick').toBeVisible()
  await expect(fileRow('shared-layout-abuja.csv')).toBeVisible()

  // BULK-E2E-01b: the cap refuses out loud. Adding four MORE files on top of the two
  // already selected (2 + 4 = 6) exceeds the five-file cap by exactly one. This second
  // setInputFiles call replaces the <input> element's OWN native FileList, never the
  // component's selection state -- addFiles (lib/importRun.ts) appends the newly
  // dispatched files onto the EXISTING React state, which is exactly the "drop more
  // files to add them" path the dropzone's own copy names, so this is a real exercise
  // of that path, not a shortcut around it.
  await fileInput.setInputFiles([LAYOUT_A_TILL, LAYOUT_B_TERMINAL, PARTIAL_FIRST, PARTIAL_DUPE])
  await expect(
    page.getByText('A run accepts at most 5 files — 1 file was not added.', { exact: true }),
    'the cap names itself and the one dropped file, never a silent truncation',
  ).toBeVisible()
  await expect(fileNames, 'exactly five files kept, in pick order').toHaveText([
    'shared-layout-lagos.csv',
    'shared-layout-abuja.csv',
    'layout-a-till.csv',
    'layout-b-terminal.csv',
    'partial-first.csv',
  ])

  // BULK-E2E-01c: removing one leaves four, in order.
  await fileRow('layout-a-till.csv').getByRole('button', { name: 'Remove' }).click()
  await expect(fileNames, 'four remain, order preserved after one removal').toHaveText([
    'shared-layout-lagos.csv',
    'shared-layout-abuja.csv',
    'layout-b-terminal.csv',
    'partial-first.csv',
  ])

  // Cleanup, not re-asserted beyond the identity check right after: drop the two files
  // that belong to the OTHER two shapes (02/03), leaving exactly the two files 01a
  // already proved were selected -- this test's own shared-layout shape.
  await fileRow('layout-b-terminal.csv').getByRole('button', { name: 'Remove' }).click()
  await fileRow('partial-first.csv').getByRole('button', { name: 'Remove' }).click()
  await expect(fileNames).toHaveText(['shared-layout-lagos.csv', 'shared-layout-abuja.csv'])

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

  // BULK-E2E-01d: a shared layout maps once -- no group pager renders (mappingGroups.ts's
  // own [coverage-sentence-is-unconditional] pairs with CreateMapping's own
  // `groups.length > 1` gate on the pager: this run's ONE group renders NO "GROUP X OF Y"
  // span at all), and the coverage sentence names both files by filename.
  await expect(page.getByText(/^GROUP \d+ OF \d+$/), 'a single-group run renders no group pager').toHaveCount(0)
  await expect(
    page.getByText('This mapping applies to 2 files: shared-layout-lagos.csv and shared-layout-abuja.csv.', { exact: true }),
  ).toBeVisible()

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()

  // Per-file progress (Core AC 2). This card's title/rows are written SYNCHRONOUSLY by
  // startRun()'s own runReducer 'start' dispatch, before the sequential loop's first
  // `await createImport(...)` even begins (App.tsx) -- so it renders on the very next
  // tick after the click, never racing either file's network round trip the way a
  // single small file's transient phase word can (see E2E-04's own note above on why it
  // does not assert the phase word).
  await expect(page.getByText('Importing 2 files', { exact: true })).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText('shared-layout-lagos.csv', { exact: true })).toBeVisible()
  await expect(page.getByText('shared-layout-abuja.csv', { exact: true })).toBeVisible()

  await importResp

  // BULK-E2E-01e: one review, all invoices -- both files' invoices land on ONE screen.
  await expect(page.getByRole('heading', { name: '4 invoices imported' })).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId('review-files-strip')).toBeVisible()
  const stripRows = page.getByTestId('review-files-strip-row')
  await expect(stripRows).toHaveCount(2)
  await expect(stripRows.filter({ hasText: 'shared-layout-lagos.csv' })).toBeVisible()
  await expect(stripRows.filter({ hasText: 'shared-layout-abuja.csv' })).toBeVisible()

  // BULK-E2E-01f: rows trace to their file -- showsSourceFile(batches) fires here
  // (batches.length === 2), so ReviewRow's own review-row-source-file testid names the
  // resolved filename, per row, off the row's OWN import_batch_id.
  const abujaRow = page.getByTestId('review-row').filter({ hasText: 'BULK-ABJ-001' })
  await expect(abujaRow).toBeVisible()
  await expect(abujaRow.getByTestId('review-row-source-file')).toHaveText('shared-layout-abuja.csv')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// BULK-E2E-02 (BULK-E2E-02a/02b): two files with different column layouts.
test('BULK-E2E-02 (Core AC 4): different-layout files map SEPARATELY, one column mapping does not silently cover the other, yet still land on one review screen', async ({
  page,
}) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BULK-01 layouts ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  await page.locator('input[type="file"][accept=".csv,.xlsx"]').setInputFiles([LAYOUT_A_TILL, LAYOUT_B_TERMINAL])

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

  // BULK-E2E-02a, group 1 of 2 -- names ONLY layout-a-till.csv, never the other file.
  await expect(page.getByText('GROUP 1 OF 2', { exact: true })).toBeVisible()
  await expect(page.getByText('This mapping applies to 1 file: layout-a-till.csv.', { exact: true })).toBeVisible()
  await expect(page.getByText('layout-b-terminal.csv', { exact: false }), "the OTHER file's name never leaks onto this group's screen").toHaveCount(0)

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  // Not the last group -- continueMapping only advances groupIndex, no network call and
  // nothing imported yet (App.tsx's continueMapping).
  await page.getByRole('button', { name: 'Continue to next file' }).click()

  // group 2 of 2 -- names ONLY layout-b-terminal.csv now.
  await expect(page.getByText('GROUP 2 OF 2', { exact: true })).toBeVisible()
  await expect(page.getByText('This mapping applies to 1 file: layout-b-terminal.csv.', { exact: true })).toBeVisible()
  await expect(page.getByText('layout-a-till.csv', { exact: false }), "the FIRST file's name is gone from this group's screen").toHaveCount(0)

  // invoice_number is never auto-recognized regardless of header spelling ("the invoice
  // number is never guessed" -- CreateMapping's own copy), so this group needs its own
  // manual placement too, onto ITS OWN header's own column name ("Ref", not "Invoice No").
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Ref', { exact: true }).click()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await importResp

  // BULK-E2E-02b: still one review screen, both files' invoices on it.
  await expect(page.getByRole('heading', { name: '2 invoices imported' })).toBeVisible({ timeout: 60_000 })
  const stripRows = page.getByTestId('review-files-strip-row')
  await expect(stripRows).toHaveCount(2)
  await expect(stripRows.filter({ hasText: 'layout-a-till.csv' })).toBeVisible()
  await expect(stripRows.filter({ hasText: 'layout-b-terminal.csv' })).toBeVisible()
  await expect(page.getByTestId('review-row').filter({ hasText: 'BULK-TILL-001' })).toBeVisible()
  await expect(page.getByTestId('review-row').filter({ hasText: 'BULK-TERM-001' })).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// BULK-E2E-03 (BULK-E2E-03a..03d): the cross-file, against-stored duplicate.
//
// partial-first.csv imports BULK-DUP-001/002 cleanly; partial-dupe.csv's only invoice
// number (BULK-DUP-001) already exists for this run's entity by the time its OWN
// createImport call runs -- [sequential-not-parallel] (App.tsx's startRun never
// Promise.all's the per-file calls) is what makes this reachable at all: a concurrent
// implementation would let both files' ExistingNumbers precheck race the same DB read
// and both would succeed. Verified directly against internal/importer/service.go: this
// second batch still finalizes 'completed' (:923) with ready_invoices/rows_valid 0 --
// the rowsTotal==0 early-'failed' finalize (:782-790) is a DIFFERENT path, for a file
// with literally zero data rows, which partial-dupe.csv (one real data row) never hits.
// reviewShellStateAll (lib/reviewBatch.ts) is 'batch' iff ANY batch is 'completed', so
// partial-first.csv's own completed batch keeps this run on the normal batch surface --
// RejectedRun (ReviewBatch.tsx) is never reached, and partial-dupe.csv's reason
// (`batch.errors`, [reason-comes-from-errors-not-status]) surfaces in
// review-files-strip-row instead. BULK-E2E-03b/03d assert on that reason TEXT below,
// never on a 'failed' status this shape does not emit.
test('BULK-E2E-03 (Core AC 5, [sequential-not-parallel]): a cross-file duplicate quarantines one file while the run keeps its earlier successes, named by reason not status', async ({
  page,
}) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BULK-01 partial ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  await page.locator('input[type="file"][accept=".csv,.xlsx"]').setInputFiles([PARTIAL_FIRST, PARTIAL_DUPE])

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

  // Same shared header on both files -- one group, one Map step (mirrors BULK-E2E-01d).
  await expect(page.getByText(/^GROUP \d+ OF \d+$/)).toHaveCount(0)

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()

  // The FIRST of two sequential createImport calls this click triggers (partial-first.csv,
  // sent before partial-dupe.csv even starts) -- proves the gateway-prefixed URL
  // (Core AC/AC-8); the heading assertion below's generous timeout is what actually
  // waits out BOTH sequential requests, not this single wait.
  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await importResp

  // BULK-E2E-03c: the run continued -- it reaches the ordinary review screen, never
  // RejectedRun (the all-rejected multi-file surface), because partial-first.csv's own
  // batch DID complete.
  await expect(page.getByRole('heading', { name: '2 invoices imported' })).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId('review-rejected-run')).toHaveCount(0)

  // BULK-E2E-03a: partial-first.csv's two invoices are on the review screen -- the
  // successes were kept, not discarded because a LATER file in the same run failed.
  await expect(page.getByTestId('review-row').filter({ hasText: 'BULK-DUP-001' })).toBeVisible()
  await expect(page.getByTestId('review-row').filter({ hasText: 'BULK-DUP-002' })).toBeVisible()

  // BULK-E2E-03b: partial-dupe.csv is named in the files strip with ZERO invoices and
  // the duplicate reason -- read off REASON TEXT, never a 'failed' status the server
  // does not emit for this shape.
  const stripRows = page.getByTestId('review-files-strip-row')
  await expect(stripRows).toHaveCount(2)
  const firstFileRow = stripRows.filter({ hasText: 'partial-first.csv' })
  const dupeFileRow = stripRows.filter({ hasText: 'partial-dupe.csv' })
  await expect(firstFileRow).toContainText('Imported')
  await expect(dupeFileRow).toContainText('Rejected')
  await expect(dupeFileRow, "the server's own reason string, never re-authored").toContainText(
    'An invoice with this number already exists for this entity.',
  )

  // BULK-E2E-03d: the deep link survives -- reloading re-derives both channels from a
  // FRESH GET, never from the run this tab already held (which the reload discards), and
  // partial-dupe.csv's reason is still shown -- it lives in batch.errors, not in `run`.
  const batchRefetch = page.waitForResponse(
    (r) => r.request().method() === 'GET' && /\/api\/invoice\/v1\/imports\/[0-9a-fA-F-]+$/.test(new URL(r.url()).pathname),
  )
  await page.reload()
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
  await batchRefetch

  await expect(page.getByRole('heading', { name: '2 invoices imported' })).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId('review-row').filter({ hasText: 'BULK-DUP-001' })).toBeVisible()
  await expect(page.getByTestId('review-row').filter({ hasText: 'BULK-DUP-002' })).toBeVisible()
  await expect(
    page.getByTestId('review-files-strip-row').filter({ hasText: 'partial-dupe.csv' }),
    'the reason survives the reload -- it was never sourced from `run`',
  ).toContainText('An invoice with this number already exists for this entity.')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// DOC-E2E-01 (Core AC 5): the deployed bundle runs on the document-id contract. The
// preview RESPONSE is the only place a client ever learns a document id, so asserting
// the import request carries THAT id is what ties the two calls together -- a part-name
// check alone would also pass on a hardcoded value. The whole-request assertion is the
// only one in this file that reads a request body; every other spec matches on method
// and path alone.
test('DOC-E2E-01 (Core AC 5): the deployed wizard imports by document_id and never re-sends the file', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `DOC-01 wire ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'doc-wire.csv', mimeType: 'text/csv', buffer: Buffer.from(buildSingleInvoiceCsv(`INV-E2E-DOC-${Date.now()}`), 'utf8') })

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  const previewBody = (await (await previewResp).json()) as { document_id?: string }
  const documentId = previewBody.document_id
  // A uuid, not just a string: typeof '' is 'string', and the toContain(documentId)
  // assertion below is true of EVERY body when the id is empty -- the spec's central
  // claim would pass vacuously. The server itself rejects a non-uuid document_id.
  expect(documentId, 'preview mints and returns the stored document id').toMatch(
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i,
  )

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  // endsWith, never includes(): '/v1/imports' is a PREFIX of '/v1/imports/preview', so
  // a relaxed matcher would resolve both of these on the preview request.
  const importReq = page.waitForRequest(
    (r) => r.method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()

  const body = requestBody(await importReq)
  expect(body, 'the import names a document').toContain('name="document_id"')
  expect(body, "and it is preview's own id, not one this spec invented").toContain(String(documentId))
  expect(body, 'the bytes are never re-sent').not.toContain('name="file"')
  expect((await importResp).status(), 'a real import returns 201').toBe(201)

  // The run reaches its end state, so the console gate below covers the whole journey
  // and not just the moment the import response landed (same fixture and mapping as
  // INVCR-E2E-2 above, which owns this route).
  await expect(page.getByTestId('invoice-detail'), 'the document-id contract completes the run').toBeVisible({ timeout: 30_000 })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
