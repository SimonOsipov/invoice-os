import { expect, test, type Locator, type Page, type TestInfo } from '@playwright/test'
import { resolveTarget } from '../targets'
import { overlapOf, rectsOverlap, WIDE_WIDTHS, type Rect } from '../topology/layout'
import { seedConsent } from './landingConsent'

// The cookie consent notice on the deployed landing: mount predicate, layout
// relationships, focus containment, and the reopen round trip.
//
// It does NOT prove the tag half of Core ACs 1 and 2. isProductionHost
// (frontend/landing/src/hubspot.ts) blocks gtag.js on every preview host regardless of
// consent, so "the tag never loaded" is true before AND after Accept. That half is an
// operator spot-check on www.ascomply.com, not an assertion here.
//
// Relationship assertions only, never a raw dimension bound — see topology/layout.ts.
// No describe.configure: the smoke config is fullyParallel, every test gets its own
// context, and consent lives in per-origin localStorage, so no test can reach another's.

const LANDING_URL = resolveTarget('LANDING_URL')
const PRIVACY_URL = `${LANDING_URL}/privacy`

// Retyped from frontend/landing/src/consent.ts: e2e pins what the deployed build serves.
const CONSENT_KEY = 'asc_consent'

const PHONE = { width: 390, height: 844 }
const MOBILE_THIRD_PX = PHONE.height / 3
const MOBILE_INSET_PX = 12 // .cookie-note's bottom inset below the 640px breakpoint
const MIN_PHONE_CARD_PX = 300 // floor: the third-of-viewport cap must not pass on an unrendered card
const BOX_SLACK_PX = 0.5 // sub-pixel rounding only
const TAB_PRESSES = 30
const NARROW_WIDTHS = [390, 375] as const
// copyright span, link group, Privacy policy and Cookie choices: the four descendants the
// copyright row is required to contain (Footer.tsx bottom row), so the floor is counted.
const MIN_COPYRIGHT_ROW_NODES = 4

// WIDE_WIDTHS at 1080 plus 1280x720 — the viewport this suite actually runs at. A
// clearance claim that only holds at 1080 is a claim about a viewport no test uses.
const CTA_STATES = [...WIDE_WIDTHS.map((width) => ({ width, height: 1080 })), { width: 1280, height: 720 }]

// The closing CTA scrolls past a fixed card, so clearance is a claim about EVERY offset
// in the band, not the one `scrollIntoViewIfNeeded` happens to pick. 50px is fine enough
// that no target can cross the card between two stops — the shortest is ~17px tall.
const CTA_SWEEP_STEP_PX = 50
// The band is the CTA's height plus a viewport, ~1400px at 1280x720; anything near this
// floor means the band collapsed and the sweep proved nothing.
const MIN_SWEEP_STOPS = 10
// Connect's three buttons, Privacy policy and Cookie choices (Footer.tsx). A floor, not the
// count: the claim is that the query reached the footer at all.
const MIN_FOOTER_CONTROLS = 5

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
  expect(errors, `console errors with the cookie notice on the page:\n${errors.join('\n')}`).toEqual([])
}

/** Two rAFs — layout after a resize is committed by the second one. */
function settleLayout(page: Page): Promise<boolean> {
  return page.evaluate(
    () => new Promise<boolean>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(true)))),
  )
}

/**
 * Scroll to the document end and wait for it to LAND.
 *
 * `html { scroll-behavior: smooth }` (landing.css:24) animates window.scrollTo over ~1.5s
 * and Playwright sets no reduced-motion preference, so two rAFs read the page at scrollY 1
 * of 14369 — every rect taken there is mid-flight, and a `scrollY > 0` control passes on a
 * page that has barely moved. Poll to the maximum offset instead: that is the settle AND
 * the non-vacuity control.
 */
async function scrollToDocumentEnd(page: Page): Promise<void> {
  await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
  await expect
    .poll(
      () =>
        page.evaluate(() => {
          const el = document.documentElement
          return Math.round(el.scrollHeight - el.clientHeight - window.scrollY)
        }),
      { message: 'the page never reached the document end (scroll-behavior: smooth animates window.scrollTo)' },
    )
    .toBeLessThanOrEqual(1)
  await settleLayout(page)
}

/** Back to the top, polled for the same reason scrollToDocumentEnd polls. */
async function scrollToTop(page: Page): Promise<void> {
  await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'instant' }))
  await expect
    .poll(() => page.evaluate(() => Math.round(window.scrollY)), {
      message: 'the page never returned to the top (scroll-behavior: smooth animates window.scrollTo)',
    })
    .toBeLessThanOrEqual(1)
  await settleLayout(page)
}

/** Do two rects share a text line? The y half of rectsOverlap, on its own. */
function sharesLine(a: Rect, b: Rect): boolean {
  return Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y) > 0
}

/** Does `outer` enclose `inner`, sub-pixel rounding aside? */
function enclosesRect(outer: Rect, inner: Rect): boolean {
  return (
    inner.x >= outer.x - BOX_SLACK_PX &&
    inner.y >= outer.y - BOX_SLACK_PX &&
    inner.x + inner.width <= outer.x + outer.width + BOX_SLACK_PX &&
    inner.y + inner.height <= outer.y + outer.height + BOX_SLACK_PX
  )
}

function notice(page: Page): Locator {
  return page.getByRole('region', { name: 'Cookie notice' })
}

/** boundingBox(), with a null failing loudly and naming where it was measured. */
async function rectOf(locator: Locator, label: string, at: string): Promise<Rect> {
  const box = await locator.boundingBox()
  expect(box, `${label} did not render ${at}`).toBeTruthy()
  return box!
}

const TOUCH_TARGET_PX = 44 // landing.css phone `.cn-actions button { height: 44px }`

/** Accept and Reject: tall enough, inside the card, one row with Accept on the left. */
async function expectTouchRow(card: Locator, cardRect: Rect, label: string, at: string) {
  const accept = await rectOf(card.locator('[data-consent="accept"]'), `${label} Accept`, at)
  const reject = await rectOf(card.locator('[data-consent="reject"]'), `${label} Reject`, at)
  for (const [name, b] of [['Accept', accept], ['Reject', reject]] as const) {
    expect(b.height, `${label} ${name} is ${b.height}px tall ${at}`).toBeGreaterThanOrEqual(TOUCH_TARGET_PX - BOX_SLACK_PX)
    expect(enclosesRect(cardRect, b), `${label} ${name} leaves the card ${at}`).toBe(true)
  }
  expect(Math.abs(accept.y - reject.y), `${label} buttons are stacked ${at} (Accept y ${accept.y}, Reject y ${reject.y})`).toBeLessThanOrEqual(BOX_SLACK_PX)
  expect(accept.x + accept.width, `${label} Accept is not left of Reject ${at}`).toBeLessThanOrEqual(reject.x + BOX_SLACK_PX)
  return { accept, reject }
}

/**
 * Is Manrope actually available for layout?
 *
 * The computed font-family always reads "Manrope, ui-sans-serif, ..." whether or not the
 * Google-hosted webfont arrived, and Chromium's document.fonts.check() answers true for a
 * family it has never heard of. Two probe spans, `Manrope, monospace` against bare
 * `monospace`, differ only when Manrope is real. A missing Manrope reads as a layout bug
 * unless this names it.
 */
function manropeIsUsable(page: Page): Promise<{ usable: boolean; withManrope: number; fallback: number }> {
  return page.evaluate(() => {
    const measure = (family: string): number => {
      const s = document.createElement('span')
      s.textContent = 'We use Google Analytics to see how people find and use this page.'
      s.style.cssText = `position:absolute;left:-9999px;top:-9999px;white-space:pre;font:400 14px ${family}`
      document.body.appendChild(s)
      const w = s.getBoundingClientRect().width
      s.remove()
      return w
    }
    const withManrope = measure('Manrope, monospace')
    const fallback = measure('monospace')
    return { usable: Math.abs(withManrope - fallback) > 1, withManrope, fallback }
  })
}

type OpenOptions = { url?: string; expectNotice?: boolean; privacy?: boolean }

/**
 * goto + settle. The non-vacuity floor lives HERE, not in a standalone control test:
 * fullyParallel means every test gets its own page, so a separate test would prove
 * nothing about another test's page.
 */
async function openLanding(page: Page, options: OpenOptions = {}) {
  const { url = LANDING_URL, expectNotice = true, privacy = false } = options
  const errors = consoleGate(page)

  const response = await page.goto(url)
  expect(response, `no response from ${url}`).toBeTruthy()
  expect(response!.ok(), `${url} returned HTTP ${response!.status()}`).toBeTruthy()

  const card = notice(page)
  if (expectNotice) await expect(card).toBeVisible()
  else await expect(card).toHaveCount(0)

  if (privacy) await expect(page.getByTestId('privacy-container')).toBeVisible()
  else await expect(page.locator('[data-closing]')).toHaveCount(1)

  // Google-hosted fonts settle once, before any measurement (landing-nav.spec.ts's reason).
  await page.evaluate(() => document.fonts.ready.then(() => true))
  await settleLayout(page)

  return { errors, card }
}

function storedConsent(page: Page): Promise<string | null> {
  return page.evaluate((key) => window.localStorage.getItem(key), CONSENT_KEY)
}

// C1 — the mount predicate, four directions. The notice sits outside App.tsx's
// privacy branch, so /privacy carries it too.
test('landing consent: the notice mounts on both routes with no stored answer, and on neither with one', async ({
  page,
}) => {
  const { errors, card } = await openLanding(page)
  await expect(card).toHaveCount(1)

  await openLanding(page, { url: PRIVACY_URL, privacy: true })
  await expect(notice(page)).toHaveCount(1)

  // Init scripts run in registration order, so the later seed is the record that lands;
  // the readback below is what proves which one did.
  await seedConsent(page, true)
  await page.reload()
  await expect(notice(page)).toHaveCount(0)

  await seedConsent(page, false)
  await page.reload()
  await expect(notice(page)).toHaveCount(0)
  const record = await storedConsent(page)
  expect(record, 'the second seed never landed, so the count-0 assertion above proved nothing').toContain(
    '"analytics":false',
  )

  expectNoConsoleErrors(errors)
})

// C2 — the notice must not cover the closing CTA's copy, at ANY scroll offset in the
// band, not the one `scrollIntoViewIfNeeded` happens to land on. That single sample is
// why this read green at 1080 and red at 720 on the same build.
//
// Right-anchored the card opens at x = W-484 while the CTA's copy column ends at most at
// container left + gutter + clamp(32px, 6vw, 72px) + 520px (656px at W=1280); across
// CTA_STATES (W >= 1280) the two x bands cannot intersect and no scroll offset can produce an overlap. The sweep is
// what turns that from an argument into an assertion — and the y-band arm below is what
// stops it passing on a page where the card simply never reaches the copy.
test('landing consent: the notice never covers the closing CTA copy at any scroll offset', async ({
  page,
}, testInfo) => {
  test.setTimeout(180_000)
  const { errors, card } = await openLanding(page)

  const closing = page.locator('[data-closing]')
  const targets: Array<{ label: string; locator: Locator }> = [
    { label: 'the closing eyebrow', locator: closing.getByText("LET'S MAKE COMPLIANCE CLEARER.", { exact: true }) },
    { label: "the CTA's h2", locator: closing.getByRole('heading', { level: 2 }) },
    {
      label: "the CTA's supporting paragraph",
      locator: closing.getByText('See how ASComply fits your invoices, your systems and your team.'),
    },
  ]
  for (const t of targets) await expect(t.locator, `${t.label} is not unique`).toHaveCount(1)

  type Stop = { state: string; scrollY: number; landedAt: number; notice: Rect; target: Rect; label: string }
  const sweep: Stop[] = []
  const perState: Array<{ state: string; stops: number; band: { from: number; to: number } }> = []
  const entry = page.viewportSize()

  try {
    for (const state of CTA_STATES) {
      const label = `${state.width}x${state.height}`
      await page.setViewportSize(state)
      await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(state.width)
      await settleLayout(page)

      // Tag from Playwright so the in-page sweep reads the SAME elements the locators
      // resolve to; a selector retyped in the browser is a second source of truth.
      await card.evaluate((el) => el.setAttribute('data-sweep', 'notice'))
      for (const [i, t] of targets.entries()) {
        await t.locator.evaluate((el, index) => el.setAttribute('data-sweep', `t${index}`), i)
      }

      // One round trip per state: scrolling and measuring from the page keeps a ~30-stop
      // sweep at four widths inside the timeout. `behavior: 'instant'` overrides
      // `html { scroll-behavior: smooth }`; landedAt is what proves it did.
      const result = await page.evaluate(
        ({ step, count }) => {
          const boxOf = (el: Element) => {
            const r = el.getBoundingClientRect()
            return { x: r.x, y: r.y, width: r.width, height: r.height }
          }
          const notice = document.querySelector('[data-sweep="notice"]')
          if (!notice) return { error: 'the cookie notice carries no sweep tag' }
          const els: Element[] = []
          for (let i = 0; i < count; i++) {
            const el = document.querySelector(`[data-sweep="t${i}"]`)
            if (!el) return { error: `target ${i} carries no sweep tag` }
            els.push(el)
          }

          const doc = document.documentElement
          const demoEl = document.querySelector('[data-closing]')
          if (!demoEl) return { error: 'the closing CTA is not on the page' }
          const maxScroll = Math.max(0, doc.scrollHeight - doc.clientHeight)
          const box = demoEl.getBoundingClientRect()
          const demoTop = box.top + window.scrollY
          const from = Math.max(0, Math.min(maxScroll, Math.floor(demoTop - window.innerHeight)))
          const to = Math.max(from, Math.min(maxScroll, Math.ceil(demoTop + box.height)))

          const offsets: number[] = []
          for (let y = from; y < to; y += step) offsets.push(y)
          offsets.push(to)

          const samples = offsets.map((y) => {
            window.scrollTo({ top: y, behavior: 'instant' })
            return { scrollY: y, landedAt: window.scrollY, notice: boxOf(notice), targets: els.map(boxOf) }
          })
          return { band: { from, to }, samples }
        },
        { step: CTA_SWEEP_STEP_PX, count: targets.length },
      )

      await page.evaluate(() =>
        document.querySelectorAll('[data-sweep]').forEach((el) => el.removeAttribute('data-sweep')),
      )

      expect(result.error, `the sweep could not run at ${label}: ${result.error}`).toBeUndefined()
      const { band, samples } = result as { band: { from: number; to: number }; samples: Array<{ scrollY: number; landedAt: number; notice: Rect; targets: Rect[] }> }
      perState.push({ state: label, stops: samples.length, band })
      for (const sample of samples) {
        for (const [i, t] of targets.entries()) {
          sweep.push({
            state: label,
            scrollY: sample.scrollY,
            landedAt: sample.landedAt,
            notice: sample.notice,
            target: sample.targets[i],
            label: t.label,
          })
        }
      }
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }

  // Attach before asserting, so a red run still carries the numbers.
  await testInfo.attach('cookie-notice-cta-clearance.json', {
    body: JSON.stringify({ perState, sweep }, null, 2),
    contentType: 'application/json',
  })
  testInfo.annotations.push({
    type: 'measurement',
    description: perState
      .map((p) => {
        const worst = sweep
          .filter((m) => m.state === p.state)
          .reduce((acc, m) => {
            const o = overlapOf(m.notice, m.target)
            return o.width * o.height > acc.width * acc.height ? o : acc
          }, { x: 0, y: 0, width: 0, height: 0 })
        return `${p.state}: ${p.stops} stops over ${p.band.from}-${p.band.to}, worst overlap ${Math.round(worst.width)}x${Math.round(worst.height)}`
      })
      .join(' | '),
  })

  // Non-vacuity, four ways: the sweep ran, it visited every state, each band is a real
  // band, and every offset it claims to have measured is the offset it actually reached.
  expect(sweep.length, 'the clearance sweep collected nothing').toBeGreaterThan(0)
  expect(
    perState.map((p) => p.state),
    'the clearance sweep did not visit every state',
  ).toEqual(CTA_STATES.map((s) => `${s.width}x${s.height}`))
  for (const p of perState) {
    expect(p.stops, `the ${p.state} band collapsed to ${p.stops} stops (${p.band.from}-${p.band.to})`).toBeGreaterThanOrEqual(MIN_SWEEP_STOPS)
  }
  for (const m of sweep) {
    expect(
      Math.abs(m.landedAt - m.scrollY),
      `the page never reached scrollY ${m.scrollY} at ${m.state} (landed at ${m.landedAt}) — scroll-behavior: smooth beat the instant hint`,
    ).toBeLessThanOrEqual(1)
    expect(m.target.width * m.target.height, `${m.label} measured an empty box at ${m.state}`).toBeGreaterThan(0)
    expect(m.notice.width * m.notice.height, `the cookie notice measured an empty box at ${m.state}`).toBeGreaterThan(0)
  }

  // The claim that makes the sweep mean something: at some offset the card and the copy
  // DO share a y band, so the only thing keeping them apart is x. Without this arm a
  // page that never scrolls the CTA under the card passes every assertion below.
  for (const state of CTA_STATES) {
    const label = `${state.width}x${state.height}`
    for (const t of targets) {
      const shared = sweep.filter((m) => m.state === label && m.label === t.label && sharesLine(m.notice, m.target))
      expect(
        shared.length,
        `${t.label} never shared a y band with the notice at ${label}, so its clearance is untested`,
      ).toBeGreaterThan(0)
    }
  }

  for (const m of sweep) {
    const o = overlapOf(m.notice, m.target)
    expect(
      rectsOverlap(m.notice, m.target),
      `the cookie notice covers ${m.label} at ${m.state}, scrollY ${m.scrollY} (overlap ${o.width}px wide by ${o.height}px tall)`,
    ).toBe(false)
  }
  expectNoConsoleErrors(errors)
})

// C3 — Core AC 5, as amended on 2026-08-18. The card is right-anchored, so the footer's
// link column now sits inside its x band and only a real reservation keeps those links
// reachable; the desktop spacer is therefore no longer zero. What survives unchanged is
// the claim that actually mattered: the card moves nothing that is laid out above it.
// The reservation is asserted as a relationship — the spacer accounts for exactly the
// document growth, and the footer's bottom edge clears the card's top edge — never as a
// second copy of the CSS literal, which would pass on the bug it exists to catch.
test('landing consent: the notice moves nothing above it and reserves the band it covers', async ({
  page,
}, testInfo) => {
  const { errors, card } = await openLanding(page)
  await expect(card).toHaveCount(1)

  const header = page.getByRole('banner')
  const demo = page.locator('[data-closing]')
  const footer = page.getByRole('contentinfo')

  const spacerRect = await rectOf(page.locator('.cn-spacer'), 'the desktop spacer', 'with the notice up')
  const before = {
    header: await rectOf(header, 'the sticky header', 'with the notice up'),
    demo: await rectOf(demo, 'the closing CTA', 'with the notice up'),
    scrollHeight: await page.evaluate(() => document.documentElement.scrollHeight),
  }

  // The reservation has to hold at the one offset where the footer and the card are
  // closest, which is the document end — anywhere above it the footer is still rising.
  await scrollToDocumentEnd(page)
  const footerRect = await rectOf(footer, 'the footer', 'at the document end')
  const noticeRect = await rectOf(card, 'the cookie notice', 'at the document end')

  await seedConsent(page, true)
  await page.reload()
  await expect(notice(page)).toHaveCount(0)
  await page.evaluate(() => document.fonts.ready.then(() => true))
  await scrollToTop(page)

  const after = {
    header: await rectOf(header, 'the sticky header', 'with the notice down'),
    demo: await rectOf(demo, 'the closing CTA', 'with the notice down'),
    scrollHeight: await page.evaluate(() => document.documentElement.scrollHeight),
  }

  const clearance = noticeRect.y - (footerRect.y + footerRect.height)
  await testInfo.attach('cookie-notice-spacer-desktop.json', {
    body: JSON.stringify({ spacerRect, footerRect, noticeRect, clearance, before, after }, null, 2),
    contentType: 'application/json',
  })
  testInfo.annotations.push({
    type: 'measurement',
    description: `spacer ${spacerRect.height}px, document grew ${before.scrollHeight - after.scrollHeight}px, footer clears the card by ${Math.round(clearance)}px`,
  })

  for (const field of ['x', 'y', 'width', 'height'] as const) {
    expect(
      Math.abs(after.header[field] - before.header[field]),
      `the sticky header's ${field} moved when the notice mounted (${before.header[field]} -> ${after.header[field]})`,
    ).toBeLessThanOrEqual(BOX_SLACK_PX)
    expect(
      Math.abs(after.demo[field] - before.demo[field]),
      `the closing CTA's ${field} moved when the notice mounted (${before.demo[field]} -> ${after.demo[field]})`,
    ).toBeLessThanOrEqual(BOX_SLACK_PX)
  }

  // Non-vacuity: a zero-height spacer would satisfy the growth identity trivially.
  expect(spacerRect.height, 'the desktop spacer reserves nothing').toBeGreaterThan(0)
  expect(
    before.scrollHeight - after.scrollHeight,
    `the notice changed the document by ${before.scrollHeight - after.scrollHeight}px but its spacer is ${spacerRect.height}px — something other than the spacer moved`,
  ).toBe(Math.round(spacerRect.height))
  expect(
    footerRect.y + footerRect.height,
    `the footer's bottom edge (${footerRect.y + footerRect.height}) is under the notice's top edge (${noticeRect.y}) — the spacer does not reserve the band the card covers`,
  ).toBeLessThanOrEqual(noticeRect.y + BOX_SLACK_PX)

  expectNoConsoleErrors(errors)
})

// C4 — out of flow, beneath the header, and covering nothing that is clicked.
//
// This is the regression that must not come back. Right-anchored the card's x band holds
// the footer's whole right-hand link column: measured on the deployed build before the
// spacer existed, the privacy link and Cookie choices were all
// covered AND unclickable at 1280, 1440 and 1920 — elementFromPoint returned the card.
// So the oracle is elementFromPoint on EVERY footer control at every width, not a rect
// check on one link: an overlap test alone cannot tell a covered control from a clear one
// when the two only ever meet on one axis.
test('landing consent: the notice is fixed under the header and leaves every footer control clickable', async ({
  page,
}, testInfo) => {
  const { errors, card } = await openLanding(page)

  const style = await card.evaluate((el) => {
    const cs = getComputedStyle(el)
    return { position: cs.position, zIndex: cs.zIndex }
  })
  expect(style.position, 'the notice is no longer out of the flow').toBe('fixed')
  expect(style.zIndex, 'the notice left its stacking band').toBe('40')

  const hits = await page.evaluate(() => {
    const el = document.querySelector('[aria-label="Cookie notice"]')!
    const header = document.querySelector('header')!
    const at = (r: DOMRect) => document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)
    const onNotice = at(el.getBoundingClientRect())
    const onHeader = at(header.getBoundingClientRect())
    return {
      noticeCentreIsNotice: !!onNotice?.closest('[aria-label="Cookie notice"]'),
      headerCentreIsHeader: !!onHeader?.closest('header'),
      headerCentreIsNotice: !!onHeader?.closest('[aria-label="Cookie notice"]'),
    }
  })
  // Control: the notice's own centre must hit the notice, else the header read below
  // proves nothing about stacking.
  expect(hits.noticeCentreIsNotice, "the notice does not receive a click at its own centre").toBe(true)
  expect(hits.headerCentreIsNotice, "the notice covers the sticky header's centre").toBe(false)
  expect(hits.headerCentreIsHeader, 'the header no longer receives a click at its own centre').toBe(true)

  type ControlProbe = {
    label: string
    rect: Rect
    inViewport: boolean
    hitIsControl: boolean
    hitIsNotice: boolean
  }
  const probes: Array<{ state: string; notice: Rect; controls: ControlProbe[] }> = []
  const entry = page.viewportSize()

  try {
    for (const state of CTA_STATES) {
      await page.setViewportSize(state)
      await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(state.width)
      // The document end is where the footer and the card are closest; every offset
      // above it holds the footer further clear.
      await scrollToDocumentEnd(page)

      const read = await page.evaluate(() => {
        const boxOf = (el: Element): Rect => {
          const r = el.getBoundingClientRect()
          return { x: r.x, y: r.y, width: r.width, height: r.height }
        }
        const noticeEl = document.querySelector('[aria-label="Cookie notice"]')
        const controls = [...document.querySelectorAll('footer a, footer button')]
        return {
          notice: noticeEl ? boxOf(noticeEl) : null,
          controls: controls.map((el) => {
            const r = el.getBoundingClientRect()
            const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)
            return {
              label: (el.textContent || '').trim().slice(0, 40) || el.tagName,
              rect: boxOf(el),
              inViewport:
                r.y >= 0 && r.y + r.height <= window.innerHeight && r.x >= 0 && r.x + r.width <= window.innerWidth,
              hitIsControl: hit === el || el.contains(hit),
              hitIsNotice: !!hit?.closest('[aria-label="Cookie notice"]'),
            }
          }),
        }
      })
      expect(read.notice, `the cookie notice did not render at ${state.width}x${state.height}`).toBeTruthy()
      probes.push({ state: `${state.width}x${state.height}`, notice: read.notice!, controls: read.controls })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }

  await testInfo.attach('cookie-notice-footer-clearance.json', {
    body: JSON.stringify(probes, null, 2),
    contentType: 'application/json',
  })
  testInfo.annotations.push({
    type: 'measurement',
    description: probes
      .map((p) => {
        const covered = p.controls.filter((c) => rectsOverlap(p.notice, c.rect)).length
        const blocked = p.controls.filter((c) => c.hitIsNotice).length
        return `${p.state}: ${p.controls.length} controls, ${covered} covered, ${blocked} blocked`
      })
      .join(' | '),
  })

  expect(probes.map((p) => p.state), 'the footer sweep did not visit every state').toEqual(
    CTA_STATES.map((s) => `${s.width}x${s.height}`),
  )
  for (const probe of probes) {
    // Population floor: the footer ships three link columns plus the Cookie choices
    // control, so a handful of hits means the query missed the footer, not that the
    // footer is clear.
    expect(
      probe.controls.length,
      `only ${probe.controls.length} footer controls were found at ${probe.state}`,
    ).toBeGreaterThanOrEqual(MIN_FOOTER_CONTROLS)

    for (const control of probe.controls) {
      // elementFromPoint answers null outside the viewport, which would read as "not
      // blocked" on a control that is simply off screen.
      expect(
        control.inViewport,
        `"${control.label}" is off screen at ${probe.state} (${JSON.stringify(control.rect)}), so the click probe below proves nothing`,
      ).toBe(true)
      const o = overlapOf(probe.notice, control.rect)
      expect(
        rectsOverlap(probe.notice, control.rect),
        `the cookie notice covers "${control.label}" at ${probe.state} (overlap ${o.width}px wide by ${o.height}px tall)`,
      ).toBe(false)
      expect(
        control.hitIsNotice,
        `the cookie notice takes the click on "${control.label}" at ${probe.state}`,
      ).toBe(false)
      expect(
        control.hitIsControl,
        `"${control.label}" does not receive a click at its own centre at ${probe.state}`,
      ).toBe(true)
    }
  }

  expectNoConsoleErrors(errors)
})

// C5 — Accept and Reject occupy one box and carry one weight. One desktop width is
// enough: .cookie-note is a fixed 460px above the 640px breakpoint, so the buttons are
// width-invariant across the desktop range.
test('landing consent: Accept and Reject are the same box at the same weight', async ({ page }) => {
  const { errors, card } = await openLanding(page)
  const accept = card.locator('[data-consent="accept"]')
  const reject = card.locator('[data-consent="reject"]')

  const entry = page.viewportSize()
  try {
    for (const state of [{ width: 1280, height: 720 }, PHONE]) {
      await page.setViewportSize(state)
      await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(state.width)
      await settleLayout(page)

      const at = `at ${state.width}x${state.height}`
      const a = await rectOf(accept, 'Accept', at)
      const r = await rectOf(reject, 'Reject', at)
      expect(a.width, `Accept collapsed ${at}`).toBeGreaterThan(0)
      expect(r.width, `Reject collapsed ${at}`).toBeGreaterThan(0)
      expect(Math.abs(a.width - r.width), `Accept and Reject differ in width ${at} (${a.width} vs ${r.width})`).toBeLessThanOrEqual(BOX_SLACK_PX)
      expect(Math.abs(a.height - r.height), `Accept and Reject differ in height ${at} (${a.height} vs ${r.height})`).toBeLessThanOrEqual(BOX_SLACK_PX)

      const weights = await card.evaluate((el) => ({
        accept: getComputedStyle(el.querySelector('[data-consent="accept"]')!).fontWeight,
        reject: getComputedStyle(el.querySelector('[data-consent="reject"]')!).fontWeight,
      }))
      expect(weights.accept, `Accept and Reject differ in weight ${at}`).toBe(weights.reject)

      // The outline must render: a per-button border-color that loses the cascade to the
      // shared `border` shorthand leaves Reject transparent or Accept-coloured.
      const borders = await card.evaluate((el) => ({
        accept: getComputedStyle(el.querySelector('[data-consent="accept"]')!).borderTopColor,
        reject: getComputedStyle(el.querySelector('[data-consent="reject"]')!).borderTopColor,
      }))
      expect(borders.reject, `Reject's outline is transparent ${at}`).not.toBe('rgba(0, 0, 0, 0)')
      expect(borders.reject, `Reject's outline matches Accept's border ${at} (${borders.reject})`).not.toBe(borders.accept)
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  expectNoConsoleErrors(errors)
})

// C6 — the notice's policy link is underlined. The v2 `a { text-decoration: none }` is
// (0,0,1) and a.lnk sets no decoration, so this is the cascade resolution, not the source.
test('landing consent: the policy link inside the notice is underlined', async ({ page }) => {
  const { errors, card } = await openLanding(page)

  const link = card.locator('a.lnk.cn-link')
  await expect(link, 'the notice does not carry exactly one policy link').toHaveCount(1)
  const decorated = await link.evaluate((el) => {
    const cs = getComputedStyle(el)
    return { line: cs.textDecorationLine, offset: cs.textUnderlineOffset }
  })
  expect(decorated.line, `the notice's policy link resolved to text-decoration-line "${decorated.line}"`).toContain('underline')
  expect(decorated.offset, 'the underline offset moved').toBe('3px')

  // Control needle: the footer's .a-link declares no decoration, so the same instrument
  // must read `none` there. Without it this test passes on a browser that underlines
  // every anchor.
  const plain = page.getByRole('contentinfo').locator('a[href="/privacy"]')
  await expect(plain, 'the footer privacy anchor is not unique').toHaveCount(1)
  const plainLine = await plain.evaluate((el) => getComputedStyle(el).textDecorationLine)
  expect(plainLine, 'the control anchor is underlined too, so the read above says nothing').toBe('none')

  expectNoConsoleErrors(errors)
})

// C7 — nothing dismisses the notice except a choice.
test('landing consent: Escape, an outside click and an in-notice click all leave the notice up', async ({ page }) => {
  const { errors, card } = await openLanding(page)
  await expect(card.locator('button'), 'the notice grew a third control (a close X dismisses without a choice)').toHaveCount(2)

  const attempts: Array<{ label: string; act: () => Promise<void> }> = [
    { label: 'Escape', act: () => page.keyboard.press('Escape') },
    { label: 'a click on the hero heading', act: () => page.getByRole('heading', { level: 1 }).click() },
    { label: "a click on the notice's own body", act: () => card.locator('.cn-body').click() },
  ]
  for (const attempt of attempts) {
    await attempt.act()
    await expect(card, `${attempt.label} dismissed the notice`).toBeVisible()
    expect(await storedConsent(page), `${attempt.label} stored a consent record`).toBeNull()
  }

  // Control: a real Reject does store one, so the nulls above are not vacuous.
  await card.locator('[data-consent="reject"]').click()
  await expect(card).toHaveCount(0)
  expect(await storedConsent(page), 'control: Reject stored nothing, so this instrument cannot see a write').toBeTruthy()

  expectNoConsoleErrors(errors)
})

// C8 — under any modal the notice stays mounted, inert and keyboard-unreachable.
// SignInModal is the load-bearing case: it handles only Escape, on window, so it has no
// Tab trap of its own.
test('landing consent: keyboard focus cannot reach the notice while a modal is open', async ({ page }) => {
  test.setTimeout(90_000) // 3 modals x 30 Tab presses, each press read back over the wire
  const { errors, card } = await openLanding(page)

  const cases = [
    { trigger: 'Platform login', dialog: 'Platform login' },
    { trigger: 'Book a demo', dialog: 'Book a demo' },
    // The header entry shows above 1219px; the default viewport is 1280.
    { trigger: 'Create an account', dialog: 'Create an account' },
  ]

  for (const c of cases) {
    await page.getByRole('banner').getByRole('button', { name: c.trigger }).click()
    await expect(page.getByRole('dialog', { name: c.dialog })).toBeVisible()
    await expect(card, `the notice unmounted under the ${c.dialog} modal, so inertness proves nothing`).toHaveCount(1)
    expect(
      await card.evaluate((el) => el.hasAttribute('inert')),
      `the notice is not inert under the ${c.dialog} modal`,
    ).toBe(true)

    for (let i = 1; i <= TAB_PRESSES; i++) {
      await page.keyboard.press('Tab')
      const inside = await page.evaluate(() => !!document.activeElement?.closest('[aria-label="Cookie notice"]'))
      expect(inside, `focus entered the cookie notice under the ${c.dialog} modal on Tab press ${i}`).toBe(false)
    }

    // The overlay itself closes every modal (its root div owns onClick={onClose}).
    await page.mouse.click(5, 5)
    await expect(page.getByRole('dialog', { name: c.dialog })).toHaveCount(0)
  }

  // Control: with no modal open Tab DOES reach the notice, else every assertion above
  // passes on a notice nothing could ever focus. The footer control is the last focusable
  // before it in DOM order.
  await page.getByRole('contentinfo').getByRole('button', { name: 'Cookie choices' }).focus()
  let reached = false
  for (let i = 0; i < 6 && !reached; i++) {
    await page.keyboard.press('Tab')
    reached = await page.evaluate(() => !!document.activeElement?.closest('[aria-label="Cookie notice"]'))
  }
  expect(reached, 'control: Tab never reached the notice with no modal open').toBe(true)

  expectNoConsoleErrors(errors)
})

// C9 — first visit at 390x844. The standard overflow check is vacuous twice here: the
// notice is position:fixed so it adds nothing to document.scrollWidth, and it sits inside
// the App root's overflow-x: clip. The oracle is the card's OWN box.
test('landing consent: the first-visit card is at most a third of the phone viewport, with 44px buttons in one row, at 390 and 375', async ({ page }, testInfo) => {
  await page.setViewportSize(PHONE)
  const { errors, card } = await openLanding(page)
  await expect(card.locator('.cn-setting'), 'a first visit must not render the current-setting line').toHaveCount(0)

  const manrope = await manropeIsUsable(page)
  expect(
    manrope.usable,
    `Manrope is not available for layout (probe ${manrope.withManrope}px vs fallback ${manrope.fallback}px) — a missing webfont reads as a layout bug`,
  ).toBe(true)

  const records = []
  for (const width of NARROW_WIDTHS) {
    const at = `at ${width}x${PHONE.height}`
    await page.setViewportSize({ width, height: PHONE.height })
    await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
    await settleLayout(page)

    const rect = await rectOf(card, 'the cookie notice', at)
    const { accept, reject } = await expectTouchRow(card, rect, 'first visit', at)
    records.push({ width, card: rect, accept, reject })
    testInfo.annotations.push({
      type: 'measurement',
      description: `first visit ${width}px: ${rect.height}px card against a ${MOBILE_THIRD_PX}px cap, buttons ${accept.height}px / ${reject.height}px`,
    })

    expect(rect.width, `the card measured ${rect.width}px wide ${at}, so the cap below would pass on an unrendered card`).toBeGreaterThan(MIN_PHONE_CARD_PX)
    expect(rect.height, `the first-visit card is ${rect.height}px tall ${at} against a ${MOBILE_THIRD_PX}px cap`).toBeLessThanOrEqual(MOBILE_THIRD_PX)
    expect(rect.x, `the card overhangs the left edge ${at} (x ${rect.x})`).toBeGreaterThanOrEqual(0)
    expect(rect.x + rect.width, `the card overhangs the right edge ${at} (right ${rect.x + rect.width})`).toBeLessThanOrEqual(width)
  }
  await testInfo.attach('cookie-notice-mobile-height.json', {
    body: JSON.stringify({ state: 'first visit', cap: MOBILE_THIRD_PX, records, manrope }, null, 2),
    contentType: 'application/json',
  })

  expectNoConsoleErrors(errors)
})

// C9b — the REOPENED card at 390x844. .cn-setting renders only when `current` is non-null,
// i.e. only on a reopen, so the taller of the two states is measured nowhere else. Core AC
// 8 does not distinguish the two, so it applies here unchanged.
test('landing consent: the reopened card is at most a third of the phone viewport, with 44px buttons in one row, at 390 and 375', async ({ page }, testInfo) => {
  await page.setViewportSize(PHONE)
  await seedConsent(page, true)
  const { errors } = await openLanding(page, { expectNotice: false })

  await page.getByRole('contentinfo').getByRole('button', { name: 'Cookie choices' }).click()
  const card = notice(page)
  await expect(card).toBeVisible()
  await expect(card.locator('.cn-setting'), 'the reopened card did not render its current-setting line, so this is the first-visit card again').toHaveText('Analytics cookies are on.')
  await settleLayout(page)

  const manrope = await manropeIsUsable(page)
  expect(
    manrope.usable,
    `Manrope is not available for layout (probe ${manrope.withManrope}px vs fallback ${manrope.fallback}px) — a missing webfont reads as a layout bug`,
  ).toBe(true)

  const records = []
  for (const width of NARROW_WIDTHS) {
    const at = `at ${width}x${PHONE.height}`
    await page.setViewportSize({ width, height: PHONE.height })
    await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
    await settleLayout(page)

    const rect = await rectOf(card, 'the reopened cookie notice', at)
    const { accept, reject } = await expectTouchRow(card, rect, 'reopened', at)
    const spacer = await rectOf(page.locator('.cn-spacer'), 'the reopened spacer', at)
    records.push({ width, card: rect, accept, reject, spacer })
    testInfo.annotations.push({
      type: 'measurement',
      description: `reopened ${width}px: ${rect.height}px card against a ${MOBILE_THIRD_PX}px cap, buttons ${accept.height}px / ${reject.height}px, spacer ${spacer.height}px`,
    })

    expect(rect.width, `the card measured ${rect.width}px wide ${at}, so the cap below would pass on an unrendered card`).toBeGreaterThan(MIN_PHONE_CARD_PX)
    expect(rect.height, `the reopened card is ${rect.height}px tall ${at} against a ${MOBILE_THIRD_PX}px cap`).toBeLessThanOrEqual(MOBILE_THIRD_PX)
    expect(rect.x, `the reopened card overhangs the left edge ${at} (x ${rect.x})`).toBeGreaterThanOrEqual(0)
    expect(rect.x + rect.width, `the reopened card overhangs the right edge ${at} (right ${rect.x + rect.width})`).toBeLessThanOrEqual(width)
    // Same bounds as C10: the band covers the card plus its inset, and no more than one extra inset.
    expect(spacer.height, `the spacer is ${spacer.height}px ${at} for a ${rect.height + MOBILE_INSET_PX}px band`).toBeGreaterThanOrEqual(rect.height + MOBILE_INSET_PX)
    expect(spacer.height, `the spacer leaves dead scroll ${at} (${spacer.height}px for a ${rect.height + MOBILE_INSET_PX}px band)`).toBeLessThanOrEqual(rect.height + 2 * MOBILE_INSET_PX)
  }
  await testInfo.attach('cookie-notice-mobile-height-reopened.json', {
    body: JSON.stringify({ state: 'reopened', cap: MOBILE_THIRD_PX, records, manrope }, null, 2),
    contentType: 'application/json',
  })

  expectNoConsoleErrors(errors)
})

// C10 — the closing CTA scrolls clear, and the spacer reserves the band the notice covers
// without becoming a second scroll gap of its own.
test('landing consent: the closing CTA scrolls clear of the notice at 390px', async ({ page }, testInfo) => {
  await page.setViewportSize(PHONE)
  const { errors, card } = await openLanding(page)

  await scrollToDocumentEnd(page)
  const firstVisit = await rectOf(card, 'the cookie notice', 'at the document end')
  const spacerRect = await rectOf(page.locator('.cn-spacer'), 'the scroll spacer', 'at the document end')

  // boundingBox() is viewport-relative, and at the document end the spacer has carried the
  // button off the TOP of the viewport — a read there is clear of the notice on any layout.
  // Scroll it in: that is the state its click happens in (C4's reason).
  const button = page.locator('[data-closing]').getByRole('button', { name: 'Book a demo' })
  await expect(button).toHaveCount(1)
  await button.scrollIntoViewIfNeeded()
  await settleLayout(page)
  const buttonRect = await rectOf(button, "the closing CTA's button", 'scrolled into view')
  const noticeRect = await rectOf(card, 'the cookie notice', 'with the closing CTA in view')
  expect(
    buttonRect.y >= 0 && buttonRect.y + buttonRect.height <= PHONE.height,
    `the closing CTA's button is still off-screen (y ${buttonRect.y}, height ${buttonRect.height}, viewport ${PHONE.height})`,
  ).toBe(true)
  expect(
    buttonRect.y + buttonRect.height,
    `the closing CTA's button (bottom ${buttonRect.y + buttonRect.height}) is still under the notice (top ${noticeRect.y})`,
  ).toBeLessThanOrEqual(noticeRect.y)

  // One CSS literal serves BOTH card states, so the band it must reserve is the TALLER
  // one: the reopened card carries an extra .cn-setting line, and a visitor reopens from
  // the footer control — which sits at the document end, where the spacer is the only
  // thing holding the last footer row clear of the notice.
  await card.locator('[data-consent="reject"]').click()
  await expect(card).toHaveCount(0)
  await page.reload()
  await expect(card, 'the notice came back on its own after Reject').toHaveCount(0)
  await page.evaluate(() => document.fonts.ready.then(() => true))
  await page.getByRole('contentinfo').getByRole('button', { name: 'Cookie choices' }).click()
  await expect(card).toBeVisible()
  await expect(card.locator('.cn-setting')).toHaveText('Analytics cookies are off.')
  await scrollToDocumentEnd(page)
  const reopened = await rectOf(card, 'the reopened cookie notice', 'at the document end')

  const reserved = reopened.height + MOBILE_INSET_PX
  await testInfo.attach('cookie-notice-spacer.json', {
    body: JSON.stringify({ firstVisit, reopened, spacer: spacerRect, inset: MOBILE_INSET_PX, reserved }, null, 2),
    contentType: 'application/json',
  })
  testInfo.annotations.push({
    type: 'measurement',
    description: `spacer ${spacerRect.height}px vs a ${reserved}px band (first visit ${firstVisit.height}px, reopened ${reopened.height}px)`,
  })

  expect(
    reopened.height,
    `the reopened card (${reopened.height}px) is not taller than the first-visit card (${firstVisit.height}px), so the band below is derived from the wrong state`,
  ).toBeGreaterThan(firstVisit.height)
  expect(
    spacerRect.height,
    `the spacer reserves ${spacerRect.height}px but the notice covers ${reserved}px`,
  ).toBeGreaterThanOrEqual(reserved)
  // Bounded above too: an over-reserve is as silent as an under-reserve. One further inset
  // band is the ceiling — beyond that it reads as a second gap below the footer.
  expect(
    spacerRect.height,
    `the spacer reserves ${spacerRect.height}px for a ${reserved}px band, leaving dead scroll below the footer`,
  ).toBeLessThanOrEqual(reserved + MOBILE_INSET_PX)

  expectNoConsoleErrors(errors)
})

// C11 — the reopen round trip shows the answer on record, for both answers.
test('landing consent: the footer control reopens the notice with the current setting', async ({ page }) => {
  const { errors, card } = await openLanding(page)
  const reopen = page.getByRole('contentinfo').getByRole('button', { name: 'Cookie choices' })
  await expect(reopen).toHaveCount(1)

  await card.locator('[data-consent="accept"]').click()
  await expect(card).toHaveCount(0)
  await page.reload()
  await expect(notice(page), 'the notice came back on its own after Accept').toHaveCount(0)

  await reopen.click()
  await expect(notice(page).locator('.cn-setting')).toHaveText('Analytics cookies are on.')

  await notice(page).locator('[data-consent="reject"]').click()
  await expect(notice(page)).toHaveCount(0)
  await page.reload()
  await expect(notice(page), 'the notice came back on its own after Reject').toHaveCount(0)

  await reopen.click()
  await expect(notice(page).locator('.cn-setting')).toHaveText('Analytics cookies are off.')

  expectNoConsoleErrors(errors)
})

// C12 — nothing is stored before the visitor answers. This is Core AC 1's STORAGE half
// only; the tag half is unobservable on a preview host (see the file header).
test('landing consent: nothing is written to storage until the visitor answers', async ({ page }) => {
  const { errors, card } = await openLanding(page)

  const before = await page.evaluate(() => Object.keys(window.localStorage))
  expect(before, `the landing wrote to localStorage before the visitor chose: ${before.join(', ')}`).toEqual([])

  await card.locator('[data-consent="reject"]').click()
  await expect(card).toHaveCount(0)
  const after = await page.evaluate(() => Object.keys(window.localStorage))
  expect(after, 'Reject did not write exactly the consent record').toEqual([CONSENT_KEY])

  expectNoConsoleErrors(errors)
})

// C13 — the footer control sits opposite the copyright and the row survives narrow widths.
// No flush-to-the-right-edge assertion is written: the row is space-between with exactly two
// children, so Chromium pins the last one to the content box by definition of the layout, and
// the claim would hold even if the wrapper were deleted. The belonging claim therefore rests
// on the group box and the overflow walk.
test('landing consent: the Cookie choices control sits opposite the copyright in the footer row', async ({ page }, testInfo) => {
  test.setTimeout(90_000)
  const { errors } = await openLanding(page)

  const footer = page.getByRole('contentinfo')
  const control = footer.getByRole('button', { name: 'Cookie choices' })
  const copyright = footer.getByText('© 2026 ASComply Africa Limited · Lagos, Nigeria', { exact: true })
  const group = control.locator('xpath=..')
  const row = copyright.locator('xpath=..')
  // Retyped from Footer.tsx's copyright row column gap ('12px 24px').
  const COPYRIGHT_ROW_GAP_PX = 24
  for (const [label, locator] of [['the Cookie choices control', control], ['the copyright string', copyright]] as const) {
    await expect(locator, `${label} is not unique in the footer`).toHaveCount(1)
  }

  const entry = page.viewportSize()
  const sweep: Array<{
    width: number
    oneLineSlack: number
    rowWrapped: boolean
    groupHoldsControl: boolean
    groupClearsCopyright: boolean
  }> = []

  try {
    for (const width of [...WIDE_WIDTHS, ...NARROW_WIDTHS]) {
      await page.setViewportSize({ width, height: 1080 })
      await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
      await copyright.scrollIntoViewIfNeeded()
      await settleLayout(page)

      const at = `at ${width}px`
      const c = await rectOf(control, 'the Cookie choices control', at)
      const r = await rectOf(copyright, 'the copyright string', at)
      const g = await rectOf(group, "the control's group box", at)
      const w = await rectOf(row, 'the copyright row', at)
      sweep.push({
        width,
        oneLineSlack: w.width - (r.width + COPYRIGHT_ROW_GAP_PX + g.width),
        rowWrapped: !sharesLine(c, r),
        groupHoldsControl: enclosesRect(g, c),
        groupClearsCopyright: !rectsOverlap(g, r),
      })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }

  await testInfo.attach('cookie-choices-proximity.json', {
    body: JSON.stringify(sweep, null, 2),
    contentType: 'application/json',
  })
  testInfo.annotations.push({
    type: 'measurement',
    description: sweep.map((m) => `${m.width}px: one-line slack ${Math.round(m.oneLineSlack)}px${m.rowWrapped ? ' (row wrapped)' : ''}`).join(' | '),
  })

  expect(sweep, 'the proximity sweep did not visit every width').toHaveLength(WIDE_WIDTHS.length + NARROW_WIDTHS.length)

  // (a1) Belonging, at EVERY width including the narrow ones: one box holds the control and
  // the copyright string is outside it. This is the claim that goes red if the control is
  // ever moved next to the copyright.
  for (const m of sweep) {
    expect(m.groupHoldsControl, `at ${m.width}px the Cookie choices control is not inside its own group box`).toBe(true)
    expect(m.groupClearsCopyright, `at ${m.width}px the control's group box overlaps the copyright string`).toBe(true)
  }

  // (a2) The wrap partition, and nothing more. No flush assertion belongs here: space-between
  // pins the last child to the row's right edge by definition of the layout, so such an
  // assertion would test Chromium's flexbox rather than this footer. Dropping the version
  // string shrank the control group, so the row needs less width to hold one line, and the
  // narrow widths now sit much closer to their wrap boundary than they did before. The exact
  // margin is not measured — it is attached per width as `one-line slack` so the real number
  // is readable in the Playwright report. Do not relax the equality to a `>=`.
  const unwrapped = sweep.filter((m) => !m.rowWrapped)
  expect(
    unwrapped.map((m) => m.width),
    'the footer copyright row wraps at a different set of widths than this test was built on',
  ).toEqual([...WIDE_WIDTHS])

  // The two are the same fact from different sources: slack is computed from three measured
  // widths, wrapping is read off the rendered line boxes. If the row ever gains horizontal
  // padding, the border-box width over-reports, the two disagree, and this reds instead of
  // the annotation quietly lying.
  for (const m of sweep) {
    expect(
      m.rowWrapped,
      `at ${m.width}px the row ${m.rowWrapped ? 'wrapped' : 'did not wrap'} but the one-line slack is ${Math.round(m.oneLineSlack)}px`,
    ).toBe(m.oneLineSlack < 0)
  }

  // (b) The row wraps and is space-between with a wrapping right-hand group, so a wrap
  // failure pushes a child past the LEFT edge as readily as the right. Both edges.
  try {
    for (const width of NARROW_WIDTHS) {
      await page.setViewportSize({ width, height: PHONE.height })
      await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
      await copyright.scrollIntoViewIfNeeded()
      await settleLayout(page)

      const walk = await copyright.evaluate((el) => {
        const root = el.parentElement!
        const rootRect = root.getBoundingClientRect()
        const offenders: Array<{ tag: string; edge: string; over: number; text: string }> = []
        const seen: string[] = []
        for (const child of Array.from(root.querySelectorAll('*'))) {
          const rect = child.getBoundingClientRect()
          const text = (child.textContent ?? '').trim().slice(0, 40)
          seen.push(text)
          const over = { left: rootRect.left - rect.left, right: rect.right - rootRect.right }
          for (const edge of ['left', 'right'] as const) {
            if (over[edge] > 1) offenders.push({ tag: child.tagName.toLowerCase(), edge, over: Math.round(over[edge]), text })
          }
        }
        return { scanned: seen.length, seen, offenders }
      })

      expect(walk.scanned, `the copyright-row walk reached ${walk.scanned} nodes at ${width}px`).toBeGreaterThanOrEqual(MIN_COPYRIGHT_ROW_NODES)
      expect(walk.seen, `the walk never reached the Cookie choices control at ${width}px`).toContain('Cookie choices')
      expect(walk.offenders, `the copyright row overflows at ${width}px: ${JSON.stringify(walk.offenders)}`).toEqual([])
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }

  expectNoConsoleErrors(errors)
})

type Gaps = { left: number; right: number; bottom: number }

/** Viewport-relative gaps of the notice, measured against the scrollbar-free client box. */
async function noticeGaps(card: Locator): Promise<Gaps & { width: number }> {
  return card.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const root = document.documentElement
    return { left: r.left, right: root.clientWidth - r.right, bottom: root.clientHeight - r.bottom, width: r.width }
  })
}

// O3 — anchored bottom-right on desktop, a band under 640px. Relationships only: equal
// right and bottom gaps, a wider left gap, and equal side and bottom gaps on a phone.
test('landing consent: O3 the notice is anchored bottom-right on desktop and a band under 640px', async ({
  page,
}, testInfo) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const { errors, card } = await openLanding(page)

  const states = [
    ...WIDE_WIDTHS.map((width) => ({ width, height: 1080, band: false })),
    { width: 600, height: 844, band: true },
    { ...PHONE, band: true },
  ]
  const measured: Array<Gaps & { state: string }> = []
  const entry = page.viewportSize()
  try {
    for (const state of states) {
      const at = `at ${state.width}x${state.height}`
      await page.setViewportSize({ width: state.width, height: state.height })
      await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(state.width)
      await settleLayout(page)

      const g = await noticeGaps(card)
      measured.push({ state: at, left: g.left, right: g.right, bottom: g.bottom })
      expect(g.width, `the notice collapsed ${at}`).toBeGreaterThan(0)
      if (state.band) {
        expect(Math.abs(g.left - g.right), `the band's side gaps differ ${at} (${g.left} vs ${g.right})`).toBeLessThanOrEqual(1)
        expect(Math.abs(g.bottom - g.left), `the band's bottom gap differs from its side gap ${at} (${g.bottom} vs ${g.left})`).toBeLessThanOrEqual(1)
      } else {
        expect(Math.abs(g.right - g.bottom), `right and bottom gaps differ ${at} (${g.right} vs ${g.bottom})`).toBeLessThanOrEqual(1)
        expect(g.left, `the notice is not right-anchored ${at} (left ${g.left} vs right ${g.right})`).toBeGreaterThan(g.right)
      }
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }

  await testInfo.attach('cookie-notice-anchoring.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })
  expect(measured.length, 'O3 measured fewer states than it declares').toBe(states.length)
  expectNoConsoleErrors(errors)
})

const TAB_CAP = 80
const MIN_O4_CONTROLS = 20 // fewer means the walk never crossed the page
const MIN_O4_CONTROLS_PRIVACY = 5 // header and footer links

type FocusRead = { inside: boolean; name: string; rect: Rect; notice: Rect } | null

/** Park focus at the page top so the next Tab starts from the first control, not a stale starting point. */
async function focusPageTop(page: Page): Promise<void> {
  await page.evaluate(() => {
    window.scrollTo(0, 0)
    const anchor = document.createElement('div')
    anchor.tabIndex = -1
    anchor.style.cssText = 'position:fixed;left:0;top:0;width:0;height:0'
    document.body.prepend(anchor)
    anchor.focus({ preventScroll: true })
  })
}

/** Tab until focus enters the notice (or the cap), returning every control focused outside it. */
async function tabToNotice(page: Page): Promise<{ controls: Array<{ name: string; rect: Rect; notice: Rect }>; entered: boolean; presses: number }> {
  const controls: Array<{ name: string; rect: Rect; notice: Rect }> = []
  for (let press = 1; press <= TAB_CAP; press++) {
    await page.keyboard.press('Tab')
    // Wait for the scroll that focus triggers to land: the rects must repeat across frames.
    const read: FocusRead = await page.evaluate(async () => {
      const card = document.querySelector('[aria-label="Cookie notice"]')
      const el = document.activeElement as HTMLElement | null
      if (!card || !el || el === document.body) return null
      const box = (e: Element) => {
        const r = e.getBoundingClientRect()
        return { x: r.x, y: r.y, width: r.width, height: r.height }
      }
      const key = () => JSON.stringify([box(el), box(card)])
      let prev = ''
      for (let i = 0; i < 12 && key() !== prev; i++) {
        prev = key()
        await new Promise<void>((r) => requestAnimationFrame(() => r()))
      }
      const name = `${el.tagName.toLowerCase()} "${(el.getAttribute('aria-label') ?? el.textContent ?? '').trim().slice(0, 40)}"`
      return { inside: card.contains(el), name, rect: box(el), notice: box(card) }
    })
    if (!read) continue
    if (read.inside) return { controls, entered: true, presses: press }
    controls.push({ name: read.name, rect: read.rect, notice: read.notice })
  }
  return { controls, entered: false, presses: TAB_CAP }
}

async function expectTabClearsNotice(page: Page, testInfo: TestInfo, label: string, floor: number) {
  await focusPageTop(page)
  const walk = await tabToNotice(page)
  await testInfo.attach(`cookie-notice-tab-${label}.json`, {
    body: JSON.stringify(walk, null, 2),
    contentType: 'application/json',
  })
  expect(walk.entered, `${label}: Tab never reached the notice in ${TAB_CAP} presses, so the walk did not cross the page`).toBe(true)
  expect(walk.controls.length, `${label}: only ${walk.controls.length} controls measured before the notice`).toBeGreaterThanOrEqual(floor)

  const covered = walk.controls
    .filter((c) => rectsOverlap(c.notice, c.rect))
    .map((c) => {
      const o = overlapOf(c.notice, c.rect)
      return `${c.name} is under the notice by ${Math.round(o.width)}x${Math.round(o.height)}px`
    })
  expect(covered, `${label}: focus landed under the notice:\n${covered.join('\n')}`).toEqual([])
}

// O4 — Tab never lands focus under the notice. The browser scrolls a focused control to
// the viewport edge, which is under a fixed notice unless scroll-padding reserves the band.
const O4_CASES = [
  { label: '1440x900 first visit', viewport: { width: 1440, height: 900 }, reopen: false },
  { label: '390x844 first visit', viewport: PHONE, reopen: false },
  { label: '390x844 reopened', viewport: PHONE, reopen: true },
  { label: '1280x720 first visit', viewport: { width: 1280, height: 720 }, reopen: false },
]
for (const c of O4_CASES) {
  test(`landing consent: O4 Tab never lands focus under the notice (${c.label})`, async ({ page }, testInfo) => {
    test.setTimeout(120_000)
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.setViewportSize(c.viewport)
    const { errors, card } = await openLanding(page)

    if (c.reopen) {
      await card.locator('[data-consent="accept"]').click()
      await expect(card).toHaveCount(0)
      await page.getByRole('contentinfo').getByRole('button', { name: 'Cookie choices' }).click()
      await expect(card.locator('.cn-setting'), 'the notice did not reopen').toHaveText('Analytics cookies are on.')
      await scrollToTop(page)
    }

    await expectTabClearsNotice(page, testInfo, c.label, MIN_O4_CONTROLS)
    expectNoConsoleErrors(errors)
  })
}

test('landing consent: O4 Tab never lands focus under the notice (/privacy 390x844)', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.setViewportSize(PHONE)
  const { errors } = await openLanding(page, { url: PRIVACY_URL, privacy: true })

  await expectTabClearsNotice(page, testInfo, 'privacy 390x844', MIN_O4_CONTROLS_PRIVACY)
  expectNoConsoleErrors(errors)
})

/** The computed value of `prop` when set to `expr` (a token) on a probe element: the cascade-free reference. */
function resolved(page: Page, prop: string, expr: string): Promise<string> {
  return page.evaluate(
    ([p, e]) => {
      const probe = document.createElement('div')
      probe.style.setProperty(p, e)
      document.body.appendChild(probe)
      const value = getComputedStyle(probe).getPropertyValue(p)
      probe.remove()
      return value
    },
    [prop, expr] as const,
  )
}

// Resolved values: card-floating and the v2 tokens must win the cascade. CN-1 reads source
// and cannot see a rule that beats the class. Each value is compared to its token, not a literal.
for (const viewport of [{ width: 1440, height: 900 }, PHONE]) {
  test(`landing consent: the card, label, setting line and buttons resolve to their v2 tokens (${viewport.width}x${viewport.height})`, async ({
    page,
  }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.setViewportSize(viewport)
    const { errors, card } = await openLanding(page)
    await card.locator('[data-consent="accept"]').click()
    await page.getByRole('contentinfo').getByRole('button', { name: 'Cookie choices' }).click()
    await expect(card.locator('.cn-setting')).toHaveText('Analytics cookies are on.')
    await settleLayout(page)

    const read = (selector: string, props: string[]) =>
      card.locator(selector).evaluate((el, ps) => {
        const cs = getComputedStyle(el)
        return Object.fromEntries(ps.map((p) => [p, cs.getPropertyValue(p)]))
      }, props)
    const rootRead = await card.evaluate((el, ps) => {
      const cs = getComputedStyle(el)
      return Object.fromEntries(ps.map((p) => [p, cs.getPropertyValue(p)]))
    }, ['background-color', 'border-top-color', 'border-top-width', 'border-top-left-radius', 'box-shadow'])
    expect(rootRead['border-top-width'], 'the card lost card-floating\'s 1px border').toBe('1px')
    expect(rootRead['background-color'], 'card background').toBe(await resolved(page, 'background-color', 'var(--cream-card)'))
    expect(rootRead['border-top-color'], 'card border colour').toBe(await resolved(page, 'border-top-color', 'var(--cream-card-border)'))
    expect(rootRead['border-top-left-radius'], 'card radius').toBe(await resolved(page, 'border-top-left-radius', 'var(--radius-md)'))
    expect(rootRead['box-shadow'], 'card shadow').toBe(await resolved(page, 'box-shadow', 'var(--shadow-card)'))

    const label = await read('.t-step', ['font-size', 'text-transform', 'color'])
    expect(label['font-size'], 'label size').toBe(await resolved(page, 'font-size', 'var(--fs-step)'))
    expect(label['text-transform'], 'the label is uppercase').toBe('uppercase')
    expect(label.color, 'label colour').toBe(await resolved(page, 'color', 'var(--step-label)'))

    const setting = await read('.cn-setting', ['font-size', 'font-weight', 'color'])
    expect(setting['font-size'], 'setting size').toBe(await resolved(page, 'font-size', 'var(--fs-card-title)'))
    expect(setting['font-weight'], 'setting weight').toBe(await resolved(page, 'font-weight', 'var(--fw-bold)'))
    expect(setting.color, 'setting colour').toBe(await resolved(page, 'color', 'var(--text-heading)'))

    expect((await read('.cn-body', ['color'])).color, 'body colour').toBe(await resolved(page, 'color', 'var(--text-copy)'))
    const link = await read('.cn-link', ['color'])
    expect(link.color, 'link colour').toBe(await resolved(page, 'color', 'var(--link)'))

    const accept = await read('[data-consent="accept"]', ['background-color', 'color', 'border-top-left-radius', 'font-size'])
    const reject = await read('[data-consent="reject"]', ['color', 'border-top-color', 'border-top-left-radius', 'font-size'])
    expect(accept['background-color'], 'Accept is the primary').toBe(await resolved(page, 'background-color', 'var(--primary)'))
    expect(accept.color, 'Accept label colour').toBe(await resolved(page, 'color', 'var(--primary-foreground)'))
    expect(reject.color, 'Reject label colour').toBe(await resolved(page, 'color', 'var(--ink)'))
    expect(reject['border-top-color'], 'Reject outline colour').toBe(await resolved(page, 'border-top-color', 'var(--button-outline-border)'))
    for (const button of [accept, reject]) {
      expect(button['border-top-left-radius'], 'button radius').toBe(await resolved(page, 'border-top-left-radius', 'var(--radius-btn)'))
      expect(button['font-size'], 'button size').toBe(await resolved(page, 'font-size', 'var(--fs-btn)'))
    }

    // The link sits at the start of its grid cell: narrower than the body, which fills the cell.
    const at = `at ${viewport.width}x${viewport.height}`
    const bodyBox = await rectOf(card.locator('.cn-body'), 'the notice body', at)
    const linkBox = await rectOf(card.locator('.cn-link'), 'the policy link', at)
    expect(linkBox.width, `the policy link spans ${linkBox.width}px of a ${bodyBox.width}px cell`).toBeLessThan(bodyBox.width - 1)
    expect(Math.abs(linkBox.x - bodyBox.x), 'the policy link is not at the start of the cell').toBeLessThanOrEqual(BOX_SLACK_PX)
    expectNoConsoleErrors(errors)
  })
}

test('landing consent: hover — Accept brightens, Reject fills, the policy link turns teal', async ({ page }) => {
  const { errors, card } = await openLanding(page)
  const accept = card.locator('[data-consent="accept"]')
  const reject = card.locator('[data-consent="reject"]')
  const link = card.locator('a.cn-link')
  const filterOf = () => accept.evaluate((el) => getComputedStyle(el).filter)
  const fillOf = () => reject.evaluate((el) => getComputedStyle(el).backgroundColor)
  const colourOf = () => link.evaluate((el) => getComputedStyle(el).color)

  const rest = { filter: await filterOf(), fill: await fillOf(), colour: await colourOf() }
  expect(rest.filter, 'control: Accept is not filtered at rest').toBe('none')
  expect(rest.fill, 'control: Reject is transparent at rest').toBe('rgba(0, 0, 0, 0)')

  await accept.hover()
  await expect.poll(filterOf, { message: 'Accept did not brighten on hover' }).toBe('brightness(1.18)')
  await reject.hover()
  await expect.poll(fillOf, { message: 'Reject did not fill on hover' }).toBe(await resolved(page, 'background-color', 'var(--muted)'))
  await expect.poll(filterOf, { message: 'Accept kept its hover filter after the pointer left' }).toBe('none')
  await link.hover()
  await expect.poll(colourOf, { message: 'the policy link did not turn teal on hover' }).toBe(await resolved(page, 'color', 'var(--teal)'))
  expect(rest.colour, 'the hover colour must differ from the rest colour').not.toBe(await resolved(page, 'color', 'var(--teal)'))
  expectNoConsoleErrors(errors)
})

test('landing consent: the entry animation runs by default and is dropped under reduced motion', async ({ page }) => {
  const { errors, card } = await openLanding(page)
  const animation = () => card.evaluate((el) => getComputedStyle(el).animationName)
  expect(await animation(), 'the notice lost its entry animation').toBe('cn-in')
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await expect.poll(animation, { message: 'reduced motion did not drop the entry animation' }).toBe('none')
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await expect.poll(animation, { message: 'the animation did not return with motion allowed' }).toBe('cn-in')
  expectNoConsoleErrors(errors)
})

// 640 is the last phone width: max-width is inclusive. The band, the spacer and the scroll
// padding all follow the one --cn-band.
test('landing consent: 640px is the phone band and 641px the desktop card', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const { errors, card } = await openLanding(page)
  const read = async (width: number) => {
    await page.setViewportSize({ width, height: 844 })
    await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
    await settleLayout(page)
    const gaps = await noticeGaps(card)
    const band = await page.evaluate(() => ({
      spacer: document.querySelector('.cn-spacer')!.getBoundingClientRect().height,
      padding: getComputedStyle(document.documentElement).scrollPaddingBottom,
      token: getComputedStyle(document.documentElement).getPropertyValue('--cn-band').trim(),
    }))
    return { gaps, band }
  }
  const phone = await read(640)
  const desktop = await read(641)

  expect(Math.abs(phone.gaps.left - phone.gaps.right), 'at 640 the card is a band').toBeLessThanOrEqual(1)
  expect(desktop.gaps.left, 'at 641 the card is right-anchored').toBeGreaterThan(desktop.gaps.right)
  expect(phone.gaps.width, 'the phone band fills the viewport, the desktop card does not').toBeGreaterThan(desktop.gaps.width)
  for (const [name, s] of [['640', phone], ['641', desktop]] as const) {
    expect(s.band.token, `--cn-band at ${name}`).not.toBe('')
    expect(s.band.spacer, `the spacer at ${name} is the band`).toBe(parseFloat(s.band.token))
    expect(s.band.padding, `the scroll padding at ${name} is the band`).toBe(s.band.token)
  }
  expect(desktop.band.spacer, 'the desktop band exceeds the phone band').toBeGreaterThan(phone.band.spacer)
  expectNoConsoleErrors(errors)
})

// The reopened card is the taller one and opens over the footer. The band and the scroll padding
// exist only while a notice is up.
for (const viewport of [{ width: 1280, height: 720 }, { width: 1440, height: 900 }]) {
  test(`landing consent: the reopened desktop card clears the footer, and band and scroll padding follow the notice (${viewport.width}x${viewport.height})`, async ({
    page,
  }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.setViewportSize(viewport)
    const { errors, card } = await openLanding(page)
    const padding = () => page.evaluate(() => getComputedStyle(document.documentElement).scrollPaddingBottom)
    const spacerHeight = () => page.locator('.cn-spacer').evaluate((el) => `${el.getBoundingClientRect().height}px`)

    const up = await spacerHeight()
    expect(parseFloat(up), 'control: the first-visit spacer reserves nothing').toBeGreaterThan(0)
    expect(await padding(), 'the scroll padding is not the spacer height while the notice is up').toBe(up)

    await card.locator('[data-consent="accept"]').click()
    await expect(card).toHaveCount(0)
    await expect(page.locator('.cn-spacer'), 'the spacer outlived the notice').toHaveCount(0)
    expect(await padding(), 'the scroll padding outlived the notice').toBe('auto')

    await page.getByRole('contentinfo').getByRole('button', { name: 'Cookie choices' }).click()
    await expect(card.locator('.cn-setting')).toHaveText('Analytics cookies are on.')
    expect(await padding(), 'the reopened notice did not restore the scroll padding').toBe(await spacerHeight())

    await scrollToDocumentEnd(page)
    const noticeRect = await rectOf(card, 'the reopened notice', 'at the document end')
    const footerRect = await rectOf(page.getByRole('contentinfo'), 'the footer', 'at the document end')
    expect(
      footerRect.y + footerRect.height,
      `the reopened card (top ${noticeRect.y}) covers the footer (bottom ${footerRect.y + footerRect.height})`,
    ).toBeLessThanOrEqual(noticeRect.y + BOX_SLACK_PX)
    const covered = await page.evaluate(() => {
      const notice = document.querySelector('[aria-label="Cookie notice"]')!.getBoundingClientRect()
      const controls = [...document.querySelectorAll('footer a, footer button')]
      return {
        count: controls.length,
        hit: controls.filter((el) => {
          const r = el.getBoundingClientRect()
          const at = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)
          return !!at?.closest('[aria-label="Cookie notice"]') || (r.y + r.height > notice.y && r.y < notice.y + notice.height && r.x < notice.x + notice.width && r.x + r.width > notice.x)
        }).length,
      }
    })
    expect(covered.count, 'the footer query found too few controls').toBeGreaterThanOrEqual(MIN_FOOTER_CONTROLS)
    expect(covered.hit, 'the reopened card covers a footer control').toBe(0)
    expectNoConsoleErrors(errors)
  })
}
