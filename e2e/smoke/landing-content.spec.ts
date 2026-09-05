import { expect, test, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import { seedConsent } from './landingConsent'

// The landing page's content contract (TEST-01-07): F-6 audience strip, F-7 how-it-works
// steps, and F-5's live-validation preview. Named for the capability the three share
// (docs/e2e-convention.md's "organize by capability, not by date"), alongside
// landing-nav/landing-demo/landing-privacy/landing-consent.
//
// CI ASYMMETRY: this spec executes only on a pull request's own deploy gate
// (dev-env.yml:891 gates the `e2e` job on `github.event_name == 'pull_request'`). On a push
// to main it is typechecked and built but NOT executed — ci.yml:70-76's `frontend` filter
// does include `e2e/**`, so `pnpm -r typecheck` still compiles this file on every push; it is
// dev-env.yml's Playwright run that push never reaches.
//
// TARGET SURFACE: `landing` is a static marketing surface (docs/e2e-convention.md → "Target
// surface"), so these assertions pin what the deployed build actually SERVES, not a backend
// contract — there is no API behind any of F-5/F-6/F-7.
//
// WHY THIS IS E2E AND NOT UNIT: F-6 and F-7 carry `unit_applicable = 0` in the system map — a
// unit test on either raises no coverage, because the unit slot is not in their denominator.
// Only a Playwright citation can close them. F-5 needs both dimensions; its unit half lives in
// Hero.validationPreview.dom.test.tsx (TEST-01-03).
//
// RETYPED, NOT IMPORTED — the opposite of the unit tests' convention, and deliberately so.
// Every expected value below is retyped from its source rather than imported: importing
// TrustStrip.tsx#AUDIENCES / data.tsx#STEPS / data.tsx#HERO_CHECKS into e2e/ would make these
// assertions agree with themselves no matter what the deployed build actually serves — the
// same reasoning landing-demo.spec.ts already applies at :41-46. Unit tests do the opposite
// (assert against the imported constant) because there the risk runs the other way: a retyped
// literal there could drift from the source without either ever red-flagging the other. Both
// are correct in their own dimension.
//
// FUNCTIONAL ONLY: no screenshot, no pixel diff, no geometry assertion — `visual_applicable`
// is 0 on every feature on this screen, and docs/e2e-convention.md is functional-only anyway.
//
// LOCAL GREEN IS NOT EXPECTED YET. `[data-strip]` / `[data-tally]` (TEST-01-01) exist in this
// branch's source but are not deployed anywhere. This spec's first real green run is this
// story's own PR deploy gate, once that PR has deployed. `#how`'s hook (`id="how"`) already
// ships in production, which is why its test is expected to pass against a production target
// today while the other two are not.

const LANDING_URL = resolveTarget('LANDING_URL')

// F-6, retyped from frontend/landing/src/components/TrustStrip.tsx#AUDIENCES, in render order.
const AUDIENCE_SEGMENTS = [
  'Medium taxpayers',
  'Accounting firms',
  'ERP consultants',
  'Distributors',
  'Manufacturers',
  'Formal SMEs',
  'Fintech',
  'CRMs',
] as const

// The accounting-system / data-format wordmarks the map drifted to on 2026-09-05 (this
// story's Objective) — the criterion's own negative half, and the half that actually broke.
const FORBIDDEN_ACCOUNTING_TERMS = ['SAP', 'NetSuite', 'Sage', 'QuickBooks', 'Zoho', 'CSV/XLSX'] as const

// F-7, retyped from frontend/landing/src/data.tsx#STEPS, in order.
const STEP_TITLES = ['Connect or import', 'Validate against MBS rules', 'Approve, archive & transmit'] as const
const STEP_NUMBERS = ['01', '02', '03'] as const
const STEP_POINTS: ReadonlyArray<readonly string[]> = [
  ['REST API & webhooks', 'CSV / XLSX bulk import', 'ERP connectors'],
  ['Nigeria rule pack', 'Field & tax logic checks', 'Inline fix suggestions'],
  ['Approval workflow', 'PDF + JSON/XML/UBL export', 'Immutable audit log'],
]

type CheckTag = 'PASS' | 'WARN' | 'FAIL'

// F-5, retyped from frontend/landing/src/data.tsx#HERO_CHECKS, in render order.
const HERO_CHECK_ROWS: ReadonlyArray<{ label: string; tag: CheckTag }> = [
  { label: 'Buyer TIN format · 12345678-0001', tag: 'PASS' },
  { label: 'VAT computed at 7.5%', tag: 'PASS' },
  { label: 'Mandatory seller fields present', tag: 'PASS' },
  { label: 'WHT applied on services line', tag: 'WARN' },
  { label: 'Invoice number not duplicated', tag: 'PASS' },
  { label: 'Line totals reconcile to header', tag: 'FAIL' },
]

// Retyped from Hero.tsx's two data-tally spans (:172, :176). Both are hardcoded literals,
// not derived from HERO_CHECKS at render time — the fact the invariant below turns on.
const TALLY_FAILURES_TEXT = '1 ERROR · 1 WARNING'
const TALLY_PASSED_TEXT = '14 / 16 CHECKS PASSED'

type ContentSinks = {
  /** console.error + pageerror, asserted empty at the end of every test in this file. */
  consoleErrors: string[]
}

function attachConsoleGate(page: Page): ContentSinks {
  const sinks: ContentSinks = { consoleErrors: [] }
  page.on('console', (msg) => {
    if (msg.type() === 'error') sinks.consoleErrors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    sinks.consoleErrors.push(`pageerror: ${err.message}`)
  })
  return sinks
}

function expectNoConsoleErrors(sinks: ContentSinks): void {
  expect(sinks.consoleErrors, `console errors on the landing page:\n${sinks.consoleErrors.join('\n')}`).toEqual([])
}

/** goto + the two console/pageerror listeners. Consent seeded false so the notice never renders. */
async function openLanding(page: Page): Promise<ContentSinks> {
  const sinks = attachConsoleGate(page)
  await seedConsent(page, false)

  const response = await page.goto(LANDING_URL)
  expect(response, `no response from ${LANDING_URL}`).toBeTruthy()
  expect(response!.ok(), `${LANDING_URL} returned HTTP ${response!.status()}`).toBeTruthy()

  return sinks
}

// E1 — F-6. The audience strip names buyer segments, and never an accounting system.
test('landing content: the audience strip names buyer segments, never an accounting system', async ({ page }) => {
  const sinks = await openLanding(page)

  const strip = page.locator('[data-strip="audience"]')
  await expect(strip, 'the audience strip did not resolve to exactly one element').toHaveCount(1)

  const segments = strip.locator('span')
  await expect(segments, 'the strip does not hold exactly 8 segments').toHaveCount(AUDIENCE_SEGMENTS.length)
  await expect(segments).toHaveText([...AUDIENCE_SEGMENTS])

  const stripText = (await strip.textContent()) ?? ''

  // Control needle: prove the scan can find something real before trusting its silence on
  // the forbidden terms below — a strip that rendered nothing would pass both checks.
  expect(stripText, 'control needle failed: the strip does not even contain "Accounting firms"').toContain(
    'Accounting firms',
  )
  for (const term of FORBIDDEN_ACCOUNTING_TERMS) {
    expect(stripText, `the audience strip names the accounting system/format "${term}"`).not.toContain(term)
  }

  expectNoConsoleErrors(sinks)
})

// E2 — F-7. Three ordered, numbered steps, each naming its own capabilities.
test('landing content: How it works renders three ordered, numbered steps', async ({ page }) => {
  const sinks = await openLanding(page)

  const how = page.locator('#how')
  await expect(how, '#how did not resolve to exactly one element').toHaveCount(1)

  const headings = how.locator('h3')
  await expect(headings, '#how does not hold exactly 3 h3 headings').toHaveCount(STEP_TITLES.length)
  await expect(headings).toHaveText([...STEP_TITLES])

  const numbers = how.locator('.mono')
  await expect(numbers, '#how does not hold exactly 3 step numbers').toHaveCount(STEP_NUMBERS.length)
  await expect(numbers).toHaveText([...STEP_NUMBERS])

  const cells = how.locator('.ios-grid > div')
  await expect(cells, '#how does not hold exactly 3 grid cells').toHaveCount(STEP_POINTS.length)
  for (let i = 0; i < STEP_POINTS.length; i++) {
    const cellText = (await cells.nth(i).textContent()) ?? ''
    for (const point of STEP_POINTS[i]) {
      expect(cellText, `step ${i + 1}'s cell is missing capability "${point}"`).toContain(point)
    }
  }

  expectNoConsoleErrors(sinks)
})

// E3 — F-5's e2e half. One row per check, pairing label with outcome tag, and the tally
// agrees with the retyped list under the invariant established by the architect (not naive
// equality — the hero card is a six-row excerpt of a sixteen-check run).
test('landing content: the live-validation preview lists every check and its tally agrees', async ({ page }) => {
  const sinks = await openLanding(page)

  const top = page.locator('#top')
  await expect(top, '#top did not resolve to exactly one element').toHaveCount(1)
  // Control needle: #top actually holds spans before the per-row loop below trusts it — a
  // #top that resolved to nothing would satisfy that loop vacuously.
  await expect(top.locator('span').first(), '#top holds no spans at all').toBeVisible()
  expect(HERO_CHECK_ROWS, 'the retyped check list must be exactly 6 entries').toHaveLength(6)

  // The 6 rows carry no selector of their own, but each row's OUTCOME TAG renders its own
  // literal text ('PASS' | 'WARN' | 'FAIL', data.tsx#HeroCheck.tag rendered verbatim). Found
  // by that text, scoped to #top, rather than by DOM position (a sibling/child-index walk
  // would silently select the wrong set the moment a wrapper div is added around the list).
  const rows = await page.evaluate(() => {
    const tagSpans = Array.from(document.querySelectorAll('#top span')).filter((el) =>
      ['PASS', 'WARN', 'FAIL'].includes((el.textContent ?? '').trim()),
    )
    return tagSpans.map((tagSpan) => {
      const row = tagSpan.parentElement
      if (!row) throw new Error('a tag span has no parent row')
      // The label is the row's other non-empty span — the icon span beside it is an <svg>
      // with no text node, so exactly one candidate is expected; more than one means the
      // row's own structure changed and this must fail loudly rather than guess.
      const candidates = Array.from(row.querySelectorAll('span')).filter(
        (s) => s !== tagSpan && (s.textContent ?? '').trim().length > 0,
      )
      if (candidates.length !== 1) {
        throw new Error(`row for tag "${tagSpan.textContent}" has ${candidates.length} label candidates, expected 1`)
      }
      return { label: (candidates[0].textContent ?? '').trim(), tag: (tagSpan.textContent ?? '').trim() }
    })
  })
  expect(rows, '#top does not hold exactly 6 outcome-tagged rows').toHaveLength(6)
  expect(rows).toEqual(HERO_CHECK_ROWS)

  const failuresLocator = page.locator('[data-tally="failures"]')
  const passedLocator = page.locator('[data-tally="passed"]')
  await expect(failuresLocator).toHaveText(TALLY_FAILURES_TEXT)
  await expect(passedLocator).toHaveText(TALLY_PASSED_TEXT)

  // The invariant (architect decision [f5-invariant-resolved]) computed from what the page
  // ACTUALLY rendered — parsed off the tally spans' own live text, and counted from the rows
  // read above — never from the retyped constants, so this stays a live oracle rather than a
  // tautology over our own fixture: errors === FAIL count, warnings === WARN count, and
  // total − passed === FAIL + WARN.
  const failuresText = (await failuresLocator.textContent()) ?? ''
  const passedText = (await passedLocator.textContent()) ?? ''
  const errorsMatch = failuresText.match(/(\d+)\s*ERROR/)
  const warningsMatch = failuresText.match(/(\d+)\s*WARNING/)
  const passedMatch = passedText.match(/(\d+)\s*\/\s*(\d+)\s*CHECKS PASSED/)
  if (!errorsMatch || !warningsMatch || !passedMatch) {
    throw new Error(`could not parse the tally spans: "${failuresText}" / "${passedText}"`)
  }
  const liveErrors = Number(errorsMatch[1])
  const liveWarnings = Number(warningsMatch[1])
  const livePassed = Number(passedMatch[1])
  const liveTotal = Number(passedMatch[2])

  const failCount = rows.filter((r) => r.tag === 'FAIL').length
  const warnCount = rows.filter((r) => r.tag === 'WARN').length

  expect(liveErrors, "the failures tally's error count !== FAIL count among the rendered rows").toBe(failCount)
  expect(liveWarnings, "the failures tally's warning count !== WARN count among the rendered rows").toBe(warnCount)
  expect(liveTotal - livePassed, 'total − passed !== FAIL + WARN among the rendered rows').toBe(
    failCount + warnCount,
  )

  expectNoConsoleErrors(sinks)
})
