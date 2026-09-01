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
// NOTE (merged from main, M4-22-03): db/seed.dev.sql now seeds 10 curated
// business_entities into THIS persona's tenant (1111...), where it previously seeded
// zero. Harmless here and deliberately not compensated for: selectEntity() (the
// workspace-switcher helper, [import-upload-unify] -- CreateUpload's own in-page
// entity <select> is gone) matches our own uniquely-named entity by label,
// freshTin()'s pid-seeded range cannot collide with the 10 curated TIN literals
// (non-contiguous: active 10012345-0001..10089012-0008 plus archived
// 10223456-0022/10234567-0023), and listEntities requests ?limit=200
// (frontend/app/src/lib/portfolio.ts:73) against 10+1 rows, so our entity cannot
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
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

import { test, expect, type Locator, type Page, type Request } from '@playwright/test'
import { login, createEntity, listInvoices, approveUntilClosed, firmApproverTokens, PERSONAS, type ExtractionDetail } from '../api/client'
import { ensureFirmPolicyActive } from '../api/contract-helpers'
import { freshTin } from '../api/fixtures'
import { approvalRun404Dropper } from './consoleGate'
import { assertFillsColumn, gaps, overlapOf, rectsOverlap, WIDE_WIDTHS, type Rect } from './layout'
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
  const dropApprovalRun404 = approvalRun404Dropper(page)
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (dropApprovalRun404(msg.text(), msg.location().url)) return
    errors.push(msg.text())
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

// approveOpenRunsForEntity(): both submitting tests below hold only invoice NUMBERS
// (review-row locator text filters), never ids -- this recovers them via the entity_id
// filter (the entity is fresh, so this can only return this run's own rows) and closes
// every open run over the side channel. Filtered to run_state 'open' rather than
// approving every row unconditionally: a kept-as-is row is never validated, so it has no
// run at all, and approveUntilClosed has nothing to read for one.
async function approveOpenRunsForEntity(token: string, entityId: string): Promise<void> {
  const { invoices } = await listInvoices(token, { entity_id: entityId })
  const approverTokens = await firmApproverTokens()
  await Promise.all(
    invoices.filter((inv) => inv.approval?.run_state === 'open').map((inv) => approveUntilClosed(inv.id, approverTokens)),
  )
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

  const fileInput = page.locator('input[type="file"]#pf-import-file')
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
  // task-408/409: the old caption ('...became part of an invoice.') overclaimed for the
  // all-duplicate shape -- replaced with a readability-only claim, true here too.
  await expect(page.getByText('Every row in the file could be read.', { exact: true })).toBeVisible()
  // Newly-owned assertion (task-408): the structural channel's own at-zero paragraph,
  // asserted nowhere in the repo before this.
  await expect(
    page.getByText('This channel stays visible even at zero, so its absence is a fact and not an omission.', { exact: true }),
  ).toBeVisible()
  // BUG-08-02's second channel, also at zero on this clean run -- BUG08-E2E-6 (task-408).
  await expect(page.getByText('0 already imported', { exact: true })).toBeVisible()
  await expect(page.getByText('Nothing in this file was already in your ledger.', { exact: true })).toBeVisible()
  // Both non-default tabs are OMITTED from the DOM at zero, never merely hidden.
  await expect(page.getByRole('button', { name: /^Unreadable rows \(/ })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^Already imported \(/ })).toHaveCount(0)

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
    .locator('input[type="file"]#pf-import-file')
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

  // Newly-owned assertion (task-408): the crossed booleans, direction 1 -- unreadable
  // rows non-zero WHILE already-imported sits at zero on this first import, proving the
  // two not-imported channels are independent rather than one boolean driving both tiles.
  await expect(page.getByText('0 already imported', { exact: true })).toBeVisible()
  await expect(page.getByText('Nothing in this file was already in your ledger.', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Already imported \(/ })).toHaveCount(0)

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
  // render THAT invoice's own content. The live surface is proven by `invoice-detail`
  // plus `status-strip`; "Audit trail" (the retired mock detail's panel title, and the
  // design's name for the same evidence) stays at zero matches because no card here
  // carries that title.
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
  // on whichever entity happened to be active by DEFAULT and how the tenant-wide
  // invoice set paginated -- the api suite alone puts 500 more rows in it before this
  // suite starts (perf.spec.ts), reset or no reset: exactly the CI-caught
  // filter-after-paginate regression this fix closes (the scoped list would render
  // EMPTY whenever the default entity's own invoices fell outside the newest-50
  // tenant-wide window, timing out this very click).
  await page.getByRole('button', { name: 'Finish · go to invoices' }).click()
  await page.getByTestId('invoices-list').getByText('INV-UI-MIX-CLEAN', { exact: true }).click()

  await expect(page.getByTestId('invoice-detail')).toBeVisible()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('INV-UI-MIX-CLEAN')
  await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('INV-UI-MIX-VIOLATE')
  await expect(page.getByTestId('status-strip')).toBeVisible()
  await expect(page.getByText('Audit trail', { exact: true })).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// BUG08-E2E-1/2/3/4/5/7 (task-408, FINAL subtask of BUG-08; folds task-409's caption fix
// as its deployed oracle): re-importing the SAME mixed fixture into the SAME entity
// forces the review screen to populate BOTH not-imported channels at once -- the shape
// neither E2E-04 (unreadable > 0 alone) nor BULK-E2E-03 (already-imported > 0 alone) can
// produce by itself. Verified against internal/importer/service.go's classify loop
// (headerConflictField checked BEFORE the against-store `existing[num]` lookup,
// :643-681): run 2 re-quarantines INV-UI-MIX-STRUCT structurally (unreadable) and
// rejects INV-UI-MIX-VIOLATE/CLEAN as store-duplicates (already-imported) -- rows_total
// 4, rows_valid 0, status 'completed'. No new fixture -- buildMixedCsv() imported twice
// is enough.
//
// ONE entity for both runs -- no-duplicate-invoice-number is scoped per entity, so a
// second entity would never collide and run 2 would just store two more invoices
// instead of colliding with run 1's.
//
// All labelled blocks below share ONE run-2 screen, this file's own idiom for a
// multi-assertion test (E2E-01/02/03/06/07, E2E-04/09) rather than six tests each
// re-running the wizard twice. The click-through (BUG08-E2E-3) is deliberately LAST: it
// navigates the SPA to `view:'detail'` with no URL to come back to (there is no
// invoice-detail route in this app -- ReviewAlreadyImportedTab.tsx:38-39), so every
// review-screen assertion runs before it, never after.
test('BUG08-E2E-1/2/3/4/5/7 (AC-1..6, task-408/409): a re-import splits genuine parse failures from already-imported rows', async ({
  page,
}) => {
  // The config default is 60s and this drives the full wizard TWICE.
  test.setTimeout(180_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BUG08-05 UI Mixed ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  // Run 1: stores INV-UI-MIX-VIOLATE and INV-UI-MIX-CLEAN, quarantines
  // INV-UI-MIX-STRUCT structurally -- E2E-04 already proves this split in detail; this
  // run exists only to seed the store for run 2's collisions.
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: 'ui-mixed.csv', mimeType: 'text/csv', buffer: Buffer.from(buildMixedCsv(), 'utf8') })

  const preview1 = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await preview1

  // subtotal has no ALIAS entry (lib/mapping.ts) so it must be click-mapped by hand,
  // same as E2E-04 -- see buildMixedCsv's own doc comment for why this is load-bearing.
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const import1 = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await import1
  // Count-agnostic arrival, E2E-04's own idiom (:441) -- run 1 only needs to have
  // settled, its own content is not re-asserted here.
  await expect(page.getByRole('button', { name: /^Invoices \(\d+\)$/ })).toBeVisible({ timeout: 60_000 })

  await page.getByRole('button', { name: 'Finish · go to invoices' }).click()

  // Run 2: the identical bytes, into the SAME entity. resetImport() (App.tsx, wired
  // through openCreate(), the "New invoice" handler) wipes files/mapping/run state, so
  // this is a genuinely fresh wizard pass, not a resubmission of run 1's in-memory state.
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: 'ui-mixed-2.csv', mimeType: 'text/csv', buffer: Buffer.from(buildMixedCsv(), 'utf8') })

  const preview2 = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await preview2

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const import2 = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await import2

  // Arrival signal for run 2 -- MUST be count-agnostic. Run 2 creates zero new
  // invoices, so the heading reads '0 invoices imported'; the positive-count wait every
  // sibling test in this file uses (`getByRole('heading', {name:'<N> invoices imported'})`
  // with N>=1) would hang for the full timeout here.
  await expect(page.getByRole('button', { name: /^Invoices \(\d+\)$/ })).toBeVisible({ timeout: 60_000 })

  const structuralMsg = 'rows disagree on issue_date'
  const unreadableTab = page.getByRole('button', { name: 'Unreadable rows (2)' })
  const alreadyImportedTabBtn = page.getByRole('button', { name: 'Already imported (2)' })

  // BUG08-E2E-1 (AC-6): both not-imported channels populated at once -- both tab
  // buttons present, with the run-2 counts the classify loop above predicts.
  await expect(unreadableTab, 'INV-UI-MIX-STRUCT must re-quarantine structurally on the re-import').toBeVisible()
  await expect(alreadyImportedTabBtn, 'INV-UI-MIX-VIOLATE/CLEAN must reject as store-duplicates').toBeVisible()

  await unreadableTab.click()
  // AC-6 spec #2: the unreadable tab shows ONLY the structural message, shipped wording
  // unchanged -- STRUCT's two rows (sheet rows 2 and 3) both disagree on issue_date,
  // same strict-mode-safe double match as E2E-04's leg 2.
  await expect(page.getByText(structuralMsg)).toHaveCount(2)

  const alreadyImportedTab = page.getByTestId('review-already-imported-tab')
  await alreadyImportedTabBtn.click()
  await expect(alreadyImportedTab).toBeVisible()
  // Only one tab body mounts at a time -- the structural message has no home here.
  await expect(page.getByText(structuralMsg)).toHaveCount(0)

  // AC-3 spec #3 (CORRECTED round 4, C2): the tab renders File / Row / View invoice and
  // NEVER an invoice number (ReviewAlreadyImportedTab.tsx:52-56) -- assert the SHEET
  // rows instead. Row order is deterministic (service.go's `order` is row-scan order):
  // VIOLATE (sheet row 4) first, then CLEAN (sheet row 5).
  await expect(alreadyImportedTab.getByText('4', { exact: true })).toBeVisible()
  await expect(alreadyImportedTab.getByText('5', { exact: true })).toBeVisible()
  const viewInvoiceBtns = alreadyImportedTab.getByRole('button', { name: 'View invoice' })
  await expect(viewInvoiceBtns).toHaveCount(2)
  await expect(viewInvoiceBtns.nth(0)).toBeEnabled()
  await expect(viewInvoiceBtns.nth(1)).toBeEnabled()

  // BUG08-E2E-2 (AC-2, CORRECTED round 4 C1): scoped to the TAB BODY, not the page --
  // the always-rendered header card on this very run-2 screen legitimately carries
  // 'No invoice exists for them.' and '...no rule was ever run. Nothing was stored.' A
  // page-wide check for these phrases fails on a CORRECT build. Mirrors
  // ReviewAlreadyImportedTab.test.tsx:63's own scoped, case-sensitive phrase list.
  const tabText = await alreadyImportedTab.innerText()
  for (const phrase of ['could not read', 'no rule was ever run', 'correct the rows', 'unreadable']) {
    expect(tabText, `already-imported tab body must not contain "${phrase}"`).not.toContain(phrase)
  }

  // BUG08-E2E-4 (AC-4): the header and the channel tiles reconcile -- summed off the
  // rendered numbers, not four independent literals, so a miscounted channel fails.
  // Tab-independent: the tiles/header render above the tab switch (§7.1).
  const bodyText = (await page.locator('body').textContent()) ?? ''
  const rowsRead = Number(/(\d+) ROWS READ/.exec(bodyText)?.[1])
  const builtFrom = Number(/Built from (\d+) rows/.exec(bodyText)?.[1])
  const alreadyImportedCount = Number(/(\d+) already imported/.exec(bodyText)?.[1])
  const unreadableCount = Number(/(\d+) unreadable rows/.exec(bodyText)?.[1])
  for (const n of [rowsRead, builtFrom, alreadyImportedCount, unreadableCount]) {
    expect(
      Number.isNaN(n),
      `expected all four numbers to parse off the page, got: rowsRead=${rowsRead} builtFrom=${builtFrom} alreadyImported=${alreadyImportedCount} unreadable=${unreadableCount}`,
    ).toBe(false)
  }
  expect([rowsRead, builtFrom, alreadyImportedCount, unreadableCount], 'built + already-imported + unreadable must equal rows read (0 + 2 + 2 = 4)').toEqual([
    4, 0, 2, 2,
  ])

  // BUG08-E2E-5 (AC-3): the non-zero already-imported caption, exact string, wired to a
  // real count. The noun-swap discrimination this literal string cannot reach on its own
  // (buildMixedCsv gives VIOLATE/CLEAN one row each, so rows and invoices are both 2)
  // belongs to BUG08-BATCH-8 (reviewBatch.test.ts), mutation-verified there.
  await expect(page.getByText('2 invoices already in your ledger. Nothing to fix.', { exact: true })).toBeVisible()

  // BUG08-E2E-7 (AC-6, AC-3): the unreadable tile's shipped non-zero caption survives
  // verbatim and is NOW TRUE alongside a non-zero already-imported tile -- proving the
  // sentence describes only the structural rows ([structural-untouched]). Plus the
  // newly-owned structural paragraph, asserted nowhere in the repo before this.
  await expect(page.getByText('No invoice exists for them.', { exact: true })).toBeVisible()
  await expect(
    page.getByText('A structural failure, not a compliance one: no rule was ever run. Nothing was stored.', { exact: true }),
  ).toBeVisible()

  // BUG08-E2E-3 (AC-5), LAST: the first row's View invoice fires the wire call and lands
  // on the invoice it collided with -- one of run 1's two stored invoice numbers, never
  // INV-UI-MIX-STRUCT (never stored at all). The id is UUID-shaped on the WIRE -- the
  // SPA has no invoice-detail URL, so the response path is the only observable.
  const invoiceGet = page.waitForResponse(
    (r) => r.request().method() === 'GET' && /\/api\/invoice\/v1\/invoices\/[0-9a-fA-F-]{36}$/.test(new URL(r.url()).pathname),
  )
  await viewInvoiceBtns.nth(0).click()
  await invoiceGet
  await expect(page.getByTestId('invoice-detail')).toBeVisible()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(/^INV-UI-MIX-(VIOLATE|CLEAN)$/)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// E2E-10 (FLOW-07, [wizard-steps-split], INVCR-01-04/task-280): the header path
// resolver, re-anchored again. `Build`/`Validate`/`Approve`/`Report` are retired by
// this subtask -- the typed path is the 1-item `Enter` strip and the import path is
// `Import · Map · Review`, sharing no label between them. EXTR-09-06 added a third,
// the document strip `Import · Review`, which DOES share the import path's labels;
// EXTR09-E2E-05 owns it. Manual entry ('Skip — enter manually' -> skipUpload ->
// createStep 'form') is still the only way to reach a TYPED_ONLY_STEPS member (the
// name DOCUMENT_ONLY_STEPS carried until EXTR-09-06 renamed it, since "document"
// now means a real document). No SANDBOX toggle: with the mock document card gone,
// LIVE and SANDBOX render an identical first step.
//
// [closes-d-04a-typed-review-residual]: the stageIndex-0-vs-2 ambiguity a 2-item
// strip used to have (index 0 and the retired index 2 rendering the same two
// labels) is structurally gone at one item -- a label-presence check alone now
// proves STAGE_OF.form === 0, so leg 2's color comparison is retired, not replaced.
// Verified no exact-text collision for Import/Map/Enter anywhere else on this
// screen: CreateUpload's card title is 'Import invoices · X', CreateMapping's are
// 'Map fields to columns · X' / 'Import N rows' / 'Map invoice number to continue' --
// none is an exact match for the bare word -- and ConnectorDetail.tsx's own 'Review'
// is a different, unmounted view (Review itself stays checked by leg 1's own
// assertions above, on the import path).
//
// E2E-08 is deliberately NOT replaced -- its subject (the sample-PDF parse -> form ->
// validate -> results run) is deleted by an earlier subtask, so nothing is left to guard.
test('E2E-10 (FLOW-07, [wizard-steps-split]): the wizard header resolves the 3-step import path on entry and the 1-step typed path once manual entry is chosen', async ({
  page,
}) => {
  const errors = collectErrors(page)

  await signInFirm(page)
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  // Leg 1 -- createStep 'upload' is NOT in TYPED_ONLY_STEPS (EXTR-09-06), so IMPORT_STEPS
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

  // Leg 2 -- 'form' IS in TYPED_ONLY_STEPS, so wizardHeader must return ENTER_STEPS
  // (Enter) at STAGE_OF.form === 0, and the import-only 'Import'/'Map'/'Review'
  // labels must disappear. EXTR-09-06 moved the typed strip off WIZARD_STEPS, which
  // is the two-item document strip now; this leg is STEPS-D8 (manual entry still
  // renders exactly one step).
  await page.getByRole('button', { name: 'Skip — enter manually' }).click()
  await expect(page.getByText('Enter', { exact: true }), '1-step ENTER_STEPS strip expected on manual entry (EXTR-09-06)').toBeVisible()
  await expect(page.getByText('Review', { exact: true })).toHaveCount(0)
  await expect(page.getByText('Import', { exact: true })).toHaveCount(0)
  await expect(page.getByText('Map', { exact: true })).toHaveCount(0)

  // [e2e-10-colour-proof-is-obsolete-not-dropped]: the color comparison this used to
  // do (Enter lit vs Review muted) is obsolete now that Review isn't rendered at all
  // on the typed path -- the toHaveCount(0) above already proves it structurally.

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
    .locator('input[type="file"]#pf-import-file')
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
  // defaultDraft), so a second create under it would 409 on
  // (tenant_id, entity_id, invoice_number) -- which a Playwright retry of this very test
  // does, against the invoice its own first attempt already filed.
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
//
// AC-6/AC-7: this file had no test.describe/beforeAll/module-scope-token infrastructure
// before this subtask -- scoped to just this submitting test rather than the whole file,
// so unrelated tests below (INVCR-E2E-2, BULK-E2E-*, ...) stay untouched by it. Self-heal
// (D3 protocol, ../api/validation.spec.ts:14-26), unwrapped: the api run ahead of this one
// leaves the firm tenant's active slot empty (contract-invoice.spec.ts's own armedInvoice
// cleanup), and [topology-never-publishes] stays satisfied -- this restores the tenant's
// OWN seeded policy, never a new one (docs/e2e-convention.md).
test.describe('INVCR-E2E-1 governs the firm tenant before submitting', () => {
  test.beforeAll(async () => {
    const token = await login(PERSONAS.A)
    await ensureFirmPolicyActive(token)
  })

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
    .locator('input[type="file"]#pf-import-file')
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

  // AC-2/[selectability]: CLEAN armed its run at import, VIOLATE only just now (the
  // revalidate above) -- this test holds only invoice NUMBERS, never ids, so recover both
  // and close their runs before select-all, or isRowSelectable leaves them disabled. The
  // rail-pill toggle below re-fetches the table server-side ([filters-are-server-side],
  // this file's own header), so no separate wait is needed to see the approvals land.
  await approveOpenRunsForEntity(token, entity.id)

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
})

// AC-2: Core AC 8's N=1 route, proven on the deployed build for the first time -- a
// single-invoice CSV lands directly on the real InvoiceDetail, asserted by PRESENCE of
// its own data-testid and status-strip, never by the review table's absence alone
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
    .locator('input[type="file"]#pf-import-file')
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
  await expect(page.getByTestId('status-strip'), 'the real detail carries the state strip').toBeVisible()
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
    .locator('input[type="file"]#pf-import-file')
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
    .locator('input[type="file"]#pf-import-file')
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
//
// AC-6/AC-7: own describe + beforeAll, same reasoning as INVCR-E2E-1's above -- scoped to
// just this submitting test, self-heals independently rather than sharing that block's.
test.describe('INVCR-E2E-7 governs the firm tenant before submitting', () => {
  test.beforeAll(async () => {
    const token = await login(PERSONAS.A)
    await ensureFirmPolicyActive(token)
  })

test('INVCR-E2E-7 kept-as-is drops out of Needs a fix and stays present-but-disabled, never absent', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `INVCR-01-16 keep ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
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

  // AC-2/[selectability]: CLEAN armed its run at import (auto-promoted to validated, zero
  // violations); VIOLATE never validates at all (kept stays non-validated), so it has no
  // run to close. Close CLEAN's before select-all below, or select-all picks up nothing.
  // The two filter-pill toggles below already re-fetch the table server-side.
  await approveOpenRunsForEntity(token, entity.id)

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

  const fileInput = page.locator('input[type="file"]#pf-import-file')
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

  await page.locator('input[type="file"]#pf-import-file').setInputFiles([LAYOUT_A_TILL, LAYOUT_B_TERMINAL])

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
// Promise.all's the per-file SPREADSHEET calls) is what makes this reachable at all: a
// concurrent implementation would let both files' ExistingNumbers precheck race the same
// DB read and both would succeed. EXTR-09-07's startDocumentRun IS concurrent, and may
// be: ImportDocument never runs that precheck. Verified directly against internal/importer/service.go: this
// second batch still finalizes 'completed' (:923) with ready_invoices/rows_valid 0 --
// the rowsTotal==0 early-'failed' finalize (:782-790) is a DIFFERENT path, for a file
// with literally zero data rows, which partial-dupe.csv (one real data row) never hits.
// reviewShellStateAll (lib/reviewBatch.ts) is 'batch' iff ANY batch is 'completed', so
// partial-first.csv's own completed batch keeps this run on the normal batch surface --
// RejectedRun (ReviewBatch.tsx) is never reached, and partial-dupe.csv's reason
// (`batch.errors`, [reason-comes-from-errors-not-status]) surfaces in
// review-files-strip-row instead. BULK-E2E-03b/03d assert on that reason TEXT below,
// never on a 'failed' status this shape does not emit.
test('BULK-E2E-03 (Core AC 5, [sequential-not-parallel], spreadsheet path): a cross-file duplicate quarantines one file while the run keeps its earlier successes, named by reason not status', async ({
  page,
}) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BULK-01 partial ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  await page.locator('input[type="file"]#pf-import-file').setInputFiles([PARTIAL_FIRST, PARTIAL_DUPE])

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

  // Newly-owned assertion (task-408): the crossed booleans, direction 2 -- run-wide
  // unreadable sits at zero WHILE already-imported is non-zero (partial-dupe.csv's one
  // collision). This is the ONLY shipped e2e rendering the exact shape task-409's
  // caption bug lived in, so it doubles as the deployed oracle for the at-zero caption
  // fix: 'Every row in the file could be read.' must be TRUE here, not just present.
  // Deliberately NOT asserting '1 invoices already in your ledger.' -- a pre-existing
  // singular/plural wart (flagged, not fixed here); BUG08-E2E-5 pins the N=2 form.
  await expect(page.getByText('0 unreadable rows', { exact: true })).toBeVisible()
  await expect(page.getByText('Every row in the file could be read.', { exact: true })).toBeVisible()
  await expect(page.getByText('1 already imported', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Already imported \(\d+\)$/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Unreadable rows \(/ })).toHaveCount(0)

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
    .locator('input[type="file"]#pf-import-file')
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

// --- EXTR-09 · the document fork, and the picker's geometry ---------------------------
//
// A browser is the only honest oracle for the fork: the unit suite pins the pure reducer,
// and nothing at node level proves App.tsx computes `runKind` from the real selection.
// EXTR09-E2E-03 is the load-bearing one — with no run kind there is nothing for an
// incoming file to contradict, so the refusal never fires.
//
// Outcomes are NOT asserted here. internal/extraction/mock.go stamps MOCK-INV-0001 on every
// fixture -- `clean-invoice` and `mockDefaultResult` alike, held there by
// TestMockExtractor_InvoiceNumberIsUnchangedAndClean -- so two documents in one run collide
// by design;
// e2e/api/contract-document-upload.spec.ts owns the outcome assertions and handles that
// collision. QA disposition A-2: Core AC #9's deploy-gate evidence is collision-recovery
// only — independent parallel success awaits a non-mock extractor (EXTR-17).
//
// AC-1 AMENDMENT: the story says a PDF reaches "a review landing naming one batch". The
// untouched routers say otherwise at ONE file — routeAfterRun sees files.length === 1 and
// routeAfterImport resolves {kind:'single'}, because DOCUP-02/03 pin a clean document
// import at status 'completed' / ready_invoices 1 and reviewQuery(batchId,'all') lists the
// draft it wrote. The landing is the real InvoiceDetail, the route INVCR-E2E-2 already
// proves for a single-invoice CSV.

const DOCUMENT_FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '../fixtures/documents')
// Committed by EXTR-09-03; shared with e2e/api/contract-document-upload.spec.ts.
const NATIVE_INVOICE_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'native_invoice.pdf'))

// Fresh bytes per pick, contract-document-upload.spec.ts's own recipe: a trailing PDF
// comment moves the content hash without moving a byte offset, so `startxref` still
// resolves. Without it, per-tenant dedupe reuses an earlier row and the PERMANENT
// per-document enqueue key skips the enqueue — the poll would then settle on a PREVIOUS
// run's job and stay green while extraction is broken.
function uniquePdfBytes(): Buffer {
  return Buffer.concat([NATIVE_INVOICE_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// Committed by EXTR-11-09. Hand-written on internal/extraction/fixtures_test.go's own recipe
// (fxAssemble :96-118 lays the objects down and computes every xref offset from their real byte
// positions; fxPage / fxStream / fxText) -- two US-Letter text pages sharing one font:
// `/Kids [3 0 R 5 0 R] /Count 2`, two `/Type /Page` objects. Verified through the SAME
// go-pdfium the worker renders with, before commit: 2 pages, both with text, each 1275x1651 at
// 150 DPI -- identical to the one-pager's grid. NATIVE_INVOICE_PDF is `/Count 1`, so without
// this file every gate run renders exactly one frame and AC-5's canvas half has no subject.
const NATIVE_INVOICE_2P_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'native_invoice_2p.pdf'))

// uniquePdfBytes()'s recipe, same reason, same trailing-comment trick -- and re-verified
// through pdfium WITH the comment appended, because a byte past %%EOF that moved the xref
// would open in some readers and fail in pdfium.
function uniqueTwoPagePdfBytes(): Buffer {
  return Buffer.concat([NATIVE_INVOICE_2P_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// A file named *.pdf whose bytes are NOT a PDF: classification is extension-only and
// upload only hashes+PUTs bytes (classify.go / service.go), so this sails through
// selection and upload, then fails pdfium.OpenDocument on every one of River's 3
// attempts (worker.go) and dead-letters deterministically. Fresh bytes per call, same
// permanent-enqueue-key reason as uniquePdfBytes().
function uniqueGarbageBytes(): Buffer {
  return Buffer.concat([Buffer.from('not a pdf at all'), Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// The step strip carries no testid, so it is anchored on the 'Import' chip label E2E-10
// above established is unique here: label span -> chip div -> strip, one chip per direct
// child div. Self-checking — a wrong ancestor resolves zero chips.
function stepChips(page: Page): Locator {
  return page.getByText('Import', { exact: true }).locator('xpath=../../..').locator('xpath=./div')
}

// A dispatched drop, not a mouse drag ([import-upload-unify] above records why). This is
// the path that matters: onDrop hands dataTransfer.files straight to addPickedFiles, so
// `accept` never sees them and classifyPickedFile is the only gate. `bytes` sizes the file
// for the oversize note; nothing dropped is ever uploaded.
async function dropFiles(page: Page, specs: { name: string; type: string; bytes?: number }[]): Promise<void> {
  await page.evaluate((list) => {
    const label = document.querySelector('label[for="pf-import-file"]')
    if (!label) throw new Error('dropzone label[for="pf-import-file"] not found')
    const dt = new DataTransfer()
    for (const spec of list) {
      const body: BlobPart[] = spec.bytes ? [new Uint8Array(spec.bytes)] : [`e2e ${spec.name}`]
      dt.items.add(new File(body, spec.name, { type: spec.type }))
    }
    label.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt }))
  }, specs)
}

// The CONTENT box, never the bounding box: children sit inside the card's own gutter, so
// a border-box comparison passes a row that overflows it. scrollWidth/clientWidth ride
// along because a stretched flex item keeps its box while its TEXT overflows — and this
// story grew the accepted-types line from three tokens to eight.
function edgesOf(el: HTMLElement) {
  const r = el.getBoundingClientRect()
  const cs = getComputedStyle(el)
  return {
    left: r.left + parseFloat(cs.borderLeftWidth) + parseFloat(cs.paddingLeft),
    right: r.right - parseFloat(cs.borderRightWidth) - parseFloat(cs.paddingRight),
    outerLeft: r.left,
    outerRight: r.right,
    scrollWidth: el.scrollWidth,
    clientWidth: el.clientWidth,
  }
}

// Two consecutive AGREEING reads, never one: a boundingBox taken as a panel opens measures
// the transform mid-flight, and two different values on two reads is the tell.
async function settledRead<T>(read: () => Promise<T>, label: string): Promise<T> {
  let previous = ''
  await expect
    .poll(
      async () => {
        const key = JSON.stringify(await read())
        const stable = key === previous
        previous = key
        return stable
      },
      { message: `${label}: geometry never settled across two consecutive reads`, timeout: 15_000 },
    )
    .toBe(true)
  return read()
}

// The accepted-types line verbatim — the eight tokens this story grew it to.
const ACCEPTED_LINE = 'ACCEPTED · CSV · XLSX · PDF · PNG · JPG · JPEG · WEBP · DOCX'

// kindRefusal() verbatim (lib/importRun.ts owns the copy), both directions — a run's kind
// is whichever file landed first.
const REFUSE_DOCUMENT_IN_SPREADSHEET_RUN =
  'A run holds one kind of file: this is a spreadsheet run, so the document was not added. Remove the spreadsheet files first, or start a separate document run.'
const REFUSE_SPREADSHEET_IN_DOCUMENT_RUN =
  'A run holds one kind of file: this is a document run, so the spreadsheet was not added. Remove the document files first, or start a separate spreadsheet run.'

test('EXTR09-E2E-01 (AC-1/AC-5): a PDF forks to the document path, extracts, and lands on a real invoice', async ({ page }) => {
  // Upload + extraction poll (LIVE_POLL_MS interval, 120s budget) + import, on a fleet
  // that may be cold. Well above the api suite's own 180s for the same three calls.
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  // The spreadsheet path must never be touched. Counted rather than asserted at one
  // moment: a single preview would prove the fork is not live, whenever it fired.
  const previewCalls: string[] = []
  page.on('request', (req) => {
    if (req.method() === 'POST' && new URL(req.url()).pathname.endsWith('/api/invoice/v1/imports/preview')) {
      previewCalls.push(req.url())
    }
  })

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `EXTR-09-08 fork ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: 'native_invoice.pdf', mimeType: 'application/pdf', buffer: uniquePdfBytes() })

  // The fork, at selection time — this is what an always-null runKind fails. The strip is
  // the document strip, the Map chip it never visits is absent, and the primary names the
  // action it will actually perform.
  await expect(stepChips(page), 'a document run has two steps, not three').toHaveText([/^1\s*Import$/, /^2\s*Review$/])
  await expect(page.getByText('Map', { exact: true }), 'documents are never mapped').toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Extract invoices' }), 'the primary IS the commit on a document run').toBeEnabled()
  await expect(page.getByRole('button', { name: 'Read columns' }), 'the spreadsheet primary is gone').toHaveCount(0)

  const uploadResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/submission/v1/documents'),
    { timeout: 120_000 },
  )
  // endsWith, never includes(): '/v1/imports' is a PREFIX of '/v1/imports/document'.
  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/document'),
    { timeout: 240_000 },
  )
  await page.getByRole('button', { name: 'Extract invoices' }).click()

  // Asserted BEFORE either response is awaited: startDocumentRun dispatches the reducer's
  // 'start' and setCreateStep('documents') synchronously in the click handler, so this
  // card is on screen before any request settles. Awaiting the upload first would race a
  // fast run past its own progress card.
  await expect(page.getByText('Importing 1 file', { exact: true }), 'the run started').toBeVisible({ timeout: 30_000 })
  await expect(stepChips(page), 'the strip stays the document strip while the run is live').toHaveText([
    /^1\s*Import$/,
    /^2\s*Review$/,
  ])

  expect((await uploadResp).status(), 'the document upload returns 201').toBe(201)
  expect((await importResp).status(), 'a settled document imports 201').toBe(201)

  // The landing, named rather than waited on: a wrong landing fails saying WHICH one
  // appeared. See the AC-1 amendment in this section's header for why this is the detail
  // page and not the review shell.
  await expect
    .poll(
      async () => {
        if (await page.getByTestId('invoice-detail').isVisible()) return 'invoice detail'
        if (await page.getByText(/^BATCH /).first().isVisible()) return 'review batch surface'
        if (await page.getByRole('button', { name: 'Extract invoices' }).isVisible()) return 'back on the picker (the run failed)'
        return 'nothing yet'
      },
      { message: 'the document run must land on the real invoice detail (routeAfterRun single)', timeout: 60_000 },
    )
    .toBe('invoice detail')
  await expect(page.getByTestId('status-strip'), 'the real detail carries the state strip').toBeVisible()
  await expect(page.getByTestId('review-table'), 'never a one-row review grid').toHaveCount(0)

  expect(previewCalls, `the document path must never call the spreadsheet preview:\n${previewCalls.join('\n')}`).toEqual([])
  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR09-E2E-02 (AC-2): the spreadsheet journey is unchanged end to end', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  // The document route must stay untouched on this path, the mirror image of E2E-01's
  // preview counter.
  const documentCalls: string[] = []
  page.on('request', (req) => {
    const path = new URL(req.url()).pathname
    if (req.method() === 'POST' && (path.endsWith('/api/submission/v1/documents') || path.endsWith('/api/invoice/v1/imports/document'))) {
      documentCalls.push(req.url())
    }
  })

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `EXTR-09-08 sheet ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  const invoiceNumber = `INV-E2E-EXTR0908-${Date.now()}`
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: 'unchanged.csv', mimeType: 'text/csv', buffer: Buffer.from(buildSingleInvoiceCsv(invoiceNumber), 'utf8') })

  // The widened picker did not move the spreadsheet fork: three chips, Map among them.
  await expect(stepChips(page), 'a spreadsheet run still has three steps').toHaveText([
    /^1\s*Import$/,
    /^2\s*Map$/,
    /^3\s*Review$/,
  ])
  await expect(page.getByRole('button', { name: 'Extract invoices' }), 'the document primary is absent').toHaveCount(0)

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
  expect((await importResp).status(), 'the shipped spreadsheet import still returns 201').toBe(201)

  await expect(page.getByTestId('invoice-detail'), 'N=1 still routes to the real detail').toBeVisible({ timeout: 60_000 })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(invoiceNumber)

  expect(documentCalls, `the spreadsheet path must never call the document route:\n${documentCalls.join('\n')}`).toEqual([])
  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// AC-3, the load-bearing spec. Four arms: both refusal directions, and both selection
// paths — the dropped file never meets `accept`, so a stale predicate survives there
// unnoticed (CreateUpload.tsx:123-127).
//
// Every arm asserts NO refusal is on screen immediately before the one it expects.
// `filesRefusal` is sticky (removeFile does not clear it) and both arms of a direction
// render the SAME sentence, so without that the second arm would pass on the first arm's
// leftover text. The row COUNT is the claim that cannot go stale: an always-null runKind
// adds the contradicting file, so two rows appear where one is required.
test('EXTR09-E2E-03 (AC-3): a mixed selection is refused in the browser, picked or dropped', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  // A fixture entity, selected before the wizard opens: `Extract invoices` is the document
  // run's COMMIT, so it gates on the resolved entity ([gate-on-the-resolved-entity]) and
  // arm C's enabled-primary claim would otherwise depend on whichever client the switcher
  // happened to open on. Nothing is imported here, so the entity stays empty.
  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `EXTR-09-08 refusal ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await expect(page.locator('label[for="pf-import-file"]'), 'dropzone renders').toBeVisible({ timeout: 30_000 })

  const fileInput = page.locator('input[type="file"]#pf-import-file')
  const fileNames = page.locator('ul li div.mono')
  const documentRefusal = page.getByText(REFUSE_DOCUMENT_IN_SPREADSHEET_RUN, { exact: true })
  const spreadsheetRefusal = page.getByText(REFUSE_SPREADSHEET_IN_DOCUMENT_RUN, { exact: true })
  const csv = (name: string) => ({ name, mimeType: 'text/csv', buffer: Buffer.from('invoice_number,subtotal\nMIX-1,100\n', 'utf8') })
  const pdf = (name: string) => ({ name, mimeType: 'application/pdf', buffer: Buffer.from('%PDF-1.4 e2e refusal probe\n', 'utf8') })

  // --- Arm A: spreadsheet run, PDF PICKED ---
  await fileInput.setInputFiles([csv('mix-a.csv')])
  await expect(fileNames, 'the run starts as one spreadsheet').toHaveText(['mix-a.csv'])
  await expect(documentRefusal, 'no refusal yet — the next assertion must not pass on stale text').toHaveCount(0)

  await fileInput.setInputFiles([pdf('mix-b.pdf')])
  await expect(documentRefusal, 'the refusal names BOTH kinds and which one was dropped').toBeVisible()
  await expect(fileNames, 'the CSV stays selected and the PDF was NOT added').toHaveText(['mix-a.csv'])
  await expect(page.getByRole('button', { name: 'Read columns' }), 'the spreadsheet run is still armed').toBeEnabled()
  await expect(stepChips(page), 'and it is still the spreadsheet strip').toHaveText([/^1\s*Import$/, /^2\s*Map$/, /^3\s*Review$/])

  // --- Arm B: spreadsheet run, PDF DROPPED (never meets `accept`) ---
  await fileInput.setInputFiles([csv('mix-c.csv')])
  await expect(fileNames, 'a second spreadsheet is accepted, which clears the refusal').toHaveText(['mix-a.csv', 'mix-c.csv'])
  await expect(documentRefusal, 'refusal cleared before the drop arm').toHaveCount(0)

  await dropFiles(page, [{ name: 'mix-d.pdf', type: 'application/pdf' }])
  await expect(documentRefusal, 'a DROPPED document is refused by classifyPickedFile, not by accept').toBeVisible()
  await expect(fileNames, 'both spreadsheets stay, the dropped PDF was not added').toHaveText(['mix-a.csv', 'mix-c.csv'])

  // --- Arm C: document run, CSV PICKED ---
  await page.locator('ul li').filter({ hasText: 'mix-a.csv' }).getByRole('button', { name: 'Remove' }).click()
  await page.locator('ul li').filter({ hasText: 'mix-c.csv' }).getByRole('button', { name: 'Remove' }).click()
  await expect(fileNames, 'the selection is empty, so the run has no kind again').toHaveCount(0)

  await fileInput.setInputFiles([pdf('mix-e.pdf')])
  await expect(fileNames, 'the run starts as one document').toHaveText(['mix-e.pdf'])
  // An accepted pick sets refusal to null, which also clears Arm B's leftover sentence.
  await expect(documentRefusal, "arm B's refusal is gone").toHaveCount(0)
  await expect(spreadsheetRefusal, 'and the opposite one has never fired').toHaveCount(0)

  await fileInput.setInputFiles([csv('mix-f.csv')])
  await expect(spreadsheetRefusal, 'the refusal reads the other way round, naming both kinds').toBeVisible()
  await expect(fileNames, 'the PDF stays selected and the CSV was NOT added').toHaveText(['mix-e.pdf'])
  await expect(page.getByRole('button', { name: 'Extract invoices' }), 'the document run is still armed').toBeEnabled()

  // --- Arm D: document run, CSV DROPPED ---
  await fileInput.setInputFiles([pdf('mix-g.pdf')])
  await expect(fileNames, 'a second document is accepted, which clears the refusal').toHaveText(['mix-e.pdf', 'mix-g.pdf'])
  await expect(spreadsheetRefusal, 'refusal cleared before the drop arm').toHaveCount(0)

  await dropFiles(page, [{ name: 'mix-h.csv', type: 'text/csv' }])
  await expect(spreadsheetRefusal, 'a DROPPED spreadsheet is refused on a document run').toBeVisible()
  await expect(fileNames, 'both documents stay, the dropped CSV was not added').toHaveText(['mix-e.pdf', 'mix-g.pdf'])

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// AC-4. The picker card's content changed twice in this story — the accepted-types line
// grew from three tokens to eight, and a chosen-file row may now carry a second note — so
// this sweeps the wide end where an unguarded cap has room to strand a band
// (topology/layout.ts's own header records BUG-03-05, where `width <= 1082` passed on
// 588px of dead space). Nothing here asserts a raw dimension: every claim is a
// relationship between two measured boxes, or an element against its own scroll width.
test('EXTR09-E2E-04 (AC-4): the picker card fits and stays centred at every width', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  await signInFirm(page)
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await expect(page.locator('label[for="pf-import-file"]'), 'dropzone renders').toBeVisible({ timeout: 30_000 })

  // One drop, three files, so a single addFiles call sets the run kind from the FIRST
  // classifiable file and the other two disagree with it — the only way a row carries a
  // kind note at all (addFiles refuses a contradicting file on any LATER call). The 16 MiB
  // spreadsheet therefore carries BOTH notes: the kind mismatch and the size cap. Dropped
  // rather than picked: nothing is uploaded here, and 16 MiB never crosses the CDP wire.
  await dropFiles(page, [
    { name: 'geometry-native.pdf', type: 'application/pdf' },
    { name: 'geometry-oversize-and-wrong-kind.csv', type: 'text/csv', bytes: 16 * 1024 * 1024 },
    { name: 'geometry-wrong-kind.csv', type: 'text/csv' },
  ])

  const acceptedLine = page.getByText(ACCEPTED_LINE, { exact: true })
  await expect(acceptedLine, 'the accepted-types line states all eight types').toBeVisible()

  // The ancestor chain, self-checked: the accepted line's parent is the card's padded
  // content div, whose parent is the card, whose grandparent is the wizard column
  // (CreateFlow's `padding: 24px 36px 56px` body). Each link is asserted by what it must
  // and must not contain, so a DOM change measures nothing rather than the wrong box.
  const cardBody = acceptedLine.locator('xpath=..')
  const card = cardBody.locator('xpath=..')
  const wizardColumn = card.locator('xpath=../..')
  await expect(card, 'the card carries its own header').toContainText('Import invoices ·')
  await expect(cardBody, 'the content div is INSIDE that header, not the card itself').not.toContainText('Import invoices ·')
  await expect(wizardColumn.getByRole('button', { name: /Cancel|Invoices/ }), 'the column carries the wizard header row').toHaveCount(1)

  const rows = cardBody.locator('ul li')
  // Non-empty FIRST: an empty locator list satisfies every assertion in the loop below,
  // and on a broken deploy that is exactly how this sweep would go green.
  await expect(rows, 'three files were dropped, so three rows must be measured').toHaveCount(3)

  const twoNoteRow = rows.filter({ hasText: 'geometry-oversize-and-wrong-kind.csv' })
  await expect(twoNoteRow, 'the second note this story added — the size cap').toContainText(
    /over the 15 MB limit\. Remove it, or split it into smaller files\./,
  )
  await expect(twoNoteRow, 'beside the first — the kind mismatch').toContainText(/a spreadsheet cannot be imported beside it/)

  const read = async () => {
    const [cardBox, columnBox, content, accepted, rowEdges] = await Promise.all([
      card.boundingBox(),
      wizardColumn.boundingBox(),
      cardBody.evaluate(edgesOf),
      acceptedLine.evaluate(edgesOf),
      rows.evaluateAll((els) =>
        els.map((el) => {
          const r = el.getBoundingClientRect()
          return { left: r.left, right: r.right, scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }
        }),
      ),
    ])
    return { cardBox, columnBox, content, accepted, rowEdges }
  }

  const measured: { width: number; gapLeft: number; gapRight: number; acceptedOverhang: number; worstRowOverhang: number }[] = []
  const entryViewport = page.viewportSize()
  try {
    // Widest first (WIDE_WIDTHS' own order, layout.ts): a cap strands only what the window
    // gives it room to strand.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      const m = await settledRead(read, `picker card at ${width}px`)
      expect(m.cardBox && m.columnBox, `the card and its column must both render at ${width}px`).toBeTruthy()
      expect(m.rowEdges.length, `all three rows must still be measurable at ${width}px`).toBe(3)

      // 1. The accepted-types line sits inside the card's CONTENT box, and its eight
      //    tokens fit the box they were given (a stretched flex item keeps its width while
      //    its text overflows, so the box check alone cannot see the second failure).
      expect(m.accepted.outerLeft, `the accepted-types line must start inside the card at ${width}px`).toBeGreaterThanOrEqual(m.content.left - 0.5)
      expect(m.accepted.outerRight, `the accepted-types line must end inside the card at ${width}px`).toBeLessThanOrEqual(m.content.right + 0.5)
      expect(m.accepted.scrollWidth, `the accepted-types line's text must fit its own box at ${width}px`).toBeLessThanOrEqual(m.accepted.clientWidth + 1)

      // 2. Every chosen-file row, the same two facts.
      for (const [i, row] of m.rowEdges.entries()) {
        expect(row.left, `file row ${i} must start inside the card at ${width}px`).toBeGreaterThanOrEqual(m.content.left - 0.5)
        expect(row.right, `file row ${i} must end inside the card at ${width}px`).toBeLessThanOrEqual(m.content.right + 0.5)
        expect(row.scrollWidth, `file row ${i}'s content must fit its own box at ${width}px`).toBeLessThanOrEqual(row.clientWidth + 1)
      }

      // 3. Nothing in the card forces a horizontal scroll — the one check that sees an
      //    overflow from an element no assertion above names.
      expect(m.content.scrollWidth, `the card's contents must not scroll horizontally at ${width}px`).toBeLessThanOrEqual(m.content.clientWidth + 1)

      // 4. The card is CENTRED in its column, not merely narrow enough for it. Both gaps,
      //    never one: a right-only measurement reads a left-pinned cap as a wide right gap
      //    and a right-pinned one as a perfect fit (layout.ts's gaps() doc comment).
      const g = gaps(
        { x: m.cardBox!.x, width: m.cardBox!.width },
        { x: m.columnBox!.x, width: m.columnBox!.width },
      )
      expect(g.left, `the card must not overflow its column's left edge at ${width}px`).toBeGreaterThanOrEqual(-0.5)
      expect(g.right, `the card must not overflow its column's right edge at ${width}px`).toBeGreaterThanOrEqual(-0.5)
      expect(
        Math.abs(g.left - g.right),
        `the card's left and right margins must agree at ${width}px (left ${g.left}, right ${g.right})`,
      ).toBeLessThanOrEqual(1)

      measured.push({
        width,
        gapLeft: g.left,
        gapRight: g.right,
        acceptedOverhang: m.accepted.outerRight - m.content.right,
        worstRowOverhang: Math.max(...m.rowEdges.map((r) => r.right - m.content.right)),
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  // The sweep ran every width, in order — a loop that measured fewer would otherwise pass
  // on whatever it did reach.
  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])
  await testInfo.attach('picker-card-geometry.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// AC-5. The strip is DERIVED from the selection (runKindOf(pickedFiles)), never stored, so
// it must follow the picker in both directions — including back to the spreadsheet strip
// when the last document is removed. A stored-and-stale run kind passes the first leg and
// fails the third.
test('EXTR09-E2E-05 (AC-5): the step strip follows the picked kind, picked or dropped', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  await signInFirm(page)
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await expect(page.locator('label[for="pf-import-file"]'), 'dropzone renders').toBeVisible({ timeout: 30_000 })

  const fileInput = page.locator('input[type="file"]#pf-import-file')
  const fileNames = page.locator('ul li div.mono')
  const IMPORT_STRIP = [/^1\s*Import$/, /^2\s*Map$/, /^3\s*Review$/]
  const DOCUMENT_STRIP = [/^1\s*Import$/, /^2\s*Review$/]

  // Leg 1 — no selection, no run kind: the shipped 3-step import strip (E2E-10's entry
  // claim, restated here as this spec's own baseline).
  await expect(stepChips(page), 'an empty picker shows the import strip').toHaveText(IMPORT_STRIP)

  // Leg 2 — a picked PDF: two chips, and Map is absent from the whole screen.
  await fileInput.setInputFiles([{ name: 'strip.pdf', mimeType: 'application/pdf', buffer: Buffer.from('%PDF-1.4 e2e strip probe\n', 'utf8') }])
  await expect(fileNames).toHaveText(['strip.pdf'])
  await expect(stepChips(page), 'a document run has exactly two chips').toHaveText(DOCUMENT_STRIP)
  await expect(page.getByText('Map', { exact: true }), 'the strip must never name a step it does not visit').toHaveCount(0)

  // Leg 3 — remove it: the strip goes BACK, which a stored run kind cannot do.
  await page.locator('ul li').filter({ hasText: 'strip.pdf' }).getByRole('button', { name: 'Remove' }).click()
  await expect(fileNames).toHaveCount(0)
  await expect(stepChips(page), 'clearing the selection clears the run kind').toHaveText(IMPORT_STRIP)

  // Leg 4 — a DROPPED PDF forks the same way, on the path `accept` never sees.
  await dropFiles(page, [{ name: 'strip-dropped.pdf', type: 'application/pdf' }])
  await expect(fileNames).toHaveText(['strip-dropped.pdf'])
  await expect(stepChips(page), 'a dropped document forks the strip too').toHaveText(DOCUMENT_STRIP)

  // Leg 5 — a spreadsheet keeps the shipped strip.
  await page.locator('ul li').filter({ hasText: 'strip-dropped.pdf' }).getByRole('button', { name: 'Remove' }).click()
  await fileInput.setInputFiles([{ name: 'strip.csv', mimeType: 'text/csv', buffer: Buffer.from('invoice_number,subtotal\nSTRIP-1,100\n', 'utf8') }])
  await expect(fileNames).toHaveText(['strip.csv'])
  await expect(stepChips(page), 'a spreadsheet run still shows all three steps').toHaveText(IMPORT_STRIP)
  await expect(page.getByRole('button', { name: 'Read columns' }), 'and the spreadsheet primary is back').toBeEnabled()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-10 · the document progress card: sampling, geometry, and the truth sweep ---
//
// D-12: no honest deployed oracle exists for WHICH transient stage word a browser
// catches -- the fleet default extractor is the mock and its work is sub-second, so
// asserting a specific word would be a flaky test pretending to be a contract. Both
// specs below assert only the closed vocabulary / geometry relationships that hold no
// matter which words a given run happens to hit, and attach every sample as evidence.

const DOCUMENT_STAGE_WORDS = new Set(['QUEUED', 'SENDING FILE', 'SERVER PROCESSING', 'READING', 'RETRYING'])

test('EXTR10-E2E-01: the document progress card samples a closed vocabulary and lands on review', async ({ page }, testInfo) => {
  // Upload + poll + import x2 concurrent files, on a fleet that may be cold -- matches
  // EXTR09-E2E-01's own cold-fleet budget for the same three calls.
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `EXTR-10-06 samples ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  const fileNames = ['progress-a.pdf', 'progress-b.pdf']
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page.locator('input[type="file"]#pf-import-file').setInputFiles(
    fileNames.map((name) => ({ name, mimeType: 'application/pdf', buffer: uniquePdfBytes() })),
  )
  await page.getByRole('button', { name: 'Extract invoices' }).click()

  const card = page.getByTestId('import-progress')
  const rows = page.getByTestId('import-progress-row')

  // Sampled every ~250ms until the card unmounts. Structurally safe, not a guess: the
  // card mounts synchronously in the click handler (EXTR09-E2E-01's own comment) and the
  // unmount needs at least two sequential HTTP round trips to the PR env (upload, then
  // import), so 250ms can never outrun either edge (Stage 1 Q1/Q2).
  const samples: { filename: string; status: string }[][] = []
  for (;;) {
    if (!((await card.count()) > 0 && (await card.isVisible()))) break
    const read = await rows.evaluateAll((els) =>
      els.map((el) => {
        const children = Array.from(el.children) as HTMLElement[]
        const filename = children[0]?.textContent?.trim() ?? ''
        const statusEl = children[1] ?? null
        // The in-flight kinds wrap a shimmer span + a label span; read only the LAST
        // descendant so the empty shimmer never pollutes the status text.
        const status = statusEl
          ? (Array.from(statusEl.querySelectorAll('*')).pop()?.textContent ?? statusEl.textContent ?? '').trim()
          : ''
        return { filename, status }
      }),
    )
    // A genuinely empty read means the card unmounted between the visibility check above
    // and this read (a real regression would starve EVERY tick, failing samples.length
    // below loudly) -- discard only this one tail read rather than record a false gap.
    if (read.length > 0) samples.push(read)
    await page.waitForTimeout(250)
  }

  // Its own named assertion, before the closed-set check below: an empty array satisfies
  // a `.every()` vacuously, so a selector regression capturing zero ticks must fail here
  // first, loudly, rather than pass the vocabulary check for having nothing to check.
  expect(samples.length, 'the card must be sampled more than once before it unmounts').toBeGreaterThan(1)

  for (const [i, sample] of samples.entries()) {
    expect(sample.map((r) => r.filename), `sample ${i}: both rows present, in picked order`).toEqual(fileNames)
    for (const row of sample) {
      const isStageWord = DOCUMENT_STAGE_WORDS.has(row.status)
      const isImportedCount = /^\d+ IMPORTED$/.test(row.status)
      // Pinned to the poll's own refusal copy (documentRun.ts deadLetterRefusal /
      // pollBudgetRefusal). startDocumentRun can also surface a raw Error.message or
      // 'extraction never settled', and this arm deliberately does NOT admit those: on a
      // happy-path run they mean a real break, so failing here is the point. With the former
      // `length > 0` the other two arms were dead code and an invented stage word passed.
      const isReason = /^Extraction (failed for this document|is still running after \d+ seconds)/.test(row.status)
      expect(
        isStageWord || isImportedCount || isReason,
        `sample ${i} (${row.filename}): "${row.status}" must be a stage word, an IMPORTED count, or one of the poll's two refusal sentences`,
      ).toBe(true)
      expect(row.status, `sample ${i} (${row.filename}): status must never imply an ordinal position`).not.toMatch(/\b\d+\s*(of|\/)\s*\d+\b/i)
    }
  }

  // Both arms stamp MOCK-INV-0001 (mock.go's `clean-invoice` fixture and `mockDefaultResult`)
  // and collide by design -- with
  // run.files.length===2, routeAfterRun can never take the single-file branch, so the
  // landing is always the review surface (Stage 1 Q5), never the real invoice detail.
  await expect
    .poll(
      async () => {
        if (await page.getByTestId('review-table').isVisible()) return 'review batch surface'
        if (await page.getByText(/^BATCH /).first().isVisible()) return 'review batch surface'
        if (await page.getByTestId('invoice-detail').isVisible()) return 'invoice detail'
        if (await page.getByRole('button', { name: 'Extract invoices' }).isVisible()) return 'back on the picker (the run failed)'
        return 'nothing yet'
      },
      { message: 'a two-document run must land on the review surface (MOCK-INV-0001 forces both files into one batch)', timeout: 60_000 },
    )
    .toBe('review batch surface')

  await testInfo.attach('document-progress-samples.json', {
    body: JSON.stringify(samples, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// EXTR10-E2E-02: a dead-lettered row's long reason, wrapped in the 240px span inside the
// 520px card, must never inflate the card. Follows EXTR09-E2E-04's own sweep machinery
// (settledRead/edgesOf/gaps/WIDE_WIDTHS, its non-empty-locator guard, its "every width
// measured, widest first" assertion).
//
// The good document's own poll is held at 'extracting' from the moment its upload
// resolves, until after both geometry sweeps. Verified from source rather than assumed:
// on the LAST of River's 3 attempts, worker.go advances the job DIRECTLY from
// `extracting` to `dead_lettered` (no intermediate retry state to linger on), and
// documentRun.ts's per-file pipeline calls onStage(failed) and resolves in the very same
// microtask chain that unblocks Promise.all and (via App.tsx's applyRoute) unmounts this
// card -- with nothing else pending, React's automatic batching (product-advisor
// consult, RALPH Phase 1) could coalesce the failed-reason render straight into the
// review-routing render, so the text this spec measures might never actually paint.
// Holding the good pipeline open removes that race structurally: Promise.all cannot
// resolve, so the card cannot unmount, until this spec explicitly releases it.
test('EXTR10-E2E-02: a dead-lettered row wraps its long reason without inflating the card', async ({ page }, testInfo) => {
  // Upload+enqueue, ~20-40s River backoff to dead-letter (attempt^4s: 1s then 16s), two
  // widest-first sweeps, then the held release -- matches EXTR09-E2E-01's cold-fleet budget.
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `EXTR-10-06 geometry ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  const goodName = 'geometry-good.pdf'
  const badName = 'geometry-dead-letter.pdf'

  let goodDocId: string | null = null
  // Record the good document's id INSIDE its POST route: the handler completes before the app
  // can see the response, so no poll for this document can precede the assignment. A
  // page.on('response') listener cannot give that ordering -- it awaits res.json() while the app
  // has already resolved the same response and fired its first poll.
  await page.route('**/api/submission/v1/documents', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.continue()
      return
    }
    const isGood = requestBody(route.request()).includes(goodName)
    const res = await route.fetch()
    const text = await res.text()
    if (isGood) {
      try {
        const body = JSON.parse(text)
        if (typeof body.document_id === 'string') goodDocId = body.document_id
      } catch {
        /* fulfil unchanged; the toHaveCount(2) below fails loudly if the upload really broke */
      }
    }
    await route.fulfill({ response: res, body: text })
  })
  await page.route('**/api/submission/v1/extractions*', async (route) => {
    const url = new URL(route.request().url())
    if (goodDocId !== null && url.searchParams.get('document_id') === goodDocId) {
      await route.fulfill({
        json: {
          jobs: [{ id: 'e2e-hold', document_id: goodDocId, state: 'extracting', created_at: new Date().toISOString(), last_error: null }],
        },
      })
      return
    }
    await route.continue()
  })

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page.locator('input[type="file"]#pf-import-file').setInputFiles([
    { name: goodName, mimeType: 'application/pdf', buffer: uniquePdfBytes() },
    { name: badName, mimeType: 'application/pdf', buffer: uniqueGarbageBytes() },
  ])
  await page.getByRole('button', { name: 'Extract invoices' }).click()

  const card = page.getByTestId('import-progress')
  await expect(card, 'the progress card must mount').toBeVisible({ timeout: 30_000 })
  const rows = page.getByTestId('import-progress-row')
  // Non-empty FIRST -- an empty locator satisfies every relationship check below vacuously.
  await expect(rows, 'both documents must be measurable').toHaveCount(2)
  const badRow = rows.filter({ hasText: badName })

  const wizardColumn = card.locator('xpath=..')
  const read = async () => {
    const [cardBox, columnBox, cardEdges, rowEdges] = await Promise.all([
      card.boundingBox(),
      wizardColumn.boundingBox(),
      card.evaluate(edgesOf),
      rows.evaluateAll((els) => els.map((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))),
    ])
    return { cardBox, columnBox, cardEdges, rowEdges }
  }

  type WidthSample = { width: number; cardWidth: number; gapLeft: number; gapRight: number }
  async function sweepWidths(label: string): Promise<WidthSample[]> {
    const out: WidthSample[] = []
    // Widest first (WIDE_WIDTHS' own order, layout.ts): a cap strands only what the
    // window gives it room to strand.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      const m = await settledRead(read, `${label} at ${width}px`)
      expect(m.cardBox && m.columnBox, `the card and its column must both render at ${width}px (${label})`).toBeTruthy()
      expect(m.rowEdges.length, `both rows must still be measurable at ${width}px (${label})`).toBe(2)

      for (const [i, row] of m.rowEdges.entries()) {
        expect(row.scrollWidth, `row ${i}'s content must fit its own box at ${width}px (${label})`).toBeLessThanOrEqual(row.clientWidth + 1)
      }
      expect(m.cardEdges.scrollWidth, `the card's contents must not scroll horizontally at ${width}px (${label})`).toBeLessThanOrEqual(
        m.cardEdges.clientWidth + 1,
      )

      const g = gaps({ x: m.cardBox!.x, width: m.cardBox!.width }, { x: m.columnBox!.x, width: m.columnBox!.width })
      expect(g.left, `the card must not overflow its column's left edge at ${width}px (${label})`).toBeGreaterThanOrEqual(-0.5)
      expect(g.right, `the card must not overflow its column's right edge at ${width}px (${label})`).toBeGreaterThanOrEqual(-0.5)
      expect(
        Math.abs(g.left - g.right),
        `the card's left and right margins must agree at ${width}px (${label}) (left ${g.left}, right ${g.right})`,
      ).toBeLessThanOrEqual(1)

      out.push({ width, cardWidth: m.cardBox!.width, gapLeft: g.left, gapRight: g.right })
    }
    expect(out.map((s) => s.width), `every WIDE_WIDTHS entry must be measured, widest first (${label})`).toEqual([...WIDE_WIDTHS])
    return out
  }

  const entryViewport = page.viewportSize()
  let shortSweep: WidthSample[]
  let longSweep: WidthSample[]
  try {
    await expect(badRow, 'the bad document must retry before it dead-letters').toContainText('RETRYING', { timeout: 60_000 })
    // Race-free by construction: the hold above pins this document at state:'extracting' for the
    // whole sweep, so READING is not a sampling gamble here the way it is in EXTR10-E2E-01.
    await expect(rows.filter({ hasText: goodName }), 'the held document must read READING').toContainText('READING')
    shortSweep = await sweepWidths('short-label (RETRYING)')

    // The good pipeline is held open, so nothing can route the run away underneath this
    // wait -- the long reason is a stable state here, not a race.
    await expect(badRow, 'the dead-letter reason must render before the long-label sweep').toContainText(
      'Extraction failed for this document',
      { timeout: 90_000 },
    )
    longSweep = await sweepWidths('long-label (dead-lettered)')
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  // D-18's actual empirical claim: a stretched flex child (the long reason) cannot
  // inflate the 520px cap. Compared against THIS run's own earlier short-label reading,
  // not a second run.
  for (const [i, width] of WIDE_WIDTHS.entries()) {
    expect(shortSweep[i].width, 'both sweeps measured the same width in the same order').toBe(width)
    expect(longSweep[i].width, 'both sweeps measured the same width in the same order').toBe(width)
    expect(
      Math.abs(longSweep[i].cardWidth - shortSweep[i].cardWidth),
      `the card's width must not change between the short and long label at ${width}px`,
    ).toBeLessThanOrEqual(1)
  }

  await page.unroute('**/api/submission/v1/extractions*')
  await expect
    .poll(
      async () => {
        if (await page.getByTestId('review-table').isVisible()) return 'review batch surface'
        if (await page.getByText(/^BATCH /).first().isVisible()) return 'review batch surface'
        return 'nothing yet'
      },
      { message: 'releasing the held poll must let the run settle and land on review', timeout: 60_000 },
    )
    .toBe('review batch surface')

  await testInfo.attach('progress-row-geometry.json', {
    body: JSON.stringify({ shortSweep, longSweep }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-11 · the review screen's document pane: the ratio oracle, the grid, the band ---
//
// Written in EXTR-11-05 (Mode A). EXTR-11-07 has built the screen; these stay red until
// EXTR-11-08 adds the route and the entry control `openExtractionReview` clicks. Nothing skips
// them: what keeps them off the gate meanwhile is the PR staying DRAFT --
// `dev-env.yml`'s deploy and E2E jobs are gated on `pull_request.draft == false`. The
// `skip-visual` label the branch strategy names is a human marker; no workflow reads it.
//
// CONTRACT OWED BY EXTR-11-08: the entry control on SourceDocumentCard must carry
// `data-testid="open-extraction-review"`. The testid is the only handle these specs have, and
// a role/name locator would couple them to copy. The copy itself is NOT open -- the story's
// Invented-copy table fixes all three strings ("Check the extraction", "This document has no
// extraction to check.", "Extraction review"); an earlier draft of this comment said otherwise.

/**
 * The document journey EXTR09-E2E-01 proved, stopped at the real invoice detail.
 *
 * `file` defaults to the one-page fixture every caller before EXTR-11-09 used, so the eight
 * specs above are byte-for-byte unchanged; only EXTR11-E2E-05 passes the two-page one.
 */
async function extractOneDocument(
  page: Page,
  label: string,
  file: { name: string; buffer: Buffer } = { name: 'native_invoice.pdf', buffer: uniquePdfBytes() },
): Promise<void> {
  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `${label} ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: file.name, mimeType: 'application/pdf', buffer: file.buffer })
  await page.getByRole('button', { name: 'Extract invoices' }).click()

  await expect(page.getByTestId('invoice-detail'), 'the document run must land on the real invoice detail').toBeVisible({
    timeout: 240_000,
  })
}

// Opens the review screen and RETURNS the 200 the SPA itself consumed. Every wire fact these
// specs assert comes off this one response -- never a literal copied out of mock.go, which
// would assert the fixture against itself.
async function openExtractionReview(page: Page): Promise<ExtractionDetail> {
  // Promise.all, not a bare waiter awaited after the click (EXTR-11-09 correction). The waiter
  // is created first either way -- an array literal evaluates left to right, so the response
  // listener is registered before the click starts -- but a click that fails its 30s
  // actionability check used to leave the 120s waiter unhandled, and its rejection surfaced as
  // worker noise ~90s AFTER the test had already reported. Promise.all attaches a handler to
  // both, so the click's failure is the one that propagates and nothing is left dangling.
  //
  // `$`-anchored: the LIST route has no id segment and the page route ends in /pages/{n}.
  const [res] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        /\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}$/.test(new URL(r.url()).pathname),
      { timeout: 120_000 },
    ),
    page.getByTestId('open-extraction-review').click(),
  ])

  expect(res.status(), 'the review screen must read its detail over a 200').toBe(200)
  await expect(page.getByTestId('extraction-review'), 'the review screen must open').toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId('extraction-page-1'), 'the canvas must render at least one frame').toBeVisible({
    timeout: 60_000,
  })
  return (await res.json()) as ExtractionDetail
}

/** The first field the extractor pointed somewhere. The specs read its region off the wire. */
function firstLocatedField(detail: ExtractionDetail) {
  const field = detail.fields.find((f) => f.region !== null)
  expect(field, 'no field on this document carries a region -- every ratio below would be vacuous').toBeTruthy()
  return { name: field!.name, region: field!.region! }
}

async function boxOf(page: Page, testid: string): Promise<Rect> {
  const box = await page.getByTestId(testid).boundingBox()
  expect(box, `${testid} did not render`).not.toBeNull()
  return box as Rect
}

// AC-4's ONLY real oracle. `overlapOf(highlight, image) === highlight` is true for a box
// anywhere inside the page and VACUOUSLY true for a 0x0 box entirely off it -- overlapOf
// clamps per axis with Math.max(0, …), so the intersection collapses to the highlight's own
// rect (layout.ts:68). Containment therefore rides along below as a cheap extra, never as the
// assertion.
//
// Per-axis pixel tolerance, not a strict float compare: the browser resolves `left: 62%` and
// `width: 28%` against the frame independently, so an edge can carry two roundings, and a
// strict toEqual on boundingBox() floats flakes. 1.5px on a 560-960px frame is under 0.3% --
// three orders coarser than the defect this catches (a wrong axis, an inverted origin, or a
// transform, all of which land tens of percent out).
const RATIO_TOL_PX = 1.5

test('EXTR11-E2E-03 (AC-4): the highlight lands where the region says, at every zoom', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-05 ratio')
  const detail = await openExtractionReview(page)
  const { name, region } = firstLocatedField(detail)

  await page.getByTestId(`extraction-field-${name}`).click()
  const imageId = `extraction-page-image-${region.page}`
  // The selection loads its own page directly, without waiting on the observer (AC-6).
  await expect(page.getByTestId(imageId), "the selected field's page must load").toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId('extraction-highlight'), 'the selection must draw exactly one highlight').toHaveCount(1)

  type Measured = {
    zoom: number
    image: Rect
    highlight: Rect
    expected: { x: number; y: number; width: number; height: number }
  }
  const measured: Measured[] = []

  for (const zoom of [50, 100, 150]) {
    await page.getByTestId(`extraction-zoom-${zoom}`).click()

    // settledRead, not a bare read: selecting a field scrolls the ground with
    // `behavior: 'smooth'`, and two boundingBox() calls taken mid-scroll disagree
    // ([[drawer-animation-defeats-geometry-specs]]). Both boxes move together, so the
    // RATIO is scroll-invariant -- but only if the pair is read from one settled frame.
    const m = await settledRead(async () => {
      const [image, highlight] = await Promise.all([boxOf(page, imageId), boxOf(page, 'extraction-highlight')])
      return { image, highlight }
    }, `highlight ratio at zoom ${zoom}`)

    const expected = {
      x: m.image.x + region.x0 * m.image.width,
      y: m.image.y + region.y0 * m.image.height,
      width: (region.x1 - region.x0) * m.image.width,
      height: (region.y1 - region.y0) * m.image.height,
    }

    // The frame really did change size, or all three zooms measure one rendering.
    expect(m.image.width, `the page image has no width at zoom ${zoom}`).toBeGreaterThan(0)

    for (const axis of ['x', 'y', 'width', 'height'] as const) {
      expect(
        Math.abs(m.highlight[axis] - expected[axis]),
        `zoom ${zoom}: the highlight's ${axis} is ${m.highlight[axis]}, the wire's region says ${expected[axis]}`,
      ).toBeLessThanOrEqual(RATIO_TOL_PX)
    }

    // The cheap extras, stated as extras.
    expect(m.highlight.width, `zoom ${zoom}: a zero-width highlight satisfies containment vacuously`).toBeGreaterThan(0)
    expect(m.highlight.height, `zoom ${zoom}: a zero-height highlight satisfies containment vacuously`).toBeGreaterThan(0)
    const inside = overlapOf(m.highlight, m.image)
    expect(Math.round(inside.width), `zoom ${zoom}: the highlight escapes its page horizontally`).toBe(
      Math.round(m.highlight.width),
    )
    expect(Math.round(inside.height), `zoom ${zoom}: the highlight escapes its page vertically`).toBe(
      Math.round(m.highlight.height),
    )

    measured.push({ zoom, image: m.image, highlight: m.highlight, expected })
  }

  // The sweep ran all three, in order -- a loop that measured fewer would otherwise pass on
  // whatever it did reach.
  expect(measured.map((m) => m.zoom), 'every zoom must be measured').toEqual([50, 100, 150])
  // And they really were three different renderings, not one page measured three times.
  expect(new Set(measured.map((m) => Math.round(m.image.width))).size, 'zoom moved nothing').toBeGreaterThan(1)

  await testInfo.attach('extraction-highlight-ratio.json', {
    body: JSON.stringify({ region, measured }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// EXTR11-E2E-04 and -04b read the SAME 200, so they share one journey rather than paying for
// two -- the file's own E2E-04/E2E-09 precedent, with labelled blocks.
test('EXTR11-E2E-04/04b: the image is the stored grid, and the wire is exactly the contract', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-05 grid')
  const detail = await openExtractionReview(page)

  // -- EXTR11-E2E-04 -- the intrinsic pixels ARE the stored grid.
  //
  // go-pdfium ceils `pt * dpi / 72` (pdfium.go:154-159), so a recomputed grid is off by one
  // row on US-Letter at 150 DPI -- 1651, not 1650. Nothing else in the suite compares the
  // bytes the browser decoded against the numbers the frame was locked to.
  expect(detail.pages.length, 'a document with no page rows cannot prove a grid').toBeGreaterThan(0)

  const grids: { page: number; wire: [number, number]; natural: [number, number] }[] = []
  for (const p of detail.pages) {
    const img = page.getByTestId(`extraction-page-image-${p.page}`)
    // Lazy by design: scroll the frame in and let the observer fire (AC-6).
    await page.getByTestId(`extraction-page-${p.page}`).scrollIntoViewIfNeeded()
    await expect(img, `page ${p.page} must load its bytes`).toBeVisible({ timeout: 60_000 })

    // Polled, never read once: naturalWidth is 0 until the blob decodes, and 0 === 0 would
    // make an unloaded image agree with a zero grid.
    let natural = ''
    await expect
      .poll(
        async () => {
          natural = await img.evaluate((el) => {
            const i = el as HTMLImageElement
            return i.complete && i.naturalWidth > 0 ? `${i.naturalWidth}x${i.naturalHeight}` : ''
          })
          return natural
        },
        { message: `page ${p.page}'s bytes never decoded`, timeout: 60_000 },
      )
      .not.toBe('')

    expect(natural, `page ${p.page}: the decoded bytes are not the grid the frame was locked to`).toBe(
      `${p.width_px}x${p.height_px}`,
    )
    const [nw, nh] = natural.split('x').map(Number)
    grids.push({ page: p.page, wire: [p.width_px, p.height_px], natural: [nw, nh] })
  }
  expect(grids.length, 'no page was measured').toBe(detail.pages.length)

  // -- EXTR11-E2E-04b -- the wire key set, off that same 200.
  //
  // It cannot live in e2e/api/extractions.spec.ts: that file creates no row, so it can never
  // hold a settled job (:29-32). This is the only DEPLOYED check of Go's `[]T` nil -> `null`
  // coercion, which no `omitempty`-free struct tag prevents.
  expect(Object.keys(detail).sort(), 'the top-level key set drifted from internal/extraction/reader.go').toEqual(
    ['document', 'document_id', 'fields', 'id', 'pages', 'state'].sort(),
  )
  expect(Array.isArray(detail.pages), 'pages arrived as null, not []').toBe(true)
  expect(Array.isArray(detail.fields), 'fields arrived as null, not []').toBe(true)
  expect(detail.document, 'document arrived as null').not.toBeNull()
  expect(typeof detail.document, 'document is not an object').toBe('object')
  expect(Object.keys(detail.document).sort()).toEqual(['content_type', 'filename', 'size_bytes', 'stored_at'].sort())
  expect(Object.keys(detail.pages[0]).sort()).toEqual(['height_px', 'page', 'width_px'].sort())
  expect(detail.fields.length, 'a settled job with no fields cannot prove a field key set').toBeGreaterThan(0)
  expect(Object.keys(detail.fields[0]).sort()).toEqual(['name', 'region', 'value'].sort())

  await testInfo.attach('extraction-wire.json', {
    body: JSON.stringify({ grids, keys: Object.keys(detail).sort(), detail }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR11-E2E-06 (AC-2/AC-5): the page frame stays in its band, centred where it fits', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-05 band')
  await openExtractionReview(page)

  const frame = page.getByTestId('extraction-page-1')
  const ground = page.getByTestId('extraction-ground')
  // The ground's inner pad -- the frame's actual containing block. Its only element child,
  // which ExtractionCanvas.test.tsx pins from the other side.
  const innerPad = ground.locator('> div').first()

  type Fit = {
    width: number
    zoom: number
    frameWidth: number
    innerWidth: number
    gapLeft: number
    gapRight: number
    fits: boolean
    groundScrollsX: boolean
    groundScrollsY: boolean
    bodyScrollsX: boolean
  }
  const measured: Fit[] = []

  const entryViewport = page.viewportSize()
  try {
    // Widest first (WIDE_WIDTHS' own order, layout.ts): a band strands only what the window
    // gives it room to strand.
    for (const zoom of [100, 150]) {
      await page.getByTestId(`extraction-zoom-${zoom}`).click()
      for (const width of WIDE_WIDTHS) {
        await page.setViewportSize({ width, height: 1080 })

        const m = await settledRead(async () => {
          const [frameBox, innerBox, scroll] = await Promise.all([
            frame.boundingBox(),
            innerPad.boundingBox(),
            ground.evaluate((el) => ({
              groundScrollsX: el.scrollWidth > el.clientWidth + 1,
              groundScrollsY: el.scrollHeight > el.clientHeight + 1,
              bodyScrollsX: document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
            })),
          ])
          return { frameBox, innerBox, ...scroll }
        }, `page frame at ${width}px, zoom ${zoom}`)

        expect(m.frameBox && m.innerBox, `the frame and its pad must both render at ${width}px`).toBeTruthy()
        const g = gaps({ x: m.frameBox!.x, width: m.frameBox!.width }, { x: m.innerBox!.x, width: m.innerBox!.width })

        // 1. The band. pageFrameStyle's min/max-width are absolute, so this holds at every
        //    width whether or not the frame fits its column.
        const [floor, ceiling] = zoom === 100 ? [560, 640] : [840, 960]
        expect(m.frameBox!.width, `the frame is below its ${floor}px floor at ${width}px, zoom ${zoom}`).toBeGreaterThanOrEqual(floor - 1)
        expect(m.frameBox!.width, `the frame is above its ${ceiling}px ceiling at ${width}px, zoom ${zoom}`).toBeLessThanOrEqual(ceiling + 1)

        // 2. `margin: 0 auto` -- but only where there is room. An overflowing block resolves
        //    both auto margins to zero and spills right, so asserting symmetry at every width
        //    would fail on CORRECT rendering wherever the floor exceeds the column.
        const fits = m.frameBox!.width <= m.innerBox!.width + 1
        if (fits) {
          expect(
            Math.abs(g.left - g.right),
            `the frame's margins must agree at ${width}px, zoom ${zoom} (left ${g.left}, right ${g.right})`,
          ).toBeLessThanOrEqual(2)
        } else {
          // 3. Where it does NOT fit, the GROUND is what scrolls.
          expect(m.groundScrollsX, `the frame overflows at ${width}px, zoom ${zoom}, and the ground does not scroll`).toBe(true)
        }

        // 4. The body never scrolls sideways, at any width or zoom. `overflow: hidden` on the
        //    body row is what contains the enlarged page; without it the whole app slides.
        expect(m.bodyScrollsX, `the page itself scrolls horizontally at ${width}px, zoom ${zoom}`).toBe(false)

        // 5. The GROUND is what scrolls vertically. At zoom 150 a US-Letter frame is ~1090px
        //    tall inside a 1080px viewport, so this holds for a one-page document. It is the
        //    only oracle in the suite for the containment (`minHeight: 0` down the flex
        //    column) that lazy loading rests on: if the ground grows to its content instead of
        //    scrolling, every frame sits inside the observer's 800px root margin and an
        //    800-page document fetches all 800 at mount. That fetch COUNT still has no oracle
        //    -- the deployed corpus has no document long enough to show it.
        if (zoom === 150) {
          expect(
            m.groundScrollsY,
            `the ground does not scroll vertically at ${width}px, zoom ${zoom} -- it grew to its content instead`,
          ).toBe(true)
        }

        measured.push({
          width,
          zoom,
          frameWidth: m.frameBox!.width,
          innerWidth: m.innerBox!.width,
          gapLeft: g.left,
          gapRight: g.right,
          fits,
          groundScrollsX: m.groundScrollsX,
          groundScrollsY: m.groundScrollsY,
          bodyScrollsX: m.bodyScrollsX,
        })
      }
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(
    measured.map((m) => `${m.zoom}@${m.width}`),
    'every zoom x WIDE_WIDTHS cell must be measured, widest first',
  ).toEqual([100, 150].flatMap((z) => WIDE_WIDTHS.map((w) => `${z}@${w}`)))

  // Both branches must have been exercised, or the two assertions above are each vacuous on
  // half the sweep and nobody would know which half.
  expect(measured.some((m) => m.fits), 'the frame never fit its column -- the centring assertion never ran').toBe(true)
  expect(measured.some((m) => !m.fits), 'the frame always fit -- the ground-scroll assertion never ran').toBe(true)

  await testInfo.attach('extraction-frame-band.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR11-E2E-08 (AC-5): the toolbar never overlaps the ground', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-05 clearance')
  await openExtractionReview(page)

  const toolbar = page.getByTestId('extraction-toolbar')
  const ground = page.getByTestId('extraction-ground')

  const measured: { width: number; toolbar: Rect; ground: Rect; overlap: Rect }[] = []
  const entryViewport = page.viewportSize()
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [t, g] = await Promise.all([toolbar.boundingBox(), ground.boundingBox()])
        return { t, g }
      }, `toolbar clearance at ${width}px`)

      expect(m.t && m.g, `the toolbar and the ground must both render at ${width}px`).toBeTruthy()
      // Non-empty first: two collapsed rects clear each other on both axes and pass vacuously.
      expect(m.t!.height, `the toolbar has no height at ${width}px`).toBeGreaterThan(0)
      expect(m.g!.height, `the ground has no height at ${width}px`).toBeGreaterThan(0)

      // Both axes, never one: `flex: none` on the toolbar plus `minHeight: 0` on the ground is
      // what keeps them stacked; without the pair the ground grows to its content and slides
      // under the toolbar (SourceDocumentModal.test.tsx spec 1's defect, one pane over).
      const overlap = overlapOf(m.t as Rect, m.g as Rect)
      expect(
        rectsOverlap(m.t as Rect, m.g as Rect),
        `the toolbar covers ${overlap.width}x${overlap.height}px of the ground at ${width}px`,
      ).toBe(false)

      measured.push({ width, toolbar: m.t as Rect, ground: m.g as Rect, overlap })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])

  await testInfo.attach('extraction-toolbar-clearance.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// EXTR-11-06's half of the pane relationship: the two panes never cover each other, and no
// field row spills the column it is laid out in. EXTR-11-07 owns `EXTR11-E2E-02` proper (the
// panes TILE the body) and `-10` (the right pane yields first, never below 470px); this row is
// the overlap half plus the spill, written where the pane's own grid is written.
test('EXTR11-E2E-02a (AC-1/AC-6): the panes never overlap, and no field row spills its column', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-06 spill')
  const detail = await openExtractionReview(page)

  // The floor, off the wire the SPA itself consumed: with no field the spill sweep below
  // measures nothing and passes vacuously.
  expect(detail.fields.length, 'no field on this document -- every spill comparison is vacuous').toBeGreaterThan(0)

  const canvas = page.getByTestId('extraction-canvas')
  const fields = page.getByTestId('extraction-fields')
  // The trailing hyphen matters: `extraction-fields` is itself prefixed by `extraction-field`.
  const rows = page.locator('[data-testid^="extraction-field-"]')
  await expect(rows, 'the pane rendered no row for a wire that carries fields').toHaveCount(detail.fields.length)

  type Spill = { testid: string; left: number; right: number }
  const measured: { width: number; canvas: Rect; fields: Rect; overlap: Rect; worst: Spill }[] = []
  const entryViewport = page.viewportSize()
  try {
    // Widest first (WIDE_WIDTHS' own order, layout.ts): a floor strands only what the window
    // is too narrow to hold, so the narrow end is where a spill appears.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [c, f, rs] = await Promise.all([
          canvas.boundingBox(),
          fields.boundingBox(),
          rows.evaluateAll((els) =>
            els.map((el) => {
              const r = el.getBoundingClientRect()
              return { testid: el.getAttribute('data-testid') ?? '', left: r.left, right: r.right, width: r.width }
            }),
          ),
        ])
        return { c, f, rs }
      }, `pane geometry at ${width}px`)

      expect(m.c && m.f, `both panes must render at ${width}px`).toBeTruthy()
      // Non-empty first: two collapsed rects clear each other on both axes and pass vacuously.
      expect(m.c!.width, `the document pane has no width at ${width}px`).toBeGreaterThan(0)
      expect(m.c!.height, `the document pane has no height at ${width}px`).toBeGreaterThan(0)
      expect(m.f!.width, `the fields pane has no width at ${width}px`).toBeGreaterThan(0)
      expect(m.f!.height, `the fields pane has no height at ${width}px`).toBeGreaterThan(0)

      // Both axes, never one (layout.ts's own rule). A fields pane that has stopped being a
      // flex sibling and started overlaying the document clears neither.
      const overlap = overlapOf(m.c as Rect, m.f as Rect)
      expect(
        rectsOverlap(m.c as Rect, m.f as Rect),
        `the fields pane covers ${overlap.width}x${overlap.height}px of the document pane at ${width}px`,
      ).toBe(false)

      // Every row stays inside its own pane, on BOTH edges -- gaps()'s rule applied to a row.
      // This is the relationship the artboard's `min-width: 470px` floor exists to protect:
      // a `1fr 1fr` grid inside a pane squeezed below it pushes its cells' content out, and
      // the body's `overflow-y: auto` (y ONLY) means a horizontal spill has nowhere to hide.
      expect(m.rs.length, `no row measured at ${width}px`).toBe(detail.fields.length)
      let worst: Spill = { testid: '', left: 0, right: 0 }
      for (const r of m.rs) {
        expect(r.width, `${r.testid} collapsed to zero width at ${width}px -- its edges are vacuous`).toBeGreaterThan(0)
        const outLeft = m.f!.x - r.left
        const outRight = r.right - (m.f!.x + m.f!.width)
        expect(outLeft, `${r.testid} starts ${outLeft.toFixed(1)}px left of the pane at ${width}px`).toBeLessThanOrEqual(1)
        expect(outRight, `${r.testid} ends ${outRight.toFixed(1)}px right of the pane at ${width}px`).toBeLessThanOrEqual(1)
        if (Math.max(outLeft, outRight) > Math.max(worst.left, worst.right)) worst = { testid: r.testid, left: outLeft, right: outRight }
      }

      measured.push({ width, canvas: m.c as Rect, fields: m.f as Rect, overlap, worst })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])

  await testInfo.attach('extraction-pane-spill.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-11-07 · the shell's two-pane flex row ------------------------------------------
//
// Written in EXTR-11-07 (Mode A). `ExtractionReview` now exists; these stay red until
// EXTR-11-08 adds the route. The PR staying DRAFT is what keeps them off the gate meanwhile.
//
// jsdom computes no layout, so nothing above this line is an oracle for the pane
// relationships: `ExtractionReview.test.tsx` asserts the STYLE OBJECTS as a structural
// regression guard and stops there. These three rows are AC-6's whole oracle.

test('EXTR11-E2E-02 (AC-1/AC-6): the panes tile the body and never overlap', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-07 tile')
  await openExtractionReview(page)

  const shellBody = page.getByTestId('extraction-review-body')
  const column = page.locator('main .pf-scroll')
  const canvas = page.getByTestId('extraction-canvas')
  const fields = page.getByTestId('extraction-fields')

  // 1. The body fills the view column at every width. A width-only assertion passes on the
  //    very defect this exists to catch (layout.ts:1-18): a capped, left-pinned body strands
  //    a band on the right and still measures the width it was told to.
  const fit = await assertFillsColumn(page, shellBody, column, 'the extraction review body')
  expect(fit.map((f) => f.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])

  type Tile = { width: number; body: Rect; canvas: Rect; fields: Rect; seam: number; leftEdge: number; rightEdge: number }
  const measured: Tile[] = []
  const entryViewport = page.viewportSize()
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [b, c, f] = await Promise.all([shellBody.boundingBox(), canvas.boundingBox(), fields.boundingBox()])
        return { b, c, f }
      }, `pane tiling at ${width}px`)

      expect(m.b && m.c && m.f, `the body and both panes must render at ${width}px`).toBeTruthy()
      // Non-empty first: three collapsed rects tile perfectly and clear each other vacuously.
      for (const [label, r] of [['body', m.b!], ['document pane', m.c!], ['fields pane', m.f!]] as const) {
        expect(r.width, `the ${label} has no width at ${width}px`).toBeGreaterThan(0)
        expect(r.height, `the ${label} has no height at ${width}px`).toBeGreaterThan(0)
      }

      // 2. Both axes, never one (layout.ts's own rule). EXTR11-E2E-02a asserts this same
      //    clearance from the fields pane's side; it is repeated here because the tiling
      //    numbers below are meaningless over two rects that cover each other.
      expect(rectsOverlap(m.c as Rect, m.f as Rect), `the panes overlap at ${width}px`).toBe(false)

      // 3. The document pane starts at the body's left edge and the fields pane ends at its
      //    right edge. Both edges, never one -- gaps()'s rule: a right-only check reads a
      //    left-pinned pair as a large right gap and a right-pinned pair as a perfect fit.
      const leftEdge = m.c!.x - m.b!.x
      const rightEdge = m.b!.x + m.b!.width - (m.f!.x + m.f!.width)
      expect(leftEdge, `the document pane starts ${leftEdge.toFixed(1)}px off the body's left edge at ${width}px`).toBeLessThanOrEqual(1)
      expect(rightEdge, `the fields pane ends ${rightEdge.toFixed(1)}px off the body's right edge at ${width}px`).toBeLessThanOrEqual(1)

      // 4. And they MEET. Clearance plus two flush outer edges still permits a stranded band
      //    between them -- the BUG-03-05 shape, moved inside the row.
      const seam = m.f!.x - (m.c!.x + m.c!.width)
      expect(Math.abs(seam), `a ${seam.toFixed(1)}px band is stranded between the panes at ${width}px`).toBeLessThanOrEqual(2)

      measured.push({ width, body: m.b as Rect, canvas: m.c as Rect, fields: m.f as Rect, seam, leftEdge, rightEdge })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])

  await testInfo.attach('extraction-pane-tiling.json', {
    body: JSON.stringify({ fit, measured }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR11-E2E-10 (AC-6): the right pane yields first, and never below its floor', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-07 floor')
  await openExtractionReview(page)

  const fields = page.getByTestId('extraction-fields')
  const frame = page.getByTestId('extraction-page-1')

  type Yield = { width: number; fieldsWidth: number; frameWidth: number }
  const measured: Yield[] = []
  const entryViewport = page.viewportSize()
  try {
    // Widest first (WIDE_WIDTHS' own order): a floor strands only what the window is too
    // narrow to hold, so the narrow end is where the pane is pushed onto its 470px floor.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [f, fr] = await Promise.all([fields.boundingBox(), frame.boundingBox()])
        return { f, fr }
      }, `pane yield at ${width}px`)

      expect(m.f && m.fr, `the fields pane and the page frame must both render at ${width}px`).toBeTruthy()

      // 1. The artboard's floor (`:223`). Below it the pane's `1fr 1fr` grid pushes its cells
      //    past their column -- the spill EXTR11-E2E-02a measures from the other side.
      expect(m.f!.width, `the fields pane is below its 470px floor at ${width}px`).toBeGreaterThanOrEqual(469)

      // 2. And the document pane did not pay for it: the page frame is still inside the band
      //    pageFrameStyle declares at zoom 100. A right pane pinned to a fixed track squeezes
      //    the frame under its floor here.
      expect(m.fr!.width, `the page frame fell below its 560px floor at ${width}px`).toBeGreaterThanOrEqual(559)
      expect(m.fr!.width, `the page frame rose above its 640px ceiling at ${width}px`).toBeLessThanOrEqual(641)

      measured.push({ width, fieldsWidth: m.f!.width, frameWidth: m.fr!.width })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])

  // 3. The pane grows with the chrome rather than pinning a track. `width === 620` would be
  //    the tempting assertion and would FAIL on correct rendering at 2560, where both panes
  //    grow; `>= 470` alone passes on a pane frozen at its basis. This is the relationship.
  const widest = measured.find((m) => m.width === 2560)
  const narrowest = measured.find((m) => m.width === 1280)
  expect(widest && narrowest, 'the sweep did not measure both ends -- the comparison below is vacuous').toBeTruthy()
  expect(
    widest!.fieldsWidth,
    `the fields pane is ${widest!.fieldsWidth}px at 2560 and ${narrowest!.fieldsWidth}px at 1280 -- it is pinned to a track, not yielding`,
  ).toBeGreaterThan(narrowest!.fieldsWidth)

  await testInfo.attach('extraction-pane-yield.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// EXTR-11-06's AC-1 ("the fields pane's body is scrollable") had no oracle at any layer: jsdom
// computes no layout, so ExtractionFields.test.tsx pins the declaration triple and stops. This
// subtask mounts the pane inside the shell whose `minHeight: 0` chain makes the scroll real, so
// the claim becomes measurable here. Written in EXTR-11-07 for that reason.
test('EXTR11-E2E-02b (AC-6): the fields pane scrolls its rows under a fixed header', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-07 scroll')
  const detail = await openExtractionReview(page)
  expect(detail.fields.length, 'no field on this document -- the overflow below cannot be produced').toBeGreaterThan(0)

  const pane = page.getByTestId('extraction-fields')
  // The pane's two children, in the artboard's order (`:225` header over `:230` body) --
  // ExtractionFields.test.tsx pins that shape from the other side, so this index is not a guess.
  const header = pane.locator('> div').first()
  const paneBody = pane.locator('> div').nth(1)

  const entryViewport = page.viewportSize()
  let measured: Record<string, number | boolean> = {}
  try {
    // Short, not narrow: the rows have to outgrow the box before "scrollable" means anything.
    await page.setViewportSize({ width: 1280, height: 320 })

    measured = await settledRead(async () => {
      const box = await paneBody.evaluate((el) => ({ scrollHeight: el.scrollHeight, clientHeight: el.clientHeight, scrollTop: el.scrollTop }))
      const paneBox = await pane.evaluate((el) => ({ scrollHeight: el.scrollHeight, clientHeight: el.clientHeight }))
      const headerBox = await header.boundingBox()
      return { ...box, paneScroll: paneBox.scrollHeight - paneBox.clientHeight, headerTop: headerBox?.y ?? -1 }
    }, 'fields pane scroll geometry')

    // The precondition, asserted rather than assumed. A failure here is the containment
    // chain, not the fixture: if the shell's `minHeight: 0` column does not bound the pane,
    // the whole screen grows and `.pf-scroll` scrolls the page instead.
    expect(
      measured.scrollHeight as number,
      `the fields body is ${measured.scrollHeight}px of content in a ${measured.clientHeight}px box -- nothing to scroll, so the claim below is vacuous`,
    ).toBeGreaterThan((measured.clientHeight as number) + 1)

    const headerTopBefore = measured.headerTop as number
    await paneBody.evaluate((el) => {
      el.scrollTop = el.scrollHeight
    })

    const after = await settledRead(async () => {
      const scrollTop = await paneBody.evaluate((el) => el.scrollTop)
      const headerBox = await header.boundingBox()
      return { scrollTop, headerTop: headerBox?.y ?? -1 }
    }, 'fields pane after scrolling')

    expect(after.scrollTop, 'the fields body did not move -- it is not the scroller').toBeGreaterThan(0)
    // The header is `flex: none` (`:225`) above the scroller, never inside it.
    expect(
      Math.abs(after.headerTop - headerTopBefore),
      `the pane header moved ${(after.headerTop - headerTopBefore).toFixed(1)}px -- it scrolled away with the rows`,
    ).toBeLessThanOrEqual(1)
    expect(measured.paneScroll as number, 'the pane itself scrolls, so the header is inside the scroller').toBeLessThanOrEqual(1)

    measured = { ...measured, scrolledTo: after.scrollTop, headerDrift: after.headerTop - headerTopBefore }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  await testInfo.attach('extraction-fields-scroll.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// EXTR-11-08's own layout claim. This subtask adds a second full-width control to a shipped
// card, which changes that card's vertical rhythm -- so the two controls clearing each other,
// and both still spanning the card's content box, is written here rather than in EXTR-11-09.
// No review screen is opened: the card on the invoice detail is the whole subject.
test('EXTR11-E2E-07 (AC-6): the entry control and its sibling clear each other on the card', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-08 card rhythm')

  const card = page.getByTestId('source-document-card')
  const control = page.getByTestId('open-extraction-review')
  const sibling = page.getByTestId('view-source-document')

  await expect(control, 'the entry control must be on the invoice detail').toBeVisible({ timeout: 60_000 })
  await expect(sibling, 'its sibling must still be there').toBeVisible()

  const measured: { width: number; control: Rect; sibling: Rect; overlap: Rect }[] = []
  const entryViewport = page.viewportSize()
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [c, s] = await Promise.all([control.boundingBox(), sibling.boundingBox()])
        return { c, s }
      }, `card control clearance at ${width}px`)

      expect(m.c && m.s, `both controls must render at ${width}px`).toBeTruthy()
      // Non-empty first: two collapsed rects clear each other on both axes and pass vacuously.
      expect(m.c!.height, `the entry control has no height at ${width}px`).toBeGreaterThan(0)
      expect(m.s!.height, `view-source-document has no height at ${width}px`).toBeGreaterThan(0)

      const overlap = overlapOf(m.c as Rect, m.s as Rect)
      expect(
        rectsOverlap(m.c as Rect, m.s as Rect),
        `the two controls share ${overlap.width}x${overlap.height}px at ${width}px`,
      ).toBe(false)
      // Beneath, not merely elsewhere: `marginTop: 12` is what stacks them, and a control
      // that floated above its sibling would clear it just as well.
      expect(
        m.c!.y,
        `the entry control sits above view-source-document at ${width}px`,
      ).toBeGreaterThanOrEqual(m.s!.y + m.s!.height)

      measured.push({ width, control: m.c as Rect, sibling: m.s as Rect, overlap })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])

  // Both fill the card's content box, so neither is a half-width button that happens to
  // clear the other. The card pads 16/18, so the expected gap is 18px a side -- comfortably
  // inside the helper's default slack, and the attachment below records what was measured.
  const controlFit = await assertFillsColumn(page, control, card, 'the entry control in the card content box')
  const siblingFit = await assertFillsColumn(page, sibling, card, 'view-source-document in the card content box')

  await testInfo.attach('extraction-entry-control-rhythm.json', {
    body: JSON.stringify({ measured, controlFit, siblingFit }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-11-09 · the deployed proof: the journey, the two-page fixture, the fidelity diff ---
//
// The FINAL subtask. `Test-first: n/a` -- at order 9 of 9 every subject already exists, so these
// specs cannot be red for the right reason; the subtask that built a thing wrote that thing's
// deployed assertion (05-08 above). What genuinely remains is the journey end to end, the
// two-page navigation AC-5's canvas half had no subject for, and the AC-8 fidelity diff.
//
// WHAT THIS SECTION CANNOT DELIVER, stated rather than faked: AC-5's second clause -- "the page
// a field lives on is reachable in one action from that field" -- has NO deployed oracle. Every
// mock region sits on page 1 (`grep -c 'Page: [2-9]' internal/extraction/mock.go` -> 0) and
// every lever that would move one is outside this story's fence (`D-16`). The two-page fixture
// below yields two FRAMES; it does not yield a field pointing at page 2, and nothing here
// pretends otherwise. The clause is verified at component level only, in EXTR-11-05.

test('EXTR11-E2E-01/09 (AC-2): the review screen opens from the invoice detail, and the journey raises no console error', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  // A page image is fetched with a bare `fetch` and a bearer header, and a refusal is swallowed
  // into an inline panel with NO console error (ExtractionCanvas.tsx's `.catch` -> 'error'
  // slot). So the console gate alone cannot see a broken page route; this observer can.
  //
  // GET only, as openExtractionReview's own waiter is: the page fetch carries an Authorization
  // header to another origin, so the browser sends a CORS preflight first, and an OPTIONS
  // answered 204 would read as a refusal here on a perfectly correct build.
  const pageImageResponses: { url: string; status: number }[] = []
  page.on('response', (r) => {
    if (r.request().method() !== 'GET') return
    if (/\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}\/pages\/\d+$/.test(new URL(r.url()).pathname)) {
      pageImageResponses.push({ url: new URL(r.url()).pathname, status: r.status() })
    }
  })

  await extractOneDocument(page, 'EXTR-11-09 journey')

  // -- EXTR11-E2E-01 -- the entry control is ENABLED, then opens the screen.
  //
  // EXTR11-E2E-07 measures this control's geometry and never asks whether it works: a deployed
  // /v1/extractions lookup that 500s leaves it permanently disabled, and -07 would pass over
  // the corpse. This is the row that asks. `toBeEnabled` polls, which is required rather than
  // convenient -- the lookup is CHAINED behind the source-document read (InvoiceDetail.tsx:201-219),
  // so the control is legitimately disabled for a beat after the detail paints.
  const control = page.getByTestId('open-extraction-review')
  await expect(control, 'the entry control must be on the invoice detail').toBeVisible({ timeout: 60_000 })
  await expect(control, 'the entry control must be ENABLED after a settled extraction').toBeEnabled({ timeout: 60_000 })

  const detail = await openExtractionReview(page)

  // "at least one page frame", off the wire the SPA itself consumed rather than a literal.
  // `[data-page]` inside the ground, not a testid prefix: `extraction-page-image-N` is itself
  // prefixed by `extraction-page-`, and `data-page` is the attribute the canvas's own observer
  // resolves frames by (ExtractionCanvas.tsx:248).
  expect(detail.pages.length, 'a settled job with no page rows cannot prove a canvas').toBeGreaterThan(0)
  const frames = page.getByTestId('extraction-ground').locator('[data-page]')
  await expect(frames, 'one frame per wire page').toHaveCount(detail.pages.length)

  // -- EXTR11-E2E-09 -- the journey raises no error, on two independent instruments.
  //
  // The floor first: the page route must actually have been exercised, or the status sweep
  // below is a claim about an empty list.
  await expect(page.getByTestId('extraction-page-image-1'), "page 1's bytes must load").toBeVisible({ timeout: 60_000 })
  expect(pageImageResponses.length, 'no page image was ever requested -- the status sweep below is vacuous').toBeGreaterThan(0)
  expect(
    pageImageResponses.filter((r) => r.status !== 200),
    `a page image was refused, and the screen swallows that silently:\n${JSON.stringify(pageImageResponses)}`,
  ).toEqual([])

  await testInfo.attach('extraction-review-entry.json', {
    body: JSON.stringify(
      { jobId: detail.id, state: detail.state, pages: detail.pages, fields: detail.fields.length, pageImageResponses },
      null,
      2,
    ),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR11-E2E-05 (AC-5): a two-page document is navigable', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-09 two pages', {
    name: 'native_invoice_2p.pdf',
    buffer: uniqueTwoPagePdfBytes(),
  })
  const detail = await openExtractionReview(page)

  // 1. TWO page-image rows really landed. Off the wire, not off the fixture: `pages` is read
  //    from extraction_page_images, so this is the assertion that go-pdfium rendered the second
  //    page and the sink stored it -- not merely that the PDF this repo committed says
  //    `/Count 2`. A one-page fixture makes every assertion below vacuous, so it is first.
  expect(
    detail.pages.map((p) => p.page),
    'the two-page fixture must produce two stored page images, in page order',
  ).toEqual([1, 2])

  const frame1 = page.getByTestId('extraction-page-1')
  const frame2 = page.getByTestId('extraction-page-2')
  await expect(frame1, 'frame 1 must render').toBeVisible({ timeout: 60_000 })
  await expect(frame2, 'frame 2 must render').toBeVisible({ timeout: 60_000 })

  // 2. The toolbar's meta line counts them. Derived from the wire, never typed: `2 PAGES` as a
  //    literal would still pass on a hard-coded string. The singular/plural fork is
  //    docMetaLine's own (extractionReview.ts:131-139), and this is its only deployed reading
  //    at a count above one.
  const expectedPages = `${detail.pages.length} PAGES`
  const metaText = (await page.getByTestId('extraction-doc-meta').innerText()).trim()
  expect(metaText, `the meta line must count the pages (got "${metaText}")`).toContain(expectedPages)
  expect(expectedPages, 'the fixture must be plural, or the assertion above proves nothing').toBe('2 PAGES')

  // 3. The ground is the scroller. `D-17` dropped the page rail, so continuous scroll IS the
  //    navigation and this is the whole of AC-5's first clause on the deployed build.
  const ground = page.getByTestId('extraction-ground')
  const before = await settledRead(async () => {
    const [scroll, g, f1, f2] = await Promise.all([
      ground.evaluate((el) => ({ scrollTop: el.scrollTop, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight })),
      ground.boundingBox(),
      frame1.boundingBox(),
      frame2.boundingBox(),
    ])
    return { scroll, g, f1, f2 }
  }, 'two-page ground geometry')

  expect(before.g && before.f1 && before.f2, 'the ground and both frames must render').toBeTruthy()
  // Non-empty first: a collapsed ground and a collapsed frame overlap vacuously.
  expect(before.f2!.height, 'frame 2 has no height').toBeGreaterThan(0)
  expect(before.g!.height, 'the ground has no height').toBeGreaterThan(0)
  expect(
    before.scroll.scrollHeight,
    `the ground holds ${before.scroll.scrollHeight}px in a ${before.scroll.clientHeight}px box -- there is nothing to scroll`,
  ).toBeGreaterThan(before.scroll.clientHeight + 1)

  const overlapBefore = overlapOf(before.f2 as Rect, before.g as Rect)

  // 4. Scrolling to the bottom brings frame 2 into the ground's visible band.
  await ground.evaluate((el) => {
    el.scrollTop = el.scrollHeight
  })
  const after = await settledRead(async () => {
    const [scrollTop, g, f2] = await Promise.all([
      ground.evaluate((el) => el.scrollTop),
      ground.boundingBox(),
      frame2.boundingBox(),
    ])
    return { scrollTop, g, f2 }
  }, 'two-page ground after scrolling')

  expect(after.scrollTop, 'the ground did not move -- it is not the scroller').toBeGreaterThan(0)
  const overlapAfter = overlapOf(after.f2 as Rect, after.g as Rect)

  // Half the ground's height, not a single pixel: `overlapOf` clamps per axis, so `> 0` passes
  // on a one-pixel sliver at the very bottom edge, which is not "reached".
  expect(
    overlapAfter.height,
    `frame 2 covers only ${overlapAfter.height.toFixed(1)}px of the ground's ${after.g!.height.toFixed(1)}px band after scrolling to the bottom`,
  ).toBeGreaterThanOrEqual(after.g!.height / 2)

  // The control needle. Without it the assertion above would also pass on a ground that never
  // scrolled because both frames already fit -- which is the one shape that would make "a
  // multi-page document is navigable" unproven while reporting green.
  expect(
    overlapAfter.height,
    `frame 2 was already ${overlapBefore.height.toFixed(1)}px into view before the scroll -- the scroll proved nothing`,
  ).toBeGreaterThan(overlapBefore.height)

  await testInfo.attach('extraction-two-page-navigation.json', {
    body: JSON.stringify(
      {
        wirePages: detail.pages,
        metaText,
        scroll: before.scroll,
        scrolledTo: after.scrollTop,
        overlapBefore,
        overlapAfter,
        groundHeight: after.g!.height,
      },
      null,
      2,
    ),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR11-E2E-11 · the AC-8 fidelity diff -----------------------------------------------
//
// AC-8 makes `.ralph/design/Recognition Review.dc.html` the spec, and a spec is only a spec if
// something reads it. Every other AC-8 assertion in this repository reads a STYLE OBJECT in
// jsdom (ExtractionCanvas.test.tsx, ExtractionFields.test.tsx, ExtractionReview.test.tsx),
// which proves the declaration was written and nothing about what the browser resolved. This
// row reads `getComputedStyle` off the deployed surface.
//
// THE ARTBOARD IS NOT IN THE REPOSITORY. `.gitignore:61` ignores `.ralph/`, so no CI checkout
// carries the file and no spec can parse it at run time. The `artboard` column below is a
// TRANSCRIPTION from the untruncated 62KB source, each row carrying the line it came from --
// stated here because a transcription can be wrong in a way a parse cannot, and a later reader
// deserves to know which of the two this is.
type FidelityRow = {
  element: string
  property: string
  /** The declaration in Recognition Review.dc.html, verbatim. */
  artboard: string
  source: string
  /** What the deployed surface must resolve to. Equal to `artboard` unless `deviation` says why not. */
  expected: string
  deviation: string | null
}

// A placeholder rather than a literal: `var(--bg-2)` resolves to whatever the deployed theme
// resolves it to, and the diff compares the toolbar against that token rather than against a
// colour typed here. Filled in from the probe below.
const BG_2_TOKEN = '<var(--bg-2)>'

const FIDELITY: FidelityRow[] = [
  // The document pane. `flex: 1 1 auto; min-width: 0`.
  { element: 'extraction-canvas', property: 'flex-grow', artboard: '1', source: ':34', expected: '1', deviation: null },
  { element: 'extraction-canvas', property: 'flex-shrink', artboard: '1', source: ':34', expected: '1', deviation: null },
  { element: 'extraction-canvas', property: 'flex-basis', artboard: 'auto', source: ':34', expected: 'auto', deviation: null },
  { element: 'extraction-canvas', property: 'min-width', artboard: '0px', source: ':34', expected: '0px', deviation: null },

  // The fields pane. `width: 620px; flex: 1 1 620px; min-width: 470px`.
  { element: 'extraction-fields', property: 'flex-grow', artboard: '1', source: ':223', expected: '1', deviation: null },
  { element: 'extraction-fields', property: 'flex-shrink', artboard: '1', source: ':223', expected: '1', deviation: null },
  { element: 'extraction-fields', property: 'flex-basis', artboard: '620px', source: ':223', expected: '620px', deviation: null },
  { element: 'extraction-fields', property: 'min-width', artboard: '470px', source: ':223', expected: '470px', deviation: null },

  // The page frame, read at zoom 100 (asserted below). `min-width: 560px; max-width: 640px;
  // margin: 0 auto 18px; border: 1px solid var(--line-2); padding: 44px 42px`.
  { element: 'extraction-page-1', property: 'min-width', artboard: '560px', source: ':72', expected: '560px', deviation: null },
  { element: 'extraction-page-1', property: 'max-width', artboard: '640px', source: ':72', expected: '640px', deviation: null },
  { element: 'extraction-page-1', property: 'margin-top', artboard: '0px', source: ':72', expected: '0px', deviation: null },
  { element: 'extraction-page-1', property: 'margin-bottom', artboard: '18px', source: ':72', expected: '18px', deviation: null },
  { element: 'extraction-page-1', property: 'border-top-width', artboard: '1px', source: ':72', expected: '1px', deviation: null },
  { element: 'extraction-page-1', property: 'border-right-width', artboard: '1px', source: ':72', expected: '1px', deviation: null },
  { element: 'extraction-page-1', property: 'border-bottom-width', artboard: '1px', source: ':72', expected: '1px', deviation: null },
  { element: 'extraction-page-1', property: 'border-left-width', artboard: '1px', source: ':72', expected: '1px', deviation: null },
  // The ONE stated deviation in this table. The artboard's page card is HTML invoice text, and
  // `44px 42px` is that text's inset; an image page has no analogue. It is also mandatory
  // rather than cosmetic: the highlight is positioned in percentages against the frame's
  // PADDING box while the image fills its CONTENT box, and padding is the only thing that
  // separates the two -- any non-zero value drifts every region (story `P-29`, :2170-2175).
  ...(['padding-top', 'padding-right', 'padding-bottom', 'padding-left'] as const).map((property) => ({
    element: 'extraction-page-1',
    property,
    artboard: property === 'padding-top' || property === 'padding-bottom' ? '44px' : '42px',
    source: ':72',
    expected: '0px',
    deviation: 'the artboard pads its page card for HTML invoice text; a rendered image has no such inset, and padding on a positioned frame drifts every highlight (P-29)',
  })),

  // The ground. `flex: 1; min-height: 0; overflow: auto` -- BOTH axes, which is what a zoomed
  // page needs and what `D-18` cites to keep the zoom control.
  { element: 'extraction-ground', property: 'overflow-x', artboard: 'auto', source: ':65', expected: 'auto', deviation: null },
  { element: 'extraction-ground', property: 'overflow-y', artboard: 'auto', source: ':65', expected: 'auto', deviation: null },

  // The toolbar. `gap: 11px; padding: 10px 16px; background: var(--bg-2)`.
  { element: 'extraction-toolbar', property: 'padding-top', artboard: '10px', source: ':36', expected: '10px', deviation: null },
  { element: 'extraction-toolbar', property: 'padding-right', artboard: '16px', source: ':36', expected: '16px', deviation: null },
  { element: 'extraction-toolbar', property: 'padding-bottom', artboard: '10px', source: ':36', expected: '10px', deviation: null },
  { element: 'extraction-toolbar', property: 'padding-left', artboard: '16px', source: ':36', expected: '16px', deviation: null },
  { element: 'extraction-toolbar', property: 'column-gap', artboard: '11px', source: ':36', expected: '11px', deviation: null },
  { element: 'extraction-toolbar', property: 'row-gap', artboard: '11px', source: ':36', expected: '11px', deviation: null },
  { element: 'extraction-toolbar', property: 'background-color', artboard: BG_2_TOKEN, source: ':36', expected: BG_2_TOKEN, deviation: null },

  // The READ ONLY pill. `font-size: 9px; letter-spacing: 0.09em; border-radius: 999px`.
  // letter-spacing is asserted separately, in ems -- see below.
  { element: 'extraction-read-only', property: 'font-size', artboard: '9px', source: ':43', expected: '9px', deviation: null },
  { element: 'extraction-read-only', property: 'border-top-left-radius', artboard: '999px', source: ':43', expected: '999px', deviation: null },
  { element: 'extraction-read-only', property: 'border-top-right-radius', artboard: '999px', source: ':43', expected: '999px', deviation: null },
  { element: 'extraction-read-only', property: 'border-bottom-right-radius', artboard: '999px', source: ':43', expected: '999px', deviation: null },
  { element: 'extraction-read-only', property: 'border-bottom-left-radius', artboard: '999px', source: ':43', expected: '999px', deviation: null },
]

// EXCLUDED BY NAME, with the reason, per this subtask's own AC. Both are elements this story
// builds that the artboard does not have, and a diff against an absent element compares nothing
// and reports all-clear:
//   - the ZOOM CONTROL (`D-18`). The artboard's pages are HTML text at 11.5px that stays crisp
//     at any size; ours are rendered images clamped to a 640px ceiling, and the user's task is
//     reading a TIN's last four characters off one.
//   - the PAGE COUNT in the toolbar's meta line (`D-17`). The artboard's toolbar carries a
//     `{{ docMeta }}` placeholder (`:41`) whose content it never fixes, and the page rail that
//     would have carried a `PAGE n OF m` stamp is dropped.
// Each is asserted PRESENT on the deployed surface below: "excluded" has to exclude something
// real, or a mistyped testid would silently exclude nothing and read as diligence.

test("EXTR11-E2E-11 (AC-8): the deployed surface matches the artboard's resolved values", async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-09 fidelity')
  await openExtractionReview(page)

  // The page frame's band is `560 * zoom` / `640 * zoom` (extractionReview.ts:114-118), so the
  // artboard's 560/640 are only the right expectation at zoom 100. Asserted, not assumed.
  await expect(
    page.getByTestId('extraction-zoom-100'),
    "the frame rows below are the artboard's numbers only at zoom 100",
  ).toHaveAttribute('aria-pressed', 'true')

  const elements = [...new Set(FIDELITY.map((r) => r.element))]
  // Waited for, not assumed present: a missing element would otherwise fail the read as a hard
  // error where it can equally be a paint this assertion arrived one frame ahead of.
  for (const testid of elements) {
    await expect(page.getByTestId(testid), `${testid} must render before its rows are read`).toBeVisible({ timeout: 30_000 })
  }

  const properties: Record<string, string[]> = {}
  for (const row of FIDELITY) (properties[row.element] ??= []).push(row.property)
  // Read once, in one frame: two evaluates across a re-render can disagree.
  const measured = await page.evaluate(
    ({ elements, properties }: { elements: string[]; properties: Record<string, string[]> }) => {
      const out: Record<string, Record<string, string> | null> = {}
      for (const testid of elements) {
        const el = document.querySelector(`[data-testid="${testid}"]`)
        if (!el) {
          out[testid] = null
          continue
        }
        const cs = getComputedStyle(el)
        const values: Record<string, string> = {}
        for (const p of properties[testid]) values[p] = cs.getPropertyValue(p)
        // The two read outside the row table, both recorded so the artifact is complete.
        values['letter-spacing'] = cs.getPropertyValue('letter-spacing')
        values['margin-left'] = cs.getPropertyValue('margin-left')
        values['margin-right'] = cs.getPropertyValue('margin-right')
        out[testid] = values
      }
      // The token, resolved in the toolbar's OWN custom-property environment rather than
      // compared against a colour typed into this file. `--bg-2` is declared on `.asc-app`, so
      // a probe outside that subtree would resolve to nothing.
      const host = document.querySelector('[data-testid="extraction-toolbar"]')
      let bg2 = ''
      if (host) {
        const probe = document.createElement('div')
        probe.style.cssText = 'position:absolute;visibility:hidden;pointer-events:none;width:0;height:0;background:var(--bg-2)'
        host.appendChild(probe)
        bg2 = getComputedStyle(probe).backgroundColor
        probe.remove()
      }
      return { out, bg2 }
    },
    { elements, properties },
  )

  for (const testid of elements) {
    expect(measured.out[testid], `${testid} did not render -- every row below it would compare nothing`).not.toBeNull()
  }
  expect(measured.bg2, 'var(--bg-2) resolved to nothing -- the toolbar background row would compare two empty strings').not.toBe('')
  expect(measured.bg2, 'var(--bg-2) resolved to transparent -- the toolbar background row would be vacuous').not.toBe('rgba(0, 0, 0, 0)')

  // `declared` keeps the raw table entry -- the placeholder included -- so the artifact still
  // shows that the toolbar row compared a TOKEN and not a colour typed into this file. Both
  // `expected` and `artboard` resolve to the same probe reading, which is what keeps the
  // "expected equals artboard unless it is a stated deviation" check below honest for that row.
  const table = FIDELITY.map((row) => {
    const expected = row.expected === BG_2_TOKEN ? measured.bg2 : row.expected
    const artboard = row.artboard === BG_2_TOKEN ? measured.bg2 : row.artboard
    const deployed = measured.out[row.element]![row.property] ?? ''
    return { ...row, declared: row.artboard, artboard, expected, deployed, match: deployed === expected }
  })

  // The floor: a table that silently shrank would report all-clear on whatever it still held.
  expect(table.length, 'the fidelity table lost rows').toBe(FIDELITY.length)
  for (const row of table) {
    expect(row.deployed, `${row.element} has no resolved ${row.property} -- an empty string matches nothing`).not.toBe('')
    expect(
      row.deployed,
      `${row.element} · ${row.property}: deployed ${row.deployed}, expected ${row.expected} (artboard declares ${row.declared}, Recognition Review.dc.html${row.source})`,
    ).toBe(row.expected)
  }

  // Exactly the four padding rows deviate, and each says why. A silent divergence added later
  // fails here rather than passing as "documented".
  const deviations = table.filter((r) => r.deviation !== null)
  expect(
    deviations.map((r) => `${r.element}·${r.property}`).sort(),
    'the set of stated deviations from the artboard changed',
  ).toEqual(['extraction-page-1·padding-bottom', 'extraction-page-1·padding-left', 'extraction-page-1·padding-right', 'extraction-page-1·padding-top'])
  for (const row of deviations) {
    expect(row.expected, `${row.element} · ${row.property} is listed as a deviation but matches the artboard`).not.toBe(row.artboard)
  }
  for (const row of table.filter((r) => r.deviation === null)) {
    expect(row.expected, `${row.element} · ${row.property} silently expects something the artboard does not declare`).toBe(row.artboard)
  }

  // -- The two rows a string compare cannot carry ------------------------------------------
  //
  // 1. `margin: 0 auto 18px` (`:72`). Chrome resolves an `auto` margin to its USED value, so
  //    there is no 'auto' string to compare -- the observable form of the pair is that the two
  //    used values agree. The vertical halves are exact rows in the table above.
  //
  //    That pair agrees only where the frame FITS its column. The 560px floor asserted above
  //    exceeds the document pane's column at this file's default 1280px viewport, and CSS 2.1
  //    10.3.3 then sets margin-left to 0 and solves for margin-right -- correct rendering, and
  //    what EXTR11-E2E-06 measures from the other side. So this row widens the window until the
  //    frame fits and asserts the centring where it means something. Guarding the assertion
  //    behind a `fits` check instead would skip it at every width this test runs at.
  const entryViewport = page.viewportSize()
  await page.setViewportSize({ width: 1920, height: 1080 })
  const frame = await settledRead(
    () =>
      page.evaluate(() => {
        const el = document.querySelector('[data-testid="extraction-page-1"]')
        const pad = document.querySelector('[data-testid="extraction-ground"] > div')
        if (!el || !pad) return null
        const cs = getComputedStyle(el)
        return {
          marginLeft: cs.marginLeft,
          marginRight: cs.marginRight,
          frameWidth: el.getBoundingClientRect().width,
          padWidth: pad.getBoundingClientRect().width,
        }
      }),
    'the page frame margins at 1920px',
  )
  expect(frame, 'the frame and its column must both render at 1920px').not.toBeNull()
  expect(
    frame!.frameWidth,
    `the frame still overflows its column at 1920px (frame ${frame!.frameWidth}, column ${frame!.padWidth}), so the centring below would pin CSS 2.1 10.3.3's overconstraint rule rather than 'margin: 0 auto'`,
  ).toBeLessThanOrEqual(frame!.padWidth + 1)
  expect(
    frame!.marginLeft,
    `the frame's auto margins disagree (left ${frame!.marginLeft}, right ${frame!.marginRight}) -- 'margin: 0 auto 18px' is what centres it`,
  ).toBe(frame!.marginRight)
  if (entryViewport) await page.setViewportSize(entryViewport)

  // 2. `letter-spacing: 0.09em` at `font-size: 9px` (`:43`). Chrome computes it to px, so a
  //    string compare would pin a rounding rather than the artboard's ratio. 0.02px on 0.81px
  //    is two orders finer than any real drift (the next tracking step in this file is 0.04em).
  const pill = measured.out['extraction-read-only']!
  const pillFontPx = parseFloat(pill['font-size'])
  const pillTrackPx = parseFloat(pill['letter-spacing'])
  expect(Number.isFinite(pillFontPx) && Number.isFinite(pillTrackPx), 'the pill resolved no font metrics').toBe(true)
  expect(
    Math.abs(pillTrackPx - 0.09 * pillFontPx),
    `the READ ONLY pill tracks ${pillTrackPx}px on ${pillFontPx}px, and the artboard's 0.09em is ${(0.09 * pillFontPx).toFixed(3)}px`,
  ).toBeLessThanOrEqual(0.02)

  // -- The exclusions, each proved to exclude something real --------------------------------
  for (const zoom of [50, 100, 150]) {
    await expect(
      page.getByTestId(`extraction-zoom-${zoom}`),
      `the zoom control is excluded from this diff by name (D-18); a missing ${zoom}% segment would make that exclusion a claim about nothing`,
    ).toBeVisible()
  }
  const meta = (await page.getByTestId('extraction-doc-meta').innerText()).trim()
  expect(
    meta,
    `the meta line's page count is excluded from this diff by name (D-17); it must exist to be excluded (got "${meta}")`,
  ).toMatch(/\b\d+ PAGES?\b/)

  await testInfo.attach('extraction-fidelity-diff.json', {
    body: JSON.stringify(
      {
        artboard: '.ralph/design/Recognition Review.dc.html (gitignored; the artboard column is a transcription)',
        excludedByName: [
          { element: 'extraction-zoom-{50,100,150}', reason: 'the artboard has no zoom control (D-18)' },
          { element: 'extraction-doc-meta page count', reason: 'the artboard fixes no content for {{ docMeta }} and has no page stamp (D-17)' },
        ],
        table,
        autoMargins: {
          measuredAtWidth: 1920,
          left: frame?.marginLeft ?? null,
          right: frame?.marginRight ?? null,
          frameWidth: frame?.frameWidth ?? null,
          columnWidth: frame?.padWidth ?? null,
        },
        readOnlyTracking: { fontSizePx: pillFontPx, letterSpacingPx: pillTrackPx, artboardEm: 0.09 },
        metaLine: meta,
      },
      null,
      2,
    ),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
