import { test, expect, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'

// The landing page's top navigation: anchor targets, scroll clearance under the
// sticky header, and the scroll-spy's active-link indicator (LAND-01).
//
// These are behaviour tests, not render checks, and they live in the smoke suite
// on purpose. docs/e2e-convention.md gives `landing` "smoke only" because there is
// no backend to exercise — but jsdom has no layout engine and this package carries no
// React testing library, so a browser check is the only place a scroll-spy can be
// exercised at all. That is the same rationale carried by
// the ops-console org-switcher test in smoke.spec.ts. "Keep the browser layer thin"
// and "functional only — no visual regression" still apply: nothing here takes a
// screenshot, and every assertion is a fact about behaviour.
//
// Every geometric expectation below was derived from measurements taken against the
// PRE-FIX deployed build, where E2/E4/E6/E7 were each observed failing (recorded on
// the PR). They cannot be run green anywhere until a build carrying LAND-01's fixes
// is deployed — the first green run is the dev-env.yml deploy gate.

const LANDING_URL = resolveTarget('LANDING_URL')

// The six nav targets, in DOM order. Mirrors NAV_LINKS in
// frontend/landing/src/components/Nav.tsx. `#how` is deliberately NOT here: the
// How-it-works section still exists on the page, it just has no nav link — which
// makes it a second non-nav section E8 could park in, alongside `#demo`.
const NAV_HREFS = ['#problem', '#modules', '#compliance', '#accountants', '#developers', '#pricing'] as const

// Sub-pixel rects: a post-jump section top measures e.g. 64.77 or 65.16 against a
// 65px header. Every geometry comparison carries this slack. It is NOT a header
// height — the header's own measured box is the only height reference in this file.
const PX = 1

// Deep enough to prove the header is pinned rather than merely still on screen.
// The document is ~6800px tall at this viewport, so this never clamps — and the
// scrollY readback in E4 makes that self-verifying rather than assumed.
const DEEP_SCROLL_PX = 2000

// Nudge used to park a section just under the viewport top, well clear of the
// scroll-spy's threshold (see E7). Also not a header height.
const SPY_NUDGE_PX = 8

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
  expect(errors, `console errors on the landing page:\n${errors.join('\n')}`).toEqual([])
}

/**
 * goto + settle + measure. Returns the console sink, the nav landmark, the header,
 * and the header's OWN measured height — the single geometric reference for this
 * whole spec, so no pixel figure from the design system is duplicated here.
 */
async function openLanding(page: Page) {
  const errors = consoleGate(page)

  const response = await page.goto(LANDING_URL)
  expect(response, `no response from ${LANDING_URL}`).toBeTruthy()
  expect(response!.ok(), `${LANDING_URL} returned HTTP ${response!.status()}`).toBeTruthy()

  const nav = page.getByRole('navigation', { name: 'Primary' })
  const header = page.getByRole('banner')
  await expect(header).toBeVisible()

  // Fonts are Google-hosted with display=swap. A swap AFTER an anchor jump reflows
  // the document under a scroll offset that has already been set, moving section
  // tops by more than PX. Polling cannot save us there — it would settle on a stable
  // WRONG value — so settle fonts once, before any measurement or click.
  await page.evaluate(() => document.fonts.ready.then(() => true))

  const headerH = (await header.boundingBox())!.height
  return { errors, nav, header, headerH }
}

/** Park a section's top just below the viewport top, well under the spy threshold. */
async function scrollSectionUnderHeader(page: Page, href: string): Promise<void> {
  await page.evaluate(
    ({ sel, nudge }) => {
      const el = document.querySelector(sel)!
      window.scrollTo(0, el.getBoundingClientRect().top + window.scrollY - nudge)
    },
    { sel: href, nudge: SPY_NUDGE_PX },
  )
}

// E1 — the anchor-target contract. Passes today; pinned so that renaming a section
// id starts failing HERE rather than silently breaking every link in the nav.
test('landing nav: every link points at exactly one section that exists', async ({ page }) => {
  const { errors } = await openLanding(page)

  for (const href of NAV_HREFS) {
    // Count 1, not toBeAttached: a duplicated id must fail too. The id selector also
    // means the footer's links, which share three of these hrefs, are irrelevant.
    await expect(page.locator(href), `expected exactly one element matching ${href}`).toHaveCount(1)
  }

  expectNoConsoleErrors(errors)
})

// E2 — clicking a link sets the hash AND leaves the section clear of the header.
// FAILED against the pre-fix build: every section landed at ~0 instead of ~65,
// i.e. squarely underneath the sticky header.
test('landing nav: clicking a link jumps to a section clear of the header', async ({ page }) => {
  const { errors, nav, headerH } = await openLanding(page)

  for (const href of NAV_HREFS) {
    // Scoped to the landmark so the footer's links can never be the one clicked.
    await nav.locator(`a[href="${href}"]`).click()
    // Web-first assertion: retries on its own. '#' is not a regex metacharacter.
    await expect(page).toHaveURL(new RegExp(`${href}$`))
    // poll, not `expect(await ...)`: boundingBox is a single-shot read, and AC #5
    // forbids measuring in the same tick as the click.
    await expect
      .poll(async () => (await page.locator(href).boundingBox())!.y, {
        message: `${href} is occluded by the ${headerH}px header after clicking its link`,
      })
      .toBeGreaterThanOrEqual(headerH - PX)
  }

  expectNoConsoleErrors(errors)
})

// E3 — the section's heading, not just its box, clears the header band. Passes today
// only because every section happens to carry top padding; this catches a future
// padding change that would re-occlude the heading.
test('landing nav: the jumped-to section heading sits below the header band', async ({ page }) => {
  const { errors, nav, headerH } = await openLanding(page)

  for (const href of NAV_HREFS) {
    await nav.locator(`a[href="${href}"]`).click()
    // Each of the six sections has exactly one <h2>; .first() is belt-and-braces.
    await expect
      .poll(async () => (await page.locator(`${href} h2`).first().boundingBox())!.y, {
        message: `the heading of ${href} is occluded by the ${headerH}px header`,
      })
      .toBeGreaterThanOrEqual(headerH - PX)
  }

  expectNoConsoleErrors(errors)
})

// E4 — the header stays pinned while the page scrolls. FAILED against the pre-fix
// build: measured bounding-box top -2000, i.e. the header rode the page off-screen.
test('landing nav: the header stays pinned once the page is scrolled', async ({ page }) => {
  const { errors, header } = await openLanding(page)

  await page.evaluate((y) => window.scrollTo(0, y), DEEP_SCROLL_PX)

  // MANDATORY precondition, not decoration: if the document were ever short enough
  // for this scroll to clamp, scrollY would stay near 0 and the header's top would be
  // trivially 0 — so the assertion below would pass against a broken build.
  await expect
    .poll(() => page.evaluate(() => window.scrollY), {
      message: `the scroll clamped, so this test cannot prove the header is pinned`,
    })
    .toBe(DEEP_SCROLL_PX)

  await expect
    .poll(async () => Math.abs((await header.boundingBox())!.y), {
      message: `the header scrolled away instead of staying pinned to the viewport top`,
    })
    .toBeLessThanOrEqual(PX)

  expectNoConsoleErrors(errors)
})

// E5 — the brand lockup returns to the very top. Passes today; pinned as a regression
// guard because scroll-padding-top could plausibly have left it short of 0 (it does
// not: #top begins at document y = headerH, so the padded target resolves to 0).
test('landing nav: the brand lockup returns to the top of the page', async ({ page }) => {
  const { errors, header } = await openLanding(page)

  await page.evaluate((y) => window.scrollTo(0, y), DEEP_SCROLL_PX)
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(DEEP_SCROLL_PX)

  // The page's only href="#top" is the lockup, and it lives inside the banner.
  await header.locator('a[href="#top"]').click()

  await expect(page).toHaveURL(/#top$/)
  await expect
    .poll(() => page.evaluate(() => window.scrollY), {
      message: `the brand lockup did not return the page to the very top`,
    })
    .toBeLessThanOrEqual(PX)

  expectNoConsoleErrors(errors)
})

// E6 — the nav is a NAMED landmark, so assistive tech can jump to it by name.
// FAILED against the pre-fix build: the <nav> carried no aria-label at all.
test('landing nav: the primary navigation is a named landmark', async ({ page }) => {
  const { errors, nav } = await openLanding(page)

  // getByRole matches on accessible name, so this count is meaningful: 0 means the
  // label is missing or renamed, 2 means a second landmark took the same name.
  await expect(nav).toHaveCount(1)
  // Doubles as the desktop-viewport guard — the nav is display:none under 600px.
  await expect(nav).toBeVisible()

  expectNoConsoleErrors(errors)
})

// E7 — the scroll-spy lights exactly the right link. FAILED against the pre-fix
// build: no link carried aria-current at any scroll offset.
test('landing nav: scrolling into a section marks exactly that link current', async ({ page }) => {
  const { errors, nav, headerH } = await openLanding(page)

  for (const href of NAV_HREFS) {
    // Parked with margin rather than reusing E2's anchor jump, which lands the section
    // top within ~1px of the spy's threshold. This also decouples E7 from scroll
    // clearance, so a clearance regression fails E2 alone instead of cascading here.
    await scrollSectionUnderHeader(page, href)

    // Precondition: prove the section really did come to rest under the threshold.
    // Guards a clamp on the last section, which would otherwise fail the identity
    // assertion below for an entirely unrelated reason.
    await expect
      .poll(
        () => page.evaluate((sel) => document.querySelector(sel)!.getBoundingClientRect().top, href),
        { message: `${href} did not scroll under the header — the scroll clamped` },
      )
      .toBeLessThanOrEqual(headerH)

    // Exactly one, never "at most one": the indicator must be unambiguous. These are
    // web-first assertions, so the spy's one-rAF delay is invisible to them.
    await expect(nav.locator('[aria-current]')).toHaveCount(1)
    await expect(nav.locator(`a[href="${href}"]`)).toHaveAttribute('aria-current', 'true')
  }

  expectNoConsoleErrors(errors)
})

// E8 — the indicator CLEARS outside the six nav sections rather than sticking on the
// first or last link. Vacuous against the pre-fix build, where nothing was ever
// marked current; meaningful now that E7 proves the indicator does appear.
test('landing nav: no link is marked current outside the six sections', async ({ page }) => {
  const { errors, nav, headerH } = await openLanding(page)

  // A count of 0 is trivially true on a page that never rendered, so prove the nav
  // is really there first. Without this, E8 would survive the nav disappearing.
  await expect(nav.getByRole('link')).toHaveCount(NAV_HREFS.length)

  // At the top of the page the last-crossed section is the hero, which has no link.
  await expect(nav.locator('[aria-current]')).toHaveCount(0)

  // The two scrolled positions below each carry a scrollY precondition, for the same
  // reason E4 does: if the document were ever short enough that these scrolls did not
  // move, the page would still be at the top, the hero would still be the last section
  // crossed, and the count would still be 0 — passing while testing nothing. The bar is
  // deliberately loose (both positions sit thousands of pixels down) so it can only
  // ever fire on a genuinely collapsed document, never on sub-pixel drift.

  // In the demo CTA — a section[id] that is deliberately not a nav target.
  await scrollSectionUnderHeader(page, '#demo')
  await expect
    .poll(() => page.evaluate(() => window.scrollY), {
      message: `the page did not scroll to the demo CTA, so this proves nothing`,
    })
    .toBeGreaterThan(headerH)
  await expect(nav.locator('[aria-current]')).toHaveCount(0)

  // And at the very bottom: the footer is a <footer>, not a section[id], so the demo
  // CTA is still the last section crossed.
  await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight))
  await expect
    .poll(() => page.evaluate(() => window.scrollY), {
      message: `the page did not scroll to the bottom, so this proves nothing`,
    })
    .toBeGreaterThan(headerH)
  await expect(nav.locator('[aria-current]')).toHaveCount(0)

  expectNoConsoleErrors(errors)
})
