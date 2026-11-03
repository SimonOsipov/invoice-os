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
