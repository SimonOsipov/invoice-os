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
import { login, createEntity, listInvoices, approveUntilClosed, firmApproverTokens, PERSONAS } from '../api/client'
import { ensureFirmPolicyActive } from '../api/contract-helpers'
import { freshTin } from '../api/fixtures'
import { approvalRun404Dropper } from './consoleGate'
import { gaps, WIDE_WIDTHS } from './layout'
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
// Outcomes are NOT asserted here. internal/extraction/mock.go:92-98 stamps MOCK-INV-0001
// on every fixture, so two documents in one run collide by design;
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

  // Both fixtures stamp MOCK-INV-0001 (mock.go:92-98) and collide by design -- with
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
