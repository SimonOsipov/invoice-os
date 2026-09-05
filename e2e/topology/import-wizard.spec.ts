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

import { test, expect, type Locator, type Page, type Request, type Route } from '@playwright/test'
import {
  login,
  apiBase,
  createEntity,
  listInvoices,
  approveUntilClosed,
  firmApproverTokens,
  getAuditLog,
  getExtractions,
  getExtractionDetail,
  postFieldCorrection,
  PERSONAS,
  type CorrectionResponse,
  type ExtractionDetail,
  type ExtractionJob,
  type ExtractionJobsResponse,
  type ExtractionReason,
  type ExtractionRegion,
} from '../api/client'
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
// (D3 protocol, ../api/validation.spec.ts:5-22), unwrapped: the api run ahead of this one
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
  // and close their runs before select-all, or the server answers can_submit:false. The
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

  // The kept row's blocked reason lives in the checkbox title only -- no line of its own,
  // no cell of its own. The toBeDisabled() above is this block's non-vacuity control. No
  // height parity here: the KEPT badge stacks under the verdict pill, so this row's
  // verdict cell is legitimately taller for a reason this story does not change.
  const violateBox = violateRow.getByTestId('review-select')
  const reviewTitle = (await violateBox.getAttribute('title')) ?? ''
  expect(reviewTitle.length, 'a kept, non-validated row must carry a real reason in its title').toBeGreaterThanOrEqual(20)

  const reviewHead = page.getByTestId('review-table').locator('.pf-list-head')
  const reviewHeadCells = await reviewHead.locator('> *').count()
  expect(reviewHeadCells, 'the review head never rendered -- the comparison below would be vacuous').toBeGreaterThan(1)
  await expect(violateRow.locator('> *'), 'a blocked review row must add no cell of its own').toHaveCount(reviewHeadCells)

  await expect(violateRow, 'the row must not print the sentence its title carries').not.toContainText(reviewTitle)

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
// Committed by EXTR-09-03, re-pointed at the rich fixture by EXTR-18-05 so the wire-derived
// specs below read real field content instead of the mock's fixed shape.
const NATIVE_INVOICE_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'rich_invoice.pdf'))

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

// Committed by EXTR-09-03. Image-only scan, no text layer -- settles document_text_layer =
// unreadable. Same recipe, same permanent-enqueue-key reason as uniquePdfBytes().
const SCANNED_INVOICE_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'scanned_invoice.pdf'))

function uniqueScannedPdfBytes(): Buffer {
  return Buffer.concat([SCANNED_INVOICE_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// Committed by EXTR-09-03. Image-only like SCANNED_INVOICE_PDF, but OCR-readable -- the pair
// proves "no text layer" and "unreadable" are not the same verdict. Same recipe.
const DENSE_INVOICE_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'dense_invoice.pdf'))

function uniqueDensePdfBytes(): Buffer {
  return Buffer.concat([DENSE_INVOICE_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// A file named *.pdf whose bytes are NOT a PDF: classification is extension-only and
// upload only hashes+PUTs bytes (classify.go / service.go), so this sails through
// selection and upload, then fails pdfium.OpenDocument on every one of River's 3
// attempts (worker.go) and dead-letters deterministically. Fresh bytes per call, same
// permanent-enqueue-key reason as uniquePdfBytes().
function uniqueGarbageBytes(): Buffer {
  return Buffer.concat([Buffer.from('not a pdf at all'), Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// The canonical DOCX type, on both sides of the wire: ACCEPTED_PICKED_TYPES (lib/importFlow.ts)
// and acceptedDocumentTypes (internal/extraction/classify.go) carry this exact spelling.
const DOCX_MIME = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'

// A byte-for-byte copy of internal/extraction/testdata/invoice.docx, the fixture the Go
// golden pins (corpus_wired_db_test.go: ASC-2026-0919 / 2026-08-14 / 4300.00). That golden
// was recorded through a LOCAL docling; EXTR15-E2E-03 is the first read of these bytes by
// the DEPLOYED sidecar.
const GOLDEN_INVOICE_DOCX = readFileSync(join(DOCUMENT_FIXTURES, 'golden_invoice.docx'))

// Fresh bytes per pick, same permanent-enqueue-key reason as uniquePdfBytes() -- but a zip
// cannot take a trailing comment the way a PDF can. What moves the content hash here is the
// zip's OWN end-of-central-directory comment: the last 22 bytes are the EOCD record, its
// comment-length field is the final two, and every reader finds the EOCD by scanning back for
// its signature. The archive stays readable and every member's offset is untouched.
// deployedProofGuards.test.ts unzips a freshened copy and reads the golden's number back out.
function uniqueGoldenDocxBytes(): Buffer {
  const comment = Buffer.from(`e2e-${crypto.randomUUID()}`, 'utf8')
  const out = Buffer.concat([GOLDEN_INVOICE_DOCX, comment])
  out.writeUInt16LE(comment.length, GOLDEN_INVOICE_DOCX.length - 2)
  return out
}

// A well-formed but EMPTY zip named *.docx -- invoice-surfaces.spec.ts's extr09Docx recipe,
// rebuilt here because importing it would re-register that file's own tests. 22 bytes of EOCD
// (PK\x05\x06 then 16 zero bytes) plus a comment that carries the uniqueness. A DOCX is
// boxless, so the worker skips the render entirely (EXTR-15-02) and this reaches the reader,
// which cannot convert it: docling answers 422 and the job dead-letters at text_not_read.
function uniqueEmptyDocxBytes(): Buffer {
  const comment = Buffer.from(`e2e-${crypto.randomUUID()}`, 'utf8')
  const out = Buffer.alloc(22 + comment.length)
  out.set([0x50, 0x4b, 0x05, 0x06], 0)
  out.writeUInt16LE(comment.length, 20)
  out.set(comment, 22)
  return out
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

// The accepted-types line verbatim — the four types EXTR-15-03 narrowed it to.
const ACCEPTED_LINE = 'ACCEPTED · CSV · XLSX · PDF · DOCX'

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
  await expect(acceptedLine, 'the accepted-types line states every accepted type').toBeVisible()

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
// This upload is *.pdf bytes that are not a PDF, so pdfium fails to open it and the worker
// dead-letters at pages_not_rendered (worker.go). EXTR-15-04 gives that kind its own sentence,
// so the needle must be a fragment of THAT sentence and of no other kind's --
// documentRun.test.ts's TS15-10b is this literal's only local oracle.
const DEAD_LETTER_NEEDLE = 'it may be damaged, or protected by a password'

// EXTR-15-12 (T1). The WHOLE pages_not_rendered sentence, copied from its sole owner,
// documentRun.ts's deadLetterRefusal. deployedProofGuards.test.ts reads that function's
// `case 'pages_not_rendered'` return and fails if this string is not byte-identical.
const DEAD_LETTER_SENTENCE =
  'This file is a PDF, and the reader could not open it — it may be damaged, or protected by a password. Enter this invoice manually to carry on.'

// The opening of the kind-less sentence -- deadLetterRefusal's `default` arm, which is what
// renders when failure_kind never reaches the wire, and the only arm that puts River's own
// last_error in front of a person. Asserting its ABSENCE is what makes T1 a claim about the
// kind and not merely about some long reason. Guarded against the same source.
const GENERIC_FAILURE_OPENING = 'Reading this document failed'

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
          jobs: [{ id: 'e2e-hold', document_id: goodDocId, state: 'extracting', created_at: new Date().toISOString(), last_error: null, failure_kind: null }],
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
      DEAD_LETTER_NEEDLE,
      { timeout: 90_000 },
    )

    // T1 (EXTR-15-12): the WHOLE sentence, not the fragment above -- a fragment cannot tell a
    // truncated render from a complete one. And the kind-less sentence must be absent: it is
    // the arm that renders when failure_kind is null, and the only one that would put River's
    // raw pdfium error in front of a person.
    await expect(badRow, 'the row must carry the whole pages_not_rendered sentence').toContainText(DEAD_LETTER_SENTENCE)
    await expect(
      badRow,
      'the row fell back to the kind-less sentence -- failure_kind did not reach this render',
    ).not.toContainText(GENERIC_FAILURE_OPENING)

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
  // EXTR-15-01: failure_kind carries no omitempty, so a job that settled cleanly still sends
  // the key with an explicit null -- the deployed proof that the tag was not written with one.
  expect(Object.keys(detail).sort(), 'the top-level key set drifted from internal/extraction/reader.go').toEqual(
    ['document', 'document_id', 'failure_kind', 'fields', 'id', 'pages', 'state'].sort(),
  )
  expect(
    (detail as unknown as Record<string, unknown>).failure_kind,
    'a succeeded job must report a null failure_kind, not a value',
  ).toBeNull()
  expect(Array.isArray(detail.pages), 'pages arrived as null, not []').toBe(true)
  expect(Array.isArray(detail.fields), 'fields arrived as null, not []').toBe(true)
  expect(detail.document, 'document arrived as null').not.toBeNull()
  expect(typeof detail.document, 'document is not an object').toBe('object')
  expect(Object.keys(detail.document).sort()).toEqual(['content_type', 'filename', 'size_bytes', 'stored_at'].sort())
  expect(Object.keys(detail.pages[0]).sort()).toEqual(['height_px', 'page', 'width_px'].sort())
  expect(detail.fields.length, 'a settled job with no fields cannot prove a field key set').toBeGreaterThan(0)
  // EVERY field, never `fields[0]`. The wire is ordered by field_name -- created_at defaults to
  // now() and writeFieldResultsTx writes a job's rows on ONE transaction, so reader.go's
  // `ORDER BY created_at, field_name, ...` degenerates to the name -- and EXTR-13-02's line
  // cells sort ahead of most header readings, which moved `fields[0]` from invoice_number to
  // buyer_tin with no assertion noticing. A read at one index was never a key-set claim.
  for (const f of detail.fields) {
    expect(Object.keys(f).sort(), `${f.name}'s key set drifted from internal/extraction/reader.go`).toEqual(
      ['alternatives', 'corrected', 'name', 'reason', 'region', 'value'].sort(),
    )
  }

  // The field SET itself, twenty-three names. EXTR-18-05 re-pointed this fixture at the wired
  // extractor's own reading (rich_invoice.pdf), so this is transcribed from the real
  // Reconcile(Resolve(...)) result -- computed via `go run ./.ralph/measure/main.go
  // internal/extraction/testdata/rich_invoice.pdf internal/extraction/testdata/rich_invoice.docling.json`,
  // never from mock.go and never from the SPA. This is the only DEPLOYED oracle that the rich
  // fixture reaches the screen unchanged, so a wiring regression must red here and not only in
  // Go. Both sides are sorted in JS, so the database's collation is not what this compares.
  const WIRE_FIELD_SET = [
    'buyer_name',
    'buyer_tin',
    'currency',
    'invoice_number',
    'issue_date',
    'line_items',
    'line_items[1].description',
    'line_items[1].line_total',
    'line_items[1].quantity',
    'line_items[1].unit_price',
    'line_items[2].description',
    'line_items[2].line_total',
    'line_items[2].quantity',
    'line_items[2].unit_price',
    'line_items[3].description',
    'line_items[3].line_total',
    'line_items[3].quantity',
    'line_items[3].unit_price',
    // Row 4 prints Handling and a line total but blank Qty/Unit Price, so the wire OMITS those
    // two cells -- EXTR13-E2E-01's empty-cell arm has no subject without them.
    'line_items[4].description',
    'line_items[4].line_total',
    'subtotal',
    'supplier_name',
    'supplier_tin',
    'total',
    'vat',
  ]
  expect(
    [...detail.fields.map((f) => f.name)].sort(),
    "the wire's field set drifted from mockDefaultResult -- a line the grid needs is missing, or a name changed shape",
  ).toEqual([...WIRE_FIELD_SET].sort())

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

  // EXTR-13-07: the fields pane renders the header vocabulary only -- a line-item cell has its
  // own grid, LineItemGrid -- so the row count below is bounded to it, the same predicate
  // EXTR11-E2E-11 uses at `wireHeaderNames`.
  const wireNamesA = detail.fields.map((f) => f.name)
  const headerNamesA = wireNamesA.filter((n) => !n.startsWith('line_items'))
  expect(headerNamesA.length, 'no header field on this document -- every spill comparison is vacuous').toBeGreaterThan(0)

  const canvas = page.getByTestId('extraction-canvas')
  const fields = page.getByTestId('extraction-fields')
  // The trailing hyphen matters: `extraction-fields` is itself prefixed by `extraction-field`.
  const rows = page.locator('[data-testid^="extraction-field-"]')
  await expect(rows, 'the pane rendered no row for a wire that carries header fields').toHaveCount(headerNamesA.length)

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
      expect(m.rs.length, `no row measured at ${width}px`).toBe(headerNamesA.length)
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

// --- EXTR-12-06 · the reason pills and the corrected marker, on the deployed build ---
//
// Neither row can execute before EXTR-12-09 marks the PR ready: `dev-env.yml` gates deploy and
// E2E on `pull_request.draft == false`. The local oracle is `pnpm -r typecheck` plus
// `playwright test --list`, exactly as the EXTR-11 block above was authored under.

// The story's Invented-copy table. A transcription, deliberately not an import: the SPA is a
// different package, and reading the mapping out of the module under test would assert it
// against itself.
const REASON_PILL: Record<Exclude<ExtractionReason, ''>, string> = {
  unreadable: "COULDN'T READ THIS CLEARLY",
  ambiguous: 'FOUND TWO POSSIBLE VALUES',
  inconsistent: "DOESN'T ADD UP",
  missing: 'NOT FOUND',
}

// internal/extraction/vocabulary.go, HeaderFields -- and the order one Save writes in. The
// file's ONE copy: EXTR-12-09's block below cites this declaration rather than repeating it,
// because two transcriptions of one list drift apart silently. A transcription, deliberately
// not an import: the SPA is a different package, and reading the order out of the module under
// test would assert it against itself. EXTR-13-06's headerFields guard in
// frontend/app/src/lib/wireMirrors.test.ts now compares this copy to the Go slice in order, so
// a drift here reds that file rather than passing quietly.
//
// EXTR-13-02 widened the mock's default result with 16 line-item names; EXTR-13-07 gives them
// their own grid, LineItemGrid, and the header pane renders none of them -- so the per-field
// loop below is bounded to this vocabulary.
const VOCABULARY = [
  'invoice_number',
  'issue_date',
  'supplier_tin',
  'supplier_name',
  'buyer_tin',
  'buyer_name',
  'currency',
  'subtotal',
  'vat',
  'total',
]

test('EXTR12-E2E-01 (AC-2): every reason the extractor reported renders its pill', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-12-06 pills')
  const detail = await openExtractionReview(page)

  // Off the wire the SPA itself consumed, never a literal copied out of mock.go. worker.go's
  // switch makes `unreadable` (on document_text_layer) mutually exclusive with the other three
  // codes on one document -- it replaces the result set wholesale -- so a readable document like
  // the rich fixture can carry at most missing/ambiguous/inconsistent. EXTR-18-07's spec covers
  // the fourth code on its own (unreadable) fixture.
  const flagged = detail.fields.filter((f) => f.reason !== '')
  const codes = new Set(flagged.map((f) => f.reason))
  expect(codes.size, `this document reported ${[...codes].join(', ')} -- fewer than the three reachable codes`).toBeGreaterThanOrEqual(3)

  // The pill loop is a header-pane claim -- a line-item cell has no header-pane home, since
  // EXTR-13-07 gave it LineItemGrid, so it is excluded here rather than left to fail the AC it
  // does not test.
  const headerFlagged = flagged.filter((f) => VOCABULARY.includes(f.name))
  expect(headerFlagged.length, 'no header-vocabulary field was flagged -- the loop below would examine nothing').toBeGreaterThan(0)

  for (const f of headerFlagged) {
    const cell = page.getByTestId(`extraction-field-${f.name}`)
    await expect(cell, `${f.name} rendered no cell`).toBeVisible()
    const pill = REASON_PILL[f.reason as Exclude<ExtractionReason, ''>]
    await expect(
      cell.getByText(pill, { exact: true }),
      `${f.name} reported "${f.reason}" and renders no "${pill}" pill`,
    ).toBeVisible()
  }

  // The code itself is machine vocabulary and never reaches the screen.
  const paneText = await page.getByTestId('extraction-fields').innerText()
  expect(paneText.length, 'the fields pane rendered no text -- the absences below are vacuous').toBeGreaterThan(0)
  for (const code of Object.keys(REASON_PILL)) {
    expect(paneText, `the pane rendered the raw reason code "${code}"`).not.toContain(code)
  }

  await testInfo.attach('extraction-reason-pills.json', {
    body: JSON.stringify(flagged.map((f) => ({ name: f.name, reason: f.reason })), null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR12-E2E-02 (AC-7): the corrected marker sits inside the value control, on its right, clear of the selection rule', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)

  // The correction must land BEFORE the screen reads its detail. ExtractionReview holds the
  // detail in a useAsync keyed on jobId and this SPA has no route to the review screen, so a
  // reload cannot bring the reader back to it -- the job id is taken off the invoice detail's
  // own lookup instead, and .json() is called inside the handler so no body is read after the
  // fact.
  const jobLookups: Promise<ExtractionJobsResponse>[] = []
  page.on('response', (r) => {
    if (r.request().method() !== 'GET') return
    if (!new URL(r.url()).pathname.endsWith('/api/submission/v1/extractions')) return
    jobLookups.push((r.json() as Promise<ExtractionJobsResponse>).catch(() => ({ jobs: [] })))
  })

  await extractOneDocument(page, 'EXTR-12-06 marker')

  const jobId = (await Promise.all(jobLookups)).flatMap((l) => l.jobs).map((j) => j.id).pop()
  expect(jobId, 'the invoice detail looked up no extraction job -- there is nothing to correct').toBeTruthy()

  // `total` is admitted: refuseField locks only invoice_number, supplier_tin and supplier_name.
  await postFieldCorrection(token, jobId as string, 'total', { value: '2222.00', method: 'typed' })

  const detail = await openExtractionReview(page)
  const corrected = detail.fields.find((f) => f.name === 'total')
  expect(
    corrected?.corrected,
    'the wire the screen read carries no correction on total -- every measurement below is vacuous',
  ).toBeTruthy()

  const cell = page.getByTestId('extraction-field-total')
  await cell.click()
  await expect(cell, 'the corrected cell did not take the selection').toHaveAttribute('aria-current', 'true')

  const control = page.getByTestId('extraction-control-total')
  const marker = page.getByTestId('extraction-marker-total')
  await expect(marker, 'the corrected field renders no marker on the deployed build').toBeVisible()

  type Measured = { width: number; cell: Rect; control: Rect; marker: Rect; leftRule: Rect; contained: Rect }
  const measured: Measured[] = []
  const entryViewport = page.viewportSize()
  try {
    // Widest first (WIDE_WIDTHS' own order, layout.ts).
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [c, ct, mk] = await Promise.all([cell.boundingBox(), control.boundingBox(), marker.boundingBox()])
        return { c, ct, mk }
      }, `marker geometry at ${width}px`)

      expect(m.c && m.ct && m.mk, `the cell, its control and its marker must all render at ${width}px`).toBeTruthy()
      // Non-empty first: a rect collapsed on either axis is contained by anything and clears
      // everything, so both claims below pass vacuously on it.
      expect(m.mk!.width, `the marker has no width at ${width}px`).toBeGreaterThan(0)
      expect(m.mk!.height, `the marker has no height at ${width}px`).toBeGreaterThan(0)
      expect(m.ct!.width, `the value control has no width at ${width}px`).toBeGreaterThan(0)

      // Containment: overlapOf clamps per axis, so the intersection collapses to the marker's
      // own rect exactly when the marker is inside the control.
      const contained = overlapOf(m.mk as Rect, m.ct as Rect)
      expect(contained, `the marker escapes its value control at ${width}px`).toEqual(m.mk)

      // The RIGHT half. `left: 11px` passes containment and fails here.
      expect(
        m.mk!.x + m.mk!.width / 2,
        `the marker sits left of the control's centre at ${width}px`,
      ).toBeGreaterThan(m.ct!.x + m.ct!.width / 2)

      // DERIVED, not measured: the selection rule is an `inset 2px 0 0` box-shadow on the
      // cell, which has no element and no box of its own. Stated because a derived oracle that
      // reads as measured is its own trap.
      const leftRule: Rect = { x: m.c!.x, y: m.c!.y, width: 2, height: m.c!.height }
      expect(
        rectsOverlap(m.mk as Rect, leftRule),
        `the marker covers the selected cell's left rule at ${width}px`,
      ).toBe(false)

      measured.push({ width, cell: m.c as Rect, control: m.ct as Rect, marker: m.mk as Rect, leftRule, contained })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([
    ...WIDE_WIDTHS,
  ])

  await testInfo.attach('extraction-corrected-marker.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-12-07 · the retired badge and the chip row, on the deployed build ---
//
// Neither row can execute before EXTR-12-09 marks the PR ready: `dev-env.yml` gates deploy and
// E2E on `pull_request.draft == false`. The local oracle is `pnpm -r typecheck` plus
// `playwright test --list`.

test('EXTR12-E2E-03 (AC-7): the document toolbar no longer claims the screen is read-only', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-12-07 read-only')
  await openExtractionReview(page)

  // The two-element floor is load-bearing: a bare toHaveCount(0) is green on a screen that
  // failed to render at all, which is how an absence row turns into a false green.
  const toolbar = page.getByTestId('extraction-toolbar')
  await expect(toolbar, 'the document toolbar did not render -- the absence below is vacuous').toBeVisible()
  await expect(
    page.getByTestId('extraction-zoom-100'),
    'the toolbar rendered no zoom control -- the absence below is vacuous',
  ).toBeVisible()

  await expect(page.getByTestId('extraction-read-only'), 'the toolbar still carries the READ ONLY badge').toHaveCount(0)
  const toolbarText = await toolbar.innerText()
  expect(toolbarText.length, 'the toolbar rendered no text -- the absence below is vacuous').toBeGreaterThan(0)
  expect(toolbarText, 'the badge lost its testid but kept its copy').not.toContain('READ ONLY')

  // The claim is false because the fields beside it are editable now. Asserted here so
  // "the badge is gone" cannot pass on a screen that also lost its controls.
  await expect(
    page.getByTestId('extraction-input-total'),
    'the badge went and no editable control replaced it',
  ).toBeVisible()

  await testInfo.attach('extraction-read-only-retired.json', {
    body: JSON.stringify({ toolbarText }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR12-E2E-04 (AC-8): the chip row shares its column evenly at every WIDE_WIDTHS', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-12-07 chips')
  const detail = await openExtractionReview(page)

  // The field name and the chip count come OFF THE WIRE, never out of mock.go: a fixture change
  // that dropped the alternatives would otherwise leave this row measuring one chip and calling
  // it even.
  // shapes.go's numericDateReadings tries exactly 2 fixed layouts and appends each at most
  // once, so one alternative is the most any single input can produce -- the floor is 1, not 2.
  const ambiguous = detail.fields.find((f) => f.reason === 'ambiguous')
  expect(ambiguous, 'this document reported no ambiguous field -- there is no chip row to measure').toBeTruthy()
  expect(
    ambiguous!.alternatives.length,
    `${ambiguous!.name} carries ${ambiguous!.alternatives.length} alternative(s) -- zero is not a row to share`,
  ).toBeGreaterThanOrEqual(1)

  const cell = page.getByTestId(`extraction-field-${ambiguous!.name}`)
  const chips = page.locator(`[data-testid^="extraction-chip-${ambiguous!.name}-"]`)
  // The row's own container: a stretched flex item, so its width IS the cell's content width.
  // The chips must fill it, and on this document they cannot be told apart any other way --
  // 2026-01-01, 2026-01-10 and 2026-10-01 are anagrams and all three sub-labels read `page 1`,
  // so three content-sized chips are ALSO within 1px of each other and clear the evenness
  // clause below. Filling the row is what `flex: 1` buys and content sizing does not.
  const chipRow = chips.first().locator('xpath=..')
  await expect(cell, 'the ambiguous field rendered no cell').toBeVisible()
  // W-3: the decided reading is itself a chip, so the row carries N + 1.
  await expect(chips, 'the ambiguous field rendered no chip row').toHaveCount(ambiguous!.alternatives.length + 1)

  type Measured = { width: number; cell: Rect; row: Rect; chips: Rect[]; rowGaps: { left: number; right: number } }
  const measured: Measured[] = []
  const entryViewport = page.viewportSize()
  try {
    // Widest first (WIDE_WIDTHS' own order, layout.ts): a floor strands only what the window is
    // too narrow for, rather than every width after it.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const c = await cell.boundingBox()
        const r = await chipRow.boundingBox()
        const boxes = await chips.all()
        const rects = await Promise.all(boxes.map((b) => b.boundingBox()))
        return { c, r, rects }
      }, `chip row geometry at ${width}px`)

      expect(m.c, `the cell must render at ${width}px`).toBeTruthy()
      expect(m.r, `the chip row's own container must render at ${width}px`).toBeTruthy()
      expect(m.rects.every(Boolean), `every chip must render at ${width}px`).toBe(true)
      const rects = m.rects as Rect[]
      for (const [i, r] of rects.entries()) {
        // Non-empty first: a rect collapsed on either axis is contained by anything and clears
        // everything, so both claims below pass vacuously on it.
        expect(r.width, `chip ${i} has no width at ${width}px`).toBeGreaterThan(0)
        expect(r.height, `chip ${i} has no height at ${width}px`).toBeGreaterThan(0)
      }

      // Evenly: `flex: 1; min-width: 0` on every chip. A fixed-width chip fails at 1280 first.
      for (const [i, r] of rects.entries()) {
        expect(
          Math.abs(r.width - rects[0].width),
          `chip ${i} is ${r.width}px beside chip 0's ${rects[0].width}px at ${width}px -- the row does not share evenly`,
        ).toBeLessThanOrEqual(1)
      }

      // Inside its column on BOTH edges. The cell is a grid item with `min-width: 0`, so its
      // own border box stays pinned however far its content spills -- these gaps are what
      // notice the spill.
      const first = rects[0]
      const last = rects[rects.length - 1]
      const rowRect = { x: first.x, width: last.x + last.width - first.x }
      const g = gaps(rowRect, m.c as Rect)
      expect(g.left, `the chip row passes its column's left edge by ${-g.left}px at ${width}px`).toBeGreaterThanOrEqual(-1)
      expect(g.right, `the chip row passes its column's right edge by ${-g.right}px at ${width}px`).toBeGreaterThanOrEqual(-1)

      // Filling it, not merely fitting inside it: `flex: none` chips are content-sized, even
      // with each other and well inside the column, and clear every clause above.
      const row = m.r as Rect
      expect(
        row.width - rowRect.width,
        `the chips take ${rowRect.width}px of their ${row.width}px row at ${width}px -- they do not share the column, they sit in it`,
      ).toBeLessThanOrEqual(1)

      measured.push({ width, cell: m.c as Rect, row, chips: rects, rowGaps: g })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  // Mirrors the sweep-length guard every sibling row carries, so a loop that ran zero times
  // cannot report clear.
  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([
    ...WIDE_WIDTHS,
  ])

  await testInfo.attach('extraction-chip-row.json', {
    body: JSON.stringify({ field: ambiguous!.name, candidates: ambiguous!.alternatives.length + 1, measured }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// EXTR-12's Invented-copy table, with the lead's Stage-1 corrections. `POINT_ARMED` replaces
// the artboard's click-arm string: this build's gesture is a DRAG, and under the 24x12 floor a
// user who obeys "click the words" gets nothing. Both are clear of the reason-code sweep above.
const POINT_IDLE = 'Not found — point at it on the document'
const POINT_ARMED = 'Waiting — drag a box around it on the document'

type Ratio = { x: number; y: number; width: number; height: number }

/** A rectangle expressed against a frame, which is the only zoom-invariant form it has. */
function ratioOf(box: Rect, frame: Rect): Ratio {
  return {
    x: (box.x - frame.x) / frame.width,
    y: (box.y - frame.y) / frame.height,
    width: box.width / frame.width,
    height: box.height / frame.height,
  }
}

test('EXTR12-E2E-05 (AC-2/AC-3/AC-6): a box drawn on page 2 is the box the screen reads back, at every zoom', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  // The two-page fixture, and the ONLY deployed source of a second page: every mock region is
  // on page 1, so page 2 is where a page-indexing defect becomes visible at all.
  await extractOneDocument(page, 'EXTR-12-08 point', {
    name: 'native_invoice_2p.pdf',
    buffer: uniqueTwoPagePdfBytes(),
  })
  const detail = await openExtractionReview(page)

  // Off the wire the SPA itself consumed, never a literal out of mock.go.
  expect(detail.pages.map((p) => p.page), 'the two-page fixture must produce two stored pages').toEqual([1, 2])
  const missing = detail.fields.find((f) => f.reason === 'missing')
  expect(missing, 'no field on this document is missing -- there is nothing to point at').toBeTruthy()
  const name = missing!.name
  expect(missing!.region, 'the missing field already carries a region, so a drawn box proves nothing').toBeNull()

  const button = page.getByTestId(`extraction-point-${name}`)
  await expect(button, `${name} offers no way to point at it`).toBeVisible({ timeout: 30_000 })

  // -- CLAIM 1 (AC-6) -- the armed state is visible on the DEPLOYED build.
  //
  // The RESOLVED value, not the declaration: the jsdom row reads `el.style.*` and would pass
  // under an app-layer.css rule overriding the inline colour. This is the row that catches it.
  const paint = async () =>
    button.evaluate((el) => {
      const cs = getComputedStyle(el)
      return { border: cs.borderTopColor, background: cs.backgroundColor, colour: cs.color }
    })

  const idlePaint = await paint()
  expect((await button.innerText()).trim(), 'the idle label is not the copy table’s').toBe(POINT_IDLE)

  await button.click()
  await expect(button, 'clicking the point button changed nothing the reader can see').toHaveText(POINT_ARMED, {
    timeout: 15_000,
  })
  const armedPaint = await paint()

  for (const key of ['border', 'background', 'colour'] as const) {
    expect(
      armedPaint[key],
      `arming left ${key} at ${idlePaint[key]} -- the armed state is invisible without hover`,
    ).not.toBe(idlePaint[key])
  }

  const surface = page.getByTestId('extraction-point-surface-2')
  await expect(surface, 'the armed field takes no drag on page 2').toBeVisible({ timeout: 15_000 })
  const save = page.getByTestId('extraction-save')

  // Required before either drag, and the whole difference between page 2 and page 1:
  // `page.mouse` takes VIEWPORT coordinates, and page 2's box sits at y=955 in a 720px viewport
  // until the ground is scrolled. `toBeVisible` does not mean in the viewport.
  await page.getByTestId('extraction-page-2').scrollIntoViewIfNeeded()
  const liveBox = page.getByTestId('extraction-point-box')

  // -- CLAIM 2 (AC-3) -- a gesture under the artboard's floor is a click, not a box.
  //
  // A build that discards EVERYTHING also passes this claim; claim 3 is its pair.
  const s0 = await boxOf(page, 'extraction-point-surface-2')
  await page.mouse.move(s0.x + 0.2 * s0.width, s0.y + 0.2 * s0.height)
  await page.mouse.down()
  await page.mouse.move(s0.x + 0.2 * s0.width + 10, s0.y + 0.2 * s0.height + 6)
  // The live box is drawn under the cursor with no floor at all, so it is the proof the surface
  // took the gesture -- without it, refusing the twitch below is equally satisfied by a mouse
  // that never reached page 2.
  await expect(liveBox, 'the twitch reached no surface, so refusing it proves nothing').toHaveCount(1)
  await page.mouse.up()

  await expect(page.getByTestId('extraction-point-box'), 'a 10x6 twitch was drafted as a region').toHaveCount(0)
  expect(await save.isDisabled(), 'a 10x6 twitch armed Save, so the boundary will be asked to store it').toBe(true)

  // -- CLAIM 3 (AC-2) -- the drawn box IS the rendered box, at every zoom.
  //
  // The oracle is the SCREEN RECTANGLE OF THE DRAG, never the region read back off the wire:
  // deriving `expected` from the wire holds under ANY normalisation, right or wrong, because a
  // transform that posts a shifted region renders a shifted highlight and the two agree
  // perfectly. That comparison is right for EXTR-11, where the region is the INPUT; it is
  // worthless here, where the region is the OUTPUT and the gesture is the input.
  //
  // Absolute pixels cannot work: the frame halves at zoom 50, so the drag rectangle is
  // converted to frame-relative RATIOS against the frame measured AT DRAG TIME.
  const frameAtDrag = await boxOf(page, 'extraction-page-2')
  const from = { x: frameAtDrag.x + 0.3 * frameAtDrag.width, y: frameAtDrag.y + 0.22 * frameAtDrag.height }
  const to = { x: frameAtDrag.x + 0.62 * frameAtDrag.width, y: frameAtDrag.y + 0.55 * frameAtDrag.height }

  await page.mouse.move(from.x, from.y)
  await page.mouse.down()
  await page.mouse.move(to.x, to.y, { steps: 8 })
  await expect(liveBox, 'the drag reached no surface on page 2').toHaveCount(1)
  await page.mouse.up()

  const want = ratioOf(
    { x: from.x, y: from.y, width: to.x - from.x, height: to.y - from.y },
    frameAtDrag,
  )

  await expect(page.getByTestId('extraction-highlight'), 'the drawn box highlights nothing').toHaveCount(1)

  const measured: { zoom: number; frame: Rect; highlight: Rect; got: Ratio }[] = []
  for (const zoom of [100, 50, 150]) {
    await page.getByTestId(`extraction-zoom-${zoom}`).click()

    // settledRead, not a bare read: both boxes move together under a smooth scroll, so the
    // RATIO is scroll-invariant only if the pair is read from one settled frame.
    const m = await settledRead(async () => {
      const [frame, highlight] = await Promise.all([
        boxOf(page, 'extraction-page-2'),
        boxOf(page, 'extraction-highlight'),
      ])
      return { frame, highlight }
    }, `drawn-box ratio at zoom ${zoom}`)

    expect(m.frame.width, `the page frame has no width at zoom ${zoom}`).toBeGreaterThan(0)
    expect(m.highlight.width, `zoom ${zoom}: a zero-width highlight satisfies every ratio vacuously`).toBeGreaterThan(0)
    expect(m.highlight.height, `zoom ${zoom}: a zero-height highlight satisfies every ratio vacuously`).toBeGreaterThan(
      0,
    )

    const got = ratioOf(m.highlight, m.frame)
    // Per-axis, so 1.5 CSS px means 1.5 px on the axis it is measured on -- and the allowance
    // widens as the frame shrinks, which is what makes zoom 50 comparable to zoom 100.
    for (const axis of ['x', 'width'] as const) {
      expect(
        Math.abs(got[axis] - want[axis]) * m.frame.width,
        `zoom ${zoom}: the highlight's ${axis} sits ${(got[axis] * m.frame.width).toFixed(1)}px into the frame, the drag drew it at ${(want[axis] * m.frame.width).toFixed(1)}px`,
      ).toBeLessThanOrEqual(RATIO_TOL_PX)
    }
    for (const axis of ['y', 'height'] as const) {
      expect(
        Math.abs(got[axis] - want[axis]) * m.frame.height,
        `zoom ${zoom}: the highlight's ${axis} sits ${(got[axis] * m.frame.height).toFixed(1)}px into the frame, the drag drew it at ${(want[axis] * m.frame.height).toFixed(1)}px`,
      ).toBeLessThanOrEqual(RATIO_TOL_PX)
    }

    measured.push({ zoom, frame: m.frame, highlight: m.highlight, got })
  }

  expect(measured.map((m) => m.zoom), 'every zoom must be measured').toEqual([100, 50, 150])
  expect(
    new Set(measured.map((m) => Math.round(m.frame.width))).size,
    'zoom moved nothing -- these are one rendering measured three times',
  ).toBeGreaterThan(1)

  // NOT COVERED, and stated rather than papered over: a transform reading the frame's BORDER
  // box instead of its padding box. Worked through, the error is (2u - W)/(W+2) -- zero at the
  // centre of the page and under one pixel at either edge, at every zoom and every frame size.
  // It therefore satisfies AC-2's one-pixel claim as written, and NO row in this story catches
  // it. The padding-box overlay stands because it is exact and needs no border arithmetic.

  // -- CLAIM 4 -- the round trip, WITHOUT a reload.
  //
  // This SPA has no route back to the review screen, so a reload cannot bring the reader here.
  // The shell re-reads the detail after every Save, and that GET is the read-back.
  await page.getByTestId('extraction-zoom-100').click()
  await page.getByTestId(`extraction-input-${name}`).fill('31775208-0003')

  const [postRes, getRes] = await Promise.all([
    page.waitForResponse(
      (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith(`/fields/${name}/corrections`),
      { timeout: 60_000 },
    ),
    page.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        /\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}$/.test(new URL(r.url()).pathname),
      { timeout: 60_000 },
    ),
    save.click(),
  ])

  expect(postRes.status(), 'the correction was refused').toBe(201)
  const sent = postRes.request().postDataJSON() as { method?: string; region?: ExtractionRegion | null }
  expect(sent.method, 'the box was recorded as some other method').toBe('pointed')
  expect(sent.region, 'the POST carried no region, so the box was never recorded').not.toBeNull()
  expect(sent.region!.page, 'the box was posted against page 1').toBe(2)

  expect(getRes.status(), 'the post-save re-read failed').toBe(200)
  const fresh = (await getRes.json()) as ExtractionDetail
  const stored = fresh.fields.find((f) => f.name === name)?.region
  // EXACT equality, never a tolerance: double precision in and JSON out is byte-exact, so a
  // tolerance here would hide a rounding defect.
  expect(stored, 'the register did not return the box it was given').toEqual(sent.region)

  const after = await settledRead(async () => {
    const [frame, highlight] = await Promise.all([
      boxOf(page, 'extraction-page-2'),
      boxOf(page, 'extraction-highlight'),
    ])
    return { frame, highlight }
  }, 'drawn-box ratio after the round trip')

  const back = ratioOf(after.highlight, after.frame)
  for (const axis of ['x', 'width'] as const) {
    expect(
      Math.abs(back[axis] - want[axis]) * after.frame.width,
      `after the round trip the highlight's ${axis} moved off the box that was drawn`,
    ).toBeLessThanOrEqual(RATIO_TOL_PX)
  }
  for (const axis of ['y', 'height'] as const) {
    expect(
      Math.abs(back[axis] - want[axis]) * after.frame.height,
      `after the round trip the highlight's ${axis} moved off the box that was drawn`,
    ).toBeLessThanOrEqual(RATIO_TOL_PX)
  }

  await testInfo.attach('extraction-point-round-trip.json', {
    body: JSON.stringify({ field: name, want, measured, posted: sent.region, stored, back }, null, 2),
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
  /**
   * How to find the element when it carries no testid. Defaults to `[data-testid="${element}"]`,
   * so no row written before EXTR-12-09 changes; `element` stays the map key and the name the
   * failure message uses. A row that sets this asserts a match count of 1 as well.
   */
  selector?: string
}

// Placeholders rather than literals: each resolves to whatever the deployed theme resolves it
// to, and the diff compares against that token rather than against a colour typed here. Filled
// in from the probes below.
const BG_2_TOKEN = '<var(--bg-2)>'
const ACTION_TOKEN = '<var(--action)>'
const ACCENT_TOKEN = '<var(--accent)>'

// AC-2's rule, once, shared by the two rows it governs.
const TEAL_IS_ACTION =
  "the artboard's `var(--accent)` here means TEAL; in this repo `--accent` IS the design system's amber and is deliberately not aliased, so every teal transcribes to `--action` (app-layer.css:37-47, WorkflowParts.tsx:7-9)"

// Measured, not reasoned: Chrome floors a fractional border-width to a whole CSS pixel and never
// to zero -- 0.5px, 1.2px, 1.5px and 1.9px all resolve `1px`, 2.5px resolves `2px` -- identically
// at deviceScaleFactor 1, 2 and 3, so this is not a retina artefact of the run's own DPR.
const BORDER_FLOOR =
  "Chrome resolves a fractional border-width to a whole CSS pixel, so the artboard's 1.5px dash resolves 1px on the deployed build. The DECLARATION stays 1.5px (ExtractionFields.tsx:196, :211) -- ExtractionFields.test.tsx:1694-1697 pins both dashes in jsdom, where the declaration is what is read"

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

  // The READ ONLY pill's five rows are RETIRED with the badge itself (EXTR-12-07, AC-7), and
  // replaced -- not dropped -- by the element that supersedes the claim they were about: the
  // candidate chip, which is what the toolbar's "read only" is now false for. The chip's radius
  // is also the one the `.pf-chip` class would destroy (`border-radius: var(--radius-pill)
  // !important`), so these four are the deployed half of the unit-level class ban.
  // `extraction-chip-issue_date-0` is the DECIDED reading's chip: mockDefaultResult reports
  // issue_date ambiguous with two alternatives, so the row has three chips on this build.
  { element: 'extraction-chip-issue_date-0', property: 'border-top-left-radius', artboard: '10px', source: ':315', expected: '10px', deviation: null },
  { element: 'extraction-chip-issue_date-0', property: 'border-top-right-radius', artboard: '10px', source: ':315', expected: '10px', deviation: null },
  { element: 'extraction-chip-issue_date-0', property: 'border-bottom-right-radius', artboard: '10px', source: ':315', expected: '10px', deviation: null },
  { element: 'extraction-chip-issue_date-0', property: 'border-bottom-left-radius', artboard: '10px', source: ':315', expected: '10px', deviation: null },
  { element: 'extraction-chip-issue_date-0', property: 'padding-top', artboard: '8px', source: ':315', expected: '8px', deviation: null },
  { element: 'extraction-chip-issue_date-0', property: 'padding-left', artboard: '11px', source: ':315', expected: '11px', deviation: null },

  // The chip's mono `where` sub-label. `font-size: 8.5px; letter-spacing: 0.05em`.
  // letter-spacing is asserted separately, in ems -- see below.
  { element: 'extraction-chip-where-issue_date-0', property: 'font-size', artboard: '8.5px', source: ':317', expected: '8.5px', deviation: null },

  // -- EXTR-12's four elements ---------------------------------------------------------------
  //
  // Chrome's resolved forms are READ off synthetic nodes, never assumed -- `border-radius: 999px`
  // stays `999px`, `flex: 1` resolves `1 / 1 / 0%`, `min-height: 38px` stays `38px`,
  // `padding: 0 12px` gives `0px` / `12px`. The one that does NOT survive is the dash's width:
  // see BORDER_FLOOR below, which the first gate run caught.
  //
  // Two elements below carry no testid and are reached by a `selector`; their `element` is a
  // NAME for the failure message, not a handle.

  // The candidate chip. `flex: 1; min-width: 0; text-align: left; border: 1px solid …`, per
  // longhand -- the table's own precedent (extraction-canvas's `flex: 1 1 auto`, above).
  // These four are what EXTR12-E2E-04's evenness clause cannot decide: 2026-01-01, 2026-01-10
  // and 2026-10-01 are anagrams, so `flex: none` chips are even too, and all three sub-labels
  // read `page 1`. A `flex: none` chip resolves `0 / 0 / auto` and reds three of them on ANY
  // fixture. Chip 0 renders CHIP_PICKED, which changes only border COLOUR, so every property
  // here is state-independent.
  { element: 'extraction-chip-issue_date-0', property: 'flex-grow', artboard: '1', source: ':315', expected: '1', deviation: null },
  { element: 'extraction-chip-issue_date-0', property: 'flex-shrink', artboard: '1', source: ':315', expected: '1', deviation: null },
  { element: 'extraction-chip-issue_date-0', property: 'flex-basis', artboard: '0%', source: ':315', expected: '0%', deviation: null },
  { element: 'extraction-chip-issue_date-0', property: 'min-width', artboard: '0px', source: ':315', expected: '0px', deviation: null },
  { element: 'extraction-chip-issue_date-0', property: 'text-align', artboard: 'left', source: ':315', expected: 'left', deviation: null },
  { element: 'extraction-chip-issue_date-0', property: 'border-top-width', artboard: '1px', source: ':315', expected: '1px', deviation: null },

  // The reason pill, `:296`. Anchored on issue_date, not vat: the real extractor decides vat
  // (reason ''), and a decided field renders no pill at all -- only issue_date (ambiguous) and
  // subtotal (inconsistent) carry one, and subtotal's is replaced by its changed row once this
  // journey corrects it. issue_date's chips put more `.mono` spans in the same cell, so the
  // selector is depth-scoped to LABEL_STRIP > PILL (ExtractionFields.tsx:359-365); chip labels
  // sit a level deeper, under a button. `letter-spacing: 0.07em` goes to the em-ratio block
  // below, never a string.
  // A build reusing RulePills.tsx instead of the artboard's own slot reds on the two font rows.
  { element: 'extraction-pill-issue_date', selector: '[data-testid="extraction-field-issue_date"] > span > span.mono', property: 'font-size', artboard: '8.5px', source: ':296', expected: '8.5px', deviation: null },
  { element: 'extraction-pill-issue_date', selector: '[data-testid="extraction-field-issue_date"] > span > span.mono', property: 'font-weight', artboard: '700', source: ':296', expected: '700', deviation: null },
  { element: 'extraction-pill-issue_date', selector: '[data-testid="extraction-field-issue_date"] > span > span.mono', property: 'border-top-left-radius', artboard: '999px', source: ':296', expected: '999px', deviation: null },
  { element: 'extraction-pill-issue_date', selector: '[data-testid="extraction-field-issue_date"] > span > span.mono', property: 'padding-top', artboard: '2px', source: ':296', expected: '2px', deviation: null },
  { element: 'extraction-pill-issue_date', selector: '[data-testid="extraction-field-issue_date"] > span > span.mono', property: 'padding-left', artboard: '8px', source: ':296', expected: '8px', deviation: null },
  { element: 'extraction-pill-issue_date', selector: '[data-testid="extraction-field-issue_date"] > span > span.mono', property: 'border-top-width', artboard: '1px', source: ':296', expected: '1px', deviation: null },
  // Also what keeps the floor walk in EXTR12-E2E-07 meaningful: a pill that dropped `nowrap`
  // would wrap its own text and hide the overflow W-6 measured.
  { element: 'extraction-pill-issue_date', selector: '[data-testid="extraction-field-issue_date"] > span > span.mono', property: 'white-space', artboard: 'nowrap', source: ':296', expected: 'nowrap', deviation: null },

  // The point-at-it button, `:324`, idle: `buyer_tin` stays missing here and the document has
  // one page. `border-top-left-radius: 10px` is the deployed half of the class ban -- a build
  // carrying the artboard's own `class="pf-btn"` resolves 9999px (app-layer.css:193-197).
  // The artboard's `width: 100%` cannot be a string row; it is asserted as a relationship below.
  { element: 'extraction-point-buyer_tin', property: 'min-height', artboard: '38px', source: ':324', expected: '38px', deviation: null },
  { element: 'extraction-point-buyer_tin', property: 'padding-top', artboard: '0px', source: ':324', expected: '0px', deviation: null },
  { element: 'extraction-point-buyer_tin', property: 'padding-left', artboard: '12px', source: ':324', expected: '12px', deviation: null },
  { element: 'extraction-point-buyer_tin', property: 'padding-right', artboard: '12px', source: ':324', expected: '12px', deviation: null },
  { element: 'extraction-point-buyer_tin', property: 'border-top-style', artboard: 'dashed', source: ':324', expected: 'dashed', deviation: null },
  { element: 'extraction-point-buyer_tin', property: 'border-top-width', artboard: '1.5px', source: ':324', expected: '1px', deviation: BORDER_FLOOR },
  { element: 'extraction-point-buyer_tin', property: 'border-top-left-radius', artboard: '10px', source: ':324', expected: '10px', deviation: null },
  { element: 'extraction-point-buyer_tin', property: 'font-size', artboard: '12.5px', source: ':324', expected: '12.5px', deviation: null },
  { element: 'extraction-point-buyer_tin', property: 'font-weight', artboard: '500', source: ':324', expected: '500', deviation: null },
  { element: 'extraction-point-buyer_tin', property: 'column-gap', artboard: '9px', source: ':324', expected: '9px', deviation: null },
  { element: 'extraction-point-buyer_tin', property: 'text-align', artboard: 'left', source: ':324', expected: 'left', deviation: null },

  // The corrected marker, `:307`. The test posts one correction on `subtotal` before it opens
  // the screen (rich fixture: `total` resolves clean, `subtotal` carries the disagreement), because
  // the marker renders only where a correction exists. A marker moved to `left: 11px` reds
  // nothing HERE -- EXTR12-E2E-02's right-of-centre clause is what catches it.
  { element: 'extraction-marker-subtotal', property: 'position', artboard: 'absolute', source: ':307', expected: 'absolute', deviation: null },
  { element: 'extraction-marker-subtotal', property: 'right', artboard: '11px', source: ':307', expected: '11px', deviation: null },
  { element: 'extraction-marker-subtotal', property: 'width', artboard: '7px', source: ':307', expected: '7px', deviation: null },
  { element: 'extraction-marker-subtotal', property: 'height', artboard: '7px', source: ':307', expected: '7px', deviation: null },
  { element: 'extraction-marker-subtotal', property: 'border-top-left-radius', artboard: '2px', source: ':307', expected: '2px', deviation: null },
  // AC-2, and an assertion rather than a comment: both tokens are probed in the pane's own
  // environment, so `expected !== artboard` fires on the fact that this repo does not resolve
  // `--accent` to teal. A literal transcription resolves the AMBER here and reds.
  { element: 'extraction-marker-subtotal', property: 'background-color', artboard: ACCENT_TOKEN, source: ':307', expected: ACTION_TOKEN, deviation: TEAL_IS_ACTION },

  // The changed label, `:335`. After the correction `settled !== null` forces the pill to null
  // and neither the was-line nor Undo is `.mono`: exactly one in that cell.
  { element: 'extraction-changed-label-subtotal', selector: '[data-testid="extraction-field-subtotal"] span.mono', property: 'font-size', artboard: '8.5px', source: ':335', expected: '8.5px', deviation: null },
  { element: 'extraction-changed-label-subtotal', selector: '[data-testid="extraction-field-subtotal"] span.mono', property: 'font-weight', artboard: '700', source: ':335', expected: '700', deviation: null },
  { element: 'extraction-changed-label-subtotal', selector: '[data-testid="extraction-field-subtotal"] span.mono', property: 'color', artboard: ACCENT_TOKEN, source: ':335', expected: ACTION_TOKEN, deviation: TEAL_IS_ACTION },
]

// The row set, as a literal beside the table (AA-24 / Z-5). It reds on a deleted row, an added
// row AND a row silently retargeted at another element or property -- none of which the
// `table.length === FIDELITY.length` check it replaces could see, because `table` is
// `FIDELITY.map(...)` and the two lengths were equal by construction.
const FIDELITY_ROW_IDS: string[] = [
  'extraction-canvas·flex-basis',
  'extraction-canvas·flex-grow',
  'extraction-canvas·flex-shrink',
  'extraction-canvas·min-width',
  'extraction-changed-label-subtotal·color',
  'extraction-changed-label-subtotal·font-size',
  'extraction-changed-label-subtotal·font-weight',
  'extraction-chip-issue_date-0·border-bottom-left-radius',
  'extraction-chip-issue_date-0·border-bottom-right-radius',
  'extraction-chip-issue_date-0·border-top-left-radius',
  'extraction-chip-issue_date-0·border-top-right-radius',
  'extraction-chip-issue_date-0·border-top-width',
  'extraction-chip-issue_date-0·flex-basis',
  'extraction-chip-issue_date-0·flex-grow',
  'extraction-chip-issue_date-0·flex-shrink',
  'extraction-chip-issue_date-0·min-width',
  'extraction-chip-issue_date-0·padding-left',
  'extraction-chip-issue_date-0·padding-top',
  'extraction-chip-issue_date-0·text-align',
  'extraction-chip-where-issue_date-0·font-size',
  'extraction-fields·flex-basis',
  'extraction-fields·flex-grow',
  'extraction-fields·flex-shrink',
  'extraction-fields·min-width',
  'extraction-ground·overflow-x',
  'extraction-ground·overflow-y',
  'extraction-marker-subtotal·background-color',
  'extraction-marker-subtotal·border-top-left-radius',
  'extraction-marker-subtotal·height',
  'extraction-marker-subtotal·position',
  'extraction-marker-subtotal·right',
  'extraction-marker-subtotal·width',
  'extraction-page-1·border-bottom-width',
  'extraction-page-1·border-left-width',
  'extraction-page-1·border-right-width',
  'extraction-page-1·border-top-width',
  'extraction-page-1·margin-bottom',
  'extraction-page-1·margin-top',
  'extraction-page-1·max-width',
  'extraction-page-1·min-width',
  'extraction-page-1·padding-bottom',
  'extraction-page-1·padding-left',
  'extraction-page-1·padding-right',
  'extraction-page-1·padding-top',
  'extraction-pill-issue_date·border-top-left-radius',
  'extraction-pill-issue_date·border-top-width',
  'extraction-pill-issue_date·font-size',
  'extraction-pill-issue_date·font-weight',
  'extraction-pill-issue_date·padding-left',
  'extraction-pill-issue_date·padding-top',
  'extraction-pill-issue_date·white-space',
  'extraction-point-buyer_tin·border-top-left-radius',
  'extraction-point-buyer_tin·border-top-style',
  'extraction-point-buyer_tin·border-top-width',
  'extraction-point-buyer_tin·column-gap',
  'extraction-point-buyer_tin·font-size',
  'extraction-point-buyer_tin·font-weight',
  'extraction-point-buyer_tin·min-height',
  'extraction-point-buyer_tin·padding-left',
  'extraction-point-buyer_tin·padding-right',
  'extraction-point-buyer_tin·padding-top',
  'extraction-point-buyer_tin·text-align',
  'extraction-toolbar·background-color',
  'extraction-toolbar·column-gap',
  'extraction-toolbar·padding-bottom',
  'extraction-toolbar·padding-left',
  'extraction-toolbar·padding-right',
  'extraction-toolbar·padding-top',
  'extraction-toolbar·row-gap',
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

// The two declarations no string row can hold, recorded beside the table they are not in. Each
// is asserted below as a RELATIONSHIP; this array is what puts them in the run's own artifact.
const DEVIATIONS_OUTSIDE_THE_TABLE = [
  {
    element: 'extraction-point-buyer_tin',
    property: 'width',
    artboard: '100%',
    source: ':324',
    reason:
      'getComputedStyle returns a used px value, so a string compare cannot express 100%. The repo declares no width at all -- an inline one would override the cell\'s min-width: 0 and let a long value widen the grid track -- and the cell\'s flex column stretches the button instead. Asserted as: the button fills the cell\'s content box.',
  },
  {
    element: 'extraction-page-1',
    property: 'margin-left / margin-right',
    artboard: 'auto',
    source: ':72',
    reason:
      "Chrome resolves an auto margin to its used value, so there is no 'auto' to compare. Asserted as: the two used values agree, at a width where the frame fits its column.",
  },
]

// EXTR-12's carried deviations, printed into this run's artifact so the record is not only prose
// in a task file. NONE is a defect to fix here.
const CARRIED_DEVIATIONS = [
  'AA-21: AC-5\'s "rendered reason" on a disabled Save is UNMET by decision. `disabled` and `filter: none` are met (ExtractionReview.tsx:261-263); the reason clause is not, because the only disabling condition is "nothing settled yet" and both shipped precedents disable without one.',
  'AA-24 / Z-5: NOT met. The retired READ ONLY badge\'s five rows were replaced by rows measuring a CHIP in the fields pane; the document toolbar slot has no fidelity coverage at all. The chip supersedes the read-only CLAIM, not the toolbar ELEMENT.',
  'CC-2: POINT_ARMED is corrected copy, not the artboard\'s. "Waiting — drag a box around it on the document" replaces `:662`\'s "Waiting — click the words on the document", because this build\'s gesture is a drag and under the 24x12 floor a click returns nothing.',
  'CC-3 / BB-3: Core AC-3\'s "the value is read from there" is UNMET and unbuildable. No migration persists token text or geometry, so no seam can read a value out of a box. The box records WHERE and the person types WHAT.',
  'W-1: Save batches everything -- chips, typing and undos -- through one shared draft, where the artboard drafts picks in `S.picked` and this subtask\'s own Test Specs said one POST per click.',
  "AA-17: the artboard's `f.was || value` fallback (`:634`) is deliberately not copied. A null reading would leave the typed value on screen while the server writes SQL NULL. A drafted undo resets to `corrected.was`, and to '' where that is null.",
  'DD-1: a transform reading the frame\'s BORDER box instead of its padding box is unfalsifiable -- the error is (2u − W)/(W+2), under one pixel everywhere at every zoom -- so it satisfies AC-2 as written and no row in this story catches it. Also stated in-file at EXTR12-E2E-05.',
  "§6's standing three: the chip's `where` sub-label is DERIVED (regionPhrase), not the artboard's anchor text; `invoice_number` is LOCKED though the artboard edits it (`:476`); the label is `VAT`, not `VAT at 7.5%`.",
  'POINT_CANCEL sits in the CELL, not in the artboard\'s banner over the document pane (`:46-56`), which would need `selTitle` and `selMode` -- two strings the copy table does not carry.',
]

// Leftovers -- for the story's list, not for building here.
const LEFTOVERS = [
  'L-1: the fidelity tautology cited as `:4134` in two documents was really the `table.length === FIDELITY.length` check; it is replaced above by the row-set pin.',
  'L-2: the `write` generation guard is unreachable and untested. `write.current !== mine` can only be true if a second Save starts while the first is in flight, and Save is disabled while writing (ExtractionReview.tsx:98, :160-161, :189).',
  'L-3: four redundant derivation slots, unkillable by construction -- `current`, `entries`, `arming` and `data` each re-test `x.jobId === jobId` after the render-phase `setX(null)` above has already discarded this pass (ExtractionReview.tsx:78-79, 86-87, 115-116, 117-118).',
  'L-4: an unenforced invariant -- "a `missing` field carries no value" is what makes `pointedEntry`\'s middle arm unreachable from the shell, and nothing enforces it (extractionReview.ts:452-456).',
  'L-5: R13 / R14 / R9\'s keep-clause / R26\'s selection clause stay flagged as PAIRS, never counted as coverage: each is satisfied by a build that never calls the thing under test.',
  'L-6: you cannot clear a field by emptying it, and nothing says so. `savableCorrections` drops a blank typed entry (extractionReview.ts:276) and the blank visibly bounces back with no explanation.',
  'L-7: a blank point survives on screen but not through a Save of another field -- the box disappears at the next Save because the entry was never posted.',
  'L-8: AA-16 -- ReviewRow.tsx:468\'s Save is `v2-btn v2-btn-primary pf-btn` with no disabled spread and no `filter: none`: a shipped instance of the defect AC-5 exists to prevent, on another screen.',
  'L-9: the pageless point label has no deployed oracle -- no fixture produces a job with zero page images, so AC-5 of subtask 08 is proved in jsdom only.',
  'L-10: DD-17 -- R27 is a negative that has never been shown to fail. Until a mutated handler is shown to red it, R27 is unproven rather than passing.',
  'L-12: the four-line grid the mock now emits gains ZERO rows in FIDELITY above, because no artboard covers it -- the prototype set carries no line-item screen to resolve values against. Its owners are in this PR: EXTR13-LAYOUT-01..04 and EXTR13-E2E-01..10, not a future story.',
  'L-11: `extraction.field_corrected` is NOT in the audit_log.invoice_id generated column\'s event list (migrations/20260822080722_audit_log_invoice_id_column_and_index.sql), so an invoice-scoped audit read of a correction returns zero rows by construction. EXTR12-E2E-06 filters on the event and matches the invoice in the payload instead, and asserts the empty invoice-scoped read as the reason.',
]

test("EXTR11-E2E-11 (AC-8): the deployed surface matches the artboard's resolved values", async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)

  // EXTR-12's marker and changed label render only over a correction (ExtractionFields.tsx:
  // 351-353, :413-417), so their nine rows need one first. EXTR12-E2E-02's recipe: the job id
  // comes off the invoice detail's own lookup, because this SPA has no route back to the review
  // screen and a reload cannot bring the reader to it. Every row written before EXTR-12-09 is
  // unchanged -- this is an arrange step, not an edit to a claim.
  const jobLookups: Promise<ExtractionJobsResponse>[] = []
  page.on('response', (r) => {
    if (r.request().method() !== 'GET') return
    if (!new URL(r.url()).pathname.endsWith('/api/submission/v1/extractions')) return
    jobLookups.push((r.json() as Promise<ExtractionJobsResponse>).catch(() => ({ jobs: [] })))
  })

  await extractOneDocument(page, 'EXTR-11-09 fidelity')

  const jobId = (await Promise.all(jobLookups)).flatMap((l) => l.jobs).map((j) => j.id).pop()
  expect(jobId, 'the invoice detail looked up no extraction job -- there is nothing to correct').toBeTruthy()
  // `subtotal` is admitted: refuseField locks only invoice_number, supplier_tin and
  // supplier_name. The rich fixture resolves `total` clean, so `subtotal` -- the field
  // carrying the flagged disagreement -- is the one with a pill to replace.
  await postFieldCorrection(token, jobId as string, 'subtotal', { value: '2222.00', method: 'typed' })

  const detail = await openExtractionReview(page)
  expect(
    detail.fields.find((f) => f.name === 'subtotal')?.corrected,
    'the wire the screen read carries no correction on subtotal -- the marker and changed-label rows would compare nothing',
  ).toBeTruthy()

  // The page frame's band is `560 * zoom` / `640 * zoom` (extractionReview.ts:114-118), so the
  // artboard's 560/640 are only the right expectation at zoom 100. Asserted, not assumed.
  await expect(
    page.getByTestId('extraction-zoom-100'),
    "the frame rows below are the artboard's numbers only at zoom 100",
  ).toHaveAttribute('aria-pressed', 'true')

  // One subject per element, in table order, each with the selector its rows resolve through.
  const subjects: { element: string; selector: string; scoped: boolean }[] = []
  const properties: Record<string, string[]> = {}
  for (const row of FIDELITY) {
    if (properties[row.element] === undefined) {
      properties[row.element] = []
      subjects.push({
        element: row.element,
        selector: row.selector ?? `[data-testid="${row.element}"]`,
        scoped: row.selector !== undefined,
      })
    }
    properties[row.element].push(row.property)
  }

  // The sweep's BOUND, checked against the wire the screen read rather than against this
  // table's own literals. EXTR-13-02 widened the mock with 15 line-item cells and EXTR-13-07
  // gives them their own grid, LineItemGrid, off this table's fidelity surface entirely -- so
  // "this table names only header-pane elements" still has to be enforced, not assumed; L-12
  // above records why the grid gains no row here. The line floor first: with no line cell on
  // the wire the exclusion below would exclude nothing.
  const wireNames = detail.fields.map((f) => f.name)
  const wireHeaderNames = wireNames.filter((n) => !n.startsWith('line_items'))
  expect(
    wireNames.filter((n) => n.startsWith('line_items')).length,
    'this document delivered no line-item field, so the exclusion below excludes nothing',
  ).toBeGreaterThan(0)
  expect(
    subjects.filter((s) => wireHeaderNames.some((n) => s.element.includes(n))).length,
    'no subject names a field the wire delivered -- the bound below is over a table of nothing',
  ).toBeGreaterThan(0)
  for (const s of subjects) {
    expect(
      `${s.element} ${s.selector}`,
      `${s.element} sweeps a line-item cell, which has no artboard to resolve values against`,
    ).not.toContain('line_items')
  }

  // Waited for, not assumed present: a missing element would otherwise fail the read as a hard
  // error where it can equally be a paint this assertion arrived one frame ahead of. A scoped
  // selector matching two nodes raises Playwright's strict-mode violation here, which is the
  // loud failure a silent retarget deserves; the count below says so in numbers.
  for (const s of subjects) {
    await expect(page.locator(s.selector), `${s.element} must render before its rows are read`).toBeVisible({ timeout: 30_000 })
  }

  // The bound's DEPLOYED half: no subject resolves inside a line-item cell. EXTR-13-07's filter
  // keeps every line name out of `extraction-field-*` and gives it to LineItemGrid, so this
  // reads the surface rather than the table above -- and it stays true, not merely vacuous,
  // now that the filter has landed. What CANNOT be asserted, so it is recorded instead of
  // skipped: the subject set is not derivable from the wire at all, because four subjects
  // (extraction-canvas, extraction-fields, extraction-ground, extraction-toolbar) and the page
  // frame are pane chrome that no wire field names. The bound is therefore a guard against a
  // row retargeted at the grid, not a discovery of what the table should hold.
  const insideALineCell = await page.evaluate(
    (selectors: string[]) =>
      selectors.filter((sel) => {
        const el = document.querySelector(sel)
        return el !== null && el.closest('[data-testid^="extraction-field-line_items"]') !== null
      }),
    subjects.map((s) => s.selector),
  )
  expect(insideALineCell, 'a fidelity subject resolved inside a line-item cell on the deployed pane').toEqual([])

  // Read once, in one frame: two evaluates across a re-render can disagree.
  const measured = await page.evaluate(
    ({ subjects, properties }: { subjects: { element: string; selector: string }[]; properties: Record<string, string[]> }) => {
      const out: Record<string, Record<string, string> | null> = {}
      const counts: Record<string, number> = {}
      for (const s of subjects) {
        counts[s.element] = document.querySelectorAll(s.selector).length
        const el = document.querySelector(s.selector)
        if (!el) {
          out[s.element] = null
          continue
        }
        const cs = getComputedStyle(el)
        const values: Record<string, string> = {}
        for (const p of properties[s.element]) values[p] = cs.getPropertyValue(p)
        // The three read outside the row table, all recorded so the artifact is complete.
        values['letter-spacing'] = cs.getPropertyValue('letter-spacing')
        values['margin-left'] = cs.getPropertyValue('margin-left')
        values['margin-right'] = cs.getPropertyValue('margin-right')
        out[s.element] = values
      }
      // Each token resolved inside the subtree that DECLARES it, never compared against a colour
      // typed into this file. All three are declared on `.asc-app`, so a probe outside that
      // subtree would resolve to nothing.
      const probeIn = (host: Element | null, declaration: string): string => {
        if (!host) return ''
        const probe = document.createElement('div')
        probe.style.cssText = `position:absolute;visibility:hidden;pointer-events:none;width:0;height:0;background:${declaration}`
        host.appendChild(probe)
        const read = getComputedStyle(probe).backgroundColor
        probe.remove()
        return read
      }
      const toolbar = document.querySelector('[data-testid="extraction-toolbar"]')
      const fields = document.querySelector('[data-testid="extraction-fields"]')
      const bg2 = probeIn(toolbar, 'var(--bg-2)')
      const action = probeIn(fields, 'var(--action)')
      const accent = probeIn(fields, 'var(--accent)')

      // The artboard's `width: 100%` on the point button (`:324`). getComputedStyle returns a
      // USED px value, so no string can express it; the observable form is that the button fills
      // its cell's content box. The repo declares no width and lets the cell's flex column
      // stretch it -- a deviation in the declaration and none in the result.
      const button = document.querySelector('[data-testid="extraction-point-buyer_tin"]')
      const cell = document.querySelector('[data-testid="extraction-field-buyer_tin"]')
      let point: { buttonWidth: number; cellContentWidth: number } | null = null
      if (button && cell) {
        const cs = getComputedStyle(cell)
        point = {
          buttonWidth: button.getBoundingClientRect().width,
          cellContentWidth: (cell as HTMLElement).clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight),
        }
      }
      return { out, counts, bg2, action, accent, point }
    },
    { subjects: subjects.map((s) => ({ element: s.element, selector: s.selector })), properties },
  )

  for (const s of subjects) {
    expect(measured.out[s.element], `${s.element} did not render -- every row below it would compare nothing`).not.toBeNull()
    // Only where the row named its own selector: a testid is unique by construction, and the
    // pane's own walk (ExtractionFields.test.tsx, "puts no new testid inside the shipped
    // extraction-field- prefix") is what keeps it so.
    if (s.scoped) {
      expect(
        measured.counts[s.element],
        `${s.selector} matches ${measured.counts[s.element]} nodes -- a class-scoped read must land on exactly one, or the rows below measure some other element diligently`,
      ).toBe(1)
    }
  }
  expect(measured.bg2, 'var(--bg-2) resolved to nothing -- the toolbar background row would compare two empty strings').not.toBe('')
  expect(measured.bg2, 'var(--bg-2) resolved to transparent -- the toolbar background row would be vacuous').not.toBe('rgba(0, 0, 0, 0)')
  for (const [name, value] of [['--action', measured.action], ['--accent', measured.accent]] as const) {
    expect(value, `var(${name}) resolved to nothing -- the two teal deviation rows would compare two empty strings`).not.toBe('')
    expect(value, `var(${name}) resolved to transparent -- the two teal deviation rows would be vacuous`).not.toBe('rgba(0, 0, 0, 0)')
  }
  // Without this AC-2 collapses into comparing a value with itself: a repo that aliased
  // `--accent` to `--action` would make both deviation rows vacuous, and this floor reds first.
  expect(
    measured.accent,
    `--accent and --action both resolve to ${measured.accent} -- this repo aliased them, and the two deviation rows claim nothing`,
  ).not.toBe(measured.action)

  // `declared` keeps the raw table entry -- the placeholder included -- so the artifact still
  // shows that the toolbar row compared a TOKEN and not a colour typed into this file. Both
  // `expected` and `artboard` resolve to the same probe reading, which is what keeps the
  // "expected equals artboard unless it is a stated deviation" check below honest for that row.
  const resolveToken = (value: string): string =>
    value === BG_2_TOKEN ? measured.bg2 : value === ACTION_TOKEN ? measured.action : value === ACCENT_TOKEN ? measured.accent : value
  const table = FIDELITY.map((row) => {
    const expected = resolveToken(row.expected)
    const artboard = resolveToken(row.artboard)
    const deployed = measured.out[row.element]![row.property] ?? ''
    return { ...row, declared: row.artboard, artboard, expected, deployed, match: deployed === expected }
  })

  // The floor, and it is the row SET, not the row count: `table` is `FIDELITY.map(...)`, so a
  // length compare was equal by construction and saw neither a deletion, an addition, nor a row
  // silently retargeted at another element or property. This sees all three.
  expect(
    table.map((r) => `${r.element}·${r.property}`).sort(),
    'the fidelity row set changed',
  ).toEqual(FIDELITY_ROW_IDS)
  for (const row of table) {
    expect(row.deployed, `${row.element} has no resolved ${row.property} -- an empty string matches nothing`).not.toBe('')
    expect(
      row.deployed,
      `${row.element} · ${row.property}: deployed ${row.deployed}, expected ${row.expected} (artboard declares ${row.declared}, Recognition Review.dc.html${row.source})`,
    ).toBe(row.expected)
  }

  // Exactly seven rows deviate, and each says why. A silent divergence added later fails here
  // rather than passing as "documented". The two teal rows are AC-2: with both tokens probed in
  // the pane's own environment, this loop's `expected !== artboard` fires on the fact that this
  // repo does not resolve `--accent` to teal.
  const deviations = table.filter((r) => r.deviation !== null)
  expect(
    deviations.map((r) => `${r.element}·${r.property}`).sort(),
    'the set of stated deviations from the artboard changed',
  ).toEqual([
    'extraction-changed-label-subtotal·color',
    'extraction-marker-subtotal·background-color',
    'extraction-page-1·padding-bottom',
    'extraction-page-1·padding-left',
    'extraction-page-1·padding-right',
    'extraction-page-1·padding-top',
    'extraction-point-buyer_tin·border-top-width',
  ])
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

  // 2. `letter-spacing: 0.05em` at `font-size: 8.5px` (`:317`). Chrome computes it to px, so a
  //    string compare would pin a rounding rather than the artboard's ratio. The subject moved
  //    from the retired READ ONLY pill to the chip's `where` sub-label, which is the em-declared
  //    tracking this screen still has.
  const where = measured.out['extraction-chip-where-issue_date-0']!
  const whereFontPx = parseFloat(where['font-size'])
  const whereTrackPx = parseFloat(where['letter-spacing'])
  expect(Number.isFinite(whereFontPx) && Number.isFinite(whereTrackPx), 'the chip sub-label resolved no font metrics').toBe(true)
  expect(
    Math.abs(whereTrackPx - 0.05 * whereFontPx),
    `the chip sub-label tracks ${whereTrackPx}px on ${whereFontPx}px, and the artboard's 0.05em is ${(0.05 * whereFontPx).toFixed(3)}px`,
  ).toBeLessThanOrEqual(0.02)

  // 3. `letter-spacing: 0.07em` on the reason pill (`:296`) and the changed label (`:335`), both
  //    at 8.5px. Same reason as the chip sub-label, and `.asc-app .mono` declares
  //    `letter-spacing: -0.015em` (app-layer.css:150), so this is also the only deployed proof
  //    that the inline value overrides the class.
  const tracking = [
    { element: 'extraction-pill-issue_date', source: ':296' },
    { element: 'extraction-changed-label-subtotal', source: ':335' },
  ].map(({ element, source }) => {
    const read = measured.out[element]!
    const fontPx = parseFloat(read['font-size'])
    const trackPx = parseFloat(read['letter-spacing'])
    expect(Number.isFinite(fontPx) && Number.isFinite(trackPx), `${element} resolved no font metrics`).toBe(true)
    expect(
      Math.abs(trackPx - 0.07 * fontPx),
      `${element} tracks ${trackPx}px on ${fontPx}px, and the artboard's 0.07em (${source}) is ${(0.07 * fontPx).toFixed(3)}px`,
    ).toBeLessThanOrEqual(0.02)
    return { element, source, fontSizePx: fontPx, letterSpacingPx: trackPx, artboardEm: 0.07 }
  })

  // 4. `width: 100%` on the point button (`:324`) -- a used px value here, so the relationship
  //    is the claim: the button fills its cell's content box.
  expect(measured.point, 'the point button and its cell must both render, or the width claim compares nothing').not.toBeNull()
  expect(measured.point!.cellContentWidth, "the buyer_tin cell has no content width").toBeGreaterThan(0)
  expect(
    Math.abs(measured.point!.buttonWidth - measured.point!.cellContentWidth),
    `the point button is ${measured.point!.buttonWidth}px inside a ${measured.point!.cellContentWidth}px cell content box -- the artboard's width: 100% is not what renders`,
  ).toBeLessThanOrEqual(1)

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
        tokens: { '--bg-2': measured.bg2, '--action': measured.action, '--accent': measured.accent },
        autoMargins: {
          measuredAtWidth: 1920,
          left: frame?.marginLeft ?? null,
          right: frame?.marginRight ?? null,
          frameWidth: frame?.frameWidth ?? null,
          columnWidth: frame?.padWidth ?? null,
        },
        chipWhereTracking: { fontSizePx: whereFontPx, letterSpacingPx: whereTrackPx, artboardEm: 0.05 },
        tracking,
        deviationsOutsideTheTable: DEVIATIONS_OUTSIDE_THE_TABLE.map((d) =>
          d.element === 'extraction-point-buyer_tin' ? { ...d, measured: measured.point } : d,
        ),
        carriedDeviations: CARRIED_DEVIATIONS,
        leftovers: LEFTOVERS,
        metaLine: meta,
      },
      null,
      2,
    ),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-12-09 · the settle-every-field journey, and the pane's floor --------------------
//
// Neither row can execute before this subtask marks the PR ready: `dev-env.yml` gates deploy and
// E2E on `pull_request.draft == false`. The local oracle is `pnpm -r typecheck` plus
// `playwright test --list`.

// The header vocabulary this block reads -- HeaderFields, in the order one Save writes in --
// is `VOCABULARY`, declared once above beside EXTR12-E2E-01. It used to be transcribed twice in
// this file, byte for byte; one list, one copy.

// internal/extraction/handlers_correction.go, lockedFields: a correction on any of the three is
// a 422, so none of them is what this journey types over.
const LOCKED_FIELDS = ['invoice_number', 'supplier_tin', 'supplier_name']

// The story's Invented-copy table, per method (artboard `:639-641`).
const CHANGED_LABEL: Record<string, string> = {
  typed: 'YOU CHANGED THIS',
  pointed: 'YOU POINTED THIS OUT',
  chosen: 'YOU CHOSE THIS',
}

test('EXTR12-E2E-06 (AC-3/AC-5): choose, type and point settle three fields, and the register and the audit log agree', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)

  // The ONE-page fixture: the point surface only needs a page, and EXTR12-E2E-05 already owns
  // the page-2 indexing claim.
  await extractOneDocument(page, 'EXTR-12-09 journey')
  const detail = await openExtractionReview(page)

  // Every subject off the wire the SPA itself consumed, never mock.go, and every floor first: a
  // fixture change that flattened the mock would otherwise leave this journey settling nothing
  // and calling it three corrections.
  const ambiguous = detail.fields.find((f) => f.reason === 'ambiguous')
  expect(ambiguous, 'this document reported no ambiguous field -- there is no candidate to choose').toBeTruthy()
  expect(
    ambiguous!.alternatives.length,
    `${ambiguous!.name} carries no alternative -- the chip this journey clicks does not exist`,
  ).toBeGreaterThanOrEqual(1)
  const chosenValue = ambiguous!.alternatives[0].value
  expect(chosenValue, `${ambiguous!.name}'s first alternative carries no value`).toBeTruthy()
  // Chip 1, never chip 0: chip 0 is the decided reading, whose value equals the wire, so
  // choosing it exercises the keep-a-no-op arm and leaves the value unmoved. This journey needs
  // the value to MOVE.
  expect(
    chosenValue,
    'the first alternative reads the same as the decided one, so choosing it would move nothing',
  ).not.toBe(ambiguous!.value)

  const missing = detail.fields.find((f) => f.reason === 'missing')
  expect(missing, 'this document reported no missing field -- there is nothing to point at').toBeTruthy()
  expect(missing!.value, 'the missing field already carries a value').toBeNull()
  expect(missing!.region, 'the missing field already carries a region, so a drawn box proves nothing').toBeNull()

  // By NAME, the way EXTR12-E2E-02 picks `total` above and EXTR11-E2E-11 picks `subtotal` below --
  // never by position. The wire is ordered by field_name (created_at defaults to now() and
  // writeFieldResultsTx writes a job's rows on ONE transaction, so reader.go's ORDER BY
  // degenerates to the name), and EXTR-13-02's line cells sort ahead of `subtotal`: "the first
  // writable inconsistent field" resolves to line_items[2].line_total, which has no header cell
  // to type into and which refuseField would 422. On the rich fixture `total` itself resolves
  // clean (reason ''), so `subtotal` is both the first writable candidate AND the only one
  // carrying the disagreement. The two properties that pick made implicit are asserted here
  // instead of assumed, so a build that locked `subtotal` or stopped flagging it reds on the wire
  // rather than at the POST. TestExtractionDetail_MockDefaultArrivesInFieldNameOrder pins the
  // ordering from the Go side.
  const typed = detail.fields.find((f) => f.name === 'subtotal')
  expect(typed, 'the wire the screen read carries no `subtotal` -- there is nothing to type over').toBeTruthy()
  expect(LOCKED_FIELDS, 'subtotal is locked now, so the correction this journey posts would be a 422').not.toContain(
    typed!.name,
  )
  expect(
    typed!.reason,
    'subtotal is not flagged inconsistent, so this journey types over a field that never asked to be settled',
  ).toBe('inconsistent')

  const names = [ambiguous!.name, missing!.name, typed!.name]
  expect(new Set(names).size, `two of the three subjects are the same field: ${names.join(', ')}`).toBe(3)
  for (const name of names) expect(VOCABULARY, `${name} is outside the header vocabulary`).toContain(name)

  // What one Save owes, in the vocabulary's own order -- the order savableCorrections sorts into
  // so the append-only table's seq follows the order the person reads.
  const plan = [
    { field: ambiguous!.name, method: 'chosen' },
    { field: missing!.name, method: 'pointed' },
    { field: typed!.name, method: 'typed' },
  ].sort((a, b) => VOCABULARY.indexOf(a.field) - VOCABULARY.indexOf(b.field))

  const save = page.getByTestId('extraction-save')

  // -- 1. CHOOSE ----------------------------------------------------------------------------
  const chips = page.locator(`[data-testid^="extraction-chip-${ambiguous!.name}-"]`)
  // W-3: the decided reading is itself a chip, so the row carries N + 1.
  await expect(chips, 'the ambiguous field rendered no chip row').toHaveCount(ambiguous!.alternatives.length + 1)
  await page.getByTestId(`extraction-chip-${ambiguous!.name}-1`).click()
  await expect(
    page.getByTestId(`extraction-chip-${ambiguous!.name}-1`),
    'the clicked chip is not the current one',
  ).toHaveAttribute('aria-current', 'true')
  await expect(
    page.getByTestId(`extraction-chip-${ambiguous!.name}-0`),
    'the decided reading is still current after another candidate was chosen',
  ).toHaveAttribute('aria-current', 'false')

  // -- 2. TYPE ------------------------------------------------------------------------------
  // Plain digits, no thousands separator: invoiceEditFor writes `$n::text::numeric` and
  // '2,468.00'::numeric raises 22P02 -> ErrValueRefused -> 400.
  const TYPED_VALUE = '2468.00'
  await page.getByTestId(`extraction-input-${typed!.name}`).fill(TYPED_VALUE)
  await expect(save, 'a chosen candidate and a typed value left Save disabled').toBeEnabled()

  // -- 3. POINT -----------------------------------------------------------------------------
  const pointButton = page.getByTestId(`extraction-point-${missing!.name}`)
  await expect(pointButton, `${missing!.name} offers no way to point at it`).toBeVisible({ timeout: 30_000 })
  await pointButton.click()
  await expect(pointButton, 'clicking the point button changed nothing the reader can see').toHaveText(POINT_ARMED, {
    timeout: 15_000,
  })

  await expect(page.getByTestId('extraction-point-surface-1'), 'the armed field takes no drag').toBeVisible({
    timeout: 15_000,
  })
  const s = await boxOf(page, 'extraction-point-surface-1')
  // Comfortably over the 24x12 floor at every frame size in the band.
  await page.mouse.move(s.x + 0.3 * s.width, s.y + 0.25 * s.height)
  await page.mouse.down()
  await page.mouse.move(s.x + 0.62 * s.width, s.y + 0.55 * s.height, { steps: 8 })
  await page.mouse.up()

  const highlight = page.getByTestId('extraction-highlight')
  // applyDraft without its `pointed` arm renders NO highlight, because the missing field's wire
  // region is null. That build reds here.
  await expect(highlight, 'the drawn box highlights nothing').toHaveCount(1)
  await expect(highlight, 'the highlight is not the box drawn on the missing field').toHaveAttribute(
    'data-snip',
    missing!.name,
  )

  // -- 4. FILL THE POINT --------------------------------------------------------------------
  // Required, not decorative: savableCorrections drops a blank pointed entry
  // (extractionReview.ts:267), so without this the box never reaches the wire.
  const POINTED_VALUE = '31775208-0003'
  await page.getByTestId(`extraction-input-${missing!.name}`).fill(POINTED_VALUE)

  // -- 5. SAVE ------------------------------------------------------------------------------
  // Registered BEFORE the click, and .json() called inside the handler so no body is read after
  // the fact.
  const corrections: {
    field: string
    method: string
    hasRegion: boolean
    status: number
    body: Promise<CorrectionResponse | null>
  }[] = []
  page.on('response', (r) => {
    const req = r.request()
    // POST, not merely non-GET: the SPA and the gateway are separate origins, so every one of
    // these is preceded by a CORS preflight OPTIONS carrying no body at all.
    if (req.method() !== 'POST') return
    const path = new URL(r.url()).pathname
    if (!path.endsWith('/corrections')) return
    const sent = (req.postDataJSON() ?? {}) as { method?: string; region?: ExtractionRegion | null }
    corrections.push({
      field: decodeURIComponent(path.split('/fields/')[1]?.split('/')[0] ?? ''),
      method: sent.method ?? '',
      hasRegion: sent.region !== null && sent.region !== undefined,
      status: r.status(),
      body: (r.json() as Promise<CorrectionResponse>).catch(() => null),
    })
  })

  const [reread] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        /\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}$/.test(new URL(r.url()).pathname),
      { timeout: 120_000 },
    ),
    save.click(),
  ])
  expect(reread.status(), 'the post-save re-read failed').toBe(200)

  // Settled before it is read: the re-read is the LAST request the Save makes, but a response
  // event and the promise that resolves on it are dispatched independently. A fourth POST
  // arriving after this poll is still caught by the equality below.
  await expect
    .poll(() => corrections.length, { message: 'the Save did not post three corrections', timeout: 15_000 })
    .toBe(3)

  // Exactly three POSTs, in vocabulary order, each 201. Firing them in parallel reds the order;
  // two POSTs reds the count.
  expect(
    corrections.map((c) => c.field),
    'the Save did not post the three settled fields in vocabulary order',
  ).toEqual(plan.map((p) => p.field))
  expect(
    corrections.map((c) => c.method),
    'the methods did not follow the fields they were posted for',
  ).toEqual(plan.map((p) => p.method))
  expect(corrections.map((c) => c.status), 'a correction was refused').toEqual([201, 201, 201])

  // Only the pointed body carries a region: a chosen request with one is a 400
  // (msgRegionDisagrees), so this is the deployed half of that rule.
  expect(
    corrections.filter((c) => c.hasRegion).map((c) => c.field),
    'a correction other than the pointed one carried a region',
  ).toEqual([missing!.name])

  // The invoice comes off the 201 bodies (CorrectionResponse.invoice_id), never guessed.
  const bodies = await Promise.all(corrections.map((c) => c.body))
  const invoiceIds = new Set(bodies.map((b) => b?.invoice_id ?? ''))
  expect(invoiceIds.size, 'the three corrections named different invoices').toBe(1)
  const invoiceId = [...invoiceIds][0]
  expect(invoiceId, 'no correction body named an invoice, so the audit read below scopes to nothing').toMatch(
    /^[0-9a-fA-F-]{36}$/,
  )

  // -- What the screen says afterwards ------------------------------------------------------
  //
  // Count AND identity: a build that marked every field reds the count, and a build whose
  // correctedMarker picks one arm for all three reds two labels.
  await expect(
    page.locator('[data-testid^="extraction-marker-"]'),
    'the settled fields did not each take exactly one marker',
  ).toHaveCount(3)
  // And the namespace resolves to those three BY NAME. EXTR-13-02 put 15 line-item cells into
  // the pane's render, and a marker renders only over a correction, so this is the assertion
  // that the widened fixture left the `extraction-marker-` sweep where it was: a count alone
  // would still read 3 if a line cell took a marker while a settled field lost its own.
  const markerNames = (
    await page
      .locator('[data-testid^="extraction-marker-"]')
      .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.testid ?? ''))
  )
    .map((id) => id.replace('extraction-marker-', ''))
    .sort()
  expect(markerNames, 'the marker namespace resolved to fields other than the three that were settled').toEqual(
    plan.map((p) => p.field).sort(),
  )
  for (const p of plan) {
    await expect(page.getByTestId(`extraction-marker-${p.field}`), `${p.field} settled and shows no marker`).toBeVisible()
    await expect(
      page.getByTestId(`extraction-field-${p.field}`).getByText(CHANGED_LABEL[p.method], { exact: true }),
      `${p.field} was ${p.method} and does not say "${CHANGED_LABEL[p.method]}"`,
    ).toBeVisible()
  }

  // A settled field stops shouting: reader.go clears Reason and empties Alternatives on a
  // corrected field, so the chip row goes and the input takes its place.
  await expect(
    page.locator(`[data-testid^="extraction-chip-${ambiguous!.name}-"]`),
    'the settled ambiguous field still offers its chips',
  ).toHaveCount(0)
  await expect(
    page.getByTestId(`extraction-input-${ambiguous!.name}`),
    'the settled field holds a value other than the candidate that was chosen',
  ).toHaveValue(chosenValue as string)
  await expect(page.getByTestId(`extraction-input-${missing!.name}`)).toHaveValue(POINTED_VALUE)
  await expect(page.getByTestId(`extraction-input-${typed!.name}`)).toHaveValue(TYPED_VALUE)

  // -- The audit rows -----------------------------------------------------------------------
  //
  // Filtered on the EVENT and matched on the payload, never `invoice_id`: audit_log.invoice_id
  // is a GENERATED column whose CASE lists no extraction event
  // (migrations/20260822080722_audit_log_invoice_id_column_and_index.sql), so an invoice-scoped
  // read of these rows is empty by construction. The control read below asserts exactly that.
  const scoped = await getAuditLog(token, { event: ['extraction.field_corrected'], limit: 100 })
  const mine = scoped.events.filter((e) => (e.payload as { invoice_id?: string }).invoice_id === invoiceId)
  expect(
    mine.length,
    `this invoice recorded ${mine.length} extraction.field_corrected events, not the three the Save posted`,
  ).toBe(3)
  expect(
    mine.map((e) => (e.payload as { field?: string }).field).sort(),
    'the audit payloads name other fields than the three that were settled',
  ).toEqual(plan.map((p) => p.field).sort())
  expect(
    mine.map((e) => (e.payload as { method?: string }).method).sort(),
    'the audit payloads record other methods than the three that were used',
  ).toEqual(plan.map((p) => p.method).sort())
  // migrations/20260829195203_audit_log_entity_for_extraction.sql resolves these rows to a
  // company THROUGH the invoice they correct; a NULL entity_id is the workspace-level spelling
  // and would misfile a client action as firm-wide.
  for (const e of mine) {
    expect(e.entity_id, `an extraction.field_corrected row was filed workspace-wide (${e.id})`).not.toBeNull()
  }

  // The event filter's own non-vacuity control: the tenant-wide log must be strictly larger, or
  // "exactly three" would also be true of a reader that returned everything it had.
  const wholeLog = await getAuditLog(token, { limit: 1 })
  expect(
    wholeLog.total,
    `the workspace log holds ${wholeLog.total} events and the extraction filter ${scoped.total} -- the filter narrowed nothing`,
  ).toBeGreaterThan(scoped.total)

  // The reason the filter is on the event. invoice-surfaces.spec.ts pins the same fact from the
  // UI side ("Document and extraction events are recorded against the workspace, not against a
  // single invoice"), and a build that added the event to the generated column reds both.
  const byInvoice = await getAuditLog(token, {
    invoice_id: invoiceId,
    event: ['extraction.field_corrected'],
    limit: 100,
  })
  expect(
    byInvoice.events.length,
    'audit_log.invoice_id now resolves an extraction correction -- invoice-surfaces.spec.ts still claims it cannot',
  ).toBe(0)

  await testInfo.attach('extraction-settle-every-field.json', {
    body: JSON.stringify(
      {
        plan,
        chosenValue,
        typedValue: TYPED_VALUE,
        pointedValue: POINTED_VALUE,
        invoiceId,
        posted: corrections.map((c) => ({ field: c.field, method: c.method, hasRegion: c.hasRegion, status: c.status })),
        audit: mine.map((e) => ({ id: e.id, event: e.event, entityId: e.entity_id, payload: e.payload })),
        auditTotals: { extractionEvents: scoped.total, wholeLog: wholeLog.total, invoiceScoped: byInvoice.events.length },
        notCovered: ['Undo (method=undone)', 'a partial-failure Save', "AA-22b's chosen-no-op arm"],
      },
      null,
      2,
    ),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR12-E2E-07 (AC-4, W-6): the fields pane keeps its floor and its two columns, and no cell spills at the floor', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-12-09 floor')
  const detail = await openExtractionReview(page)
  expect(detail.fields.length, 'no field on this document -- every measurement below is vacuous').toBeGreaterThan(0)

  // EXTR-13-07: the fields pane renders the header vocabulary only -- a line-item cell has its
  // own grid, LineItemGrid -- so every rendered-row count below is bounded to it.
  const wireNamesB = detail.fields.map((f) => f.name)
  const headerNamesB = wireNamesB.filter((n) => !n.startsWith('line_items'))
  expect(headerNamesB.length, 'no header field on this document -- every measurement below is vacuous').toBeGreaterThan(0)

  const pane = page.getByTestId('extraction-fields')
  const shellBody = page.getByTestId('extraction-review-body')
  // The pane's two children, in the artboard's order (`:225` header over `:230` body) --
  // EXTR11-E2E-02b resolves the scroller the same way, and the body is the only scroller.
  const paneBody = pane.locator('> div').nth(1)
  const cells = page.locator('[data-testid^="extraction-field-"]')

  type Wide = {
    width: number
    pane: Rect
    body: Rect
    paneGaps: { left: number; right: number }
    columns: number[]
    cells: number
    scrollWidth: number
    clientWidth: number
  }
  const measured: Wide[] = []
  const entryViewport = page.viewportSize()
  try {
    // Widest first (WIDE_WIDTHS' own order, layout.ts).
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [p, b] = await Promise.all([pane.boundingBox(), shellBody.boundingBox()])
        const scroller = await paneBody.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
        // The track count read from GEOMETRY, not from a stylesheet: the grid div carries no
        // testid, and a structural selector into it would silently retarget.
        const xs = await cells.evaluateAll((els) => els.map((el) => Math.round(el.getBoundingClientRect().x)))
        return { p, b, scroller, xs }
      }, `fields pane geometry at ${width}px`)

      expect(m.p && m.b, `the fields pane and the shell body must both render at ${width}px`).toBeTruthy()
      expect(m.p!.width, `the fields pane has no width at ${width}px`).toBeGreaterThan(0)

      // 1. The artboard's floor (`:223`). A PRECONDITION here, and the same claim
      //    EXTR11-E2E-10 makes -- stated so it is not counted twice.
      expect(m.p!.width, `the fields pane is below its 470px floor at ${width}px`).toBeGreaterThanOrEqual(469)

      // 2. Inside the shell body on BOTH edges -- gaps()'s rule.
      const g = gaps(m.p as Rect, m.b as Rect)
      expect(g.left, `the fields pane passes the body's left edge by ${-g.left}px at ${width}px`).toBeGreaterThanOrEqual(-1)
      expect(g.right, `the fields pane passes the body's right edge by ${-g.right}px at ${width}px`).toBeGreaterThanOrEqual(-1)

      // 3. Two columns, exactly. A collapsed one-column grid reports 1, a three-track grid 3,
      //    and nothing else in this suite asserts the track count.
      expect(m.xs.length, `no field cell measured at ${width}px`).toBe(headerNamesB.length)
      const columns = [...new Set(m.xs)].sort((a, b) => a - b)
      expect(columns.length, `the grid reports ${columns.length} column(s) at ${width}px, not two`).toBe(2)

      // 4. The pane's own scroller has nothing to scroll sideways. `overflow-y: auto` with
      //    `overflow-x: visible` computes to `overflow-x: auto`, so the body IS a scroll
      //    container on both axes and its scrollWidth is well defined.
      expect(m.scroller.clientWidth, `the pane body has no width at ${width}px`).toBeGreaterThan(0)
      expect(
        m.scroller.scrollWidth,
        `the pane body scrolls ${m.scroller.scrollWidth - m.scroller.clientWidth}px sideways at ${width}px`,
      ).toBeLessThanOrEqual(m.scroller.clientWidth + 1)

      measured.push({
        width,
        pane: m.p as Rect,
        body: m.b as Rect,
        paneGaps: g,
        columns,
        cells: m.xs.length,
        scrollWidth: m.scroller.scrollWidth,
        clientWidth: m.scroller.clientWidth,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])

  // -- W-6: the label strip AT the pane's floor ---------------------------------------------
  //
  // NO LOCAL ORACLE. The overflow is a text-measurement fact, so it needs the deployed build's
  // real IBM Plex Mono and Inter; this row first executes on the deploy gate. The declaration
  // half is ExtractionFields.test.tsx, "wraps the label strip".
  //
  // Measured before the fix, in Chromium at the floor's own geometry (470 - 40 body padding -
  // 16 grid gap, halved, - 16 cell padding = 191px of cell content): the pill overhung
  // `issue_date` by 7.84px and `vat` by 6.02px, and by 15.34 / 13.52 with CI's classic
  // scrollbar in the pane body. `flexWrap: 'wrap'` on LABEL_STRIP is what closed it.
  //
  // The pane is NOT at its floor at any WIDE_WIDTHS -- both panes are flex siblings and the
  // shrink is proportional -- so this descends until it is.
  let floorWidth: number | null = null
  try {
    for (let width = 1280; width >= 1000; width -= 40) {
      await page.setViewportSize({ width, height: 1080 })
      const paneWidth = await settledRead(async () => (await pane.boundingBox())?.width ?? 0, `pane width at ${width}px`)
      if (paneWidth > 0 && paneWidth <= 471) {
        floorWidth = width
        break
      }
    }

    expect(
      floorWidth,
      'the 470px floor is unreachable at or above 1000px -- W-6 cannot be measured from this side, and that is itself a finding',
    ).not.toBeNull()

    // Every cell, not only the two predicted offenders: the walk is cheap and a wrong build
    // spills wherever its copy is longest.
    const spill = await page.evaluate(() => {
      const out: {
        testid: string
        scrollWidth: number
        clientWidth: number
        worst: { node: string; outLeft: number; outRight: number }
      }[] = []
      for (const cell of Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="extraction-field-"]'))) {
        const c = cell.getBoundingClientRect()
        let worst = { node: '', outLeft: 0, outRight: 0 }
        for (const el of Array.from(cell.querySelectorAll<HTMLElement>('*'))) {
          const r = el.getBoundingClientRect()
          // A rect collapsed on both axes is inside anything and would pass vacuously.
          if (r.width === 0 && r.height === 0) continue
          const outLeft = c.left - r.left
          const outRight = r.right - c.right
          if (Math.max(outLeft, outRight) > Math.max(worst.outLeft, worst.outRight)) {
            worst = { node: el.dataset.testid ?? el.tagName.toLowerCase(), outLeft, outRight }
          }
        }
        out.push({ testid: cell.dataset.testid ?? '', scrollWidth: cell.scrollWidth, clientWidth: cell.clientWidth, worst })
      }
      return out
    })

    expect(spill.length, `no field cell measured at the ${floorWidth}px floor`).toBe(headerNamesB.length)
    for (const cell of spill) {
      expect(cell.clientWidth, `${cell.testid} has no width at the floor -- its edges are vacuous`).toBeGreaterThan(0)
      expect(
        cell.worst.outLeft,
        `${cell.testid}: ${cell.worst.node} starts ${cell.worst.outLeft.toFixed(2)}px left of its cell at ${floorWidth}px`,
      ).toBeLessThanOrEqual(1)
      expect(
        cell.worst.outRight,
        `${cell.testid}: ${cell.worst.node} ends ${cell.worst.outRight.toFixed(2)}px right of its cell at ${floorWidth}px`,
      ).toBeLessThanOrEqual(1)
      // A second and independent instrument. Verified in Chromium that scrollWidth DOES report
      // inline-end overflow on an `overflow: visible` box, which CSSOM's step 3 would not
      // predict -- so this is measured behaviour, not a spec reading.
      expect(
        cell.scrollWidth,
        `${cell.testid} holds ${cell.scrollWidth}px of content in a ${cell.clientWidth}px box at ${floorWidth}px`,
      ).toBeLessThanOrEqual(cell.clientWidth + 1)
    }

    await testInfo.attach('extraction-fields-pane-floor.json', {
      body: JSON.stringify({ wide: measured, floorWidth, spill }, null, 2),
      contentType: 'application/json',
    })
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-13-09 · the line-item grid on the deployed fleet -------------------------------
//
// The FINAL subtask of EXTR-13. Everything provable in jsdom (LineItemGrid.test.tsx,
// lineItems.test.ts, ExtractionReview.test.tsx) or over HTTP (EXTR13-API-01/02/03 in
// e2e/api/extractions.spec.ts) is deliberately NOT re-proven here. What is left is what only a
// browser against the deployed fleet can observe: the grid's real geometry at four widths, the
// live recompute, the row -> region highlight, and one Save that reaches the deployed store.
//
// THE NINE PINNED SPECS this story's sweep named, and where each stands. All nine were moved by
// the earlier subtasks; this row is the ledger, not a second edit:
//   EXTR11-E2E-02a (:3155)  UPDATED in 13-07 -- its `extraction-field-` sweep is bounded to the
//                           header vocabulary (`headerNamesA`), so a line cell cannot join it.
//   EXTR12-E2E-07 (:5307)   UPDATED in 13-07 -- same bound at the pane's 470px floor
//                           (`headerNamesB`); the grid sits BELOW the two-column grid and is not
//                           a third column, which EXTR13-LAYOUT-03 below asserts from the front.
//   EXTR12-E2E-01 (:3569)   UPDATED in 13-02/07 -- the per-field pill loop excludes line names.
//   EXTR11-E2E-04/04b (:2858) UPDATED in 13-02 -- the wire body's field SET is the widened
//                           twenty-three, listed literally as the only deployed oracle for it.
//   EXTR11-E2E-11 (:4611)   UPDATED in 13-02/07 per D-13 -- zero FIDELITY rows, one LEFTOVERS
//                           entry naming this PR, and both sweeps bounded to the header pane.
//   EXTR12-E2E-06 (:4998)   UPDATED in 13-02 -- the `extraction-marker-` sweep resolves BY NAME
//                           to the three settled fields, so a line marker cannot stand in.
//   EXTR11-E2E-02 (:3257)   DELIBERATELY UNCHANGED -- it measures the shell's two flex siblings
//                           tiling the body. The grid is a child of the fields pane's scrolling
//                           body and adds no pane, so the tiling claim is untouched.
//   EXTR11-E2E-10 (:3328)   DELIBERATELY UNCHANGED -- the pane's 470px floor is declared on
//                           PANE, not derived from its content, and the grid absorbs its own
//                           overflow in `line-item-scroll` (EXTR13-LAYOUT-01), so it cannot
//                           raise that floor.
//   EXTR11-E2E-02b (:3395)  DELIBERATELY UNCHANGED -- it asserts the pane body is the scroller
//                           and the header does not move. The grid adds rows INSIDE that body,
//                           which strengthens its own precondition rather than changing it.
//
// TWO GAPS this subtask does NOT close, named so they are not mistaken for coverage:
//   1. The EXTRACTION-FOUND-NOTHING path has no deployed subject: the mock keys its fixtures by
//      SHA-256 and `uniquePdfBytes()` mints a fresh hash per upload, so every deployed run
//      resolves to `mockDefaultResult`, which always carries four lines. This is a gap in how
//      the panel is REACHED, not in the panel. The panel itself is deployed-covered by
//      EXTR13-E2E-02 below, which removes every row to reach the same single branch.
//   2. `extraction-write-error` still has no deployed coverage (jsdom only) -- unchanged from
//      EXTR-12, and not this story's to close.

const LINE_ROLE_NAMES = ['description', 'quantity', 'unit_price', 'line_total'] as const
type LineRoleName = (typeof LINE_ROLE_NAMES)[number]

// lineItems.ts's LINE_FIELD_RE, restated rather than imported: e2e/ compiles against no
// frontend source, and a spec that imported the parser would assert the parser against itself.
const LINE_CELL_RE = /^line_items\[([1-9][0-9]*)\]\.(description|quantity|unit_price|line_total)$/

type WireLineCell = { name: string; value: string | null; region: ExtractionRegion | null }
type WireLine = { index: number; cells: Partial<Record<LineRoleName, WireLineCell>> }

/** The line block off the wire the SPA itself consumed, in the grid's own row order. */
function wireLines(detail: ExtractionDetail): WireLine[] {
  const byIndex = new Map<number, WireLine>()
  for (const f of detail.fields) {
    const m = LINE_CELL_RE.exec(f.name)
    if (m === null) continue
    const index = Number(m[1])
    let row = byIndex.get(index)
    if (row === undefined) {
      row = { index, cells: {} }
      byIndex.set(index, row)
    }
    row.cells[m[2] as LineRoleName] = { name: f.name, value: f.value, region: f.region }
  }
  return [...byIndex.values()].sort((a, b) => a.index - b.index)
}

function decimalOf(raw: string | null | undefined): number | null {
  if (raw === null || raw === undefined || raw.trim() === '') return null
  const n = Number(raw)
  return Number.isFinite(n) ? n : null
}

// rowArithmetic's rule (lineItems.ts), computed here from the wire's own numbers so the grid is
// checked against the document rather than against the module that draws it. LINE_TOLERANCE is
// 0.01 and the comparison is STRICTLY greater, so exactly 0.01 does not flag; the 1e-9 absorbs
// the binary float this arithmetic runs in, three orders below the 150.00 the mock disagrees by.
function wireRowState(row: WireLine): 'ok' | 'flagged' | 'unchecked' {
  const q = decimalOf(row.cells.quantity?.value)
  const p = decimalOf(row.cells.unit_price?.value)
  const t = decimalOf(row.cells.line_total?.value)
  if (q === null || p === null || t === null) return 'unchecked'
  return Math.abs(q * p - t) > 0.01 + 1e-9 ? 'flagged' : 'ok'
}

/** Every rendered cell value, by ordinal then role, as the grid holds it right now. */
async function gridValues(page: Page, rowCount: number): Promise<Record<LineRoleName, string>[]> {
  const out: Record<LineRoleName, string>[] = []
  for (let n = 1; n <= rowCount; n += 1) {
    const row = {} as Record<LineRoleName, string>
    for (const role of LINE_ROLE_NAMES) {
      row[role] = await page.getByTestId(`line-item-input-${n}-${role}`).inputValue()
    }
    out.push(row)
  }
  return out
}

/** The rendered ordinals of a `line-item-*` namespace, ascending. */
async function lineOrdinals(page: Page, prefix: string): Promise<number[]> {
  const ids = await page
    .locator(`[data-testid^="${prefix}"]`)
    .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.testid ?? ''))
  return ids.map((id) => Number(id.slice(prefix.length))).sort((a, b) => a - b)
}

/**
 * One upload, one review screen, and the floors every claim below rests on.
 *
 * The floors are asserted rather than assumed: a fixture change that flattened the mock's line
 * block would otherwise leave every sweep in this section measuring nothing and passing.
 */
async function openLineGrid(page: Page, label: string): Promise<{ detail: ExtractionDetail; lines: WireLine[] }> {
  await extractOneDocument(page, label)
  const detail = await openExtractionReview(page)
  const lines = wireLines(detail)
  expect(
    lines.length,
    'this document delivered no line-item cell on the wire -- every grid claim below is vacuous',
  ).toBeGreaterThanOrEqual(2)

  await expect(page.getByTestId('line-item-grid'), 'the fields pane rendered no line-item grid').toBeVisible({
    timeout: 30_000,
  })
  await expect(
    page.locator('[data-testid^="line-item-row-"]'),
    'the grid did not render one row per line the wire carried',
  ).toHaveCount(lines.length)
  return { detail, lines }
}

test('EXTR13-E2E-01 (Core AC 1-7): the deployed grid reads, flags, sums, selects, remaps, grows, shrinks and saves', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const { detail, lines } = await openLineGrid(page, 'EXTR-13-09 journey')

  // -- 1. every cell the wire carried reaches its own input, by name ------------------------
  const present = lines.flatMap((row, i) =>
    LINE_ROLE_NAMES.filter((role) => row.cells[role] !== undefined).map((role) => ({
      n: i + 1,
      role,
      wire: row.cells[role] as WireLineCell,
    })),
  )
  const absent = lines.flatMap((row, i) =>
    LINE_ROLE_NAMES.filter((role) => row.cells[role] === undefined).map((role) => ({ n: i + 1, role })),
  )
  expect(present.length, 'the wire carried no line cell at all').toBeGreaterThanOrEqual(LINE_ROLE_NAMES.length)
  // The control needle for the empty-cell arm below: with nothing absent it asserts nothing.
  expect(
    absent.length,
    'every line on this document is complete, so the empty-cell arm below has no subject',
  ).toBeGreaterThanOrEqual(1)

  for (const c of present) {
    await expect(
      page.getByTestId(`line-item-input-${c.n}-${c.role}`),
      `${c.wire.name} did not reach row ${c.n}'s ${c.role} input`,
    ).toHaveValue(c.wire.value ?? '')
  }
  for (const c of absent) {
    await expect(
      page.getByTestId(`line-item-input-${c.n}-${c.role}`),
      `row ${c.n} has no ${c.role} on the wire, yet the grid put something in that cell`,
    ).toHaveValue('')
  }

  // -- 2. the flag is per row, and lands only where the wire's own numbers disagree ----------
  const states = lines.map((row, i) => ({ n: i + 1, state: wireRowState(row) }))
  const flagged = states.filter((s) => s.state === 'flagged').map((s) => s.n)
  const settledRows = states.filter((s) => s.state === 'ok').map((s) => s.n)
  expect(flagged.length, "no row's own numbers disagree -- the flag claim is vacuous").toBeGreaterThanOrEqual(1)
  expect(
    settledRows.length,
    'every checkable row disagrees, so a grid that flagged all of them would pass -- there is no negative arm',
  ).toBeGreaterThanOrEqual(1)
  expect(
    await lineOrdinals(page, 'line-item-flag-'),
    'the flagged rows are not the rows whose own numbers disagree',
  ).toEqual([...flagged].sort((a, b) => a - b))

  // -- 3. the table-level statement carries BOTH numbers ------------------------------------
  for (const row of lines) {
    const raw = row.cells.line_total?.value
    if (raw === null || raw === undefined) continue
    expect(raw, `${raw} carries more than two decimals, so the rendered sum is not toFixed(2)`).toMatch(
      /^-?\d+(\.\d{1,2})?$/,
    )
  }
  const totals = lines.map((r) => decimalOf(r.cells.line_total?.value)).filter((n): n is number => n !== null)
  expect(totals.length, 'no line carries a parseable total -- there is no sum to state').toBeGreaterThanOrEqual(2)
  const printedRaw = detail.fields.find((f) => f.name === 'subtotal')?.value ?? null
  expect(printedRaw, 'this document prints no subtotal -- the disagreement below cannot be shown').not.toBeNull()
  expect(printedRaw as string, 'the printed subtotal carries more than two decimals').toMatch(/^-?\d+(\.\d{1,2})?$/)
  const summed = totals.reduce((a, b) => a + b, 0)
  const printed = decimalOf(printedRaw) as number
  expect(
    Math.abs(summed - printed),
    'the lines already agree with the printed subtotal, so the statement does not render at all',
  ).toBeGreaterThan(0.01)

  const sumLine = page.getByTestId('line-item-sum')
  await expect(sumLine, 'the lines disagree with the printed subtotal and the grid says nothing').toBeVisible()
  const sumText = (await sumLine.textContent()) ?? ''
  expect(sumText, `the statement does not carry the summed figure ${summed.toFixed(2)}`).toContain(summed.toFixed(2))
  expect(sumText, `the statement does not carry the printed figure ${printed.toFixed(2)}`).toContain(printed.toFixed(2))

  // -- 4. a line cell selects, and the document draws THAT cell's region ---------------------
  const located = present.find((c) => c.wire.region !== null)
  expect(located, 'no line cell carries a region -- the highlight claim is vacuous').toBeTruthy()
  const region = (located as { wire: WireLineCell }).wire.region as ExtractionRegion
  const cell = page.getByTestId(`line-item-cell-${located!.n}-${located!.role}`)
  await cell.click()
  const imageId = `extraction-page-image-${region.page}`
  await expect(page.getByTestId(imageId), "the selected cell's page must load").toBeVisible({ timeout: 60_000 })
  await expect(cell, 'the clicked line cell does not read as selected').toHaveAttribute('aria-current', 'true')
  const highlight = page.getByTestId('extraction-highlight')
  await expect(highlight, 'a selected line cell drew no highlight, or drew more than one').toHaveCount(1)
  // The identity claim, and AC-7's whole point: a line cell reaches the canvas over the SAME
  // channel a header cell does, so the box the document paints names the line field by name.
  await expect(highlight, 'the highlight names a field other than the line cell that was clicked').toHaveAttribute(
    'data-snip',
    located!.wire.name,
  )
  const highlightBox = await boxOf(page, 'extraction-highlight')
  const imageBox = await boxOf(page, imageId)
  expect(
    highlightBox.width * highlightBox.height,
    'the highlight has no area, so the containment below is vacuous',
  ).toBeGreaterThan(0)
  const inside = overlapOf(highlightBox, imageBox)
  expect(inside.width, 'the highlight is not horizontally inside the page it names').toBeGreaterThanOrEqual(
    highlightBox.width - RATIO_TOL_PX,
  )
  expect(inside.height, 'the highlight is not vertically inside the page it names').toBeGreaterThanOrEqual(
    highlightBox.height - RATIO_TOL_PX,
  )

  // -- 5. correcting the flagged row recomputes live, with no request ------------------------
  const flaggedN = flagged[0]
  const flaggedRow = lines[flaggedN - 1]
  const q = decimalOf(flaggedRow.cells.quantity?.value) as number
  const p = decimalOf(flaggedRow.cells.unit_price?.value) as number
  const settledTotal = (q * p).toFixed(2)
  expect(
    settledTotal,
    'the corrected total equals what row ' + flaggedN + ' already holds, so typing it moves nothing',
  ).not.toBe(flaggedRow.cells.line_total?.value)

  const lineWrites: string[] = []
  page.on('request', (r) => {
    if (r.method() !== 'POST') return
    if (new URL(r.url()).pathname.endsWith('/line-items')) lineWrites.push(r.url())
  })

  await page.getByTestId(`line-item-input-${flaggedN}-line_total`).fill(settledTotal)
  await expect(
    page.getByTestId(`line-item-flag-${flaggedN}`),
    `row ${flaggedN}'s numbers now agree and it still carries its flag`,
  ).toHaveCount(0)
  expect(
    await lineOrdinals(page, 'line-item-flag-'),
    'correcting one row moved another row’s flag',
  ).toEqual(flagged.filter((n) => n !== flaggedN))
  await expect(
    page.getByTestId(`line-item-marker-${flaggedN}-line_total`),
    'the corrected cell shows no changed marker',
  ).toBeVisible()
  expect(lineWrites, 'a live recompute posted to the server -- nothing reaches the store before Save').toEqual([])

  // -- 6. remapping a column moves the cells, and remapping back restores them ---------------
  const swappable = lines
    .map((row, i) => ({ n: i + 1, row }))
    .filter(
      ({ row }) =>
        row.cells.quantity !== undefined &&
        row.cells.unit_price !== undefined &&
        row.cells.quantity.value !== row.cells.unit_price.value,
    )
  expect(
    swappable.length,
    'no row carries two DIFFERENT values in the two columns being swapped -- a swap could not be observed',
  ).toBeGreaterThanOrEqual(1)

  const quantityColumn = page.getByTestId('line-item-role-quantity')
  await quantityColumn.selectOption('unit_price')
  for (const { n, row } of swappable) {
    await expect(
      page.getByTestId(`line-item-input-${n}-quantity`),
      `row ${n}'s quantity column did not take the unit price`,
    ).toHaveValue(row.cells.unit_price?.value ?? '')
    await expect(
      page.getByTestId(`line-item-input-${n}-unit_price`),
      `row ${n}'s unit-price column did not take the quantity`,
    ).toHaveValue(row.cells.quantity?.value ?? '')
  }
  // The selector always reads its OWN column's role, so the same choice repeats the swap.
  await quantityColumn.selectOption('unit_price')
  for (const { n, row } of swappable) {
    await expect(
      page.getByTestId(`line-item-input-${n}-quantity`),
      `row ${n}'s quantity was not restored by the reverse remap`,
    ).toHaveValue(row.cells.quantity?.value ?? '')
    await expect(
      page.getByTestId(`line-item-input-${n}-unit_price`),
      `row ${n}'s unit price was not restored by the reverse remap`,
    ).toHaveValue(row.cells.unit_price?.value ?? '')
  }

  // -- 7. add and remove, with the ordinals staying 1..N --------------------------------------
  const rowCount = lines.length
  await page.getByTestId('line-item-add').click()
  await expect(
    page.locator('[data-testid^="line-item-row-"]'),
    'Add left the row count where it was',
  ).toHaveCount(rowCount + 1)
  await expect(
    page.getByTestId(`line-item-input-${rowCount + 1}-description`),
    'the appended row is not numbered N+1, or carries a value it was never given',
  ).toHaveValue('')
  await page.getByTestId(`line-item-remove-${rowCount + 1}`).click()
  await expect(page.locator('[data-testid^="line-item-row-"]'), 'Remove left the row count where it was').toHaveCount(
    rowCount,
  )
  expect(await lineOrdinals(page, 'line-item-row-'), 'the row ordinals are not 1..N with no gap').toEqual(
    Array.from({ length: rowCount }, (_, i) => i + 1),
  )

  // -- 8. one Save, one line POST, and the store answers -------------------------------------
  const save = page.getByTestId('extraction-save')
  await expect(save, 'a line-only edit left Save disabled').toBeEnabled()

  const posts: { status: number; body: Promise<Record<string, unknown> | null> }[] = []
  page.on('response', (r) => {
    if (r.request().method() !== 'POST') return
    if (!new URL(r.url()).pathname.endsWith('/line-items')) return
    posts.push({ status: r.status(), body: (r.json() as Promise<Record<string, unknown>>).catch(() => null) })
  })

  const [reread] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        /\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}$/.test(new URL(r.url()).pathname),
      { timeout: 120_000 },
    ),
    save.click(),
  ])
  expect(reread.status(), 'the post-save re-read failed').toBe(200)

  await expect
    .poll(() => posts.length, { message: 'the Save posted no line set', timeout: 15_000 })
    .toBe(1)
  expect(posts[0].status, 'the line set was refused').toBe(201)
  const posted = await posts[0].body
  expect(posted, 'the line-items 201 carried no body').not.toBeNull()
  const echoed = (posted as Record<string, unknown>).lines as Record<string, string | null>[]
  expect(echoed.length, 'the server wrote a different number of lines than the grid held').toBe(rowCount)
  expect(
    echoed.map((l) => l.line_total),
    'the total that was corrected is not in the set the server wrote',
  ).toContain(settledTotal)

  // The server's own values, read back. The line set lands on the `line_items` BLOCK row as one
  // canonical-JSON correction (handlers_lineitems.go:169-181), and the reader projects that row
  // onto the per-cell `line_items[N].role` readings, which no correction ever names directly
  // (expandLineCorrection, internal/extraction/reader.go). The block row is where the write's
  // own record is checked, below; the cells it projects onto are what the reopen reads.
  const fresh = (await reread.json()) as ExtractionDetail
  const block = fresh.fields.find((f) => f.name === 'line_items')
  expect(block, 'the re-read carries no line_items block row -- the correction had nothing to land on').toBeTruthy()
  expect(block!.corrected, 'the Save reached the store and left no correction on the block row').not.toBeNull()
  expect(block!.corrected!.method, 'a line set is recorded as typed').toBe('typed')
  expect(
    block!.value,
    'the block row does not hold the set the server echoed at 201 -- the write and the read disagree',
  ).toBe(JSON.stringify(echoed))

  await expect(page.getByTestId('extraction-write-error'), 'the Save reported an error').toHaveCount(0)
  await expect(save, 'a committed Save left something still to save').toBeDisabled()

  // The reopen, and the defect it closes: a saved line correction snapped back to the
  // extractor's own reading on every reopen. The re-read JSON above proves the write happened,
  // never that a fresh mount shows it.
  //
  // NOT a reload. The screen has no URL of its own -- App.tsx renders it while
  // `view === 'extraction'` off a jobId in component state, and nothing puts that jobId in the
  // hash -- so a reload lands on the default view and re-requests no detail. Sidebar out,
  // entry control back in, which unmounts ExtractionReview and drops the draft it held.
  //
  // Nav idiom: invoice-surfaces.spec.ts's goToInvoices/openInvoiceRow. The row is taken by
  // count -- this entity was created for this run and the list is narrowed to it server-side --
  // so a second row fails here rather than opening some other invoice.
  await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: /Invoices/ }).click()
  await expect(page, 'inline nav to Invoices did not update the URL').toHaveURL(/\/invoices$/)
  await expect(page.getByTestId('invoices-list'), 'the sidebar nav did not land on the invoices list').toBeVisible({
    timeout: 60_000,
  })
  await expect(
    page.getByTestId('extraction-review'),
    'the review screen is still mounted, so what follows is not a reopen',
  ).toHaveCount(0)
  const invoiceRows = page.getByTestId('invoice-row')
  await expect(
    invoiceRows,
    'this entity was created for this run and holds exactly the one imported invoice',
  ).toHaveCount(1)
  await invoiceRows.click()
  await expect(page.getByTestId('invoice-detail'), 'the row did not open the live invoice detail').toBeVisible({
    timeout: 60_000,
  })

  // The same entry control every spec in this section opens the screen with.
  const reopened = await openExtractionReview(page)
  const savedCell = flaggedRow.cells.line_total as WireLineCell
  const reopenedCell = reopened.fields.find((f) => f.name === savedCell.name)
  expect(reopenedCell, `the reopened detail carries no ${savedCell.name} reading at all`).toBeTruthy()
  expect(
    reopenedCell!.value,
    `${savedCell.name} came back from a fresh read as the extractor's own reading ` +
      `(${savedCell.value}) instead of the saved ${settledTotal}`,
  ).toBe(settledTotal)
  await expect(page.getByTestId('line-item-grid'), 'the reopened page rendered no line-item grid').toBeVisible({
    timeout: 30_000,
  })
  await expect(
    page.getByTestId(`line-item-input-${flaggedN}-line_total`),
    `row ${flaggedN}'s line_total snapped back to the extractor's own reading ` +
      `(${flaggedRow.cells.line_total?.value}) on reopen, instead of keeping the saved ${settledTotal}`,
  ).toHaveValue(settledTotal)

  await testInfo.attach('extraction-line-grid-journey.json', {
    body: JSON.stringify(
      { lines: lines.length, flagged, settledRows, summed, printed, settledTotal, echoed },
      null,
      2,
    ),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR13-E2E-02 · the columns E2E-01 leaves untouched, and the empty panel -------------
//
// Its own upload, because E2E-01 ends on a Save and a reopen: an edit added after that point
// could not be restored, and one added before it would move the set that Save posts. Nothing
// here saves, so every gesture below is free to leave the draft where it lands.

test('EXTR13-E2E-02 (Core AC 2, 7, 8): every column selector, both untyped columns, and the empty panel', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const { lines } = await openLineGrid(page, 'EXTR-13-13 columns and empty')
  const rowCount = lines.length

  // -- 1. all four column selectors render, each reading its own column ----------------------
  // E2E-01 drives `line-item-role-quantity` alone, so a grid that shipped one selector passes it.
  for (const role of LINE_ROLE_NAMES) {
    const select = page.getByTestId(`line-item-role-${role}`)
    await expect(select, `the ${role} column carries no role selector`).toBeVisible()
    await expect(select, `the ${role} selector does not read its own column`).toHaveValue(role)
    await expect(select, `the ${role} selector is not operable`).toBeEnabled()
  }

  // -- 2. each of the four is individually operable, and moves only its own pair -------------
  // Expectations are read off the grid immediately before each swap rather than off the wire,
  // so a later pair is measured against what the earlier ones actually left on screen.
  const pairs: [LineRoleName, LineRoleName][] = [
    ['description', 'quantity'],
    ['quantity', 'unit_price'],
    ['unit_price', 'line_total'],
    ['line_total', 'description'],
  ]
  for (const [driver, partner] of pairs) {
    const before = await gridValues(page, rowCount)
    expect(
      before.filter((row) => row[driver] !== row[partner]).length,
      `no row holds different values in ${driver} and ${partner} -- a swap could not be observed`,
    ).toBeGreaterThanOrEqual(1)

    await page.getByTestId(`line-item-role-${driver}`).selectOption(partner)
    const after = await gridValues(page, rowCount)
    for (let i = 0; i < before.length; i += 1) {
      const n = i + 1
      expect(after[i][driver], `row ${n}'s ${driver} column did not take the ${partner}`).toBe(before[i][partner])
      expect(after[i][partner], `row ${n}'s ${partner} column did not take the ${driver}`).toBe(before[i][driver])
      for (const other of LINE_ROLE_NAMES) {
        if (other === driver || other === partner) continue
        expect(after[i][other], `the ${driver} remap disturbed row ${n}'s ${other} column`).toBe(before[i][other])
      }
    }

    // The selector always reads its OWN column's role, so the same choice repeats the swap.
    await page.getByTestId(`line-item-role-${driver}`).selectOption(partner)
    expect(await gridValues(page, rowCount), `the ${driver} remap is not its own inverse`).toEqual(before)
  }

  // -- 3. the two columns E2E-01 never types into --------------------------------------------
  const settled = lines.map((row, i) => ({ n: i + 1, row })).filter(({ row }) => wireRowState(row) === 'ok')
  expect(
    settled.length,
    'no row on this document settles, so there is no row a keystroke can be shown to break',
  ).toBeGreaterThanOrEqual(1)
  const { n: settledN, row: settledRow } = settled[0]
  await expect(
    page.getByTestId(`line-item-flag-${settledN}`),
    `the BEFORE arm: row ${settledN} settles on the wire and must start unflagged`,
  ).toHaveCount(0)

  const typed = 'Rebar, 12mm, cut to length'
  expect(
    settledRow.cells.description?.value,
    'this row already holds the description about to be typed, so the edit moves nothing',
  ).not.toBe(typed)
  const descriptionInput = page.getByTestId(`line-item-input-${settledN}-description`)
  await descriptionInput.fill(typed)
  await descriptionInput.press('Tab')
  await expect(descriptionInput, 'the description column took no keystroke').toHaveValue(typed)
  await expect(
    page.getByTestId(`line-item-marker-${settledN}-description`),
    'the typed description shows no changed marker',
  ).toBeVisible()
  await expect(
    page.getByTestId(`line-item-flag-${settledN}`),
    'a description edit moved the row arithmetic',
  ).toHaveCount(0)

  // The auto-derive arm: a grid deriving line_total from quantity x unit price would rewrite
  // this row's total to match and never flag it.
  const q = decimalOf(settledRow.cells.quantity?.value) as number
  const documentTotal = settledRow.cells.line_total?.value as string
  const brokenPrice = ((decimalOf(settledRow.cells.unit_price?.value) as number) + 1000).toFixed(2)
  expect(
    Math.abs(q * Number(brokenPrice) - (decimalOf(documentTotal) as number)),
    `row ${settledN} still settles at a unit price of ${brokenPrice} -- the flag below would never appear`,
  ).toBeGreaterThan(0.01 + 1e-9)

  const priceInput = page.getByTestId(`line-item-input-${settledN}-unit_price`)
  await priceInput.fill(brokenPrice)
  await priceInput.press('Tab')
  await expect(priceInput, 'the unit-price column took no keystroke').toHaveValue(brokenPrice)
  await expect(
    page.getByTestId(`line-item-flag-${settledN}`),
    'the unit-price keystroke did not recompute the row flag',
  ).toBeVisible()
  await expect(
    page.getByTestId(`line-item-input-${settledN}-line_total`),
    'the grid derived the line total from quantity x unit price instead of keeping what the document read',
  ).toHaveValue(documentTotal)

  // -- 4. removing every line lands on the empty panel ----------------------------------------
  await expect(
    page.getByTestId('line-item-empty'),
    'the empty panel showed while the document lines were still on screen',
  ).toHaveCount(0)
  for (let left = rowCount; left > 0; left -= 1) {
    await page.getByTestId('line-item-remove-1').click()
    await expect(
      page.locator('[data-testid^="line-item-row-"]'),
      `removing the first of ${left} rows left the row count where it was`,
    ).toHaveCount(left - 1)
  }

  const empty = page.getByTestId('line-item-empty')
  await expect(empty, 'a grid holding no row renders no panel at all').toBeVisible()
  await expect(empty, 'the panel does not say the document carries no lines').toContainText(
    'We found no line items on this document.',
  )
  await expect(empty, 'the panel does not say what an empty grid costs').toContainText(
    'An invoice cannot be filed until it has at least one line, so add one here.',
  )
  await expect(
    page.getByTestId('line-item-grid').locator('table'),
    'an empty table survived the last removal',
  ).toHaveCount(0)
  await expect(page.getByTestId('line-item-sum'), 'a grid holding no line still states a sum').toHaveCount(0)

  // Not a dead end: the panel's own Add is the way back to a fileable invoice.
  await page.getByTestId('line-item-add').click()
  await expect(
    page.locator('[data-testid^="line-item-row-"]'),
    'Add from the empty panel produced no row',
  ).toHaveCount(1)
  await expect(page.getByTestId('line-item-empty'), 'the panel outlived the row it asked for').toHaveCount(0)
  for (const role of LINE_ROLE_NAMES) {
    await expect(
      page.getByTestId(`line-item-input-1-${role}`),
      `the added row's ${role} is not a blank, editable cell`,
    ).toHaveValue('')
  }

  await testInfo.attach('extraction-line-columns-and-empty.json', {
    body: JSON.stringify({ rowCount, settledN, typed, brokenPrice, documentTotal }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- the four layout relationships -------------------------------------------------------
//
// Relationships, never dimensions (layout.ts's thesis): containment in a named parent,
// equality with a named sibling, the contained-overflow PAIR, and reachability across a
// measured extent. `assertFillsColumn` is deliberately not used for any of them -- it bounds
// the gaps from ABOVE only, so a scrollbox spilling its pane satisfies it.

// The fields pane's two children, in the artboard's order -- EXTR11-E2E-02b's own indices, and
// ExtractionFields.test.tsx pins that shape from the other side.
function fieldsPaneBody(page: Page): Locator {
  return page.getByTestId('extraction-fields').locator('> div').nth(1)
}

test('EXTR13-LAYOUT-01: the grid scrollbox stays inside the fields pane body, on both edges, at every width', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await openLineGrid(page, 'EXTR-13-09 contained')

  const paneBody = fieldsPaneBody(page)
  const scroll = page.getByTestId('line-item-scroll')

  const measured: { width: number; left: number; right: number; bodyScrollWidth: number; bodyClientWidth: number }[] = []
  const entryViewport = page.viewportSize()
  try {
    // Widest first, WIDE_WIDTHS' own order.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [s, b] = await Promise.all([scroll.boundingBox(), paneBody.boundingBox()])
        const flow = await paneBody.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
        return { s, b, flow }
      }, `line grid containment at ${width}px`)

      expect(m.s && m.b, `both the scrollbox and the pane body must render at ${width}px`).toBeTruthy()
      // Non-empty first: a rect collapsed to zero is inside anything and passes vacuously.
      expect(m.s!.width, `the scrollbox collapsed to zero width at ${width}px -- its edges are vacuous`).toBeGreaterThan(0)
      expect(m.b!.width, `the pane body collapsed to zero width at ${width}px`).toBeGreaterThan(0)

      // gaps()'s rule, with a FLOOR: a positive gap is slack, a negative one is a spill. Bounding
      // it from above would pass on the very defect this exists to catch.
      const g = gaps(m.s as Rect, m.b as Rect)
      expect(g.left, `the scrollbox starts ${(-g.left).toFixed(1)}px left of the pane body at ${width}px`).toBeGreaterThanOrEqual(-1)
      expect(g.right, `the scrollbox ends ${(-g.right).toFixed(1)}px right of the pane body at ${width}px`).toBeGreaterThanOrEqual(-1)

      // The other half of containment: the pane body declares `overflow-y: auto` (y ONLY), so a
      // grid whose overflow escaped its own scrollbox would have nowhere to hide.
      expect(
        m.flow.scrollWidth,
        `the pane body holds ${m.flow.scrollWidth}px of content in a ${m.flow.clientWidth}px box at ${width}px -- the grid's overflow escaped its scrollbox`,
      ).toBeLessThanOrEqual(m.flow.clientWidth + 1)

      measured.push({
        width,
        left: g.left,
        right: g.right,
        bodyScrollWidth: m.flow.scrollWidth,
        bodyClientWidth: m.flow.clientWidth,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([
    ...WIDE_WIDTHS,
  ])

  await testInfo.attach('extraction-line-grid-containment.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Forty is the story's own number (Core AC 9). Nothing on the deployed fleet PRODUCES forty
// lines -- the mock emits four and its fixtures are keyed by content hash -- so the grid is
// grown by the gesture a person would use.
const FORTY_LINES = 40

test('EXTR13-LAYOUT-02: forty lines overflow the scrollbox at 1280 and never the page', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const { lines } = await openLineGrid(page, 'EXTR-13-09 forty')

  const add = page.getByTestId('line-item-add')
  for (let n = lines.length; n < FORTY_LINES; n += 1) await add.click()
  await expect(
    page.locator('[data-testid^="line-item-row-"]'),
    `the grid did not reach ${FORTY_LINES} rows`,
  ).toHaveCount(FORTY_LINES)
  await expect(page.getByTestId(`line-item-row-${FORTY_LINES}`), `row ${FORTY_LINES} did not render`).toBeVisible()

  const scroll = page.getByTestId('line-item-scroll')
  const readAt = async (width: number) => {
    await page.setViewportSize({ width, height: 1080 })
    return settledRead(async () => {
      const box = await scroll.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
      const doc = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
      }))
      return { width, box, doc }
    }, `forty-line overflow at ${width}px`)
  }

  const entryViewport = page.viewportSize()
  // Widest first, the house order.
  const sweep: Awaited<ReturnType<typeof readAt>>[] = []
  try {
    sweep.push(await readAt(2560))
    sweep.push(await readAt(1280))
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }
  expect(sweep.map((s) => s.width), 'both arms must be measured, widest first').toEqual([2560, 1280])
  const [wide, narrow] = sweep

  expect(narrow.box.clientWidth, 'the scrollbox has no width at 1280px -- every claim here is vacuous').toBeGreaterThan(0)
  expect(wide.box.clientWidth, 'the scrollbox has no width at 2560px').toBeGreaterThan(0)

  // The PAIR is what proves containment rather than absence. Half one: at 1280 the grid really
  // does overflow, so there is something for the scrollbox to hold.
  expect(
    narrow.box.scrollWidth,
    `the grid fits its scrollbox at 1280px (${narrow.box.scrollWidth} in ${narrow.box.clientWidth}) -- there is no overflow to contain, so the page claim below proves nothing`,
  ).toBeGreaterThan(narrow.box.clientWidth + 1)

  // Half two, and Core AC 9: the page itself never gains a horizontal scroll under forty lines.
  expect(
    narrow.doc.scrollWidth,
    `forty lines pushed the page ${narrow.doc.scrollWidth - narrow.doc.clientWidth}px past its viewport at 1280px`,
  ).toBeLessThanOrEqual(narrow.doc.clientWidth + 1)
  expect(
    wide.doc.scrollWidth,
    `forty lines pushed the page ${wide.doc.scrollWidth - wide.doc.clientWidth}px past its viewport at 2560px`,
  ).toBeLessThanOrEqual(wide.doc.clientWidth + 1)

  // The control arm. Without it a grid pinned to a constant width -- one that ALWAYS overflows,
  // at any viewport -- passes both halves above. At 2560 the same forty lines fit their
  // scrollbox, which places the overflow in the viewport rather than in the grid.
  expect(
    wide.box.scrollWidth,
    `the same forty lines still overflow at 2560px (${wide.box.scrollWidth} in ${wide.box.clientWidth}) -- the grid overflows by construction, not because the window is narrow`,
  ).toBeLessThanOrEqual(wide.box.clientWidth + 1)

  await testInfo.attach('extraction-line-grid-forty.json', {
    body: JSON.stringify({ rows: FORTY_LINES, wide, narrow }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR13-LAYOUT-03: the grid takes the same left and right edges as the header grid above it', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const { detail } = await openLineGrid(page, 'EXTR-13-09 aligned')

  // The sibling this aligns to, anchored by what it CONTAINS rather than by position alone: a
  // wrong ancestor resolves zero header cells and reds here instead of measuring the wrong box.
  const headerNames = detail.fields.map((f) => f.name).filter((n) => !n.startsWith('line_items'))
  expect(headerNames.length, 'this document carries no header field -- there is no sibling to align to').toBeGreaterThan(0)
  const paneBody = fieldsPaneBody(page)
  const headerGrid = paneBody.locator('> div').first()
  await expect(
    headerGrid.locator('> [data-testid^="extraction-field-"]'),
    'the first pane-body child is not the header field grid',
  ).toHaveCount(headerNames.length)

  const grid = page.getByTestId('line-item-grid')

  const measured: { width: number; left: number; right: number; headerWidth: number }[] = []
  const entryViewport = page.viewportSize()
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [g, h] = await Promise.all([grid.boundingBox(), headerGrid.boundingBox()])
        return { g, h }
      }, `line grid alignment at ${width}px`)

      expect(m.g && m.h, `both grids must render at ${width}px`).toBeTruthy()
      expect(m.g!.width, `the line grid collapsed to zero width at ${width}px`).toBeGreaterThan(0)
      expect(m.h!.width, `the header grid collapsed to zero width at ${width}px`).toBeGreaterThan(0)

      // EQUALITY, not containment: bounded from both sides, so a grid indented inside the column
      // and a grid spilling past it both red. This is what says the line block is a full-width
      // section under the two-column header grid and not a third column of it.
      const g = gaps(m.g as Rect, m.h as Rect)
      expect(
        Math.abs(g.left),
        `the line grid's left edge is ${g.left.toFixed(1)}px off the header grid's at ${width}px`,
      ).toBeLessThanOrEqual(1)
      expect(
        Math.abs(g.right),
        `the line grid's right edge is ${g.right.toFixed(1)}px off the header grid's at ${width}px`,
      ).toBeLessThanOrEqual(1)

      measured.push({ width, left: g.left, right: g.right, headerWidth: m.h!.width })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([
    ...WIDE_WIDTHS,
  ])

  await testInfo.attach('extraction-line-grid-alignment.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR13-LAYOUT-04: the rightmost column is reachable inside the scroll extent', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await openLineGrid(page, 'EXTR-13-09 reachable')

  const scroll = page.getByTestId('line-item-scroll')
  // The last column's control, and the one a person has to reach to drop a line.
  const rightmost = page.getByTestId('line-item-remove-1')

  const entryViewport = page.viewportSize()
  let before: { extent: number; client: number; outRight: number } | null = null
  let after: { scrollLeft: number; outRight: number; outLeft: number; width: number } | null = null
  try {
    // 1280, the narrow end of WIDE_WIDTHS: it is where a fixed-width table has an extent at all.
    await page.setViewportSize({ width: 1280, height: 1080 })

    const start = await settledRead(async () => {
      const flow = await scroll.evaluate((el) => ({
        scrollWidth: el.scrollWidth,
        clientWidth: el.clientWidth,
        scrollLeft: el.scrollLeft,
      }))
      const [box, control] = await Promise.all([scroll.boundingBox(), rightmost.boundingBox()])
      return { flow, box, control }
    }, 'line grid reach, unscrolled')

    expect(start.box && start.control, 'the scrollbox and its rightmost control must both render').toBeTruthy()
    expect(start.control!.width, 'the rightmost control has no width -- its edges are vacuous').toBeGreaterThan(0)

    // There IS an extent to cross. Without this the scroll below moves nothing and every claim
    // after it is satisfied by a grid that was never cut off.
    expect(
      start.flow.scrollWidth,
      `the grid already fits at 1280px (${start.flow.scrollWidth} in ${start.flow.clientWidth}) -- there is no extent to reach across`,
    ).toBeGreaterThan(start.flow.clientWidth + 1)

    // The needle: unscrolled, the rightmost control really is outside the visible box. A control
    // that was already fully visible would make the reachability claim below vacuous.
    const startOutRight = start.control!.x + start.control!.width - (start.box!.x + start.box!.width)
    expect(
      startOutRight,
      'the rightmost control is already fully visible before any scrolling, so reaching it proves nothing',
    ).toBeGreaterThan(1)
    before = { extent: start.flow.scrollWidth, client: start.flow.clientWidth, outRight: startOutRight }

    await scroll.evaluate((el) => {
      el.scrollLeft = el.scrollWidth
    })

    const end = await settledRead(async () => {
      const scrollLeft = await scroll.evaluate((el) => el.scrollLeft)
      const [box, control] = await Promise.all([scroll.boundingBox(), rightmost.boundingBox()])
      return { scrollLeft, box, control }
    }, 'line grid reach, scrolled to the end')

    expect(end.box && end.control, 'the scrollbox and its rightmost control must still render after scrolling').toBeTruthy()
    expect(end.scrollLeft, 'the scrollbox did not move -- it is not the scroller').toBeGreaterThan(0)

    // Reachability, on BOTH edges: at the end of its own extent the rightmost control is inside
    // the visible box. A grid clipped past its scroll extent reds on the right; one pushed off
    // the other way reds on the left.
    const outRight = end.control!.x + end.control!.width - (end.box!.x + end.box!.width)
    const outLeft = end.box!.x - end.control!.x
    expect(
      outRight,
      `the rightmost control is still ${outRight.toFixed(1)}px past the visible box at the end of the scroll extent`,
    ).toBeLessThanOrEqual(1)
    expect(
      outLeft,
      `the rightmost control sits ${outLeft.toFixed(1)}px left of the visible box at the end of the scroll extent`,
    ).toBeLessThanOrEqual(1)
    expect(end.control!.width, 'the rightmost control collapsed while being reached').toBeGreaterThan(0)
    after = { scrollLeft: end.scrollLeft, outRight, outLeft, width: end.control!.width }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  await testInfo.attach('extraction-line-grid-reach.json', {
    body: JSON.stringify({ before, after }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-15-13 (task-857) · AC-1: the spreadsheet flow, untouched -----------------------
//
// EXTR-15-09 rewrote 31 copy sites behind a `unit` branch. Its hardest constraint is the one
// no census can state: the SPREADSHEET arm still renders on the deployed build, byte for byte.
// reviewCopy.census.test.ts pins BOTH literals of every site in SOURCE; only a browser can say
// which branch a real CSV run takes.
//
// DELIBERATELY ABOVE the EXTR-15 deployed-proof marker below. That span requires every
// `buffer:` argument inside it to be a fresh-per-call fixture helper, because a repeated
// document collides on the permanent per-document enqueue key and settles on a previous run's
// job. A CSV import enqueues no extraction at all, so the rule has nothing to protect here --
// the span's own header gives that same reason for the file's other Buffer.from(...) probes.
//
// TWO imports, because the three literals never share a screen. `ROWS READ` (census R1/R2) is
// the batch header and `Row` (U4) is the Unreadable tab's grid header, both on the batch
// surface; `Rows stored` (B5) is a RejectedFile tile, which renders only when NO batch in the
// run reached 'completed' (reviewShellStateAll, lib/reviewBatch.ts). A header-only file is the
// one fixture that reaches it.
//
// Each literal is asserted WITH its document twin's absence. Presence alone still passes on a
// screen rendering both branches, which is what a half-landed sweep looks like.

/**
 * The screen's rendered text, for byte-exact copy claims.
 *
 * NOT getByText(string), which is case-INSENSITIVE substring matching: it cannot state
 * "byte-identical", and it reads the tab label `Already imported (1)` as the tile value
 * `already imported`. innerText, never textContent -- textContent concatenates across element
 * boundaries and would manufacture substrings that nothing renders.
 */
async function screenText(page: Page): Promise<string> {
  return (await page.locator('body').innerText()).replace(/\u00a0/g, ' ')
}

// R1/R2's two arms minus the ${...} each interpolates, copied from the census table
// (frontend/app/src/lib/reviewCopy.census.test.ts) rather than retyped off the component.
const SPREADSHEET_READ_LINE = 'ROWS READ · SERVER VERDICT · RULE SET '
const DOCUMENT_READ_LINE = 'DOCUMENTS READ · SERVER VERDICT · RULE SET '

test('EXTR15-E2E-05 (AC-1): a spreadsheet run still reads ROWS READ, Rows stored and Row', async ({ page }) => {
  // Two full CSV imports through the wizard on a fleet that may be cold. Neither enqueues an
  // extraction, so this is the cheapest of the EXTR-15 deployed cases.
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `EXTR-15-13 spreadsheet ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  // --- import 1: the mixed fixture, whose rows quarantine structurally (E2E-04's own flow) --
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: 'extr15-sweep.csv', mimeType: 'text/csv', buffer: Buffer.from(buildMixedCsv(), 'utf8') })

  const sweepPreview = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await sweepPreview

  // subtotal has no ALIAS entry, so auto-recognize never places it -- E2E-04's own reasoning.
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const sweepImport = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await sweepImport

  // (a) the batch header's counted noun (R1/R2), and the tab label's (R3).
  const unreadableTab = page.getByRole('button', { name: /^Unreadable rows \(\d+\)$/ })
  await expect(unreadableTab, "the mixed fixture's structural failures must open this tab").toBeVisible({
    timeout: 60_000,
  })
  await expect(
    page.getByRole('button', { name: /^Unreadable documents \(/ }),
    'the document tab label reached a spreadsheet run',
  ).toHaveCount(0)

  const batchScreen = await screenText(page)
  expect(batchScreen, 'the batch header must keep the spreadsheet noun').toContain(SPREADSHEET_READ_LINE)
  expect(batchScreen, 'the document branch reached a spreadsheet run').not.toContain(DOCUMENT_READ_LINE)

  // (b) the Unreadable tab's grid header (U4). Resolved from a SIBLING cell rather than
  // searched page-wide: an exact 'Row' is three characters and would match a cell elsewhere.
  // The chain is self-checked by what the resolved row must also contain (EXTR09-E2E-04's idiom).
  await unreadableTab.click()
  const whyCell = page.getByText('Why it could not be read', { exact: true })
  await expect(whyCell, "the unreadable grid's last header cell").toHaveCount(1)
  const headerRow = whyCell.locator('xpath=..')
  await expect(
    headerRow.getByText('File', { exact: true }),
    'the resolved element is the header row, not the card around it',
  ).toHaveCount(1)
  await expect(headerRow.getByText('Row', { exact: true }), 'the row-number column must still be headed Row').toHaveCount(1)
  await expect(
    headerRow.getByText('—', { exact: true }),
    "the document arm's em-dash header reached a spreadsheet run",
  ).toHaveCount(0)

  // --- import 2: a header-only file, the one fixture reaching RejectedFile (INVCR-E2E-4's) --
  await page.getByRole('button', { name: 'Finish · go to invoices' }).click()
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page.locator('input[type="file"]#pf-import-file').setInputFiles({
    name: 'extr15-header-only.csv',
    mimeType: 'text/csv',
    buffer: Buffer.from(buildHeaderOnlyCsv(), 'utf8'),
  })

  const rejectedPreview = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await rejectedPreview

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()

  const rejectedImport = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Import 0 rows' }).click()
  await rejectedImport

  // (c) the RejectedFile tiles (B5/B6) and the sentence above them (B4).
  await expect(page.getByText('Nothing was imported', { exact: true }), "§7.5's rejected-file surface").toBeVisible({
    timeout: 60_000,
  })
  const rejectedScreen = await screenText(page)
  expect(rejectedScreen, 'the stored tile must keep the spreadsheet noun').toContain('Rows stored')
  expect(rejectedScreen, "B5's document arm reached a spreadsheet run").not.toContain('Documents stored')
  expect(rejectedScreen, 'the quarantined tile must keep the spreadsheet noun').toContain('Rows quarantined')
  expect(rejectedScreen, "B6's document arm reached a spreadsheet run").not.toContain('Documents quarantined')
  expect(rejectedScreen, "B4's spreadsheet arm").toContain(
    'it held no data rows — a spreadsheet with only a header row, for example.',
  )
  expect(rejectedScreen, "B4's document arm reached a spreadsheet run").not.toContain(
    'nothing invoice-shaped could be found in it',
  )

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// === EXTR-15 · the deployed proof: the terminal states and the hand-off =================
//
// GATE-ONLY, every test from here to the EXTR-18-07 marker below. Topology specs run only on
// the deploy gate and this PR's environment does not exist while the PR is a draft, so none
// of them has a local oracle beyond `typecheck` + `playwright test --list`.
//
// deployedProofGuards.test.ts scans exactly this span -- marker to marker -- and requires
// every `buffer:` argument inside it to be one of the fresh-per-call fixture helpers. The
// EXTR-18-07 block below carries the same rule under a narrower allowlist; this span is
// separate because two of its documents are DOCX, which no unique*PdfBytes() helper mints.
//
// --- EXTR-15-07 (task-833) · the hand-off row's geometry --------------------------------
//
// AC-10, authored in Mode A and UNEXECUTED here: topology specs run only on the deploy
// gate, and this PR's environment does not exist until it leaves draft. Subtask 13 is what
// runs it. The local oracle at authoring time was `pnpm --filter @invoice-os/e2e typecheck`
// plus `playwright test --list`.
//
// Placed ABOVE the EXTR-18-07 block deliberately: deployedProofGuards.test.ts scans that
// block from its marker to EOF and requires every `buffer:` argument in it to be a
// `unique*PdfBytes()` call. This test's fixture is uniqueGarbageBytes() — fresh per call for
// the same enqueue-key reason, but not a PDF and not that helper family.
//
// Reached by a run whose ONLY file dead-letters: routeAfterRun answers `none`, applyRoute
// calls markRunFailed, and CreateFlow renders the failures card on the 'documents' step.
// A single-file run removes EXTR10-E2E-02's race entirely -- 'failed' is terminal, so the
// card cannot be routed out from under the sweep and nothing needs holding open.
//
// Containment is measured with overlapOf (layout.ts:68-77), which returns a Rect. The
// identity `overlapOf(inner, outer) === inner` is the only expression of "inner is wholly
// inside outer"; rectsOverlap (layout.ts:55-58) returns a boolean and is true for a row
// hanging half out of its card, so it cannot state this at all.
//
// NO ASSERTION STATES A PIXEL WIDTH. A width assertion passes on the very bug it should
// catch (a row that fits at 2560 and overflows at 1280 has the same width at both). What
// is asserted instead is a RELATIONSHIP that must hold at every width.
function sameRect(a: Rect, b: Rect, slackPx = 1): boolean {
  return (
    Math.abs(a.x - b.x) <= slackPx &&
    Math.abs(a.y - b.y) <= slackPx &&
    Math.abs(a.width - b.width) <= slackPx &&
    Math.abs(a.height - b.height) <= slackPx
  )
}

test('EXTR15-E2E-01 (AC-10): the hand-off row sits inside its card, and its gutter holds at every width', async ({
  page,
}, testInfo) => {
  // Upload + enqueue + River's attempt^4s backoff to dead-letter, four widths, then AC-5's
  // hand-off journey (a filing plus an invoice-detail load) on the far side of all of it.
  test.setTimeout(420_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `EXTR-15-07 handoff ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  // Registered before the pick, same reason as EXTR09-E2E-01's own waiter: the upload can
  // resolve before an awaited setInputFiles returns. AC-5 below compares the filed invoice's
  // source document against THIS id.
  const documentPost = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/submission/v1/documents'),
    { timeout: 120_000 },
  )
  // *.pdf bytes that are not a PDF: pdfium refuses it on all three attempts and the worker
  // dead-letters deterministically (uniqueGarbageBytes' own doc comment).
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: 'handoff-dead-letter.pdf', mimeType: 'application/pdf', buffer: uniqueGarbageBytes() })
  await page.getByRole('button', { name: 'Extract invoices' }).click()

  const handOffDocumentId = ((await (await documentPost).json()) as { document_id?: string }).document_id
  expect(handOffDocumentId, 'the refused document must still be stored').toMatch(/^[0-9a-fA-F-]{36}$/)

  const card = page.getByTestId('document-failures-card')
  await expect(card, 'an all-failed document run must land on the failures card').toBeVisible({ timeout: 180_000 })

  const rows = page.getByTestId('document-failure-row')
  // Non-empty FIRST: an empty locator satisfies every containment check below vacuously.
  await expect(rows, 'the failure must render as its own row').toHaveCount(1)
  await expect(rows.first(), 'the row must name why the document was refused').toContainText(DEAD_LETTER_NEEDLE)

  const button = rows.first().getByRole('button', { name: 'Enter it by hand' })
  await expect(button, 'a stored document must offer the hand-off').toHaveCount(1)
  const reason = rows.first().locator('span').filter({ hasText: DEAD_LETTER_NEEDLE }).first()
  await expect(reason, 'the reason sentence must be measurable').toHaveCount(1)

  type Sample = { width: number; clearance: number; rowWidth: number }
  const samples: Sample[] = []
  const entryViewport = page.viewportSize()

  try {
    // Widest first — WIDE_WIDTHS' own order (layout.ts:22): a cap strands only what the
    // window gives it room to strand.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const read = async () => {
        const [cardBox, rowBox, reasonBox, buttonBox, rowEdges] = await Promise.all([
          card.boundingBox(),
          rows.first().boundingBox(),
          reason.boundingBox(),
          button.boundingBox(),
          rows.first().evaluate(edgesOf),
        ])
        return { cardBox, rowBox, reasonBox, buttonBox, rowEdges }
      }
      const m = await settledRead(read, `hand-off row at ${width}px`)
      expect(
        m.cardBox && m.rowBox && m.reasonBox && m.buttonBox,
        `card, row, reason and button must all render at ${width}px`,
      ).toBeTruthy()

      // (a) the row is CONTAINED by the card — the intersection is the row's own rect.
      expect(
        sameRect(overlapOf(m.rowBox!, m.cardBox!), m.rowBox!),
        `the row must sit wholly inside the card at ${width}px (row ${JSON.stringify(m.rowBox)}, card ${JSON.stringify(m.cardBox)})`,
      ).toBe(true)

      // (b) the reason and the control are both contained by their row, same identity.
      expect(
        sameRect(overlapOf(m.reasonBox!, m.rowBox!), m.reasonBox!),
        `the reason must sit wholly inside its row at ${width}px (reason ${JSON.stringify(m.reasonBox)}, row ${JSON.stringify(m.rowBox)})`,
      ).toBe(true)
      expect(
        sameRect(overlapOf(m.buttonBox!, m.rowBox!), m.buttonBox!),
        `the control must sit wholly inside its row at ${width}px (button ${JSON.stringify(m.buttonBox)}, row ${JSON.stringify(m.rowBox)})`,
      ).toBe(true)

      // (c) the gutter between the control's right edge and the row's right CONTENT edge
      // (edgesOf subtracts the row's own border and padding, so this is the gap the
      // recipe's `padding: '10px 14px'` declares, not the border box).
      samples.push({
        width,
        clearance: m.rowEdges.right - (m.buttonBox!.x + m.buttonBox!.width),
        rowWidth: m.rowBox!.width,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(
    samples.map((s) => s.width),
    'every WIDE_WIDTHS entry must be measured, widest first',
  ).toEqual([...WIDE_WIDTHS])

  // The gutter is a RELATIONSHIP, compared against this run's own widest reading rather
  // than a literal. A row that fits at 2560 and overflows at 1280 moves this number;
  // a row whose width merely changes with the viewport does not.
  const widest = samples[0].clearance
  for (const s of samples) {
    expect(
      Math.abs(s.clearance - widest),
      `the control's clearance from the row's right content edge must not change with the viewport (${s.width}px: ${s.clearance}, 2560px: ${widest})`,
    ).toBeLessThanOrEqual(1)
    expect(s.clearance, `the control must not overflow the row's right content edge at ${s.width}px`).toBeGreaterThanOrEqual(-0.5)
  }

  // --- AC-5 (EXTR-15-12): the single-document hand-off, end to end -----------------------
  //
  // The geometry above proved the control is THERE. This walks it: enabled under a persona
  // whose entity is resolved, then the manual form, then a real filing, then the document
  // reappearing on the invoice's own detail screen. Appended to this test rather than given
  // its own so the ~3-minute dead-letter wait is paid once.
  await expect(button, 'a resolved entity must arm the hand-off, not merely render it').toBeEnabled()
  await button.click()

  const handOffNumber = `EXTR15-HO-${Date.now()}`
  const invoiceId = await fileHandOffDraft(page, handOffNumber)

  // The affirmation is the server's own row, the same one every other filing path lands on.
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(handOffNumber)

  // Equality against the file that was uploaded, never "a card is present": the card renders
  // for an invoice with NO document too, and says so.
  const sourceCard = page.getByTestId('source-document-card')
  await expect(sourceCard, "the hand-off's document must reach the invoice it produced").toContainText(
    'handoff-dead-letter.pdf',
  )
  await expect(page.getByTestId('view-source-document'), 'the stored file must be openable from here').toHaveCount(1)
  await expect(
    page.getByTestId('why-no-source-document'),
    'the card took its no-document arm -- source_document_id never crossed the create wire',
  ).toHaveCount(0)

  // And the wire behind it, read with this run's own token: the card renders a filename, and
  // two documents in one tenant can share one.
  const source = await readSourceDocument(token, invoiceId)
  expect(source.document, 'the source-document read returned no document for a hand-off invoice').not.toBeNull()
  expect(source.document!.id, "the invoice names a document that is not the one that failed").toBe(handOffDocumentId)

  await testInfo.attach('handoff-row-geometry.json', {
    body: JSON.stringify(samples, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-15-12 (task-836) · the pipeline, proved on the deployed build -----------------
//
// Three claims EXTR-15 makes that have no honest unit oracle, and can only be settled here:
//   1. the DEPLOYED sidecar reads a real DOCX -- the Go golden was recorded through a LOCAL
//      docling, and this project has already been burned by that once (a local canary read
//      55 OCR tokens where Railway read zero);
//   2. a hand-off from a MULTI-document run attaches the row's own document -- every other
//      guard on this is a mocked fixture, and a mock cannot catch an id-mapping defect
//      because the mock is where the id mapping is asserted;
//   3. a boxless format that the reader cannot open dead-letters at text_not_read, the kind
//      whose sentence EXTR-15-04 wrote.

/** Fills the manual form the hand-off lands on, files it, and returns the new invoice's id. */
async function fileHandOffDraft(page: Page, invoiceNumber: string): Promise<string> {
  const fileBtn = page.getByRole('button', { name: 'File invoice' })
  await expect(fileBtn, 'the hand-off must land on the manual entry form').toBeVisible({ timeout: 60_000 })

  // A fresh number per call: defaultDraft seeds a FIXED literal and
  // (tenant_id, entity_id, invoice_number) is unique, so a Playwright retry would 409 on the
  // row its own first attempt filed.
  await page.getByPlaceholder('INV-0000-00000').fill(invoiceNumber)
  await expect(fileBtn, 'a resolved entity and a non-blank number must arm the primary').toBeEnabled()

  const [res] = await Promise.all([
    page.waitForResponse(
      (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices'),
      { timeout: 60_000 },
    ),
    fileBtn.click(),
  ])
  expect(res.status(), 'the hand-off filing must be a 201').toBe(201)
  const id = ((await res.json()) as { id?: string }).id
  expect(id, 'the create response carried no invoice id').toMatch(/^[0-9a-fA-F-]{36}$/)

  await expect(page.getByTestId('invoice-detail'), 'a filed hand-off must land on the real invoice detail').toBeVisible({
    timeout: 60_000,
  })
  return id as string
}

// A narrow local type over GET /v1/invoices/{id}/source-document rather than a client.ts
// helper: frontend/app already mirrors this body key-for-key under wireMirrors.test.ts, and a
// second typed mirror here would be one more thing to keep in step for one assertion.
interface SourceDocumentBody {
  invoice_id: string
  source_rows: number[] | null
  document: { id: string; filename: string | null } | null
}

async function readSourceDocument(token: string, invoiceId: string): Promise<SourceDocumentBody> {
  const res = await fetch(`${apiBase()}/api/invoice/v1/invoices/${invoiceId}/source-document`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(res.status, `GET /v1/invoices/${invoiceId}/source-document`).toBe(200)
  return (await res.json()) as SourceDocumentBody
}

/**
 * Runs N documents through the wizard in ONE run and returns each one's stored id and the job
 * it settled on. Asserts NO landing: the three tests below each land somewhere different (the
 * invoice detail, the review batch surface, the failures card), so a helper that picked one
 * would be wrong for the other two — each states its own.
 */
async function runDocuments(
  page: Page,
  label: string,
  files: { name: string; mimeType: string; buffer: Buffer }[],
): Promise<{
  token: string
  entityId: string
  documentIds: Record<string, string>
  jobs: Record<string, ExtractionJob>
}> {
  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `${label} ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  return { token, entityId: entity.id, ...(await runDocumentsIn(page, token, files)) }
}

/**
 * runDocuments' wizard half: pick, extract and settle N documents in the entity the page has
 * ALREADY selected. Split out for EXTR15-E2E-06 (EXTR-15-13), which needs a SECOND run in the
 * same entity — a cross-run collision is the only way the already-imported channel has a
 * determined winner.
 *
 * The route handler is registered per call. Playwright matches the most recently added handler
 * first, so an earlier run's `files` closure never sees this run's uploads.
 */
async function runDocumentsIn(
  page: Page,
  token: string,
  files: { name: string; mimeType: string; buffer: Buffer }[],
): Promise<{ documentIds: Record<string, string>; jobs: Record<string, ExtractionJob> }> {
  const documentIds: Record<string, string> = {}
  // Recorded INSIDE the POST route, EXTR10-E2E-02's own idiom: the handler completes before
  // the app can see the response, so no poll for this document can precede the assignment. A
  // page.on('response') listener cannot give that ordering.
  //
  // Named and unrouted below, never an inline closure: this helper is called twice on one page
  // by EXTR15-E2E-06, and a handler left installed by the first call still matches the second
  // call's uploads while searching them for the FIRST call's filenames -- finding none, writing
  // nothing, and fulfilling the request so the live handler never sees it.
  const captureUpload = async (route: Route) => {
    if (route.request().method() !== 'POST') {
      await route.continue()
      return
    }
    const body = requestBody(route.request())
    const res = await route.fetch()
    const text = await res.text()
    try {
      const parsed = JSON.parse(text) as { document_id?: string }
      // The multipart body carries `filename="…"` in ASCII near its head, so this resolves
      // even though the rest of it is binary.
      const named = files.find((f) => body.includes(f.name))
      if (named && typeof parsed.document_id === 'string') documentIds[named.name] = parsed.document_id
    } catch {
      /* fulfil unchanged; the poll below fails loudly if the upload really broke */
    }
    await route.fulfill({ response: res, body: text })
  }
  await page.route('**/api/submission/v1/documents', captureUpload)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page.locator('input[type="file"]#pf-import-file').setInputFiles(files)
  await page.getByRole('button', { name: 'Extract invoices' }).click()

  await expect
    .poll(() => Object.keys(documentIds).length, {
      message: `not every document reached storage (${JSON.stringify(documentIds)})`,
      timeout: 120_000,
    })
    .toBe(files.length)
  // Every upload is captured, so the handler has nothing left to do and must not outlive this
  // call -- see captureUpload's own note.
  await page.unroute('**/api/submission/v1/documents', captureUpload)

  const jobs: Record<string, ExtractionJob> = {}
  for (const f of files) {
    await expect
      .poll(
        async () => {
          const { jobs: found } = await getExtractions(token, documentIds[f.name])
          const job = found[0]
          if (!job) return 'no job for this document yet'
          jobs[f.name] = job
          // 'failed' is NOT terminal — River retries it; only succeeded/dead_lettered end it.
          return job.state === 'succeeded' || job.state === 'dead_lettered' ? 'terminal' : job.state
        },
        { message: `${f.name}'s extraction never reached a terminal state`, timeout: 240_000, intervals: [1_000] },
      )
      .toBe('terminal')
  }
  return { documentIds, jobs }
}

const GOLDEN_DOCX_NAME = 'golden_invoice.docx'
const EMPTY_DOCX_NAME = 'empty_container.docx'

test('EXTR15-E2E-02 (AC-6): a two-document run hands off the row that was clicked, not its sibling', async ({
  page,
}) => {
  // Two documents through a cold sidecar, then a filing and a detail load.
  test.setTimeout(600_000)
  const errors = collectErrors(page)

  const scannedName = 'scanned_invoice.pdf'
  const denseName = 'dense_invoice.pdf'
  // Both quarantine, for DIFFERENT measured reasons, and neither needs a new artifact: the
  // scan settles document_text_layer = unreadable with no number at all, and the dense page's
  // label OCRs as "INV0ICE NO:" so its invoice_number settles missing (D-33). They carry
  // different filenames and different document ids, so the two rows are genuinely
  // distinguishable — a pair whose items are naturally equal proves nothing.
  const { token, documentIds } = await runDocuments(page, 'EXTR-15-12 two documents', [
    { name: scannedName, mimeType: 'application/pdf', buffer: uniqueScannedPdfBytes() },
    { name: denseName, mimeType: 'application/pdf', buffer: uniqueDensePdfBytes() },
  ])

  expect(
    documentIds[scannedName],
    'both files resolved to ONE stored document -- the pair cannot discriminate anything below',
  ).not.toBe(documentIds[denseName])

  // (a) routeAfterRun cannot take the 'single' arm on a two-file run (lib/importRun.ts), so
  // the landing is the review surface carrying EVERY batch id. The hash is where that id list
  // is observable: App.tsx mirrors reviewBatchIds into it, one id per batch.
  await expect
    .poll(() => new URL(page.url()).hash, {
      message: 'a two-document run must land on the review batch surface with both batch ids',
      timeout: 180_000,
    })
    .toMatch(/^#review\/[0-9a-fA-F-]{36},[0-9a-fA-F-]{36}$/)

  // (b) both documents reach the Unreadable tab, told apart by their own file labels. A
  // tab-level scalar document id would pass a same-document pair; two labelled rows cannot.
  // The tab is matched on its prefix, not on its count: the count is a second statement of
  // what toHaveCount below already asserts, and matching it here would fail one step earlier
  // with a locator error instead of a row count.
  const unreadableTab = page.getByRole('button', { name: /^Unreadable documents \(/ })
  await expect(unreadableTab, 'neither document quarantined -- there is no unreadable tab').toHaveCount(1)
  await unreadableTab.click()

  const unreadableRows = page.getByTestId('unreadable-row')
  await expect(unreadableRows, 'both documents must render as their own rows').toHaveCount(2)
  const rowLabels = await unreadableRows.evaluateAll((els) => els.map((el) => el.textContent ?? ''))
  const scannedIdx = rowLabels.findIndex((t) => t.includes(scannedName))
  const denseIdx = rowLabels.findIndex((t) => t.includes(denseName))
  expect(scannedIdx, `no row names ${scannedName}: ${JSON.stringify(rowLabels)}`).toBeGreaterThanOrEqual(0)
  expect(denseIdx, `no row names ${denseName}: ${JSON.stringify(rowLabels)}`).toBeGreaterThanOrEqual(0)
  expect(scannedIdx, 'one row names both files -- the labels do not discriminate the rows').not.toBe(denseIdx)

  // (c) the SCANNED row's own control, and the invoice it produces named by equality against
  // the scanned document — never "is not null", which the dense document would satisfy too.
  const scannedButton = unreadableRows.nth(scannedIdx).getByRole('button', { name: 'Enter it by hand' })
  await expect(scannedButton, 'a resolved entity must arm the hand-off on this row').toBeEnabled()
  await scannedButton.click()

  const invoiceNumber = `EXTR15-MULTI-${Date.now()}`
  const invoiceId = await fileHandOffDraft(page, invoiceNumber)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(invoiceNumber)

  const source = await readSourceDocument(token, invoiceId)
  expect(source.document, 'the filed invoice names no source document at all').not.toBeNull()
  expect(source.document!.id, "the hand-off attached the wrong row's document").toBe(documentIds[scannedName])
  expect(source.document!.id, 'the hand-off attached the sibling document').not.toBe(documentIds[denseName])

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

/**
 * Waits for a document run to come to REST and names the surface it stopped on. An
 * OBSERVATION: the caller attaches the answer rather than asserting it, so a routing change
 * moves a recorded string instead of reddening a case about extraction.
 */
async function settledRunSurface(page: Page): Promise<string> {
  let seen = 'nothing yet'
  await expect
    .poll(
      async () => {
        if (await page.getByTestId('invoice-detail').isVisible()) seen = 'invoice detail'
        else if (await page.getByTestId('review-table').isVisible()) seen = 'review batch surface'
        else if (await page.getByText(/^BATCH /).first().isVisible()) seen = 'review batch surface'
        else if (await page.getByTestId('document-failures-card').isVisible()) seen = 'the failures card'
        else seen = 'nothing yet'
        return seen !== 'nothing yet'
      },
      { message: 'the run never came to rest on any surface', timeout: 180_000 },
    )
    .toBe(true)
  return seen
}

test('EXTR15-E2E-03: the deployed sidecar reads a real DOCX, and reads its printed fields', async ({ page }, testInfo) => {
  // One DOCX on a fleet that may be cold, then the import and the detail landing.
  test.setTimeout(600_000)
  const errors = collectErrors(page)

  const { token, jobs } = await runDocuments(page, 'EXTR-15-12 docx', [
    { name: GOLDEN_DOCX_NAME, mimeType: DOCX_MIME, buffer: uniqueGoldenDocxBytes() },
  ])
  const job = jobs[GOLDEN_DOCX_NAME]
  expect(
    job.state,
    `the DOCX did not settle succeeded (kind ${job.failure_kind ?? 'null'}, error ${job.last_error ?? 'none'})`,
  ).toBe('succeeded')

  const detail = await getExtractionDetail(token, job.id)
  // Zero pages is the CONTRACT, not an absence of evidence: pageImageFormats marks DOCX
  // boxless (classify.go), so worker.go skips the render and the page rows. Since EXTR-19-04 it
  // does write a b1 layout -- a column on the job, never a page row.
  expect(detail.pages, 'a boxless format must write no page rows').toEqual([])

  // Equality on all three, never a negation: a reachable-but-EMPTY sidecar settles succeeded
  // with every field absent, and `undefined !== 'MOCK-INV-0001'` passes on exactly that. Three
  // fields, so one lucky read cannot carry the case. The values are the Go golden's
  // (corpus_wired_db_test.go's TestRLS_DocxFixtureResolvesNamedFields), which was recorded
  // through a local docling -- this is the first read of them by the deployed one.
  const valueOf = (name: string) => {
    const f = detail.fields.find((x) => x.name === name)
    expect(f, `no ${name} field on the wire (fields: ${detail.fields.map((x) => x.name).join(', ') || 'none'})`).toBeTruthy()
    return f!.value
  }
  expect(valueOf('invoice_number'), "the settled number is not the DOCX's printed number").toBe('ASC-2026-0919')
  expect(valueOf('issue_date'), "the settled date is not the DOCX's printed date").toBe('2026-08-14')
  expect(valueOf('total'), "the settled total is not the DOCX's printed total").toBe('4300.00')

  // OBSERVED, not asserted. Which surface the run lands on is routeAfterRun's business and
  // no AC of this story's; what this waits for is the run coming to REST, so the console gate
  // below reads a finished journey rather than one mid-navigation.
  const landing = await settledRunSurface(page)
  await testInfo.attach('docx-run-landing.txt', { body: landing, contentType: 'text/plain' })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR15-E2E-04 (T3): a DOCX the reader cannot open dead-letters at text_not_read', async ({ page }) => {
  // Upload plus River's three attempts at attempt^4s backoff, each one a fast 422.
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const { jobs } = await runDocuments(page, 'EXTR-15-12 empty docx', [
    { name: EMPTY_DOCX_NAME, mimeType: DOCX_MIME, buffer: uniqueEmptyDocxBytes() },
  ])
  const job = jobs[EMPTY_DOCX_NAME]
  expect(job.state, 'a DOCX the reader refuses must dead-letter, not succeed').toBe('dead_lettered')

  // EQUALITY, never "not pages_not_rendered": five other kinds satisfy that negation, and the
  // whole claim here is that the render stage never ran at all for a boxless format.
  expect(
    job.failure_kind,
    `the kind was ${job.failure_kind ?? 'null'} (error ${job.last_error ?? 'none'})`,
  ).toBe('text_not_read')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-15-13 (task-857) · AC-2/AC-3: the document unit, rendered and contained --------
//
// AC-2 is EXTR15-E2E-05's mirror: the DOCUMENT arm of the same census sites, on the deployed
// build. AC-3 is the containment sweep over what this story LENGTHENED -- the hand-off control
// subtask 11 put in the Unreadable tab, and the two "Not imported" tiles whose text grew
// ("already in the register" is six characters longer than "already imported").
//
// ONE test for both, because the journey is the expensive part: three extractions on a fleet
// that may be cold. EXTR15-E2E-01 folds its AC-5 walk into its geometry sweep for the same
// reason.
//
// TWO RUNS IN ONE ENTITY, and that ordering IS the oracle. The already-imported channel needs
// the colliding invoice to exist BEFORE the second import runs. Two copies inside one run
// would race -- startDocumentRun imports through Promise.all (lib/documentRun.ts) -- and while
// the SHAPE would still hold (storeDuplicateRowError sets rule_key on both the up-front
// precheck and the racing-INSERT backstop, and isAlreadyImported reads only that key), nothing
// would say WHICH file landed in which channel, and this case names files.
//
// Run 2's second file is scanned_invoice.pdf, EXTR15-E2E-02's own choice and for its reason:
// it settles SUCCEEDED with no readable number, so the import quarantines it into the
// unreadable channel with its document id attached (EXTR-15-10) -- which is what puts a
// hand-off control on the row at all. A file that dead-letters produces no batch and never
// reaches this screen.
//
// NO LOCAL ORACLE, like every case in this span. If the deployed sidecar reads either fixture
// differently from EXTR15-E2E-02/03's measurements, this reds on a job state or a tab count
// with a message naming which.

const REPEAT_DOCX_NAME = 'golden_invoice_again.docx'
const QUARANTINED_PDF_NAME = 'scanned_invoice.pdf'
// The golden's printed number, the same literal EXTR15-E2E-03 asserts off the wire.
const GOLDEN_DOCX_NUMBER = 'ASC-2026-0919'

test('EXTR15-E2E-06 (AC-2/AC-3): the document review screen says documents and register, and holds its controls at every width', async ({
  page,
}, testInfo) => {
  // Three extractions across two runs on a fleet that may be cold, then a four-width sweep.
  test.setTimeout(900_000)
  const errors = collectErrors(page)

  // --- run 1: one DOCX, whose invoice is what run 2 collides with ------------------------
  const { token, entityId, jobs: seedJobs } = await runDocuments(page, 'EXTR-15-13 register', [
    { name: GOLDEN_DOCX_NAME, mimeType: DOCX_MIME, buffer: uniqueGoldenDocxBytes() },
  ])
  expect(
    seedJobs[GOLDEN_DOCX_NAME].state,
    `the seeding DOCX did not settle succeeded (kind ${seedJobs[GOLDEN_DOCX_NAME].failure_kind ?? 'null'}, error ${seedJobs[GOLDEN_DOCX_NAME].last_error ?? 'none'})`,
  ).toBe('succeeded')

  // The precondition read off the REGISTER, not off the landing: which surface run 1 ends on is
  // routeAfterRun's business and no AC of this story's, but the invoice existing is the whole
  // basis of run 2. Listed by entity rather than searched by `q`, so nothing here depends on
  // the search predicate's matching rules.
  // POLLED, not read once: a settled extraction is not a filed invoice. The SPA calls the
  // import endpoint AFTER the job reaches its terminal state, so a single read here races the
  // write it is checking for and returns an empty register.
  await expect
    .poll(
      async () => (await listInvoices(token, { entity_id: entityId, limit: 50 })).invoices.map((i) => i.invoice_number),
      {
        message: 'run 1 stored no invoice under the golden number -- run 2 has nothing to collide with',
        timeout: 120_000,
        intervals: [1_000],
      },
    )
    .toContain(GOLDEN_DOCX_NUMBER)

  // --- run 2: the same document again, beside one that quarantines -----------------------
  const { jobs } = await runDocumentsIn(page, token, [
    { name: REPEAT_DOCX_NAME, mimeType: DOCX_MIME, buffer: uniqueGoldenDocxBytes() },
    { name: QUARANTINED_PDF_NAME, mimeType: 'application/pdf', buffer: uniqueScannedPdfBytes() },
  ])
  expect(jobs[REPEAT_DOCX_NAME].state, 'the repeat DOCX must read as well as the first one did').toBe('succeeded')
  expect(
    jobs[QUARANTINED_PDF_NAME].state,
    `the scan settled ${jobs[QUARANTINED_PDF_NAME].state} (kind ${jobs[QUARANTINED_PDF_NAME].failure_kind ?? 'null'}) -- a dead-letter writes no batch and never reaches the review screen`,
  ).toBe('succeeded')

  // Two files cannot take routeAfterRun's 'single' arm, so the landing is the review surface
  // carrying both batch ids -- EXTR15-E2E-02's own reading of the hash.
  await expect
    .poll(() => new URL(page.url()).hash, {
      message: 'a two-document run must land on the review batch surface with both batch ids',
      timeout: 180_000,
    })
    .toMatch(/^#review\/[0-9a-fA-F-]{36},[0-9a-fA-F-]{36}$/)

  // --- AC-2: the header, the tiles and both tab labels ----------------------------------
  const registerTabName = 'Already imported (1)'
  const unreadableTabName = 'Unreadable documents (1)'
  await expect(
    page.getByRole('button', { name: unreadableTabName }),
    'the scan must quarantine into its own tab, labelled in documents',
  ).toHaveCount(1)
  await expect(
    page.getByRole('button', { name: registerTabName }),
    'the repeat DOCX must collide into the already-imported channel',
  ).toHaveCount(1)
  await expect(
    page.getByRole('button', { name: /^Unreadable rows \(/ }),
    'the spreadsheet tab label reached a document run',
  ).toHaveCount(0)

  const header = await screenText(page)
  expect(header, 'the batch header must count documents (R1/R2)').toContain(`2 ${DOCUMENT_READ_LINE}`)
  expect(header, 'the spreadsheet branch reached a document run').not.toContain(SPREADSHEET_READ_LINE)
  expect(header, "B1's document arm").toContain('Built from 0 documents. Every one of these exists in the ledger')
  expect(header, "B1's spreadsheet arm reached a document run").not.toContain(
    'rows. Every one of these exists in the ledger',
  )
  expect(header, "B8's document arm -- the AC's own wording").toContain('1 already in the register')
  expect(header, "B10's document arm").toContain('1 invoices already in the register. Nothing to fix.')
  expect(header, 'the spreadsheet ledger wording reached a document run').not.toContain('already in your ledger')
  expect(header, "B2's document arm").toContain('1 unreadable documents')
  expect(header, "B2's spreadsheet arm reached a document run").not.toContain('1 unreadable rows')

  // --- AC-2: the already-imported tab body ----------------------------------------------
  await page.getByRole('button', { name: registerTabName }).click()
  const register = await screenText(page)
  expect(register, "A3's document arm").toContain('1 documents were already in the register')
  expect(register, "A4's document arm").toContain(
    'These documents are already in the register, so the import had nothing new to add. Nothing is wrong with them and there is nothing to correct.',
  )
  expect(register, "A7's document arm").toContain('1 of 2 documents were already in the register.')
  expect(register, 'the spreadsheet ledger wording reached a document run').not.toContain('already in your ledger')

  // A5/A6: the grid header, resolved from a sibling cell for the reason EXTR15-E2E-05 gives.
  const registerVerdictCell = page.getByText('Invoice already in the register', { exact: true })
  await expect(registerVerdictCell, "the already-imported grid's verdict header (A6)").toHaveCount(1)
  const registerHeaderRow = registerVerdictCell.locator('xpath=..')
  await expect(
    registerHeaderRow.getByText('File', { exact: true }),
    'the resolved element is the header row, not the card around it',
  ).toHaveCount(1)
  await expect(registerHeaderRow.getByText('—', { exact: true }), "A5's document arm").toHaveCount(1)
  await expect(
    registerHeaderRow.getByText('Row', { exact: true }),
    "A5's spreadsheet arm reached a document run",
  ).toHaveCount(0)

  // --- AC-2: the unreadable tab body, and the row AC-3 then measures ---------------------
  await page.getByRole('button', { name: unreadableTabName }).click()
  const unreadable = await screenText(page)
  expect(unreadable, "U2's document arm").toContain('1 documents never became invoices')
  expect(unreadable, "U3's document arm").toContain(
    'The extractor could not read them, so no rule was ever run against them and nothing was stored. They cannot be fixed here: replace the documents and import again.',
  )
  expect(unreadable, "U3's spreadsheet arm reached a document run").not.toContain('The importer could not read them')
  expect(unreadable, "U5's document arm").toContain('1 of 2 documents. The invoices that did import are unaffected.')

  const whyCell = page.getByText('Why it could not be read', { exact: true })
  await expect(whyCell, "the unreadable grid's last header cell").toHaveCount(1)
  const unreadableHeaderRow = whyCell.locator('xpath=..')
  await expect(
    unreadableHeaderRow.getByText('File', { exact: true }),
    'the resolved element is the header row, not the card around it',
  ).toHaveCount(1)
  await expect(unreadableHeaderRow.getByText('—', { exact: true }), "U4's document arm").toHaveCount(1)
  await expect(
    unreadableHeaderRow.getByText('Row', { exact: true }),
    "U4's spreadsheet arm reached a document run",
  ).toHaveCount(0)

  // --- AC-3: the containment sweep ------------------------------------------------------
  //
  // Non-empty FIRST, every time: an empty locator satisfies every containment check below
  // vacuously, and on a broken deploy that is exactly how this sweep would go green.
  const row = page.getByTestId('unreadable-row')
  await expect(row, 'the scan must render as its own row').toHaveCount(1)
  await expect(row.first(), 'the row must name the file it came from').toContainText(QUARANTINED_PDF_NAME)
  const card = row.first().locator('xpath=..')
  await expect(card, 'the resolved parent is the grid card, which also carries the header').toContainText(
    'Why it could not be read',
  )
  const handOff = row.first().getByRole('button', { name: 'Enter it by hand' })
  await expect(handOff, 'a stored document must offer the hand-off (EXTR-15-11)').toHaveCount(1)

  // The two tiles this story lengthened, each resolved from its VALUE text up to the Tile root
  // (ReviewBatch.tsx's Tile renders value and caption as the two children of one div). Each
  // chain is self-checked by the caption its tile must also carry.
  const registerTileValue = page.getByText('1 already in the register', { exact: true })
  await expect(registerTileValue, "B8's tile value").toHaveCount(1)
  const registerTile = registerTileValue.locator('xpath=..')
  await expect(registerTile, 'the resolved parent is the tile, which also carries its caption').toContainText(
    'Nothing to fix.',
  )
  const unreadableTileValue = page.getByText('1 unreadable documents', { exact: true })
  await expect(unreadableTileValue, "B2's tile value").toHaveCount(1)
  const unreadableTile = unreadableTileValue.locator('xpath=..')
  await expect(unreadableTile, 'the resolved parent is the tile, which also carries its caption').toContainText(
    'No invoice exists for them.',
  )

  type Fit = { width: number; rowSlack: number; handOffSlack: number; registerSlack: number; unreadableSlack: number }
  const fits: Fit[] = []
  const entryViewport = page.viewportSize()

  try {
    // Widest first -- WIDE_WIDTHS' own order (layout.ts:22): a cap strands only what the window
    // gives it room to strand.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const read = async () => {
        const [cardBox, rowBox, handOffBox, rowEdges, registerText, registerBox, unreadableText, unreadableBox] =
          await Promise.all([
            card.boundingBox(),
            row.first().boundingBox(),
            handOff.boundingBox(),
            row.first().evaluate(edgesOf),
            registerTileValue.evaluate(edgesOf),
            registerTile.evaluate(edgesOf),
            unreadableTileValue.evaluate(edgesOf),
            unreadableTile.evaluate(edgesOf),
          ])
        return { cardBox, rowBox, handOffBox, rowEdges, registerText, registerBox, unreadableText, unreadableBox }
      }
      const m = await settledRead(read, `document review screen at ${width}px`)
      expect(m.cardBox && m.rowBox && m.handOffBox, `card, row and control must all render at ${width}px`).toBeTruthy()

      // (a) the row is CONTAINED by its card -- the intersection is the row's own rect.
      // rectsOverlap (layout.ts:55-58) is a boolean and is true for a row hanging half out, so
      // it cannot state this at all; overlapOf returns the Rect that can.
      expect(
        sameRect(overlapOf(m.rowBox!, m.cardBox!), m.rowBox!),
        `the row must sit wholly inside its card at ${width}px (row ${JSON.stringify(m.rowBox)}, card ${JSON.stringify(m.cardBox)})`,
      ).toBe(true)

      // (b) the hand-off control is contained by its row, the same identity. This is the
      // subtask-11 control, and it lives inside the row's 1fr track -- the one track that can
      // be squeezed to nothing.
      expect(
        sameRect(overlapOf(m.handOffBox!, m.rowBox!), m.handOffBox!),
        `the hand-off control must sit wholly inside its row at ${width}px (button ${JSON.stringify(m.handOffBox)}, row ${JSON.stringify(m.rowBox)})`,
      ).toBe(true)

      // (c) the row's four grid tracks fit the box the card gave it. (a) cannot see this: a
      // grid container keeps its own width while its tracks overflow, so the box stays inside
      // the card while the cells hang out of it.
      expect(
        m.rowEdges.scrollWidth,
        `the row's grid tracks overflow their own box at ${width}px (${m.rowEdges.scrollWidth} > ${m.rowEdges.clientWidth})`,
      ).toBeLessThanOrEqual(m.rowEdges.clientWidth + 1)

      // (d) each lengthened tile value stays inside its tile, and its TEXT fits the box it was
      // given. Both, never one: a block child keeps its parent's content width while the text
      // inside it overflows, so the edge check alone passes on the very defect this guards.
      for (const [label, text, tile] of [
        ['the already-in-the-register tile', m.registerText, m.registerBox],
        ['the unreadable-documents tile', m.unreadableText, m.unreadableBox],
      ] as const) {
        expect(text.outerLeft, `${label}'s value must start inside its tile at ${width}px`).toBeGreaterThanOrEqual(
          tile.left - 0.5,
        )
        expect(text.outerRight, `${label}'s value must end inside its tile at ${width}px`).toBeLessThanOrEqual(
          tile.right + 0.5,
        )
        expect(
          text.scrollWidth,
          `${label}'s value text overflows its own box at ${width}px (${text.scrollWidth} > ${text.clientWidth})`,
        ).toBeLessThanOrEqual(text.clientWidth + 1)
      }

      fits.push({
        width,
        rowSlack: m.rowEdges.clientWidth - m.rowEdges.scrollWidth,
        handOffSlack: m.rowEdges.right - (m.handOffBox!.x + m.handOffBox!.width),
        registerSlack: m.registerBox.right - m.registerText.outerRight,
        unreadableSlack: m.unreadableBox.right - m.unreadableText.outerRight,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  // The sweep ran every width, in order -- a loop that measured fewer would otherwise pass on
  // whatever it did reach.
  expect(fits.map((f) => f.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])
  await testInfo.attach('document-review-containment.json', {
    body: JSON.stringify(fits, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-18-07 · the deployed proof: docling's real reading, not the mock's fixed shape ---
//
// Cannot run before EXTRACTOR=docling is set on production and this PR leaves draft (D-35) --
// dev-env.yml gates the whole deployed e2e job on `pull_request.draft == false`. Authored and
// pushed now; their first real run is that deploy gate, not this pass.

test("EXTR18-E2E-01 (AC-5): the deployed reading is the document's own number", async ({ page }) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-18-07 rich')
  const detail = await openExtractionReview(page)

  const field = detail.fields.find((f) => f.name === 'invoice_number')
  expect(field, 'no invoice_number field on the wire').toBeTruthy()
  // Equality, never a negation: a reachable-but-empty sidecar settles succeeded with the field
  // absent, and `undefined !== 'MOCK-INV-0001'` would pass on a broken extractor.
  expect(field!.value, "the settled number is not the fixture's printed number").toBe('ASC-2026-0918')
  expect(field!.reason, 'a decided field must carry reason ""').toBe('')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// A document whose invoice_number never resolves mints a QUARANTINED batch, not an invoice
// (internal/importer/document.go:161) -- so routeAfterRun's 'single' arm is unreachable and
// extractOneDocument's wait for invoice-detail can never settle. Both fixtures below are that
// case by construction: the scan has no recoverable text at all, and the dense page's label
// OCRs as "INV0ICE NO:" (D-33). The verdict is therefore read off the deployed wire, which is
// also exactly what EXTR-15's Core AC will consume. The review SCREEN cannot serve as the
// oracle here: its only entry point is SourceDocumentCard's open-extraction-review, which
// renders on the invoice detail -- building a surface for these terminal states is EXTR-15's.
async function settleOneDocument(
  page: Page,
  label: string,
  file: { name: string; buffer: Buffer },
): Promise<{ token: string; jobId: string; detail: ExtractionDetail }> {
  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `${label} ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  // Registered before the pick, same reason as EXTR09-E2E-01's own waiter: the upload can
  // resolve before an awaited setInputFiles returns.
  const documentPost = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/submission/v1/documents'),
    { timeout: 120_000 },
  )
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: file.name, mimeType: 'application/pdf', buffer: file.buffer })
  await page.getByRole('button', { name: 'Extract invoices' }).click()

  const documentId = ((await (await documentPost).json()) as { document_id?: string }).document_id
  expect(documentId, 'the upload must mint a stored document id').toMatch(/^[0-9a-fA-F-]{36}$/)

  let jobId = ''
  await expect
    .poll(
      async () => {
        const { jobs } = await getExtractions(token, documentId)
        const job = jobs[0]
        if (!job) return 'no job for this document yet'
        jobId = job.id
        return job.state
      },
      { message: 'the extraction never reached a terminal state', timeout: 240_000, intervals: [1_000] },
    )
    // succeeded, not dead_lettered: an unreadable TEXT layer is a reading, not a failure
    // (TestRLS_ScannedFixtureSettlesTheTextLayerUnreadable asserts the same state wired).
    .toBe('succeeded')

  // The run's own landing, pinned because it is the terminal state EXTR-15 must surface: a
  // quarantined batch still exists, so routeAfterRun returns 'review', never 'none'.
  await expect
    .poll(
      async () => {
        if (await page.getByTestId('review-table').isVisible()) return 'review batch surface'
        if (await page.getByText(/^BATCH /).first().isVisible()) return 'review batch surface'
        if (await page.getByTestId('invoice-detail').isVisible()) return 'invoice detail'
        if (await page.getByRole('button', { name: 'Extract invoices' }).isVisible()) return 'back on the picker'
        return 'nothing yet'
      },
      { message: 'a quarantined single-document run must land on the review batch surface', timeout: 120_000 },
    )
    .toBe('review batch surface')

  return { token, jobId, detail: await getExtractionDetail(token, jobId) }
}

test('EXTR18-E2E-02 (AC-8): a document with no recoverable text settles unreadable, and its pages still render', async ({
  page,
}) => {
  test.setTimeout(600_000)
  const errors = collectErrors(page)

  const { token, jobId, detail } = await settleOneDocument(page, 'EXTR-18-07 scanned', {
    name: 'scanned_invoice.pdf',
    buffer: uniqueScannedPdfBytes(),
  })

  const textLayer = detail.fields.find((f) => f.name === 'document_text_layer')
  expect(textLayer, 'no document_text_layer field on the wire').toBeTruthy()
  expect(textLayer!.reason, 'a scan with no recoverable text must settle unreadable').toBe('unreadable')
  // worker.go's zero-text branch replaces `results` WHOLESALE, so the unreadable verdict is
  // the only row -- never one reason among ten missing fields.
  expect(
    detail.fields.map((f) => f.name),
    'the zero-text branch must settle exactly one field row',
  ).toEqual(['document_text_layer'])

  // unreadable is a TEXT verdict, not a render failure -- the distinction EXTR-15's T5 rests
  // on. Positive dimensions, not a count: a row with a 0x0 page never rendered.
  expect(detail.pages.length, 'a document with no page rows cannot prove the pages render').toBeGreaterThan(0)
  for (const p of detail.pages) {
    expect(p.width_px, `page ${p.page} rendered no width`).toBeGreaterThan(0)
    expect(p.height_px, `page ${p.page} rendered no height`).toBeGreaterThan(0)
  }

  // The stored bytes themselves, the deployed counterpart of the canvas's naturalWidth check.
  const img = await fetch(`${apiBase()}/api/submission/v1/extractions/${jobId}/pages/1`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(img.status, "page 1's image must be served").toBe(200)
  const bytes = Buffer.from(await img.arrayBuffer())
  expect(bytes.length, "page 1's image is empty").toBeGreaterThan(1_000)
  expect(bytes.subarray(0, 8).toString('hex'), 'page 1 is not a PNG').toBe('89504e470d0a1a0a')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR18-E2E-03: an image-only page the OCR can read is NOT unreadable', async ({ page }) => {
  test.setTimeout(600_000)
  const errors = collectErrors(page)

  const { detail } = await settleOneDocument(page, 'EXTR-18-07 dense', {
    name: 'dense_invoice.pdf',
    buffer: uniqueDensePdfBytes(),
  })

  // The OCR-succeeded half of the pair: no document_text_layer row at all.
  const textLayer = detail.fields.find((f) => f.name === 'document_text_layer')
  expect(textLayer, 'an OCR-readable image page must carry no document_text_layer field').toBeUndefined()

  const total = detail.fields.find((f) => f.name === 'total')
  const currency = detail.fields.find((f) => f.name === 'currency')
  expect(total, 'no total field on the wire').toBeTruthy()
  expect(currency, 'no currency field on the wire').toBeTruthy()
  expect(total!.reason, 'total must be decided').toBe('')
  expect(currency!.reason, 'currency must be decided').toBe('')
  expect(total!.value, 'total must carry a value').not.toBeNull()
  expect(currency!.value, 'currency must carry a value').not.toBeNull()

  // Not asserted: invoice_number. OCR reads the label as "INV0ICE NO:" (digit zero for letter
  // O), the Tier-1 anchor misses, and the field settles missing (D-33) -- which is why this
  // document quarantines rather than filing, and why settleOneDocument exists.

  const lines = wireLines(detail)
  expect(lines.length, 'the OCR read fewer than 2 line rows').toBeGreaterThanOrEqual(2)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
