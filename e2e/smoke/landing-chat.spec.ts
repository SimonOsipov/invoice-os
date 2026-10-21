import { expect, test as base, type Locator, type Page, type Response } from '@playwright/test'
import { resolveTarget } from '../targets'
import { consoleGate, HUBSPOT_CHAT_DOMAINS, isStubbedHost, routeProductionHosts, STUB_HEADER } from './landingConsent'
import { isSentryHost } from './sentryHost'

// The HubSpot chat on the landing: dark on every preview host, loaded after Accept on the
// production host. Arms: (a) the PR host, unrouted; (b) the production hostnames routed to the
// fork with HubSpot stubbed; (c) the live site, skipped unless LANDING_URL is www.ascomply.com.

const LANDING_URL = resolveTarget('LANDING_URL')
const LIBRARY_URL = resolveTarget('LIBRARY_URL')
const LIVE = new URL(LANDING_URL).hostname.toLowerCase() === 'www.ascomply.com'
const WWW = 'https://www.ascomply.com'
const LIB = 'https://library.ascomply.com'
const LOADER_URL = 'https://js-eu1.hs-scripts.com/148915098.js' // CHAT_SCRIPT_URL in frontend/landing/src/hubspotChat.ts
const COOKIE_NAMES = ['__hstc', 'hubspotutk', '__hssrc', '__hssc', 'messagesUtk'] // HUBSPOT_COOKIE_NAMES in hubspotChat.ts
const OPT_OUT_COOKIE = '__hs_do_not_track'
const WIDGET = '#hubspot-messages-iframe-container'
const GOOGLE_DOMAINS = ['google.com', 'google-analytics.com', 'googletagmanager.com', 'doubleclick.net', 'gstatic.com', 'googleapis.com']

const consent = (analytics: boolean) => ({
  name: 'asc_consent',
  value: encodeURIComponent(JSON.stringify({ analytics, ts: new Date().toISOString(), v: 1 })),
  domain: '.ascomply.com',
  path: '/',
  secure: true,
  sameSite: 'Lax' as const,
})
const hubspotCookies = COOKIE_NAMES.map((name) => ({ name, value: 'x', domain: '.ascomply.com', path: '/', secure: true }))

const notice = (page: Page): Locator => page.getByRole('region', { name: 'Cookie notice' })
const landingChoices = (page: Page): Locator => page.getByRole('contentinfo').getByRole('button', { name: 'Cookie choices' })
const libraryChoices = (page: Page): Locator => page.locator('aside').getByRole('button', { name: 'Cookie choices' })
const hubspotNames = async (page: Page): Promise<string[]> =>
  (await page.context().cookies(WWW)).map((c) => c.name).filter((n) => COOKIE_NAMES.includes(n))

const isHubSpotHost = (rawUrl: string): boolean => {
  try {
    const host = new URL(rawUrl).hostname.toLowerCase()
    return HUBSPOT_CHAT_DOMAINS.some((d) => host === d || host.endsWith(`.${d}`))
  } catch {
    return false
  }
}

/** Collects the loader requests and their responses; attach before navigating. */
function watchLoader(page: Page): Response[] {
  const loaded: Response[] = []
  page.on('response', (res) => {
    if (res.url() === LOADER_URL) loaded.push(res)
  })
  return loaded
}

async function openWww(page: Page): Promise<void> {
  await page.goto(`${WWW}/`)
  await expect(page.getByRole('heading', { level: 1, name: 'Africa moves. Compliance keeps up.' })).toBeVisible()
}

const routedTest = base.extend<{ routed: Page }>({
  routed: async ({ browser }, use) => {
    const context = await browser.newContext()
    await routeProductionHosts(context, { landing: LANDING_URL, library: LIBRARY_URL })
    await use(await context.newPage())
    await context.unrouteAll({ behavior: 'ignoreErrors' })
    await context.close()
  },
})

/** A mismatch means the routing failed, not the chat code. */
async function controlRouting(page: Page): Promise<void> {
  const routed = await page.goto(`${WWW}/build.txt`)
  const direct = await page.request.get(`${LANDING_URL}/build.txt`)
  expect(direct.ok(), `routing, not chat: ${LANDING_URL}/build.txt did not answer 2xx`).toBeTruthy()
  const expected = (await direct.text()).trim()
  expect(expected, 'routing, not chat: the fork serves an empty /build.txt').not.toBe('')
  expect((await routed?.text())?.trim(), 'routing, not chat: www does not serve the fork build').toBe(expected)
}

base('landing chat: Accept on the PR host loads no HubSpot chat', async ({ page }) => {
  base.skip(LIVE, 'the production host loads the chat by design; arm (c) covers it')
  const errors = consoleGate(page)
  const hubspot: string[] = []
  page.on('request', (req) => {
    if (isHubSpotHost(req.url())) hubspot.push(req.url())
  })
  await page.goto(`${LANDING_URL}/`)
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await notice(page).getByRole('button', { name: 'Accept' }).click()
  await expect(notice(page)).toHaveCount(0)
  await page.waitForLoadState('networkidle')

  expect(await page.evaluate(() => window.localStorage.getItem('asc_consent')), 'Accept stored nothing, so the gate was never tested').toContain('"analytics":true')
  expect(hubspot, `the PR host requested HubSpot:\n${hubspot.join('\n')}`).toEqual([])
  await expect(page.locator('#hs-script-loader')).toHaveCount(0)
  await expect(page.locator(WIDGET)).toHaveCount(0)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

routedTest('landing chat: Accept on the production host requests the loader', async ({ routed: page }) => {
  await controlRouting(page)
  const errors = consoleGate(page)
  const loaded = watchLoader(page)
  const leaked: string[] = []
  page.on('requestfinished', async (r) => {
    if (isStubbedHost(r.url()) && (await r.response())?.headers()[STUB_HEADER] !== '1') leaked.push(r.url())
  })
  await openWww(page)
  await notice(page).getByRole('button', { name: 'Accept' }).click()
  await expect(notice(page)).toHaveCount(0)

  await expect.poll(() => loaded.length, 'Accept never requested the loader').toBe(1)
  expect(loaded[0].headers()[STUB_HEADER], 'the loader was not answered by the stub').toBe('1')
  await expect(page.locator('#hs-script-loader')).toHaveCount(1)
  await page.waitForLoadState('networkidle')
  expect(loaded).toHaveLength(1)
  expect(leaked, 'a stubbed host was not answered by the stub').toEqual([])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

routedTest('landing chat: a returning visitor gets the loader at boot', async ({ routed: page }) => {
  await controlRouting(page)
  const loaded = watchLoader(page)
  await page.context().addCookies([consent(true)])
  await openWww(page)
  await expect(notice(page)).toHaveCount(0)
  await expect.poll(() => loaded.length, 'a granted record did not request the loader').toBe(1)
})

routedTest('landing chat: Reject after Accept deletes the HubSpot cookies on the parent domain', async ({ routed: page }) => {
  await controlRouting(page)
  const errors = consoleGate(page)
  const loaded = watchLoader(page)
  await page.context().addCookies([consent(true), ...hubspotCookies])
  await openWww(page)
  await expect.poll(() => loaded.length, 'control: the chat never loaded, so Reject has nothing to remove').toBe(1)
  expect(await hubspotNames(page), 'control: the five cookies must exist before Reject').toHaveLength(COOKIE_NAMES.length)

  await landingChoices(page).click()
  await notice(page).getByRole('button', { name: 'Reject' }).click()

  await expect.poll(() => hubspotNames(page), 'Reject left HubSpot cookies behind').toEqual([])
  const queue = await page.evaluate(() => (window as unknown as { _hsq?: unknown[] })._hsq ?? [])
  expect(JSON.stringify(queue), 'Reject did not push doNotTrack').toContain('["doNotTrack"]')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

routedTest('landing chat: a denied record requests no loader', async ({ routed: page }) => {
  await controlRouting(page)
  const loaded = watchLoader(page)
  await page.context().addCookies([consent(false)])
  await openWww(page)
  await expect(notice(page)).toHaveCount(0)
  await page.waitForLoadState('networkidle')
  expect(loaded, 'a denied record requested the loader').toHaveLength(0)
  await expect(page.locator('#hs-script-loader')).toHaveCount(0)
})

routedTest('landing chat: Reject on the routed Library deletes the shared HubSpot cookies', async ({ routed: page }) => {
  await controlRouting(page)
  await page.context().addCookies([consent(true), ...hubspotCookies])
  await page.goto(`${LIB}/`)
  await expect(page.locator('#lib-main').getByRole('heading').first()).toBeVisible()
  expect(await hubspotNames(page), 'control: the five cookies must exist before Reject').toHaveLength(COOKIE_NAMES.length)

  await libraryChoices(page).click()
  await notice(page).getByRole('button', { name: 'Reject' }).click()
  await expect.poll(() => hubspotNames(page), 'the Library Reject left HubSpot cookies behind').toEqual([])
})

base.describe('live', () => {
  base.skip(!LIVE, 'live arm: runs only when LANDING_URL is https://www.ascomply.com')

  base('landing chat: live — Accept shows the widget and Reject removes it', async ({ page }) => {
    const loaded = watchLoader(page)
    await openWww(page)
    await notice(page).getByRole('button', { name: 'Accept' }).click()
    await expect.poll(() => loaded.length, 'Accept never requested the loader').toBeGreaterThan(0)
    await expect(page.locator(WIDGET)).toBeVisible({ timeout: 15_000 })

    await landingChoices(page).click()
    await notice(page).getByRole('button', { name: 'Reject' }).click()
    await expect(page.locator(WIDGET)).toHaveCount(0)
    await expect.poll(() => hubspotNames(page)).toEqual([])
  })

  base('landing chat: live — Reject, reload, Accept lifts the opt-out', async ({ page }) => {
    await openWww(page)
    await notice(page).getByRole('button', { name: 'Accept' }).click()
    await expect(page.locator(WIDGET)).toBeVisible({ timeout: 15_000 })
    await landingChoices(page).click()
    await notice(page).getByRole('button', { name: 'Reject' }).click()
    await expect(page.locator(WIDGET)).toHaveCount(0)

    await page.reload()
    await landingChoices(page).click()
    await notice(page).getByRole('button', { name: 'Accept' }).click()
    await expect
      .poll(async () => (await page.context().cookies(WWW)).some((c) => c.name === OPT_OUT_COOKIE), { timeout: 10_000 })
      .toBe(false)
    await expect(page.locator(WIDGET)).toBeVisible({ timeout: 15_000 })
  })

  base('landing chat: live — every HubSpot host the page contacts is named on the privacy page', async ({ page }) => {
    const hosts = new Set<string>()
    page.on('request', (req) => {
      try {
        hosts.add(new URL(req.url()).hostname.toLowerCase())
      } catch {
        // data: and blob: URLs have no host to name
      }
    })
    await openWww(page)
    await notice(page).getByRole('button', { name: 'Accept' }).click()
    await page.waitForLoadState('networkidle')

    const lastTwo = (host: string): string => host.split('.').slice(-2).join('.')
    const named = [...hosts].filter(
      (h) => !h.endsWith('.ascomply.com') && h !== 'ascomply.com' && !GOOGLE_DOMAINS.includes(lastTwo(h)) && !isSentryHost(`https://${h}/`),
    )
    expect(named.some(isHubSpotHost), 'control: no HubSpot host was recorded').toBe(true)

    await page.goto(`${WWW}/privacy`)
    const text = await page.locator('body').innerText()
    const missing = named.filter((h) => !text.includes(lastTwo(h)))
    expect(missing, `contacted hosts not named on /privacy:\n${missing.join('\n')}`).toEqual([])
  })
})
