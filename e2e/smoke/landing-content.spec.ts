import { expect, test, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import { seedConsent } from './landingConsent'

// The landing page's content contract (TEST-01-07): F-6 audience strip and F-5's
// live-validation preview. Named for the capability they share
// (.claude/rules/e2e.md: organise specs by capability, never by date), alongside
// landing-nav/landing-demo/landing-privacy/landing-consent.
//
// CI ASYMMETRY: this spec executes only on a pull request's own deploy gate
// (dev-env.yml:891 gates the `e2e` job on `github.event_name == 'pull_request'`). On a push
// to main it is typechecked and built but NOT executed — ci.yml:70-76's `frontend` filter
// does include `e2e/**`, so `pnpm -r typecheck` still compiles this file on every push; it is
// dev-env.yml's Playwright run that push never reaches.
//
// TARGET SURFACE: `landing` is a static marketing surface, so these assertions pin what the
// deployed build actually SERVES, not a backend contract — there is no API behind F-5 or F-6.
//
// WHY THIS IS E2E AND NOT UNIT: F-6 carries `unit_applicable = 0` in the system map — a
// unit test on it raises no coverage, because the unit slot is not in its denominator.
// Only a Playwright citation can close it. F-5 needs both dimensions; its unit half lives in
// Hero.validationPreview.dom.test.tsx (TEST-01-03).
//
// RETYPED, NOT IMPORTED — the opposite of the unit tests' convention, and deliberately so.
// Every expected value below is retyped from its source rather than imported: importing
// AudienceStrip.tsx#AUDIENCES / data.tsx#HERO_CHECKS into e2e/ would make these
// assertions agree with themselves no matter what the deployed build actually serves — the
// same reasoning landing-demo.spec.ts already applies at :41-46. Unit tests do the opposite
// (assert against the imported constant) because there the risk runs the other way: a retyped
// literal there could drift from the source without either ever red-flagging the other. Both
// are correct in their own dimension.
//
// FUNCTIONAL ONLY: no screenshot, no pixel diff, no geometry assertion — `visual_applicable`
// is 0 on every feature on this screen, and .claude/rules/e2e.md bars screenshot and pixel gates anyway.
//
// LOCAL GREEN IS NOT EXPECTED YET. `[data-strip]` / `[data-tally]` (TEST-01-01) exist in this
// branch's source but are not deployed anywhere. This spec's first real green run is this
// story's own PR deploy gate, once that PR has deployed.

const LANDING_URL = resolveTarget('LANDING_URL')

// F-6, retyped from frontend/landing/src/components/AudienceStrip.tsx#AUDIENCES, in render order.
const AUDIENCE_SEGMENTS = [
  'Finance teams',
  'Accounting firms',
  'Growing businesses',
  'Fintech',
  'Technology partners',
] as const

// The accounting-system / data-format wordmarks the map drifted to on 2026-09-05 (this
// story's Objective) — the criterion's own negative half, and the half that actually broke.
const FORBIDDEN_ACCOUNTING_TERMS = ['SAP', 'NetSuite', 'Sage', 'QuickBooks', 'Zoho', 'CSV/XLSX'] as const

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

// Retyped from Hero.tsx's two data-tally spans. Both are hardcoded literals,
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

  const band = page.locator('section.band-sage').filter({ has: strip })
  await expect(band, 'the strip is not inside exactly one section.band-sage').toHaveCount(1)
  const bandBg = await band.evaluate((el) => getComputedStyle(el).backgroundColor)
  const sageBg = await page.evaluate(() => {
    const probe = document.createElement('div')
    probe.style.background = 'var(--sage)'
    document.body.appendChild(probe)
    const bg = getComputedStyle(probe).backgroundColor
    probe.remove()
    return bg
  })
  expect(sageBg, 'control: the --sage probe resolved to a colour').not.toBe('rgba(0, 0, 0, 0)')
  expect(bandBg, 'the band background is not var(--sage)').toBe(sageBg)

  const label = strip.locator(':scope > div').first()
  await expect(label, 'the strip label is missing').toHaveCount(1)
  await expect(label).toHaveText('Built for the way your business works')

  const segments = strip.locator('span')
  await expect(segments, 'the strip does not hold exactly 5 segments').toHaveCount(AUDIENCE_SEGMENTS.length)
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

// E4 — the retired positioning copy. None of these four strings carries a data-* hook, so the
// selectors are structural; App.landingCopy.dom.test.tsx asserts the same four on the SSR tree,
// which is what keeps a bad selector here from costing a fleet rebuild to discover.
const HERO_LEAD_TEXT = 'Bring invoices, approvals and changing country requirements into one connected solution. Available for Nigeria.'
const SOLUTION_HEADING_TEXT = 'ASComply is your invoice compliance solution.'
const SOLUTION_BODY_TEXT =
  'We help your team validate invoices before they are submitted, manage approvals internally, store audit-ready records and submit them to the regulatory bodies.'
// Retyped from Footer.tsx's tagline (V459-483).
const FOOTER_TAGLINE_TEXT = 'Clarity for every invoice. Confidence for your business.'

test('landing content: the retired positioning copy is replaced everywhere it shipped', async ({ page }) => {
  const sinks = await openLanding(page)

  const heroParagraph = page.locator('#top p')
  await expect(heroParagraph, '#top does not hold exactly one hero paragraph').toHaveCount(1)
  await expect(heroParagraph).toHaveText(HERO_LEAD_TEXT)

  const solutionHeading = page.locator('#solution h2')
  await expect(solutionHeading, '#solution does not hold exactly one heading').toHaveCount(1)
  await expect(solutionHeading).toHaveText(SOLUTION_HEADING_TEXT)

  // .mod-body excluded: those are the module-card paragraphs, not the section intro.
  const solutionIntro = page.locator('#solution p:not(.mod-body)')
  await expect(solutionIntro, '#solution does not hold exactly two intro paragraphs').toHaveCount(2)
  await expect(solutionIntro.nth(1)).toHaveText(SOLUTION_BODY_TEXT)

  const footerTagline = page.locator('footer p')
  await expect(footerTagline, 'footer does not hold exactly one tagline paragraph').toHaveCount(1)
  await expect(footerTagline).toHaveText(FOOTER_TAGLINE_TEXT)

  expectNoConsoleErrors(sinks)
})

// Retyped from App.landingCopy.dom.test.tsx's Nigeria-only sweep, not imported.
const FORBIDDEN_COUNTRY_PATTERNS = [/kenya/i, /south africa/i, /\bKRA\b/, /etims/i, /\bSARS\b/, /pan-african/i, /expansion/i, /across africa/i, /nigeria and africa/i, /africa moves/i]
const KEPT_ROADMAP_SENTENCES = [
  'Our roadmap connects official updates, AI-assisted analysis and human review.',
  'The systems shown on this page are roadmap targets, not confirmed partnerships or available connectors.',
]

test('landing content: the page names Nigeria only', async ({ page }) => {
  const sinks = await openLanding(page)

  await expect(page.locator('#intelligence'), 'control: #intelligence is on the page').toHaveCount(1)
  await expect(page.locator('#coverage'), '#coverage is gone').toHaveCount(0)
  await expect(page.locator('a[href$="#coverage"]'), 'no link targets #coverage').toHaveCount(0)
  await expect(page.locator('footer a[href$="#intelligence"]'), 'control: the footer links #intelligence').toHaveCount(1)

  await expect(page.locator('#intelligence button[aria-pressed]:not([data-intel-step])'), 'no country chip in #intelligence').toHaveCount(0)
  await expect(page.locator('#intelligence button[aria-pressed]'), 'control: the four steps are pressed-state buttons').toHaveCount(4)
  await expect(page.locator('[data-intel-workspace] aside'), 'the workspace aside names Nigeria').toContainText('Nigeria')

  const text = await page.evaluate(() => {
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    const parts: string[] = []
    for (let n = walker.nextNode(); n; n = walker.nextNode()) parts.push(n.nodeValue ?? '')
    return parts.join(' ')
  })
  expect(text, 'control: the H1 reads Nigeria').toContain('Nigeria moves.')
  for (const re of FORBIDDEN_COUNTRY_PATTERNS) expect(text, String(re)).not.toMatch(re)
  const roadmap = (text.replace(/\s+/g, ' ').match(/[^.]*roadmap[^.]*\./gi) ?? []).map((x) => x.trim())
  expect(roadmap, 'every roadmap sentence is a kept one').toEqual(KEPT_ROADMAP_SENTENCES)

  expectNoConsoleErrors(sinks)
})

// Retyped from frontend/landing/src/data.tsx#MODULES, in render order.
const MODULE_TITLES = [
  'Business profile',
  'User access',
  'Customer / vendor',
  'Invoice management',
  'Validation engine',
  'Approval workflow',
  'Document generation',
  'Integration / API',
  'Archive & audit',
  'Reporting & analytics',
  'Partner portal',
  'Platform admin',
] as const

// Retyped from frontend/landing/src/data.tsx#API_BULLETS, in render order.
const API_BULLET_TEXTS = [
  'REST API: create, validate, fetch status, fetch documents',
  'Signed webhooks on status change and submission events',
  'OAuth2 and scoped API keys, with per-tenant isolation',
  'Sandbox MBS/NRS adapter. Production on accreditation.',
] as const

// Retyped from frontend/landing/src/data.tsx#FAQS (questions), in render order.
const FAQ_QUESTIONS = [
  'What does ASComply Africa do?',
  'Do we need to replace our accounting system?',
  'Are the integration partners already available?',
  'How does AI-supported regulatory monitoring work?',
  'Can accounting firms work with multiple clients?',
] as const

test('landing content: the solution grid lists the twelve modules in order', async ({ page }) => {
  const sinks = await openLanding(page)

  await expect(page.locator('#solution .mod-cell'), '#solution does not hold exactly 12 module cells').toHaveCount(
    MODULE_TITLES.length,
  )
  await expect(page.locator('#solution .mod-cell h3')).toHaveText([...MODULE_TITLES])

  expectNoConsoleErrors(sinks)
})

test('landing content: the API band lists its four surfaces and the validate sample', async ({ page }) => {
  const sinks = await openLanding(page)

  const api = page.locator('#api')
  await expect(api, '#api did not resolve to exactly one element').toHaveCount(1)

  // Every #api span in DOM order, filtered to the bullet texts: order and exactly-once in one assertion.
  const spanTexts = await api.locator('span').allTextContents()
  const bullets = spanTexts.map((t) => t.trim()).filter((t) => (API_BULLET_TEXTS as readonly string[]).includes(t))
  expect(bullets, 'the #api bullets differ from the four retyped, in order and once each').toEqual([...API_BULLET_TEXTS])

  const panel = page.locator('[data-api-panel]')
  await expect(panel, '[data-api-panel] did not resolve to exactly one element').toHaveCount(1)
  await expect(panel).toContainText('POST /v1/invoices/validate')
  const sample = panel.locator('pre')
  await expect(sample).toContainText('curl https://api.ascomply.africa/v1/invoices/validate')
  await expect(sample).toContainText('"status": "validated"')

  expectNoConsoleErrors(sinks)
})

test('landing content: Request API access opens the Book a demo dialog', async ({ page }) => {
  const sinks = await openLanding(page)

  const dialog = page.getByRole('dialog', { name: 'Book a demo' })
  await expect(dialog, 'a Book a demo dialog was open before the click').toHaveCount(0)

  await page.locator('#api').getByRole('button', { name: 'Request API access' }).click()
  await expect(dialog).toBeVisible()

  expectNoConsoleErrors(sinks)
})

test('landing content: every integration partner card says In progress', async ({ page }) => {
  const sinks = await openLanding(page)

  const cards = page.locator('[data-partner]')
  await expect(cards, 'the page does not hold exactly 6 partner cards').toHaveCount(6)
  for (let i = 0; i < 6; i++) {
    const badge = cards.nth(i).locator('.ds-badge')
    await expect(badge, `partner card ${i + 1} does not hold exactly one badge`).toHaveCount(1)
    await expect(badge).toBeVisible()
    await expect(badge).toHaveText('In progress')
  }

  expectNoConsoleErrors(sinks)
})

test('landing content: the FAQ opens the first answer and keeps one open at a time', async ({ page }) => {
  const sinks = await openLanding(page)

  const buttons = page.locator('#faq .ds-faq-btn')
  const answers = page.locator('#faq .ds-faq-a')
  await expect(buttons, '#faq does not hold exactly 5 questions').toHaveCount(FAQ_QUESTIONS.length)
  await expect(buttons).toHaveText([...FAQ_QUESTIONS])
  await expect(answers, '#faq does not hold exactly 5 answers').toHaveCount(FAQ_QUESTIONS.length)

  const expanded = () => buttons.evaluateAll((els) => els.map((el) => el.getAttribute('aria-expanded')))

  await expect.poll(expanded, 'on load, only the first question should be expanded').toEqual([
    'true', 'false', 'false', 'false', 'false',
  ])
  await expect(answers.nth(0)).toBeVisible()
  for (const i of [1, 2, 3, 4]) await expect(answers.nth(i), `answer ${i + 1} is visible on load`).toBeHidden()

  await buttons.nth(2).click()
  await expect.poll(expanded, 'after clicking question 3, only it should be expanded').toEqual([
    'false', 'false', 'true', 'false', 'false',
  ])
  await expect(answers.nth(0)).toBeHidden()
  await expect(answers.nth(2)).toBeVisible()

  expectNoConsoleErrors(sinks)
})

test('landing content: the closing Book a demo opens the dialog', async ({ page }) => {
  const sinks = await openLanding(page)

  const dialog = page.getByRole('dialog', { name: 'Book a demo' })
  await expect(dialog, 'a Book a demo dialog was open before the click').toHaveCount(0)

  await page.locator('[data-closing]').getByRole('button', { name: 'Book a demo' }).click()
  await expect(dialog).toBeVisible()

  expectNoConsoleErrors(sinks)
})
