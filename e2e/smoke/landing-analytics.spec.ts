import { randomUUID } from 'node:crypto'
import { expect, test, type APIRequestContext, type BrowserContext, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import { expectedStatusDropper, type Dropper } from '../topology/consoleGate'
import { consentCookie, consentRecordOf, consoleGate, routeProductionHosts, STUB_HEADER, watchStubLeaks } from './landingConsent'

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

type Sinks = { errors: string[]; leaked: string[]; pending: Promise<void>[] }

/** Granted consent, routing control, stub-leak sink and console gate; opens the routed landing. */
async function openRoutedLanding(page: Page, request: APIRequestContext, extraCookies: Parameters<BrowserContext['addCookies']>[0] = [], expected?: Dropper): Promise<Sinks> {
  await page.context().addCookies([consentCookie(true), ...extraCookies])
  await controlRouting(page, request)
  const errors = consoleGate(page, expected)
  const { leaked, pending } = watchStubLeaks(page)
  await page.goto(`${WWW}/`)
  await expect(page.getByRole('heading', { level: 1, name: 'Nigeria moves. Compliance keeps up.' })).toBeVisible()
  await expect
    .poll(() => page.evaluate(() => ((window as unknown as { dataLayer?: ArrayLike<unknown>[] }).dataLayer ?? []).map((e) => e[0])), 'the tag never loaded on the routed host')
    .toContain('config')
  return { errors, leaked, pending }
}

async function expectClean(page: Page, { errors, leaked, pending }: Sinks): Promise<void> {
  await page.waitForLoadState('networkidle')
  await Promise.all(pending)
  expect(leaked, 'a GA, Sentry or chat request was not answered by the stub').toEqual([])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
}

routedTest('landing analytics: the routed landing loads the tag and sends no event before a visitor acts', async ({ routed: page, request }) => {
  const sinks = await openRoutedLanding(page, request)
  await expectClean(page, sinks)
  expect(await gaEvents(page)).toEqual([])
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
  // targets sit 10+ points from each milestone; scrollHeight is read live at each step
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
  const gatewayPosts: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url() === `${GATEWAY_URL}/contacts/demo-request`) gatewayPosts.push(r.url())
  })
  // Chromium logs a non-2xx Forms stub reply as a console error
  const expected = formsStatus >= 400 ? expectedStatusDropper(page, formsStatus, /hsforms\.com/) : undefined
  const sinks = await openRoutedLanding(page, request, [], expected)

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
  return { sinks, formsPosts, gatewayPosts, gatewayReply, dialog }
}

routedTest('landing analytics: a submitted demo request sends one generate_lead', async ({ routed: page, request }) => {
  const run = await submitDemoLead(page, request, 200, true)
  expect((await run.gatewayReply!).status()).toBe(202) // handler: internal/gateway/contacts.go
  await expect(run.dialog.locator('#dm-success')).toBeVisible()
  expect(run.formsPosts).toHaveLength(1)
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
  const events = await gaEvents(page)
  expect(events.filter((e) => e.name === 'demo_submit_failed')).toEqual([{ name: 'demo_submit_failed', params: FORM_PARAMS }])
  expect(events.filter((e) => e.name === 'generate_lead')).toEqual([])
  expect(run.gatewayPosts, 'the form must stop before the gateway').toEqual([])
  await expectClean(page, run.sinks)
})

const GA_HOSTS = ['googletagmanager.com', 'google-analytics.com']
const isGaRequest = (rawUrl: string) => {
  const host = new URL(rawUrl).hostname.toLowerCase()
  return GA_HOSTS.some((d) => host === d || host.endsWith(`.${d}`))
}

routedTest('landing analytics: sign_up is held until Accept on the routed production landing', async ({ routed: page, request }) => {
  await controlRouting(page, request)
  const errors = consoleGate(page)
  const { leaked, pending } = watchStubLeaks(page)
  const gaRequests: string[] = []
  page.on('request', (r) => {
    if (isGaRequest(r.url())) gaRequests.push(r.url())
  })
  const registerReply = page.waitForResponse((r) => r.request().method() === 'POST' && r.url() === `${GATEWAY_URL}/auth/register`)
  await page.goto(`${WWW}/?register`)
  const dialog = page.getByRole('dialog', { name: 'Create an account' })
  await expect(dialog).toBeVisible()
  const id = randomUUID()
  await dialog.getByLabel('Work email', { exact: true }).fill(`land07-${id}@example.com`)
  await dialog.getByLabel('Password', { exact: true }).fill(id.slice(0, 16))
  await dialog.getByLabel('Your name', { exact: true }).fill('Landing Analytics')
  await dialog.getByLabel('Workspace name', { exact: true }).fill(`Landing Analytics ${id.slice(0, 8)}`)
  await dialog.getByRole('radio', { name: 'For our own company — in-house' }).check() // KINDS in frontend/landing/src/components/RegisterModal.tsx
  await dialog.getByRole('button', { name: 'Create account →' }).click()
  expect((await registerReply).status()).toBe(202) // handler: internal/gateway/register.go
  await expect(dialog.getByRole('heading', { name: 'Check your email', exact: true })).toBeVisible()
  expect(gaRequests, 'a GA request left before Accept').toEqual([])
  expect(await page.evaluate(() => typeof (window as unknown as { dataLayer?: unknown }).dataLayer)).toBe('undefined')

  await dialog.getByRole('button', { name: 'Close', exact: true }).last().click() // the header X is the first
  await expect(dialog).toBeHidden()
  await page.getByRole('region', { name: 'Cookie notice' }).getByRole('button', { name: 'Accept', exact: true }).click()
  await expect
    .poll(() => page.evaluate(() => ((window as unknown as { dataLayer?: ArrayLike<unknown>[] }).dataLayer ?? []).map((e) => e[0])), 'the tag never loaded after Accept')
    .toContain('config')
  const layer = await page.evaluate(() => ((window as unknown as { dataLayer?: ArrayLike<unknown>[] }).dataLayer ?? []).map((e) => Array.from(e)))
  const kinds = layer.map((e) => e[0])
  expect(kinds.indexOf('config')).toBeLessThan(kinds.indexOf('event'))
  expect(layer.filter((e) => e[0] === 'event')).toEqual([['event', 'sign_up', {}]])
  await page.waitForLoadState('networkidle')
  await Promise.all(pending)
  expect(leaked, 'a GA, Sentry or chat request was not answered by the stub').toEqual([])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

routedTest('landing analytics: email_verified is held until Accept on the routed production landing', async ({ routed: page, request }) => {
  await controlRouting(page, request)
  const errors = consoleGate(page)
  const { leaked, pending } = watchStubLeaks(page)
  const gaRequests: string[] = []
  page.on('request', (r) => {
    if (isGaRequest(r.url())) gaRequests.push(r.url())
  })
  const layer = () => page.evaluate(() => ((window as unknown as { dataLayer?: ArrayLike<unknown>[] }).dataLayer ?? []).map((e) => Array.from(e)))

  await page.goto(`${WWW}/?verified=1`)
  await expect(page.getByRole('status').filter({ hasText: 'Your email address is verified. Please sign in.' })).toBeVisible() // COPY.verified in VerifyNotice.tsx
  expect(gaRequests, 'a GA request left before Accept').toEqual([])
  expect(await page.evaluate(() => typeof (window as unknown as { dataLayer?: unknown }).dataLayer)).toBe('undefined')

  await page.getByRole('region', { name: 'Cookie notice' }).getByRole('button', { name: 'Accept', exact: true }).click()
  await expect.poll(async () => (await layer()).map((e) => e[0]), 'the tag never loaded after Accept').toContain('config')
  const sent = await layer()
  const kinds = sent.map((e) => e[0])
  expect(kinds.indexOf('config')).toBeLessThan(kinds.indexOf('event'))
  expect(sent.filter((e) => e[0] === 'event')).toEqual([['event', 'email_verified', {}]])

  // a failed verification loads the tag and sends no email_verified
  await page.goto(`${WWW}/?verify=failed`)
  await expect(page.getByRole('status').filter({ hasText: 'This link is already used or expired.' })).toBeVisible() // COPY.failed in VerifyNotice.tsx
  await expect.poll(async () => (await layer()).map((e) => e[0]), 'the tag never loaded on the failed page').toContain('config')
  expect((await layer()).filter((e) => e[0] === 'event' && e[1] === 'email_verified')).toEqual([])

  await page.waitForLoadState('networkidle')
  await Promise.all(pending)
  expect(leaked, 'a GA, Sentry or chat request was not answered by the stub').toEqual([])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})
