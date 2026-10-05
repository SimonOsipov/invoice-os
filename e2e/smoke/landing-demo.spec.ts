import { expect, test, type Locator, type Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { resolveTarget } from '../targets'
import { enclosesRect, gaps, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from '../topology/layout'
import { seedConsent } from './landingConsent'
import { isProductionHost, isSentryHost } from './sentryHost'

// The Book-a-Demo lead capture modal, against the PR's own deployed landing (LAND-02).
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
// frontend/landing's vitest project defaults to `node`; a file may opt into jsdom per-file,
// but this package carries no React testing library and jsdom has no layout engine — so a
// submit event on a rendered modal, a focus trap, an async timing property and a rendered
// geometry measurement can only be observed in a browser. Functional only — nothing
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
const GATEWAY_URL = resolveTarget('GATEWAY_URL')

// Read from the landing source, not retyped: the request must carry the sentence the box shows.
const MARKETING_SRC = new URL('../../frontend/landing/src/components/MarketingConsent.tsx', import.meta.url)
const MARKETING_CONSENT_TEXT = /export const MARKETING_CONSENT_TEXT = '((?:[^'\\]|\\.)*)'/.exec(readFileSync(MARKETING_SRC, 'utf8'))?.[1]
if (MARKETING_CONSENT_TEXT == null) throw new Error(`${MARKETING_SRC.pathname}: no MARKETING_CONSENT_TEXT literal`)

// The GA4 tag's gate is an exact-hostname allowlist (frontend/landing/src/hubspot.ts), so
// which arm the assertions expect is decided once, here, from the target under test.
const LANDING_HOST = new URL(LANDING_URL).hostname.toLowerCase()
const EXPECT_TAG = LANDING_HOST === 'www.ascomply.com'
const EXPECT_SENTRY_SILENT = !isProductionHost(LANDING_URL)

// Mirrors TAXPAYER_SIZE_OPTIONS / DEFAULT_TAXPAYER_SIZE in
// frontend/landing/src/components/demoForm.ts. Deliberately RETYPED rather than imported:
// e2e/ must pin what the deployed build actually serves. Importing the constant would make
// E4 tautological — it would agree with itself no matter what shipped. ₦ is U+20A6, the
// band separator is U+2013 EN DASH; both are exact.
const TAXPAYER_SIZE_BANDS = ['Large ₦5bn+', 'Medium ₦1bn–₦5bn', 'Small ₦50m–₦1bn', 'Below ₦50m'] as const

// The band the modal select defaults to. Pinned by name rather than by index into the list
// above, and used ONLY by E6: it is the value whose fit is being measured, so a build that
// quietly defaulted to a shorter band must fail rather than pass by being easy to fit.
const DEFAULT_TAXPAYER_SIZE = 'Medium ₦1bn–₦5bn'

// The bare size words the bands replaced. A build that regressed to any of these would
// still render "a taxpayer size", so E4 names them explicitly.
const BARE_SIZE_WORDS = ['Micro', 'Small', 'Medium', 'Large'] as const

// DemoLeadForm's demo-mode stub resolves after 1300ms (`runStub`), and a closed gate, a
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
// sits between #dm-marketing and the submit button in the DOM and is absent here on purpose:
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
  '#dm-marketing',
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

// Viewports for E6, measured on the open modal. 1280 is AC #6's mandated measurement.
// The rest are the widths at which the UNGUARDED value measurably broke, wide to phone.
const ASSERTED_WIDTHS = [1280, 1150, 1100, 960, 921, 600, 500, 430, 390, 375] as const

type LandingSinks = {
  /** console.error + pageerror. AC #3: a closed gate must stay silent. */
  consoleErrors: string[]
  /** THE oracle for "no visitor data left the browser". Fed by page.on('request'). */
  hubspotRequests: string[]
  /** Requests to the gateway's POST /contacts/demo-request: method and body. */
  gatewayDemoRequests: Array<{ method: string; body: string | null }>
  /** Non-vacuity guard: proves the request listener was live at all. */
  allRequests: string[]
  /** THE oracle for "the GA4 tag loaded". Filled by the SAME listener as allRequests. */
  gaRequests: string[]
  /** A PR environment runs with Sentry off, so this stays empty. Same listener as allRequests. */
  sentryRequests: string[]
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

/**
 * Hostname-parsed, never a substring match. `fonts.googleapis.com` and `fonts.gstatic.com`
 * are requested on EVERY run (index.html's preconnects), so an over-broad predicate makes
 * the biconditional below permanently red.
 */
function isGoogleAnalyticsHost(rawUrl: string): boolean {
  let host: string
  try {
    host = new URL(rawUrl).hostname.toLowerCase()
  } catch {
    return false
  }
  return (
    host === 'googletagmanager.com' ||
    host.endsWith('.googletagmanager.com') ||
    host === 'google-analytics.com' ||
    host.endsWith('.google-analytics.com') ||
    host === 'analytics.google.com' ||
    host.endsWith('.analytics.google.com') ||
    host === 'g.doubleclick.net' ||
    host.endsWith('.g.doubleclick.net')
  )
}

/** Attach every sink BEFORE navigating; returns the sinks the assertions read. */
function attachSinks(page: Page): LandingSinks {
  const sinks: LandingSinks = {
    consoleErrors: [],
    hubspotRequests: [],
    gatewayDemoRequests: [],
    allRequests: [],
    gaRequests: [],
    sentryRequests: [],
    abortedByGuard: [],
  }
  page.on('console', (msg) => {
    if (msg.type() === 'error') sinks.consoleErrors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    sinks.consoleErrors.push(`pageerror: ${err.message}`)
  })
  // ONE listener feeds every sink, so allRequests' non-vacuity floor covers the other
  // sinks too — a second listener could be dead while the first was live.
  page.on('request', (req) => {
    const url = req.url()
    sinks.allRequests.push(url)
    if (isHubSpotHost(url)) sinks.hubspotRequests.push(url)
    if (url.split('?')[0] === `${GATEWAY_URL}/contacts/demo-request`) {
      sinks.gatewayDemoRequests.push({ method: req.method(), body: req.postData() })
    }
    if (isGoogleAnalyticsHost(url)) sinks.gaRequests.push(url)
    if (isSentryHost(url)) sinks.sentryRequests.push(url)
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

/**
 * The GA safety net. FULFILS rather than aborts: an aborted `<script src>` makes the
 * browser log a failed resource load, which this file's zero-console-error assertion would
 * then fail. An empty 200 records the request, loads a no-op, and sends Google nothing.
 */
async function guardGoogleAnalytics(page: Page, sinks: LandingSinks): Promise<void> {
  await page.route(
    (url) => isGoogleAnalyticsHost(url.toString()),
    async (route) => {
      sinks.abortedByGuard.push(route.request().url())
      await route.fulfill({ status: 200, body: '', contentType: 'application/javascript' })
    },
  )
}

/** goto + settle. Fonts are settled ONCE here, before any interaction or measurement. */
async function openLanding(page: Page): Promise<LandingSinks> {
  const sinks = attachSinks(page)
  await guardHubSpot(page, sinks)
  await guardGoogleAnalytics(page, sinks)
  // Granted: EXPECT_TAG asserts the tag loads, which a denied default would falsify.
  await seedConsent(page, true)

  const response = await page.goto(LANDING_URL)
  expect(response, `no response from ${LANDING_URL}`).toBeTruthy()
  expect(response!.ok(), `${LANDING_URL} returned HTTP ${response!.status()}`).toBeTruthy()

  await expect(page.getByRole('banner')).toBeVisible()

  // Manrope is Google-hosted with display=swap. A swap AFTER a measurement
  // reflows geometry that has already been read, and E6 measures geometry to the
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
async function fillRequiredFields(root: Locator, prefix: string): Promise<void> {
  await root.locator(`#${prefix}-name`).fill(LEAD.name)
  await root.locator(`#${prefix}-email`).fill(LEAD.email)
  await root.locator(`#${prefix}-company`).fill(LEAD.company)
}

function submitButton(root: Locator): Locator {
  return root.getByRole('button', { name: /^Book my demo/ })
}

/**
 * The success panel, asserted identically wherever it appears. E5's claim that the honeypot
 * path is indistinguishable from the closed-gate path is exactly "E1 and E5 both end here,
 * both no faster than the shared stub" — so both call THIS, rather than each describing
 * success in its own words.
 */
async function expectSuccessPanel(root: Locator, prefix: string): Promise<void> {
  await expect(root.locator(`#${prefix}-success`)).toBeVisible()
  await expect(root.locator(`#${prefix}-success-done`)).toBeVisible()
  await expect(root).toContainText(`Thanks, ${LEAD.firstName}.`)
  await expect(root).toContainText(LEAD.email)
  // The form is gone, not merely covered.
  await expect(root.locator(`#${prefix}-name`)).toHaveCount(0)
}

/** AC #2 + AC #3, asserted at the end of every test in this file: zero HubSpot requests, and no console errors. */
function expectZeroHubSpotRequests(sinks: LandingSinks): void {
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
  if (EXPECT_SENTRY_SILENT) {
    expect(sinks.sentryRequests, `the landing sent requests to a Sentry host:\n${sinks.sentryRequests.join('\n')}`).toEqual([])
  }
  expect(sinks.consoleErrors, `console errors on the landing page:\n${sinks.consoleErrors.join('\n')}`).toEqual([])
  // A biconditional, not "zero GA requests": the tag SHOULD load when the target is the
  // real production host. Red either way round — a gate weakened to admit a preview host,
  // or a production build that stopped loading the tag at all.
  expect(
    sinks.gaRequests.length > 0,
    `landing host ${LANDING_HOST}: expected gtag.js ${EXPECT_TAG ? 'to be requested' : 'never to be requested'}; ` +
      `recorded ${sinks.gaRequests.length}:\n${sinks.gaRequests.join('\n')}`,
  ).toBe(EXPECT_TAG)
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

/** Two rAFs — layout after a resize is committed by the second one. */
function settleLayout(page: Page): Promise<boolean> {
  return page.evaluate(
    () => new Promise<boolean>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(true)))),
  )
}

// E0 — the classifier every GA assertion in this file consumes, exercised directly. No
// `page` parameter, so Playwright opens no browser. A predicate that matched nothing would
// leave gaRequests permanently empty and pass every biconditional while observing nothing.
test('landing analytics: the analytics-host classifier accepts GA hosts and nothing else', () => {
  for (const url of [
    'https://www.googletagmanager.com/gtag/js?id=G-E409H76XYY',
    'https://region1.google-analytics.com/g/collect?v=2',
    'https://analytics.google.com/g/collect',
    'https://stats.g.doubleclick.net/g/collect',
  ]) {
    expect(isGoogleAnalyticsHost(url), `${url} should be recognised as an analytics host`).toBe(true)
  }

  for (const url of [
    // The two highest-value rejections: Google Fonts hosts.
    'https://fonts.googleapis.com/css2?family=Inter',
    'https://fonts.gstatic.com/s/inter/v13/x.woff2',
    'https://www.googletagmanager.com.attacker.example/gtag/js',
    'https://fake-google-analytics.com/g/collect',
    'https://evil.example/?x=googletagmanager.com',
    'https://js.hsforms.net/forms/embed/v2/148915098.js',
    `${LANDING_URL}/assets/index.js`,
    'not-a-url',
    '',
  ]) {
    expect(isGoogleAnalyticsHost(url), `${url} should NOT be recognised as an analytics host`).toBe(false)
  }
})

// E1 — the whole happy path with the gate closed: the visitor sees success, HubSpot sees
// nothing, and our own gateway gets exactly one request carrying the marketing sentence.
test('landing demo: a complete submission on a closed gate succeeds locally and sends the request to our own gateway only', async ({ page }) => {
  const sinks = await openLanding(page)
  const dialog = await openDemoModal(page)

  await fillRequiredFields(dialog, 'dm')
  await dialog.locator('#dm-consent').check()
  await expect(dialog.locator('#dm-consent')).toBeChecked()
  await dialog.locator('#dm-marketing').check()
  await expect(dialog.locator('#dm-marketing')).toBeChecked()

  const accepted = page.waitForResponse((res) => res.request().method() === 'POST' && res.url().split('?')[0] === `${GATEWAY_URL}/contacts/demo-request`)
  const startedAt = Date.now()
  await submitButton(dialog).click()
  await expectSuccessPanel(dialog, 'dm')
  const elapsedMs = Date.now() - startedAt
  expect((await accepted).status(), 'the gateway did not accept the demo request').toBe(202)

  // A CORS preflight (OPTIONS) is not the submission; only POSTs count.
  const posts = sinks.gatewayDemoRequests.filter((r) => r.method === 'POST')
  expect(posts, 'exactly one POST to the gateway').toHaveLength(1)
  expect(JSON.parse(posts[0].body ?? 'null')).toMatchObject({
    email: LEAD.email,
    marketing_consent_text: MARKETING_CONSENT_TEXT,
  })

  // The closed gate routes through the same 1300ms stub the honeypot uses. Asserted here as
  // well as in E5 so that the pair establishes "both paths are slow" WITHOUT comparing two
  // wall-clock numbers across two runs, which CI would make flaky.
  expect(
    elapsedMs,
    `the closed-gate submit resolved in ${elapsedMs}ms — far below the shared ${STUB_DELAY_FLOOR_MS}ms stub floor`,
  ).toBeGreaterThanOrEqual(STUB_DELAY_FLOOR_MS)

  expectZeroHubSpotRequests(sinks)
})

// E1b — the other half of "the sentence only when ticked": an unticked box still sends the request, without a sentence.
test('landing demo: an unticked marketing box sends the request to our own gateway without a consent sentence', async ({ page }) => {
  const sinks = await openLanding(page)
  const dialog = await openDemoModal(page)

  await fillRequiredFields(dialog, 'dm')
  await dialog.locator('#dm-consent').check()
  await expect(dialog.locator('#dm-marketing')).not.toBeChecked()

  const accepted = page.waitForResponse((res) => res.request().method() === 'POST' && res.url().split('?')[0] === `${GATEWAY_URL}/contacts/demo-request`)
  await submitButton(dialog).click()
  await expectSuccessPanel(dialog, 'dm')
  expect((await accepted).status(), 'the gateway did not accept the demo request').toBe(202)

  const posts = sinks.gatewayDemoRequests.filter((r) => r.method === 'POST')
  expect(posts, 'exactly one POST to the gateway').toHaveLength(1)
  const body = JSON.parse(posts[0].body ?? 'null') as Record<string, unknown>
  expect(body).toMatchObject({ email: LEAD.email })
  expect(body, 'an unticked box carried a consent sentence').not.toHaveProperty('marketing_consent_text')

  expectZeroHubSpotRequests(sinks)
})

// E2 — consent is a hard gate. An unticked box must block the submit, say so inline, move
// focus to the control at fault, and (trivially but explicitly) send nothing.
test('landing demo: an unticked consent box blocks the submit and names the reason inline', async ({ page }) => {
  const sinks = await openLanding(page)
  const dialog = await openDemoModal(page)

  await fillRequiredFields(dialog, 'dm')
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

  expectZeroHubSpotRequests(sinks)
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

  expectZeroHubSpotRequests(sinks)
})

/** A `<select>`'s rendered options: value, text, and disabled state, in DOM order. */
async function readSizeOptions(select: Locator): Promise<Array<{ value: string; text: string; disabled: boolean }>> {
  return select.locator('option').evaluateAll((els) =>
    els.map((el) => {
      const option = el as HTMLOptionElement
      return { value: option.value, text: (option.textContent ?? '').trim(), disabled: option.disabled }
    }),
  )
}

// E4 — the modal select offers exactly the four turnover bands ON THE DEPLOYED BUILD.
// This asserts membership only; whether the value FITS its box is E6's job.
test('landing demo: the modal select offers exactly the four turnover bands', async ({ page }) => {
  const sinks = await openLanding(page)
  const dialog = await openDemoModal(page)

  const modalSelect = dialog.locator('#dm-size')
  // Web-first, and it settles the read below: four bands plus the disabled placeholder.
  await expect(modalSelect.locator('option')).toHaveCount(TAXPAYER_SIZE_BANDS.length + 1)
  const modalRendered = await readSizeOptions(modalSelect)

  const selectable = modalRendered.filter((o) => !o.disabled)
  const placeholders = modalRendered.filter((o) => o.disabled)

  expect(selectable.map((o) => o.value)).toEqual([...TAXPAYER_SIZE_BANDS])
  expect(selectable.map((o) => o.text)).toEqual([...TAXPAYER_SIZE_BANDS])
  expect(placeholders.map((o) => o.value), 'the only disabled option must be the empty placeholder').toEqual([''])

  // A build that regressed to bare size words would still offer "a taxpayer size", so the
  // regression is named rather than implied.
  for (const word of BARE_SIZE_WORDS) {
    expect(modalRendered.map((o) => o.text), `the select still offers the bare word "${word}"`).not.toContain(word)
  }

  // The select's default, read from the deployed build rather than assumed.
  const selectDefault = await modalSelect.inputValue()
  expect(TAXPAYER_SIZE_BANDS, 'the select defaults to something that is not one of the four bands').toContain(
    selectDefault,
  )

  expectZeroHubSpotRequests(sinks)
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

  await fillRequiredFields(dialog, 'dm')
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
  await expectSuccessPanel(dialog, 'dm')
  const elapsedMs = Date.now() - startedAt

  expect(
    elapsedMs,
    `the tripped honeypot resolved in ${elapsedMs}ms. An early return is time-distinguishable ` +
      `from a real submit, which lets a bot detect the trap and retry without it.`,
  ).toBeGreaterThanOrEqual(STUB_DELAY_FLOOR_MS)
  expect(sinks.gatewayDemoRequests, 'a tripped honeypot reached the gateway').toEqual([])

  expectZeroHubSpotRequests(sinks)
})

// The wide end has room to strand a band; ASSERTED_WIDTHS alone had never swept above 1280.
const CARD_FIT_WIDTHS = [...new Set([...WIDE_WIDTHS, ...ASSERTED_WIDTHS])]

type CardSizeMeasurement = {
  /** #dm-size.value, read from the deployed build. */
  sizeValue: string
  /** Rounded to 2dp. */
  sizeHeightPx: number
  size: Rect
  caret: Rect
  volume: Rect
  card: Rect
  rowScrollWidth: number
  rowClientWidth: number
}

/**
 * Reads the modal's taxpayer-size control and its neighbours by id, throwing a named
 * error per missing node rather than measuring nulls.
 */
function measureCardSizeControl(page: Page): Promise<CardSizeMeasurement> {
  return page.evaluate(() => {
    // Rounded to 2dp, same precedent as sizeHeightPx below: keeps measureCardStable's
    // byte-equality check honest against float jitter rather than brittle to it.
    const round = (n: number) => Math.round(n * 100) / 100
    const rectOf = (el: Element) => {
      const r = el.getBoundingClientRect()
      return { x: round(r.x), y: round(r.y), width: round(r.width), height: round(r.height) }
    }

    const size = document.getElementById('dm-size')
    if (!size) throw new Error('no #dm-size select in the open modal')
    // The caret is named by being the wrapper's only span, not by position.
    const wrapper = size.parentElement
    const spans = wrapper ? Array.from(wrapper.querySelectorAll('span')) : []
    if (spans.length !== 1) {
      throw new Error(`#dm-size's wrapper has ${spans.length} <span> children, expected the one caret`)
    }
    const caret = spans[0]

    const volume = document.getElementById('dm-volume')
    if (!volume) throw new Error('no #dm-volume select in the open modal')

    const row = document.querySelector('[role="dialog"] .dm-row')
    if (!row) throw new Error('no .dm-row inside the modal')

    const card = document.querySelector('[role="dialog"] > div')
    if (!card) throw new Error('no card inside the modal')

    return {
      sizeValue: (size as HTMLSelectElement).value,
      sizeHeightPx: Math.round(size.getBoundingClientRect().height * 100) / 100,
      size: rectOf(size),
      caret: rectOf(caret),
      volume: rectOf(volume),
      card: rectOf(card),
      rowScrollWidth: row.scrollWidth,
      rowClientWidth: row.clientWidth,
    }
  })
}

/**
 * Re-measures until two consecutive readings agree, the same wait-with-an-assertion
 * pattern the rest of this file uses instead of a sleep.
 */
async function measureCardStable(page: Page, width: number): Promise<CardSizeMeasurement> {
  await settleLayout(page)
  const first = await measureCardSizeControl(page)
  await settleLayout(page)
  const second = await measureCardSizeControl(page)
  expect(second, `the modal's taxpayer-size control was still reflowing at ${width}px`).toEqual(first)
  return second
}

// E6 — AC #6's measurement obligation. The modal renders a real <select>, so the
// browser owns wrapping and truncation and the old text-level oracles are gone. What is
// still ours: the control and its caret stay inside the boxes that clip them, and the
// two-select row shrinks rather than overflowing.
test('landing demo: the modal taxpayer-size value fits its card', async ({ page }, testInfo) => {
  const sinks = await openLanding(page)
  const dialog = await openDemoModal(page)
  // The open animation moves the card; settle it before the first stable-read pair.
  await settleAnimations(dialog.locator(':scope > div'))

  const sweep: Array<CardSizeMeasurement & { viewportWidth: number }> = []
  for (const width of CARD_FIT_WIDTHS) {
    await page.setViewportSize({ width, height: 900 })
    sweep.push({ viewportWidth: width, ...(await measureCardStable(page, width)) })
  }

  await testInfo.attach('taxpayer-size-value-fit.json', {
    body: JSON.stringify({ url: LANDING_URL, measuredAt: new Date().toISOString(), sweep }, null, 2),
    contentType: 'application/json',
  })
  for (const m of sweep) {
    testInfo.annotations.push({
      type: 'measurement',
      description:
        `${m.viewportWidth}px: value "${m.sizeValue}", control height ${m.sizeHeightPx}px, ` +
        `row scrollWidth ${m.rowScrollWidth} vs clientWidth ${m.rowClientWidth}`,
    })
  }

  // Widths 375-600 are where the row-overflow (or below 480, the stack) assertion has teeth;
  // the wide end is cheap insurance, kept because this file had never swept above 1280 before.
  for (const m of sweep) {
    const at = `at ${m.viewportWidth}px`
    // Non-vacuity: a build that regressed to a short bare word would fit everywhere.
    expect(m.sizeValue, `the modal is not showing the default turnover band ${at}`).toBe(DEFAULT_TAXPAYER_SIZE)
    expect(enclosesRect(m.size, m.caret, 1), `the chevron caret is not contained by its control ${at}`).toBe(true)
    expect(enclosesRect(m.card, m.size, 1), `the size control is not contained by the card ${at}`).toBe(true)
    // #dm-size is the row's first flex item: an overflowing row pushes #dm-volume out, not
    // #dm-size, so the card must be asserted to enclose both.
    expect(enclosesRect(m.card, m.volume, 1), `the volume control is not contained by the card ${at}`).toBe(true)

    if (m.viewportWidth >= 480) {
      expect(m.rowScrollWidth, `the two-select row overflows ${at}`).toBeLessThanOrEqual(m.rowClientWidth)
    } else {
      // Below 480px .dm-row switches to flex-direction: column (DEMO_FORM_CSS), so the row
      // can never overflow here — assert the real behaviour instead: size and volume stack
      // vertically, each stretched to the row's full width.
      expect(m.volume.y, `the controls did not stack vertically ${at}`).toBeGreaterThanOrEqual(
        m.size.y + m.size.height - 1,
      )
      expect(m.size.width, `the size control is not full-width when stacked ${at}`).toBeGreaterThan(
        m.rowClientWidth * 0.9,
      )
      expect(m.volume.width, `the volume control is not full-width when stacked ${at}`).toBeGreaterThan(
        m.rowClientWidth * 0.9,
      )
    }

    // DemoLeadForm declares the select at 42px; two-sided so a shrunk control also fails.
    const heightMsg = `the control is ${m.sizeHeightPx}px, not within 1px of the declared 42px ${at}`
    expect(m.sizeHeightPx, heightMsg).toBeGreaterThanOrEqual(41)
    expect(m.sizeHeightPx, heightMsg).toBeLessThanOrEqual(43)
    // Flex items never overlap; cheap insurance rather than a real oracle.
    expect(rectsOverlap(m.size, m.volume), `the size and volume controls overlap ${at}`).toBe(false)
  }

  expectZeroHubSpotRequests(sinks)
})

// O1: the modal card fits the viewport and sits centred, widest first, then a phone.
test('landing demo: the modal card fits and sits centred at every width', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 1080 })
  await openLanding(page)
  const dialog = await openDemoModal(page)
  const card = dialog.locator(':scope > div')
  await expect(card).toHaveCount(1)

  const viewports = [...WIDE_WIDTHS.map((width) => ({ width, height: 1080 })), { width: 390, height: 667 }]
  const measured: Array<{ width: number; height: number; cardWidth: number; left: number; right: number }> = []
  for (const viewport of viewports) {
    await page.setViewportSize(viewport)
    const at = `${viewport.width}x${viewport.height}`
    // The resize re-lays the overlay; poll until the card is settled, enclosed and centred.
    await expect
      .poll(async () => {
        await settleAnimations(card)
        const box = await card.boundingBox()
        if (!box) return null
        const side = gaps(box, { x: 0, width: viewport.width })
        const fits = enclosesRect({ x: 0, y: 0, width: viewport.width, height: viewport.height }, box, 0.5)
        return fits && Math.abs(side.left - side.right) <= 1
      }, { message: `the card is not enclosed and centred at ${at}` })
      .toBe(true)
    const box = (await card.boundingBox())!
    const side = gaps(box, { x: 0, width: viewport.width })
    measured.push({ ...viewport, cardWidth: box.width, left: side.left, right: side.right })
  }

  // Both scrims pad alike at 390: the demo overlay carries no phone-only override.
  const demoPadding = await dialog.evaluate((el) => getComputedStyle(el).padding)
  await page.goto(`${LANDING_URL}/?state=${'A'.repeat(43)}&signin=ready`)
  const signIn = page.getByRole('dialog', { name: 'Platform login' })
  await expect(signIn).toBeVisible()
  const signInPadding = await signIn.evaluate((el) => getComputedStyle(el).padding)
  expect(signInPadding, 'the sign-in scrim has no padding').not.toBe('')
  expect(demoPadding, 'the demo scrim pads differently from the sign-in scrim at 390').toBe(signInPadding)

  await testInfo.attach('demo-modal-fit.json', { body: JSON.stringify(measured, null, 2), contentType: 'application/json' })
  expect(measured.length, 'one reading per width').toBe(WIDE_WIDTHS.length + 1)
  expect(measured[0].width, 'the widest width is read first').toBe(2560)
  for (const m of measured) expect(m.cardWidth, `the card has no width at ${m.width}`).toBeGreaterThan(0)
})

// O2: at 390x667 the card scrolls its own content; the page behind it does not move.
test('landing demo: the modal card scrolls inside itself at 390', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1080 })
  await openLanding(page)
  const dialog = await openDemoModal(page)
  const card = dialog.locator(':scope > div')
  await expect(card).toHaveCount(1)
  await page.setViewportSize({ width: 390, height: 667 })
  await settleAnimations(card)

  const scrollY0 = await page.evaluate(() => window.scrollY)
  const submit = submitButton(dialog)
  await submit.scrollIntoViewIfNeeded()

  const overflow = await card.evaluate((el) => ({ scrollHeight: el.scrollHeight, clientHeight: el.clientHeight }))
  // Non-vacuity: a card that fits has nothing to scroll.
  expect(overflow.scrollHeight, 'the card has no overflow to scroll at 390x667').toBeGreaterThan(overflow.clientHeight + 1)
  await expect.poll(() => card.evaluate((el) => el.scrollTop), { message: 'the card did not scroll' }).toBeGreaterThan(0)

  const [cardBox, submitBox] = await Promise.all([card.boundingBox(), submit.boundingBox()])
  expect(cardBox, 'the card has no box').not.toBeNull()
  expect(submitBox, 'the submit has no box').not.toBeNull()
  expect(enclosesRect(cardBox!, submitBox!, 0.5), 'the card does not enclose the submit').toBe(true)
  await expect(submit).toBeInViewport()
  expect(await page.evaluate(() => window.scrollY), 'the page behind the modal scrolled').toBe(scrollY0)
})

// O3: the marketing row sits under the consent row on the same left edge, inside the card.
test('landing demo: the marketing row lines up under the consent row at 1440 and 390', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1080 })
  await openLanding(page)
  const dialog = await openDemoModal(page)
  const card = dialog.locator(':scope > div')
  await expect(card).toHaveCount(1)

  for (const viewport of [{ width: 1440, height: 1080 }, { width: 390, height: 667 }]) {
    await page.setViewportSize(viewport)
    const at = `${viewport.width}px`
    await settleAnimations(card)
    // The card scrolls its own height at 390; both rows must be reachable, then measured.
    await dialog.locator('#dm-marketing').scrollIntoViewIfNeeded()
    await settleLayout(page)
    const m = await page.evaluate(() => {
      const rect = (el: Element) => {
        const r = el.getBoundingClientRect()
        return { x: r.x, y: r.y, width: r.width, height: r.height }
      }
      const consent = document.getElementById('dm-consent')!
      const marketing = document.getElementById('dm-marketing')!
      return {
        card: rect(document.querySelector('[role="dialog"] > div')!),
        consentBox: rect(consent),
        marketingBox: rect(marketing),
        consentRow: rect(consent.closest('label')!),
        marketingRow: rect(marketing.closest('label')!),
      }
    })
    expect(Math.abs(m.marketingBox.x - m.consentBox.x), `${at}: marketing checkbox left ${m.marketingBox.x} vs consent ${m.consentBox.x}`).toBeLessThanOrEqual(0.5)
    expect(m.marketingRow.y, `${at}: the marketing row does not start below the consent row`).toBeGreaterThanOrEqual(m.consentRow.y + m.consentRow.height - 1)
    for (const [name, box] of [['consent row', m.consentRow], ['marketing row', m.marketingRow]] as const) {
      expect(enclosesRect(m.card, box, 1), `${at}: the ${name} is not enclosed by the card: ${JSON.stringify({ card: m.card, box })}`).toBe(true)
    }
  }
})

// E7 — the deployed scroll-depth path. Reads the whole page, opens no modal, and asserts
// the same gate biconditional: the only run in this file that drives App.tsx's scroll
// listener on the build that shipped.
test('landing analytics: reading the whole page requests gtag.js only on the live host', async ({ page }) => {
  const sinks = await openLanding(page)

  for (const fraction of [0.25, 0.5, 0.75, 1]) {
    await page.evaluate((f) => {
      window.scrollTo(0, (document.documentElement.scrollHeight - window.innerHeight) * f)
    }, fraction)
    await settleLayout(page)
  }

  // Non-vacuity: a page that never moved would exercise no milestone at all.
  expect(await page.evaluate(() => window.scrollY), 'the landing page did not scroll').toBeGreaterThan(0)
  await expect(page.getByRole('dialog')).toHaveCount(0)

  expectZeroHubSpotRequests(sinks)
})
