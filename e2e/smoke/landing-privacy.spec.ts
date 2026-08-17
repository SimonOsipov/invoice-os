import { test, expect, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import { gaps, WIDE_WIDTHS } from '../topology/layout'

// The privacy page (LAND-04): route reachability, footer/nav round trip, console
// cleanliness, and column layout at WIDE_WIDTHS + 390px. Relationship assertions only —
// gaps() and declared-vs-measured — never a raw width bound (see topology/layout.ts).

const LANDING_URL = resolveTarget('LANDING_URL')
const PRIVACY_URL = `${LANDING_URL}/privacy`

const GAP_SLACK_PX = 2 // sub-pixel halving of an odd content width
const MIN_PARAGRAPHS = 8 // non-vacuity floor; the page ships 22+
const MIN_DECLARED_MEASURE_PX = 320 // narrower than the narrowest phone this repo reasons about
const PHONE = { width: 390, height: 844 }
const MIN_PHONE_MEASURE_PX = 300

/** Attach the console/pageerror gate BEFORE navigating; returns the sink to assert on. */
function consoleGate(page: Page): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })
  return errors
}

function expectNoConsoleErrors(errors: string[]): void {
  expect(errors, `console errors on the privacy page:\n${errors.join('\n')}`).toEqual([])
}

/** Two rAFs — layout after a resize is committed by the second one. */
function settleLayout(page: Page): Promise<boolean> {
  return page.evaluate(
    () => new Promise<boolean>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(true)))),
  )
}

/**
 * goto + settle. The non-vacuity floor lives HERE, not in a standalone control test:
 * fullyParallel means every test gets its own page, so a separate test would prove
 * nothing about another test's page.
 */
async function openPrivacy(page: Page, url: string = PRIVACY_URL) {
  const errors = consoleGate(page)

  const response = await page.goto(url)
  expect(response, `no response from ${url}`).toBeTruthy()
  expect(response!.ok(), `${url} returned HTTP ${response!.status()}`).toBeTruthy()

  const container = page.getByTestId('privacy-container')
  const prose = page.getByTestId('privacy-prose')
  await expect(container).toBeVisible()
  await expect(prose).toBeVisible()
  await expect(prose.getByRole('heading', { level: 1 })).toHaveCount(1)

  await expect
    .poll(() => prose.locator('p').count(), { message: 'the prose column never filled in with paragraphs' })
    .toBeGreaterThanOrEqual(MIN_PARAGRAPHS)

  // Google-hosted fonts settle once, before any measurement (landing-nav.spec.ts's reason).
  await page.evaluate(() => document.fonts.ready.then(() => true))

  return { errors, container, prose }
}

// P1 — typing the path serves the privacy page, not the sales page.
test('landing privacy: typing /privacy serves the page and not the sales page', async ({ page }) => {
  const { errors } = await openPrivacy(page)
  await expect(page.locator('#problem')).toHaveCount(0)
  expectNoConsoleErrors(errors)
})

// P2 — isPrivacyPath strips exactly one trailing slash (route.ts:4).
test('landing privacy: a trailing slash reaches the same page', async ({ page }) => {
  const { errors } = await openPrivacy(page, `${PRIVACY_URL}/`)
  await expect(page).toHaveURL(/\/privacy\/$/)
  await expect(page.locator('#problem')).toHaveCount(0)
  expectNoConsoleErrors(errors)
})

// P3 — deep-link claim: a reload is a fresh request, so this only holds if the SPA
// fallback (Caddy try_files) is still serving index.html for /privacy.
test('landing privacy: a reload stays on the privacy page', async ({ page }) => {
  const { errors, container } = await openPrivacy(page)

  const r = await page.reload()
  expect(r, `no response reloading ${PRIVACY_URL}`).toBeTruthy()
  expect(r!.ok(), `reload of ${PRIVACY_URL} returned HTTP ${r!.status()}`).toBeTruthy()

  await expect(page).toHaveURL(/\/privacy$/)
  await expect(container).toBeVisible()
  await expect(page.locator('#problem')).toHaveCount(0)
  expectNoConsoleErrors(errors)
})

// P4 — negative control: the root still serves the sales page, not Privacy.
test('landing privacy: the site root still serves the sales page', async ({ page }) => {
  const errors = consoleGate(page)
  const response = await page.goto(LANDING_URL)
  expect(response, `no response from ${LANDING_URL}`).toBeTruthy()
  expect(response!.ok(), `${LANDING_URL} returned HTTP ${response!.status()}`).toBeTruthy()

  await expect(page.getByTestId('privacy-container')).toHaveCount(0)
  await expect(page.locator('#problem')).toBeVisible()
  expectNoConsoleErrors(errors)
})

// P5 — footer round trip, located by href only. footerHref() must never prefix
// /privacy (that would produce the protocol-relative //privacy).
test('landing privacy: the footer link reaches the page from the root, and never as //privacy', async ({
  page,
}) => {
  const errors = consoleGate(page)
  const response = await page.goto(LANDING_URL)
  expect(response, `no response from ${LANDING_URL}`).toBeTruthy()
  expect(response!.ok(), `${LANDING_URL} returned HTTP ${response!.status()}`).toBeTruthy()

  const link = page.getByRole('contentinfo').locator('a[href="/privacy"]')
  await expect(link).toHaveCount(1)
  await link.click()

  await expect(page).toHaveURL(/\/privacy$/)
  await expect(page.getByTestId('privacy-container')).toBeVisible()

  const footer = page.getByRole('contentinfo')
  await expect(footer.locator('a[href="//privacy"]')).toHaveCount(0)
  await expect(footer.locator('a[href="/privacy"]')).toHaveCount(1)
  expectNoConsoleErrors(errors)
})

// P6 — the nav's hrefPrefix carries every hash target back to '/' on the privacy page.
test('landing privacy: the nav returns the visitor to the sales page', async ({ page }) => {
  const { errors } = await openPrivacy(page)
  const nav = page.getByRole('navigation', { name: 'Primary' })

  const hrefs = await nav.locator('a').evaluateAll((els) => els.map((e) => e.getAttribute('href')))
  expect(hrefs).toHaveLength(6)
  for (const href of hrefs) {
    expect(href ?? '', 'a nav link lost its /privacy hrefPrefix').toMatch(/^\/#/)
  }

  // "The Problem" carries no shed class, so it stays visible at the default viewport.
  await nav.locator('a[href="/#problem"]').click()
  await expect(page).toHaveURL(/\/#problem$/)
  await expect(page.getByTestId('privacy-container')).toHaveCount(0)
  await expect(page.locator('#problem')).toBeVisible()
  expectNoConsoleErrors(errors)
})

// P7 — gutter symmetry and containment. Widest first: a cap only strands what the
// window gives it room to strand. No raw width bound — see topology/layout.ts header.
test('landing privacy: the prose column stays centred in its container at every wide width', async ({
  page,
}, testInfo) => {
  const { errors, container, prose } = await openPrivacy(page)
  const entry = page.viewportSize()
  const sweep: Array<{ width: number; left: number; right: number; containerWidth: number; proseWidth: number }> = []

  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
      await settleLayout(page)

      const [proseBox, containerBox] = await Promise.all([prose.boundingBox(), container.boundingBox()])
      expect(proseBox, `prose column did not render at ${width}px`).toBeTruthy()
      expect(containerBox, `container did not render at ${width}px`).toBeTruthy()
      const g = gaps(proseBox!, containerBox!)
      sweep.push({ width, left: g.left, right: g.right, containerWidth: containerBox!.width, proseWidth: proseBox!.width })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }

  // Attach the whole sweep before asserting, so a red run still carries the numbers.
  await testInfo.attach('privacy-prose-gaps.json', { body: JSON.stringify(sweep, null, 2), contentType: 'application/json' })
  testInfo.annotations.push({
    type: 'measurement (LAND-04-05 / D9)',
    description: sweep.map((m) => `${m.width}px: left ${m.left}px, right ${m.right}px`).join(' | '),
  })

  expect(sweep).toHaveLength(WIDE_WIDTHS.length)
  for (const m of sweep) {
    const at = `at ${m.width}px`
    expect(m.left, `prose column overhangs its container on the left ${at}`).toBeGreaterThanOrEqual(0)
    expect(m.right, `prose column overhangs its container on the right ${at}`).toBeGreaterThanOrEqual(0)
    expect(
      Math.abs(m.left - m.right),
      `prose column is not centred ${at} (left ${m.left}px, right ${m.right}px)`,
    ).toBeLessThanOrEqual(GAP_SLACK_PX)
  }
  expectNoConsoleErrors(errors)
})

// P8 — its own sweep: gaps() alone can't see a shrunk-but-still-centred column
// (both gutters stay equal). Pins the RENDERED width to data-prose-max, the
// published contract — this package's own PROSE_MAX_WIDTH is unreachable from e2e.
test('landing privacy: the prose column renders as wide as it declares itself to be', async ({
  page,
}, testInfo) => {
  const { errors, prose } = await openPrivacy(page)

  const declared = Number(await prose.getAttribute('data-prose-max'))
  expect(Number.isFinite(declared), `data-prose-max is missing or not a number (read "${declared}")`).toBeTruthy()
  expect(
    declared,
    `declared prose measure ${declared}px is narrower than ${MIN_DECLARED_MEASURE_PX}px`,
  ).toBeGreaterThanOrEqual(MIN_DECLARED_MEASURE_PX)

  const entry = page.viewportSize()
  const sweep: Array<{ width: number; measured: number }> = []

  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
      await settleLayout(page)

      const proseBox = await prose.boundingBox()
      expect(proseBox, `prose column did not render at ${width}px`).toBeTruthy()
      sweep.push({ width, measured: proseBox!.width })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }

  await testInfo.attach('privacy-prose-width.json', {
    body: JSON.stringify({ declared, sweep }, null, 2),
    contentType: 'application/json',
  })
  testInfo.annotations.push({
    type: 'measurement (LAND-04-05 / D12)',
    description: `declared ${declared}px | ${sweep.map((m) => `${m.width}px: measured ${m.measured}px`).join(' | ')}`,
  })

  expect(sweep).toHaveLength(WIDE_WIDTHS.length)
  for (const m of sweep) {
    expect(
      Math.abs(m.measured - declared),
      `prose column measured ${m.measured}px at ${m.width}px, declared ${declared}px`,
    ).toBeLessThanOrEqual(GAP_SLACK_PX)
  }
  expectNoConsoleErrors(errors)
})

// P9 — 390px. Assertion 2 is what actually carries the readability claim: .asc-app's
// overflowX:'clip' (App.tsx) removes descendant overflow from the document's
// scrollable region, so an overflowing element is invisible AND unscrollable to
// assertion 3 alone.
test('landing privacy: the page is readable at 390px', async ({ page }) => {
  await page.setViewportSize(PHONE)
  const { errors, container, prose } = await openPrivacy(page)

  const proseBox = await prose.boundingBox()
  const containerBox = await container.boundingBox()
  expect(proseBox, 'prose column did not render at 390px').toBeTruthy()
  expect(containerBox, 'container did not render at 390px').toBeTruthy()

  // 1. Measure. The scrollbar-independent relationship is the primary oracle; the
  // floor only backstops it.
  expect(proseBox!.width, `prose column is ${proseBox!.width}px wide at 390px`).toBeGreaterThanOrEqual(
    MIN_PHONE_MEASURE_PX,
  )
  const containerContentBox = await container.evaluate((el) => {
    const cs = getComputedStyle(el)
    return el.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)
  })
  expect(
    Math.abs(proseBox!.width - containerContentBox),
    `prose width ${proseBox!.width}px does not match the container's content box ${containerContentBox}px`,
  ).toBeLessThanOrEqual(1)
  const g = gaps(proseBox!, containerBox!)
  expect(g.left, 'prose column overhangs its container on the left at 390px').toBeGreaterThanOrEqual(0)
  expect(g.right, 'prose column overhangs its container on the right at 390px').toBeGreaterThanOrEqual(0)

  // 2. Nothing in the column's own subtree overhangs its right edge.
  const overhang = await prose.evaluate((root) => {
    const rootRight = root.getBoundingClientRect().right
    const offenders: Array<{ tag: string; over: number; text: string }> = []
    let scanned = 0
    for (const el of Array.from(root.querySelectorAll('*'))) {
      scanned++
      const over = el.getBoundingClientRect().right - rootRight
      if (over > 1) {
        offenders.push({ tag: el.tagName.toLowerCase(), over: Math.round(over), text: (el.textContent ?? '').trim().slice(0, 60) })
      }
    }
    return { scanned, offenders }
  })
  expect(overhang.scanned, 'the subtree walk did not run').toBeGreaterThanOrEqual(MIN_PARAGRAPHS)
  expect(overhang.offenders, `content overflows the prose column at 390px: ${JSON.stringify(overhang.offenders)}`).toEqual([])

  // 3. Near-vacuous alone (see file header) — only red if the clip above is removed
  // while something still overflows. Kept because it fails on a different bug than #2.
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth)
  const clientWidth = await page.evaluate(() => document.documentElement.clientWidth)
  expect(
    scrollWidth,
    `document scrolls horizontally at 390px (scrollWidth ${scrollWidth} vs clientWidth ${clientWidth})`,
  ).toBeLessThanOrEqual(clientWidth + 1)

  expectNoConsoleErrors(errors)
})
