import { randomUUID } from 'node:crypto'
import { expect, test, type APIRequestContext, type BrowserContext, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import { consentCookie, consentRecordOf, consoleGate, isStubbedHost, routeProductionHosts, STUB_HEADER } from './landingConsent'

// GA4 events of the landing on the production hostname served from the fork. The stubbed tag
// writes no cookies and sends nothing; the events are read from window.dataLayer.

const LANDING_URL = resolveTarget('LANDING_URL')
const LIBRARY_URL = resolveTarget('LIBRARY_URL')
const GATEWAY_URL = resolveTarget('GATEWAY_URL')
const WWW = 'https://www.ascomply.com'
const GA_NAME = /^_ga(_|$)/

const routedTest = test.extend<{ routed: Page }>({
  routed: async ({ browser }, use) => {
    const context = await browser.newContext()
    await routeProductionHosts(context, { landing: LANDING_URL, library: LIBRARY_URL, gateway: GATEWAY_URL })
    await use(await context.newPage())
    // in-flight route.fetch calls reject once the context closes
    await context.unrouteAll({ behavior: 'ignoreErrors' })
    await context.close()
  },
})

type GaEvent = { name: string; params: Record<string, unknown> }

// gtag pushes `arguments` objects: Array.from turns each into an array.
async function gaEvents(page: Page): Promise<GaEvent[]> {
  const entries = await page.evaluate(() =>
    ((window as unknown as { dataLayer?: ArrayLike<unknown>[] }).dataLayer ?? []).map((e) => Array.from(e)),
  )
  return entries.filter((e) => e[0] === 'event').map((e) => ({ name: e[1] as string, params: e[2] as Record<string, unknown> }))
}

const scrollDepths = async (page: Page): Promise<number[]> =>
  (await gaEvents(page)).filter((e) => e.name === 'scroll_depth').map((e) => e.params.percent_scrolled as number)

/** Routing control: a mismatch means the routing failed, not the analytics. */
async function controlRouting(page: Page, request: APIRequestContext): Promise<void> {
  const routed = await page.goto(`${WWW}/build.txt`)
  const direct = await request.get(`${LANDING_URL}/build.txt`)
  expect(direct.ok(), `routing, not analytics: ${LANDING_URL}/build.txt did not answer 2xx`).toBeTruthy()
  const expected = (await direct.text()).trim()
  expect(expected, 'routing, not analytics: the fork serves an empty /build.txt').not.toBe('')
  expect((await routed?.text())?.trim(), `routing, not analytics: ${WWW} does not serve the fork's build`).toBe(expected)
}

type Sinks = { errors: string[]; leaked: string[] }

/** Granted consent, routing control, stub-leak sink and console gate; opens the routed landing. */
async function openRoutedLanding(page: Page, request: APIRequestContext, extraCookies: Parameters<BrowserContext['addCookies']>[0] = []): Promise<Sinks> {
  await page.context().addCookies([consentCookie(true), ...extraCookies])
  await controlRouting(page, request)
  const errors = consoleGate(page)
  const leaked: string[] = []
  page.on('requestfinished', async (r) => {
    if (isStubbedHost(r.url()) && (await r.response())?.headers()[STUB_HEADER] !== '1') leaked.push(r.url())
  })
  await page.goto(`${WWW}/`)
  await expect(page.getByRole('heading', { level: 1, name: 'Africa moves. Compliance keeps up.' })).toBeVisible()
  await expect
    .poll(() => page.evaluate(() => ((window as unknown as { dataLayer?: ArrayLike<unknown>[] }).dataLayer ?? []).map((e) => e[0])), 'the tag never loaded on the routed host')
    .toContain('config')
  return { errors, leaked }
}

async function expectClean(page: Page, { errors, leaked }: Sinks): Promise<void> {
  await page.waitForLoadState('networkidle')
  expect(leaked, 'a GA, Sentry or chat request was not answered by the stub').toEqual([])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
}

routedTest('landing analytics: the routed landing loads the tag and sends no event before a visitor acts', async ({ routed: page, request }) => {
  const sinks = await openRoutedLanding(page, request)
  expect(await gaEvents(page)).toEqual([])
  await expectClean(page, sinks)
})

routedTest('landing analytics: opening the demo modal sends one demo_open from the nav', async ({ routed: page, request }) => {
  const sinks = await openRoutedLanding(page, request)
  await page.getByRole('banner').getByRole('button', { name: 'Book a demo' }).click()
  await expect(page.getByRole('dialog', { name: 'Book a demo' })).toBeVisible()
  await expect.poll(async () => (await gaEvents(page)).filter((e) => e.name === 'demo_open')).toEqual([{ name: 'demo_open', params: { cta_location: 'nav' } }])
  await expectClean(page, sinks)
})

routedTest('landing analytics: scroll depth reports 25, 50, 75 and 100 once each', async ({ routed: page, request }) => {
  const sinks = await openRoutedLanding(page, request)
  // D12: targets sit 10+ points from each milestone; top is read live at each step.
  const scrollToFraction = (fraction: number) =>
    page.evaluate((f) => window.scrollTo({ top: (document.documentElement.scrollHeight - window.innerHeight) * f, behavior: 'instant' }), fraction)
  const steps: [number, number[]][] = [
    [0.35, [25]],
    [0.6, [25, 50]],
    [0.85, [25, 50, 75]],
    [1, [25, 50, 75, 100]],
  ]
  for (const [fraction, want] of steps) {
    await scrollToFraction(fraction)
    await expect.poll(() => scrollDepths(page), `after scrolling to ${fraction * 100}%`).toEqual(want)
  }
  await scrollToFraction(0)
  await scrollToFraction(1)
  await page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))))
  expect(await scrollDepths(page)).toEqual([25, 50, 75, 100])
  await expectClean(page, sinks)
})

routedTest('landing analytics: Reject on the landing deletes the _ga cookies', async ({ routed: page, request }) => {
  const seeded = [
    { name: '_ga', value: 'GA1.1.1.1', domain: '.ascomply.com', path: '/', secure: true },
    { name: '_ga_TEST', value: 'GS1.1.3', domain: '.ascomply.com', path: '/', secure: true },
  ]
  const sinks = await openRoutedLanding(page, request, seeded)
  const gaCookies = async () => (await page.context().cookies()).filter((c) => GA_NAME.test(c.name))
  expect(await gaCookies(), 'control: both _ga cookies must exist before Reject').toHaveLength(2)

  await page.getByRole('contentinfo').getByRole('button', { name: 'Cookie choices' }).click()
  await page.getByRole('region', { name: 'Cookie notice' }).getByRole('button', { name: 'Reject' }).click()

  await expect.poll(async () => (await gaCookies()).map((c) => c.name)).toEqual([])
  expect(JSON.parse((await consentRecordOf(page))!)).toMatchObject({ analytics: false })
  await expectClean(page, sinks)
})

const FORM_PARAMS = { form_name: 'book_a_demo' } // FORM_NAME in frontend/landing/src/analytics.ts
const isFormsHost = (rawUrl: string) => {
  const host = new URL(rawUrl).hostname.toLowerCase()
  return host === 'hsforms.com' || host.endsWith('.hsforms.com')
}

/** Stubs HubSpot Forms with `formsStatus`, then submits a unique .example lead from the nav modal. */
async function submitDemoLead(page: Page, request: APIRequestContext, formsStatus: number, expectGateway: boolean) {
  const formsPosts: string[] = []
  await page.context().route((url) => isFormsHost(url.href), (route) => {
    if (route.request().method() === 'POST') formsPosts.push(route.request().url())
    return route.fulfill({
      status: formsStatus,
      body: '{}',
      contentType: 'application/json',
      headers: { [STUB_HEADER]: '1', 'access-control-allow-origin': '*' },
    })
  })
  const formsStubbed: (string | undefined)[] = []
  const formsConsoleUrls: string[] = []
  const gatewayPosts: string[] = []
  page.on('response', (r) => {
    if (isFormsHost(r.url()) && r.request().method() === 'POST') formsStubbed.push(r.headers()[STUB_HEADER])
  })
  page.on('console', (m) => {
    if (m.type() === 'error') formsConsoleUrls.push(m.location().url)
  })
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url() === `${GATEWAY_URL}/contacts/demo-request`) gatewayPosts.push(r.url())
  })
  const sinks = await openRoutedLanding(page, request)

  await page.getByRole('banner').getByRole('button', { name: 'Book a demo' }).click()
  const dialog = page.getByRole('dialog', { name: 'Book a demo' })
  await expect(dialog).toBeVisible()
  await dialog.locator('#dm-name').fill('Landing Analytics')
  await dialog.locator('#dm-email').fill(`ncov08-${randomUUID().slice(0, 8)}@landing-analytics.example`) // .example: swept by the contact cleanup
  await dialog.locator('#dm-company').fill('Landing Analytics Ltd')
  await dialog.locator('#dm-consent').check()
  const gatewayReply = expectGateway
    ? page.waitForResponse((r) => r.request().method() === 'POST' && r.url() === `${GATEWAY_URL}/contacts/demo-request`)
    : undefined
  await dialog.getByRole('button', { name: /^Book my demo/ }).click()
  return { sinks, formsPosts, formsStubbed, formsConsoleUrls, gatewayPosts, gatewayReply, dialog }
}

routedTest('landing analytics: a submitted demo request sends one generate_lead', async ({ routed: page, request }) => {
  const run = await submitDemoLead(page, request, 200, true)
  expect((await run.gatewayReply!).status()).toBe(202) // handler: internal/gateway/contacts.go
  await expect(run.dialog.locator('#dm-success')).toBeVisible()
  expect(run.formsPosts).toHaveLength(1)
  expect(run.formsStubbed, 'the Forms POST was not answered by the stub').toEqual(['1'])
  const events = await gaEvents(page)
  expect(events.filter((e) => e.name === 'generate_lead')).toEqual([{ name: 'generate_lead', params: FORM_PARAMS }])
  expect(events.filter((e) => e.name === 'demo_submit_failed')).toEqual([])
  await expectClean(page, run.sinks)
})

routedTest('landing analytics: a refused demo request sends demo_submit_failed and no generate_lead', async ({ routed: page, request }) => {
  const run = await submitDemoLead(page, request, 400, false)
  await expect(run.dialog.getByRole('heading', { name: 'Something went wrong' })).toBeVisible()
  await expect(run.dialog.locator('#dm-error-retry')).toBeVisible()
  expect(run.formsPosts).toHaveLength(1)
  expect(run.formsStubbed, 'the Forms POST was not answered by the stub').toEqual(['1'])
  const events = await gaEvents(page)
  expect(events.filter((e) => e.name === 'demo_submit_failed')).toEqual([{ name: 'demo_submit_failed', params: FORM_PARAMS }])
  expect(events.filter((e) => e.name === 'generate_lead')).toEqual([])
  expect(run.gatewayPosts, 'the form must stop before the gateway').toEqual([])
  await page.waitForLoadState('networkidle')
  expect(run.sinks.leaked).toEqual([])
  // Chromium logs the Forms 400 as a console error; it is the only one allowed.
  expect(run.sinks.errors, `console errors:\n${run.sinks.errors.join('\n')}`).toHaveLength(1)
  expect(run.formsConsoleUrls).toHaveLength(1)
  expect(isFormsHost(run.formsConsoleUrls[0])).toBe(true)
})
