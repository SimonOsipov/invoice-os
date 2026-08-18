import { expect, test, type Locator, type Page } from '@playwright/test'
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
// span + wrapper div + Cookie choices + version string: the four descendants the
// copyright row is required to contain, so the walk's floor is counted, not invented.
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
// Three footer link columns (3 + 3 + 4) plus the Cookie choices control. A floor, not the
// count: the claim is that the query reached the footer at all.
const MIN_FOOTER_CONTROLS = 8

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

function centreDistance(a: Rect, b: Rect): number {
  return Math.hypot(a.x + a.width / 2 - (b.x + b.width / 2), a.y + a.height / 2 - (b.y + b.height / 2))
}

/**
 * Is Inter actually available for layout?
 *
 * The computed font-family always reads "Inter, ui-sans-serif, …" whether or not the
 * Google-hosted webfont arrived, and Chromium's document.fonts.check() answers true for a
 * family it has never heard of. Two probe spans — `Inter, monospace` against bare
 * `monospace` — differ only when Inter is real. The mobile body wraps with ~6px of slack,
 * so a missing Inter costs a whole line and reads as a layout bug unless this names it.
 */
function interIsUsable(page: Page): Promise<{ usable: boolean; withInter: number; fallback: number }> {
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
    const withInter = measure('Inter, monospace')
    const fallback = measure('monospace')
    return { usable: Math.abs(withInter - fallback) > 1, withInter, fallback }
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
  else await expect(page.locator('#demo')).toHaveCount(1)

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
// Right-anchored the card opens at x = W-484 while the CTA's left column ends at
// 88 + 0.6*(min(W,1280)-224); those separate at W >= 1094, so across CTA_STATES the two
// x bands cannot intersect and no scroll offset can produce an overlap. The sweep is
// what turns that from an argument into an assertion — and the y-band arm below is what
// stops it passing on a page where the card simply never reaches the copy.
test('landing consent: the notice never covers the closing CTA copy at any scroll offset', async ({
  page,
}, testInfo) => {
  test.setTimeout(180_000)
  const { errors, card } = await openLanding(page)

  const demo = page.locator('#demo')
  const targets: Array<{ label: string; locator: Locator }> = [
    { label: 'the BOOK A DEMO eyebrow', locator: demo.getByText('BOOK A DEMO', { exact: true }) },
    { label: "the CTA's h2", locator: demo.getByRole('heading', { level: 2 }) },
    { label: "the CTA's supporting paragraph", locator: demo.getByText(/A 20-minute walkthrough/) },
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
          const demoEl = document.querySelector('#demo')
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
  const demo = page.locator('#demo')
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
// spacer existed, the privacy link, Cookie choices, Security, Status and Pricing were all
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
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  expectNoConsoleErrors(errors)
})

// C6 — the notice's policy link is underlined. `.asc-app a { text-decoration: none }` is
// (0,1,1) and outranks a bare .cn-link, so this is the cascade resolution, not the source.
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

  // Control needle: the footer's .ios-link declares no decoration, so the same instrument
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

// C8 — under either modal the notice stays mounted, inert and keyboard-unreachable.
// SignInModal is the load-bearing case: its only onKeyDown is the OTP handler, so it has
// no Tab trap of its own.
test('landing consent: keyboard focus cannot reach the notice while a modal is open', async ({ page }) => {
  test.setTimeout(60_000) // 2 modals x 30 Tab presses, each press read back over the wire
  const { errors, card } = await openLanding(page)

  const cases = [
    { trigger: 'Explore the platform', dialog: 'Sign in' },
    { trigger: 'Book a demo', dialog: 'Book a demo' },
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

    // The overlay itself closes both modals (its root div owns onClick={onClose}).
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
// .asc-app's overflow-x: clip. The oracle is the card's OWN box.
test('landing consent: the first-visit card is at most a third of the phone viewport', async ({ page }, testInfo) => {
  await page.setViewportSize(PHONE)
  const { errors, card } = await openLanding(page)
  await expect(card.locator('.cn-setting'), 'a first visit must not render the current-setting line').toHaveCount(0)

  const inter = await interIsUsable(page)
  expect(
    inter.usable,
    `Inter is not available for layout (probe ${inter.withInter}px vs fallback ${inter.fallback}px) — the body wraps with ~6px of slack, so a missing webfont costs a whole line and reads as a layout bug`,
  ).toBe(true)

  const rect = await rectOf(card, 'the cookie notice', `at ${PHONE.width}x${PHONE.height}`)
  await testInfo.attach('cookie-notice-mobile-height.json', {
    body: JSON.stringify({ state: 'first visit', viewport: PHONE, cap: MOBILE_THIRD_PX, rect, inter }, null, 2),
    contentType: 'application/json',
  })
  testInfo.annotations.push({
    type: 'measurement',
    description: `first visit: ${rect.height}px card against a ${MOBILE_THIRD_PX}px cap`,
  })

  expect(rect.width, `the card measured ${rect.width}px wide, so the cap below would pass on an unrendered card`).toBeGreaterThan(MIN_PHONE_CARD_PX)
  expect(rect.height, `the first-visit card is ${rect.height}px tall against a ${MOBILE_THIRD_PX}px cap`).toBeLessThanOrEqual(MOBILE_THIRD_PX)
  expect(rect.x, `the card overhangs the left edge (x ${rect.x})`).toBeGreaterThanOrEqual(0)
  expect(rect.x + rect.width, `the card overhangs the right edge (right ${rect.x + rect.width})`).toBeLessThanOrEqual(PHONE.width)

  expectNoConsoleErrors(errors)
})

// C9b — the REOPENED card at 390x844. .cn-setting renders only when `current` is non-null,
// i.e. only on a reopen, so the taller of the two states is measured nowhere else. Core AC
// 8 does not distinguish the two, so it applies here unchanged.
test('landing consent: the reopened card is at most a third of the phone viewport', async ({ page }, testInfo) => {
  await page.setViewportSize(PHONE)
  await seedConsent(page, true)
  const { errors } = await openLanding(page, { expectNotice: false })

  await page.getByRole('contentinfo').getByRole('button', { name: 'Cookie choices' }).click()
  const card = notice(page)
  await expect(card).toBeVisible()
  await expect(card.locator('.cn-setting'), 'the reopened card did not render its current-setting line, so this is the first-visit card again').toHaveText('Analytics cookies are on.')
  await settleLayout(page)

  const inter = await interIsUsable(page)
  expect(
    inter.usable,
    `Inter is not available for layout (probe ${inter.withInter}px vs fallback ${inter.fallback}px) — a missing webfont costs a whole line of body copy`,
  ).toBe(true)

  const rect = await rectOf(card, 'the reopened cookie notice', `at ${PHONE.width}x${PHONE.height}`)
  await testInfo.attach('cookie-notice-mobile-height-reopened.json', {
    body: JSON.stringify({ state: 'reopened', viewport: PHONE, cap: MOBILE_THIRD_PX, rect, inter }, null, 2),
    contentType: 'application/json',
  })
  testInfo.annotations.push({
    type: 'measurement',
    description: `reopened: ${rect.height}px card against a ${MOBILE_THIRD_PX}px cap`,
  })

  expect(rect.width, `the card measured ${rect.width}px wide, so the cap below would pass on an unrendered card`).toBeGreaterThan(MIN_PHONE_CARD_PX)
  expect(rect.height, `the reopened card is ${rect.height}px tall against a ${MOBILE_THIRD_PX}px cap`).toBeLessThanOrEqual(MOBILE_THIRD_PX)
  expect(rect.x, `the reopened card overhangs the left edge (x ${rect.x})`).toBeGreaterThanOrEqual(0)
  expect(rect.x + rect.width, `the reopened card overhangs the right edge (right ${rect.x + rect.width})`).toBeLessThanOrEqual(PHONE.width)

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
  const button = page.locator('#demo').getByRole('button', { name: /Book my demo/ })
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
  // Bounded above too: the spacer is a bare literal coupled to nothing, so an over-reserve
  // is as silent as an under-reserve. One further inset band is the ceiling — beyond that
  // it reads as a second gap below the footer.
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

// C13 — the footer control belongs to the version string, and the row survives narrow
// widths. Two oracles, because one does not span the wrap: the control and the version
// share a group box at EVERY width, and centre-to-centre proximity is asserted where the
// row has not wrapped.
test('landing consent: the Cookie choices control reads as part of the version string', async ({ page }, testInfo) => {
  test.setTimeout(90_000)
  const { errors } = await openLanding(page)

  const footer = page.getByRole('contentinfo')
  const control = footer.getByRole('button', { name: 'Cookie choices' })
  const version = footer.getByText('v 1.0 · MBS ADAPTER · SANDBOX', { exact: true })
  const copyright = footer.getByText('© 2026 ASCOMPLY AFRICA · LAGOS · NG', { exact: true })
  const group = control.locator('xpath=..')
  for (const [label, locator] of [['the Cookie choices control', control], ['the version string', version], ['the copyright string', copyright]] as const) {
    await expect(locator, `${label} is not unique in the footer`).toHaveCount(1)
  }

  const entry = page.viewportSize()
  const sweep: Array<{
    width: number
    toVersion: number
    toCopyright: number
    rowWrapped: boolean
    groupHoldsControl: boolean
    groupHoldsVersion: boolean
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
      const v = await rectOf(version, 'the version string', at)
      const r = await rectOf(copyright, 'the copyright string', at)
      const g = await rectOf(group, "the control's group box", at)
      sweep.push({
        width,
        toVersion: centreDistance(c, v),
        toCopyright: centreDistance(c, r),
        rowWrapped: !sharesLine(c, r),
        groupHoldsControl: enclosesRect(g, c),
        groupHoldsVersion: enclosesRect(g, v),
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
    description: sweep.map((m) => `${m.width}px: version ${Math.round(m.toVersion)}px, copyright ${Math.round(m.toCopyright)}px${m.rowWrapped ? ' (row wrapped)' : ''}`).join(' | '),
  })

  expect(sweep, 'the proximity sweep did not visit every width').toHaveLength(WIDE_WIDTHS.length + NARROW_WIDTHS.length)

  // (a1) Belonging, at EVERY width including the narrow ones: one box holds the control
  // and the version string, and the copyright string is outside it. This is the claim that
  // goes red if the control is ever moved next to the copyright.
  for (const m of sweep) {
    expect(m.groupHoldsControl, `at ${m.width}px the Cookie choices control is not inside its own group box`).toBe(true)
    expect(m.groupHoldsVersion, `at ${m.width}px the version string is not inside the control's group box`).toBe(true)
    expect(m.groupClearsCopyright, `at ${m.width}px the control's group box overlaps the copyright string`).toBe(true)
  }

  // (a2) Centre-to-centre distance, scoped to the unwrapped row. It is a proximity proxy
  // only while all three sit on ONE line; once the row wraps it is confounded by string
  // width and INVERTS. At 390 the control and the version share a line 16px apart, yet the
  // version's centre reads 164px away because that string is 204px wide, while the
  // copyright — a whole line above — reads 93px; at 375 the metric passes only because the
  // group itself broke apart and stacked the version under the control. AC #2 claims the
  // belonging at 2560/1920/1440/1280, and that is exactly the unwrapped set; the narrow
  // widths stay in the sweep and are carried by (a1) and (b).
  const unwrapped = sweep.filter((m) => !m.rowWrapped)
  expect(
    unwrapped.map((m) => m.width),
    'the footer row no longer wraps where this test assumes it does, so the scoping below is stale',
  ).toEqual([...WIDE_WIDTHS])
  for (const m of unwrapped) {
    expect(
      m.toVersion,
      `at ${m.width}px the Cookie choices control sits nearer the copyright string (${Math.round(m.toCopyright)}px) than the version string (${Math.round(m.toVersion)}px)`,
    ).toBeLessThan(m.toCopyright)
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
      expect(walk.seen, `the walk never reached the version string at ${width}px`).toContain('v 1.0 · MBS ADAPTER · SANDBOX')
      expect(walk.offenders, `the copyright row overflows at ${width}px: ${JSON.stringify(walk.offenders)}`).toEqual([])
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }

  expectNoConsoleErrors(errors)
})
