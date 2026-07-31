import { expect, test, type Locator, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'

// The Book-a-Demo lead-capture modal against the PR's own deployed landing (LAND-02).
//
// EVERY test in this file runs with the HubSpot gate CLOSED. The gate is
// `resolveSubmitTarget(window.location.hostname)`, which returns a target only on an
// exact-match production hostname (frontend/landing/src/hubspot.ts). A PR's ephemeral
// Railway environment has a generated, non-production hostname by construction, so no
// run of this file can reach an open gate. These tests therefore prove that a CLOSED
// gate stays silent and sends nothing — never that a lead is created. Core AC 1 ("a real
// contact appears in HubSpot with the six answers") is not provable in CI at all and is
// discharged by the story's Manual Prerequisites, not by anything here.
//
// Why a browser test rather than a unit test: docs/e2e-convention.md's "Target surface"
// grants `landing` render checks PLUS client-side behaviour that has no other harness.
// frontend/landing's vitest project runs in `node` with no jsdom, so the repo has no DOM
// component-test layer: a submit event, a focus trap, an async timing property and a
// rendered text measurement can only be observed in a browser. Functional only — nothing
// here takes a screenshot, and every assertion is a fact about behaviour or geometry.
//
// TWO INDEPENDENT HUBSPOT GUARDS, and the distinction is load-bearing:
//   1. a REQUEST SINK (`page.on('request')`) that records any HubSpot URL the page issues.
//      This is what every assertion reads.
//   2. a SAFETY-NET ROUTE that records and then ABORTS the same URLs, so that a gate which
//      somehow opened in CI still could not create a real lead.
// Assertions must never read the route's bookkeeping: `page.on('request')` fires whether or
// not the route later aborts, so the sink can tell "never sent" from "sent and blocked" —
// the route's own list cannot, and reading it would silently defeat E1/E2/E5's zero-request
// claim the moment the guard started doing real work.

const LANDING_URL = resolveTarget('LANDING_URL')

// Mirrors TAXPAYER_SIZE_OPTIONS / DEFAULT_TAXPAYER_SIZE in
// frontend/landing/src/components/demoForm.ts. Deliberately RETYPED rather than imported:
// e2e/ must pin what the deployed build actually serves. Importing the constant would make
// E4 tautological — it would agree with itself no matter what shipped. ₦ is U+20A6, the
// band separator is U+2013 EN DASH; both are exact.
const TAXPAYER_SIZE_BANDS = ['Large ₦5bn+', 'Medium ₦1bn–₦5bn', 'Small ₦50m–₦1bn', 'Below ₦50m'] as const

// The band both surfaces default to. Pinned by name rather than by index into the list
// above, and used ONLY by E6: it is the value whose fit is being measured, so a build that
// quietly defaulted to a shorter band must fail rather than pass by being easy to fit. E4
// deliberately does NOT route through this constant — comparing two surfaces to one literal
// is not the same claim as the two surfaces agreeing with each other.
const DEFAULT_TAXPAYER_SIZE = 'Medium ₦1bn–₦5bn'

// The bare size words the bands replaced. A build that regressed to any of these would
// still render "a taxpayer size", so E4 names them explicitly.
const BARE_SIZE_WORDS = ['Micro', 'Small', 'Medium', 'Large'] as const

// DemoModal's demo-mode stub resolves after 1300ms (`runStub`), and a closed gate, a
// tripped honeypot and an injected submit all route through that ONE helper — which is the
// property E5 exists to defend. The floor is set below 1300 rather than at it: setTimeout
// never fires early, but the click-to-timer-start latency is measured on our side of the
// wire and CI clocks are not worth trusting to the millisecond. 1200ms is still three
// orders of magnitude above the ~0ms an early-return honeypot would produce, which is the
// only difference that matters. Deliberately a LOWER bound with no upper bound: a slow CI
// box may take seconds, and failing that run would say nothing about indistinguishability.
const STUB_DELAY_FLOOR_MS = 1200

// One lead, used by every test. Nothing is created anywhere: the gate is closed, the route
// guard aborts, and `.example` is a reserved TLD that cannot resolve.
const LEAD = {
  name: 'Ada Okafor',
  firstName: 'Ada',
  email: 'ada.okafor@okafor-partners.example',
  company: 'Okafor & Partners',
} as const

// What a naive bot puts in a field called "website".
const HONEYPOT_VALUE = 'https://bot.example/'

// The Tab ring inside the modal card, in DOM order, as the trap's own selector
// (`input,select,button,textarea,a[href]` filtered by isFocusable) sees it. The honeypot
// sits between #dm-consent and the submit button in the DOM and is absent here on purpose:
// every step below asserts focus equals one of these keys, so the ring never containing
// `[name=website]` IS the proof that focus never lands on the honeypot.
const TAB_RING = [
  '[aria-label=Close]',
  '#dm-name',
  '#dm-email',
  '#dm-company',
  '#dm-role',
  '#dm-size',
  '#dm-volume',
  '#dm-consent',
  'button[type=submit]',
] as const

// The honeypot's hardening, as it stands after LAND-02-02's autofill fix. Asserted against
// the DEPLOYED DOM: the SSR render test can see the markup, but only a browser can confirm
// the build that shipped still carries it. Attribute NAMES are lowercase here because the
// HTML parser and setAttribute both lowercase them for HTML elements — React writing
// `autoComplete` in JSX has no bearing on what getAttribute reads back.
const HONEYPOT_ATTRS: ReadonlyArray<readonly [string, string]> = [
  ['tabindex', '-1'],
  ['aria-hidden', 'true'],
  // NOT "off": Chrome profile autofill and every major password manager ignore
  // autocomplete="off", and an autofilled honeypot turns a real human into a silent drop.
  ['autocomplete', 'new-password'],
  ['data-lpignore', 'true'],
  ['data-1p-ignore', 'true'],
  ['data-bwignore', 'true'],
  ['data-form-type', 'other'],
]

// Viewports for E6. 1280 is AC #6's mandated measurement. 600 is the exact width at which
// landing.css's `@media (max-width: 600px)` .ios-demo-card padding rule turns on, so it is
// the narrow width that actually exercises the rule named in the plan.
const ASSERTED_WIDTHS = [1280, 600] as const
// Recorded but NOT asserted — see the long note on E6.
const RECORDED_WIDTHS = [1100, 960, 500, 430, 390, 375] as const

type LandingSinks = {
  /** console.error + pageerror. AC #3: a closed gate must stay silent. */
  consoleErrors: string[]
  /** THE oracle for "no visitor data left the browser". Fed by page.on('request'). */
  hubspotRequests: string[]
  /** Non-vacuity guard: proves the request listener was live at all. */
  allRequests: string[]
  /** Safety-net route bookkeeping. Diagnostics only — NEVER assert on this (see header). */
  abortedByGuard: string[]
}

/** Hostname-parsed, never a substring match: `https://evil.example/?x=hsforms.com` is not HubSpot. */
function isHubSpotHost(rawUrl: string): boolean {
  let host: string
  try {
    host = new URL(rawUrl).hostname.toLowerCase()
  } catch {
    return false
  }
  return (
    host === 'hsforms.com' ||
    host.endsWith('.hsforms.com') ||
    host.endsWith('.hsforms.net') ||
    host.endsWith('.hubspot.com') ||
    host.endsWith('.hs-scripts.com')
  )
}

/** Attach every sink BEFORE navigating; returns the sinks the assertions read. */
function attachSinks(page: Page): LandingSinks {
  const sinks: LandingSinks = { consoleErrors: [], hubspotRequests: [], allRequests: [], abortedByGuard: [] }
  page.on('console', (msg) => {
    if (msg.type() === 'error') sinks.consoleErrors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    sinks.consoleErrors.push(`pageerror: ${err.message}`)
  })
  page.on('request', (req) => {
    const url = req.url()
    sinks.allRequests.push(url)
    if (isHubSpotHost(url)) sinks.hubspotRequests.push(url)
  })
  return sinks
}

/**
 * The safety net. Uses the SAME predicate as the sink, so the two can never disagree about
 * what counts as HubSpot. Aborting is the point: if a build ever shipped with the gate open
 * to a preview hostname, this stops a real lead being created — and the sink still records
 * the attempt, so the test fails loudly instead of passing quietly.
 */
async function guardHubSpot(page: Page, sinks: LandingSinks): Promise<void> {
  await page.route(
    (url) => isHubSpotHost(url.toString()),
    async (route) => {
      sinks.abortedByGuard.push(route.request().url())
      await route.abort()
    },
  )
}

/** goto + settle. Fonts are settled ONCE here, before any interaction or measurement. */
async function openLanding(page: Page): Promise<LandingSinks> {
  const sinks = attachSinks(page)
  await guardHubSpot(page, sinks)

  const response = await page.goto(LANDING_URL)
  expect(response, `no response from ${LANDING_URL}`).toBeTruthy()
  expect(response!.ok(), `${LANDING_URL} returned HTTP ${response!.status()}`).toBeTruthy()

  await expect(page.getByRole('banner')).toBeVisible()

  // Inter and Fraunces are Google-hosted with display=swap. A swap AFTER a measurement
  // reflows text under geometry that has already been read, and E6 measures text to the
  // sub-pixel. Settle once, here, before anything else — same reason as landing-nav.spec.ts.
  await page.evaluate(() => document.fonts.ready.then(() => true))

  return sinks
}

/** Opens the modal from the banner CTA — the entry point a visitor actually uses. */
async function openDemoModal(page: Page): Promise<Locator> {
  await page.getByRole('banner').getByRole('button', { name: 'Book a demo' }).click()
  const dialog = page.getByRole('dialog', { name: 'Book a demo' })
  await expect(dialog).toBeVisible()
  return dialog
}

/** The three required answers. Consent is deliberately NOT ticked here — E2 needs it unticked. */
async function fillRequiredFields(dialog: Locator): Promise<void> {
  await dialog.locator('#dm-name').fill(LEAD.name)
  await dialog.locator('#dm-email').fill(LEAD.email)
  await dialog.locator('#dm-company').fill(LEAD.company)
}

function submitButton(dialog: Locator): Locator {
  return dialog.getByRole('button', { name: /^Book my demo/ })
}

/**
 * The success panel, asserted identically wherever it appears. E5's claim that the honeypot
 * path is indistinguishable from the closed-gate path is exactly "E1 and E5 both end here,
 * both no faster than the shared stub" — so both call THIS, rather than each describing
 * success in its own words.
 */
async function expectSuccessPanel(dialog: Locator): Promise<void> {
  await expect(dialog.locator('#dm-success-done')).toBeVisible()
  await expect(dialog).toContainText(`Thanks, ${LEAD.firstName}.`)
  await expect(dialog).toContainText(LEAD.email)
  // The form is gone, not merely covered.
  await expect(dialog.locator('#dm-name')).toHaveCount(0)
}

/** AC #2 + AC #3, asserted at the end of every test in this file. */
function expectClosedGateStayedSilent(sinks: LandingSinks): void {
  // Non-vacuity: an empty hubspotRequests list proves nothing if the listener never fired
  // at all. The landing always issues at least its own document/asset requests.
  expect(
    sinks.allRequests.length,
    'the request sink recorded no requests whatsoever, so "zero HubSpot requests" proves nothing',
  ).toBeGreaterThan(0)
  expect(
    sinks.hubspotRequests,
    `the closed gate sent something to HubSpot:\n${sinks.hubspotRequests.join('\n')}`,
  ).toEqual([])
  expect(sinks.consoleErrors, `console errors on the landing page:\n${sinks.consoleErrors.join('\n')}`).toEqual([])
}

/** A stable key for document.activeElement — id, else name, else aria-label, else tag[type]. */
function activeKey(page: Page): Promise<string> {
  return page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null
    if (!el || el === document.body) return '<body>'
    if (el.id) return `#${el.id}`
    const name = el.getAttribute('name')
    if (name) return `[name=${name}]`
    const label = el.getAttribute('aria-label')
    if (label) return `[aria-label=${label}]`
    const type = el.getAttribute('type')
    return type ? `${el.tagName.toLowerCase()}[type=${type}]` : el.tagName.toLowerCase()
  })
}

// Every field is relative or derived — nothing viewport-absolute — so two readings taken at
// the same viewport must be byte-identical, which is what makes the stability check below a
// real assertion rather than a sleep.
type SizeValueMeasurement = {
  /** The rendered value text, so a build that regressed to "Medium" cannot pass by fitting. */
  text: string
  /** Line boxes the value occupies. THE oracle: 1 = no wrap. */
  lines: number
  widestLinePx: number
  /** Gap between the value's right edge and the ▾ caret's left edge. <= 0 is a collision. */
  caretGapPx: number
  /** How far the text's last line falls below the box's border box. > 0 is a visible bleed. */
  bleedBelowBoxPx: number
  boxHeightPx: number
  clientWidth: number
  scrollWidth: number
  clientHeight: number
  scrollHeight: number
}

/**
 * Measures the inline #demo card's taxpayer-size value.
 *
 * The value is a BARE TEXT NODE — DemoCta renders `{DEFAULT_TAXPAYER_SIZE} <span>▾</span>`
 * inside a fixed `height: 42` flex box with no overflow/whiteSpace/minWidth guard anywhere
 * in the element, its flex:1 wrapper or its parent row. There is no element wrapping the
 * text, so a Range over the text node is the only way to measure it, and its per-line client
 * rects are the only oracle that sees a WRAP. scrollHeight is reported below but is NOT the
 * primary oracle: two 14px lines still fit inside a 40px content box, so a wrapped value
 * reads back as scrollHeight === clientHeight while looking visibly broken.
 */
function measureTaxpayerSizeValue(page: Page): Promise<SizeValueMeasurement> {
  return page.evaluate(() => {
    const label = Array.from(document.querySelectorAll<HTMLElement>('#demo .label')).find((el) =>
      (el.textContent ?? '').trim().startsWith('Taxpayer size'),
    )
    if (!label) throw new Error('no "Taxpayer size" label inside the #demo card')
    const box = label.nextElementSibling as HTMLElement | null
    if (!box) throw new Error('the "Taxpayer size" label has no value box beside it')

    const textNode = Array.from(box.childNodes).find(
      (n): n is Text => n.nodeType === Node.TEXT_NODE && (n.textContent ?? '').trim().length > 0,
    )
    if (!textNode) throw new Error('the taxpayer-size value box holds no text node')

    const range = document.createRange()
    range.selectNodeContents(textNode)
    const rects = Array.from(range.getClientRects())
    if (!rects.length) throw new Error('the taxpayer-size value produced no client rects')

    const caret = box.querySelector('span')
    if (!caret) throw new Error('the taxpayer-size value box has no ▾ caret span')

    const boxRect = box.getBoundingClientRect()
    const caretRect = caret.getBoundingClientRect()
    const right = Math.max(...rects.map((r) => r.right))
    const bottom = Math.max(...rects.map((r) => r.bottom))
    const round = (n: number): number => Math.round(n * 100) / 100

    return {
      text: (textNode.textContent ?? '').trim(),
      lines: rects.length,
      widestLinePx: round(Math.max(...rects.map((r) => r.width))),
      caretGapPx: round(caretRect.left - right),
      bleedBelowBoxPx: round(bottom - boxRect.bottom),
      boxHeightPx: round(boxRect.height),
      clientWidth: box.clientWidth,
      scrollWidth: box.scrollWidth,
      clientHeight: box.clientHeight,
      scrollHeight: box.scrollHeight,
    }
  })
}

/** Two rAFs — layout after a resize is committed by the second one. */
function settleLayout(page: Page): Promise<boolean> {
  return page.evaluate(
    () => new Promise<boolean>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(true)))),
  )
}

/**
 * Re-measures until two consecutive readings agree. Fonts are already settled, so the only
 * thing this waits out is the resize reflow — and it waits it out with an ASSERTION rather
 * than a sleep: if the box is still moving, the equality below fails and says so. Every
 * field of the measurement is relative or derived, never viewport-absolute, which is what
 * makes byte-equality the right stability test.
 */
async function measureStable(page: Page, width: number): Promise<SizeValueMeasurement> {
  await settleLayout(page)
  const first = await measureTaxpayerSizeValue(page)
  await settleLayout(page)
  const second = await measureTaxpayerSizeValue(page)
  expect(second, `the taxpayer-size value box was still reflowing at ${width}px`).toEqual(first)
  return second
}

// E1 — the whole happy path with the gate closed: the visitor sees success, and NOTHING
// leaves the browser. This is the story's Core AC 2 stated as an assertion.
test('landing demo: a complete submission on a closed gate succeeds locally and sends nothing', async ({ page }) => {
  const sinks = await openLanding(page)
  const dialog = await openDemoModal(page)

  await fillRequiredFields(dialog)
  await dialog.locator('#dm-consent').check()
  await expect(dialog.locator('#dm-consent')).toBeChecked()

  const startedAt = Date.now()
  await submitButton(dialog).click()
  await expectSuccessPanel(dialog)
  const elapsedMs = Date.now() - startedAt

  // The closed gate routes through the same 1300ms stub the honeypot uses. Asserted here as
  // well as in E5 so that the pair establishes "both paths are slow" WITHOUT comparing two
  // wall-clock numbers across two runs, which CI would make flaky.
  expect(
    elapsedMs,
    `the closed-gate submit resolved in ${elapsedMs}ms — far below the shared ${STUB_DELAY_FLOOR_MS}ms stub floor`,
  ).toBeGreaterThanOrEqual(STUB_DELAY_FLOOR_MS)

  expectClosedGateStayedSilent(sinks)
})

// E2 — consent is a hard gate. An unticked box must block the submit, say so inline, move
// focus to the control at fault, and (trivially but explicitly) send nothing.
test('landing demo: an unticked consent box blocks the submit and names the reason inline', async ({ page }) => {
  const sinks = await openLanding(page)
  const dialog = await openDemoModal(page)

  await fillRequiredFields(dialog)
  await expect(dialog.locator('#dm-consent')).not.toBeChecked()

  await submitButton(dialog).click()

  // Asserted FIRST: it proves the submit was actually processed, without which every
  // assertion below would be trivially true on a page that simply did nothing.
  const consentError = dialog.locator('#dm-consent-error')
  await expect(consentError).toBeVisible()
  await expect(consentError).toHaveAttribute('role', 'alert')
  await expect(dialog.locator('#dm-consent')).toHaveAttribute('aria-invalid', 'true')
  await expect(dialog.locator('#dm-consent')).toHaveAttribute('aria-describedby', 'dm-consent-error')

  // Focus moved to the control at fault, so a keyboard user is put where the problem is.
  await expect(dialog.locator('#dm-consent')).toBeFocused()

  // Still the form, never the success panel — and never even the submitting state, whose
  // button reads "Booking…" instead.
  await expect(submitButton(dialog)).toBeVisible()
  await expect(dialog.locator('#dm-success-done')).toHaveCount(0)

  expectClosedGateStayedSilent(sinks)
})

// E3 — the honeypot did not break the Tab trap. The trap's own selector matches the
// honeypot input unconditionally and an off-screen input keeps a non-null offsetParent, so
// only its tabIndex keeps it out of the ring — which makes this a real regression guard.
test('landing demo: the Tab trap wraps in both directions and never lands on the honeypot', async ({ page }) => {
  const sinks = await openLanding(page)
  const dialog = await openDemoModal(page)

  // The open effect focuses the first field after 60ms; this web-first assertion absorbs it.
  await expect(dialog.locator('#dm-name')).toBeFocused()

  // Step back once, natively, to reach the trap's FIRST node (the header's close button).
  await page.keyboard.press('Shift+Tab')
  await expect.poll(() => activeKey(page), { message: 'Shift+Tab from the first field did not reach the close button' }).toBe(
    TAB_RING[0],
  )

  // Forward through every control. Each step asserts focus equals a ring key, and the ring
  // contains no honeypot — so passing this loop IS "focus never lands on the honeypot".
  for (let i = 1; i < TAB_RING.length; i++) {
    await page.keyboard.press('Tab')
    await expect
      .poll(() => activeKey(page), { message: `Tab from ${TAB_RING[i - 1]} did not reach ${TAB_RING[i]}` })
      .toBe(TAB_RING[i])
  }

  // One more Tab from the last control wraps to the first...
  await page.keyboard.press('Tab')
  await expect
    .poll(() => activeKey(page), { message: 'Tab on the last control escaped the modal instead of wrapping' })
    .toBe(TAB_RING[0])

  // ...and Shift+Tab from the first wraps back to the last.
  await page.keyboard.press('Shift+Tab')
  await expect
    .poll(() => activeKey(page), { message: 'Shift+Tab on the first control escaped the modal instead of wrapping' })
    .toBe(TAB_RING[TAB_RING.length - 1])

  // The honeypot's hardening, on the build that actually shipped.
  const honeypot = dialog.locator('input[name="website"]')
  await expect(honeypot).toHaveCount(1)
  for (const [attr, value] of HONEYPOT_ATTRS) {
    await expect(honeypot, `honeypot ${attr}`).toHaveAttribute(attr, value)
  }
  // Off-screen, NOT display:none — bots skip display:none fields, which would defeat the
  // trap entirely. Both halves asserted: a real box, pushed off the left edge.
  const honeypotBox = await honeypot.boundingBox()
  expect(honeypotBox, 'the honeypot has no layout box at all — it is display:none or detached').toBeTruthy()
  expect(honeypotBox!.x + honeypotBox!.width, 'the honeypot is on-screen, where a human can see it').toBeLessThan(0)
  await expect
    .poll(() => honeypot.evaluate((el) => getComputedStyle(el).display))
    .not.toBe('none')

  expectClosedGateStayedSilent(sinks)
})

// E4 — the modal's select and the inline card agree about the turnover bands ON THE
// DEPLOYED BUILD. Two surfaces, two source files, one contract. This asserts agreement and
// membership only; whether the agreed value FITS its box is E6's job.
test('landing demo: the modal select and the inline card agree on the turnover bands', async ({ page }) => {
  const sinks = await openLanding(page)
  const dialog = await openDemoModal(page)

  const options = dialog.locator('#dm-size option')
  // Web-first, and it settles the read below: four bands plus the disabled placeholder.
  await expect(options).toHaveCount(TAXPAYER_SIZE_BANDS.length + 1)

  const rendered = await options.evaluateAll((els) =>
    els.map((el) => {
      const option = el as HTMLOptionElement
      return { value: option.value, text: (option.textContent ?? '').trim(), disabled: option.disabled }
    }),
  )
  const selectable = rendered.filter((o) => !o.disabled)
  const placeholders = rendered.filter((o) => o.disabled)

  expect(selectable.map((o) => o.value)).toEqual([...TAXPAYER_SIZE_BANDS])
  expect(selectable.map((o) => o.text)).toEqual([...TAXPAYER_SIZE_BANDS])
  expect(placeholders.map((o) => o.value), 'the only disabled option must be the empty placeholder').toEqual([''])

  // A build that regressed to bare size words would still offer "a taxpayer size", so the
  // regression is named rather than implied.
  for (const word of BARE_SIZE_WORDS) {
    expect(rendered.map((o) => o.text), `the select still offers the bare word "${word}"`).not.toContain(word)
  }

  // The select's default, read from the deployed build rather than assumed.
  const selectDefault = await dialog.locator('#dm-size').inputValue()
  expect(TAXPAYER_SIZE_BANDS, 'the select defaults to something that is not one of the four bands').toContain(
    selectDefault,
  )

  // Close the modal and read the OTHER surface. Escape is the modal's own close path.
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)

  const card = await measureStable(page, page.viewportSize()!.width)
  expect(card.text, "the inline card's taxpayer size disagrees with the modal's default").toBe(selectDefault)

  expectClosedGateStayedSilent(sinks)
})

// E5 — TIMING INDISTINGUISHABILITY of the honeypot path. Carried in from LAND-02-02's QA,
// which mutation-tested handleSubmit by making the honeypot branch early-return with no
// delay and watched the ENTIRE landing suite plus typecheck stay green. Nothing else in this
// repo guards the property, and nothing else can: the SSR render harness has no jsdom, so it
// can neither dispatch a submit nor measure async timing.
//
// The property: a bot that fills the honeypot must see exactly what a human sees, no sooner.
// Same visible outcome (expectSuccessPanel, shared verbatim with E1) and no faster than the
// shared stub floor (also asserted in E1, so the pair covers both sides of the comparison).
test('landing demo: a tripped honeypot is dropped silently, and no faster than a real submit', async ({ page }) => {
  const sinks = await openLanding(page)
  const dialog = await openDemoModal(page)

  await fillRequiredFields(dialog)
  await dialog.locator('#dm-consent').check()

  // Assigned programmatically, never typed: the input is positioned off-screen at x=-9999,
  // so a real click/type is not something a user (or Playwright's actionability checks) can
  // do to it. This is also precisely the naive-bot behaviour the component is written to
  // catch — a direct .value assignment with no input event — which is why handleSubmit reads
  // the field off the form with FormData instead of mirroring it into React state.
  const honeypot = dialog.locator('input[name="website"]')
  await honeypot.evaluate((el, value) => {
    ;(el as HTMLInputElement).value = value
  }, HONEYPOT_VALUE)
  // Non-vacuity: without this, an unfilled honeypot would sail through the real submit path
  // and this test would "pass" having never exercised the trap at all.
  await expect(honeypot).toHaveValue(HONEYPOT_VALUE)

  const startedAt = Date.now()
  await submitButton(dialog).click()
  await expectSuccessPanel(dialog)
  const elapsedMs = Date.now() - startedAt

  expect(
    elapsedMs,
    `the tripped honeypot resolved in ${elapsedMs}ms. An early return is time-distinguishable ` +
      `from a real submit, which lets a bot detect the trap and retry without it.`,
  ).toBeGreaterThanOrEqual(STUB_DELAY_FLOOR_MS)

  expectClosedGateStayedSilent(sinks)
})

// E6 — AC #6's measurement obligation, carried in from LAND-02-03.
//
// DEFAULT_TAXPAYER_SIZE went from 'Medium' (6 chars) to 'Medium ₦1bn–₦5bn' (16), and the
// inline card renders it in a fixed height:42 flex box with NO overflow, whiteSpace or
// minWidth guard on the element, its flex:1 wrapper, its parent row, or landing.css:159. So
// the risk is not only a ▾ collision but wrapping and bleeding out of the box.
//
// The full sweep is measured and attached FIRST, before any assertion, so the numbers reach
// the PR even on a failing run — AC #6 requires the result recorded either way.
//
// WHY ONLY TWO WIDTHS ARE ASSERTED. An offline Chromium replica of this exact layout (real
// Inter, the same inline styles and the same two media queries) measured the value at
// fourteen widths before this file was written. It occupies ONE line at 1280 (133.5px of
// text in a 153px content box) and at 600 (133.5 in 188), and it WRAPS to two or three lines
// between ~921 and ~1150 — just above the 920px single-column breakpoint, where the 0.8fr
// column is at its narrowest — and again below ~500. At three lines (~921 and ~375) it also
// bleeds ~4.5px below the 42px box. The same replica with the old 'Medium' fits on one line
// at every width from 375 to 1440, so this is a LAND-02-03 regression, not pre-existing.
// Those widths are outside AC #6's mandate and outside this subtask's scope to fix, so they
// are RECORDED, not asserted: a red deploy gate here would block the story on a defect it
// was never scoped to fix, and would bury the measurement instead of surfacing it. Once a
// mitigation lands, move the width into ASSERTED_WIDTHS — the assertions need no change.
test('landing demo: the inline card taxpayer-size value fits its box', async ({ page }, testInfo) => {
  const sinks = await openLanding(page)

  const sweep: Array<SizeValueMeasurement & { viewportWidth: number; asserted: boolean }> = []

  for (const width of ASSERTED_WIDTHS) {
    await page.setViewportSize({ width, height: 900 })
    sweep.push({ viewportWidth: width, asserted: true, ...(await measureStable(page, width)) })
  }
  for (const width of RECORDED_WIDTHS) {
    await page.setViewportSize({ width, height: 900 })
    // Deliberately NOT measureStable: its stability check is an assertion, and a recorded-
    // only width must be incapable of failing the run for any reason.
    await settleLayout(page)
    sweep.push({ viewportWidth: width, asserted: false, ...(await measureTaxpayerSizeValue(page)) })
  }

  await testInfo.attach('taxpayer-size-value-fit.json', {
    body: JSON.stringify({ url: LANDING_URL, measuredAt: new Date().toISOString(), sweep }, null, 2),
    contentType: 'application/json',
  })
  testInfo.annotations.push({
    type: 'measurement (LAND-02-03 / AC #6)',
    description: sweep
      .map(
        (m) =>
          `${m.viewportWidth}px: ${m.lines} line(s), widest line ${m.widestLinePx}px, ` +
          `box content width ${m.clientWidth}px, caret gap ${m.caretGapPx}px, ` +
          `bleed below box ${m.bleedBelowBoxPx}px${m.asserted ? '' : ' [recorded only]'}`,
      )
      .join(' | '),
  })

  for (const m of sweep.filter((s) => s.asserted)) {
    const at = `at ${m.viewportWidth}px`
    // Non-vacuity: prove we measured the LONG value. A build that regressed to 'Medium'
    // would fit everywhere and pass every assertion below while testing nothing.
    expect(m.text, `the inline card is not showing the default turnover band ${at}`).toBe(DEFAULT_TAXPAYER_SIZE)
    // THE oracle. One line box, or it wrapped.
    expect(m.lines, `"${m.text}" wrapped onto ${m.lines} lines inside its 42px box ${at}`).toBe(1)
    expect(m.bleedBelowBoxPx, `"${m.text}" bleeds ${m.bleedBelowBoxPx}px below its box ${at}`).toBeLessThanOrEqual(0)
    expect(m.caretGapPx, `"${m.text}" collides with the ▾ caret ${at}`).toBeGreaterThan(0)
    // The box itself is still the 42px line box it declares (+1px of sub-pixel slack).
    expect(m.boxHeightPx, `the value box grew to ${m.boxHeightPx}px ${at}`).toBeLessThanOrEqual(43)
    // Reported for the record and asserted for completeness, but weak by construction: two
    // 14px lines still fit a 40px content box, so these stay equal through a visible wrap.
    expect(m.scrollHeight, `the value box overflows vertically ${at}`).toBeLessThanOrEqual(m.clientHeight)
    expect(m.scrollWidth, `the value box overflows horizontally ${at}`).toBeLessThanOrEqual(m.clientWidth)
  }

  expectClosedGateStayedSilent(sinks)
})
