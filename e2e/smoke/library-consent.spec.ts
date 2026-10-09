import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import { enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from '../topology/layout'
import { consentRecordOf, consoleGate, isStubbedHost, routeProductionHosts, seedConsent, STUB_HEADER } from './landingConsent'

// The consent notice and GA4 tag on the deployed Feature Library. The tag loads after Accept on
// the production library host only; on a fork it stays dark whatever the visitor answers.
// Relationship assertions only (topology/layout.ts); forks keep consent in per-origin storage,
// so parallel tests cannot reach each other. The SC tests route the production hostnames to the
// fork to prove the shared cookie.

const LIBRARY_URL = resolveTarget('LIBRARY_URL')
const LIBRARY_HOST = new URL(LIBRARY_URL).hostname.toLowerCase()
const LIBRARY_PRODUCTION_HOST = 'library.ascomply.com' // retyped from LIBRARY_HOSTNAMES in frontend/landing/src/hubspot.ts
const EXPECT_TAG = LIBRARY_HOST === LIBRARY_PRODUCTION_HOST
const LANDING_URL = resolveTarget('LANDING_URL')
const LANDING_FALLBACK_ORIGIN = 'https://www.ascomply.com'
const GA_ID = /G-[A-Z0-9]{6,}/
const SLACK_PX = 0.5 // sub-pixel rounding only

const isTagRequest = (rawUrl: string): boolean => {
  try {
    return new URL(rawUrl).hostname.toLowerCase() === 'www.googletagmanager.com'
  } catch {
    return false
  }
}

const notice = (page: Page): Locator => page.getByRole('region', { name: 'Cookie notice' })

async function openLibrary(page: Page, path = '/', expectNotice = true): Promise<void> {
  const response = await page.goto(`${LIBRARY_URL}${path}`)
  expect(response?.ok(), `${LIBRARY_URL}${path} did not answer 2xx`).toBeTruthy()
  // Only the home route has an h1; group and feature pages open on an h2.
  await expect(page.locator('#lib-main').getByRole('heading').first()).toBeVisible()
  if (expectNotice) await expect(notice(page)).toBeVisible()
  else await expect(notice(page)).toHaveCount(0)
  await page.evaluate(() => document.fonts.ready.then(() => true))
}

async function rectOf(locator: Locator, label: string): Promise<Rect> {
  await settleAnimations(locator)
  const box = await locator.boundingBox()
  expect(box, `${label} did not render`).toBeTruthy()
  return box!
}

async function setWidth(page: Page, width: number): Promise<void> {
  await page.setViewportSize({ width, height: 1080 })
  await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
}

test('library consent: the notice mounts with no answer and not with one', async ({ page }) => {
  await openLibrary(page)
  await expect(notice(page).getByRole('button', { name: 'Accept' })).toBeVisible()
  await expect(notice(page).getByRole('button', { name: 'Reject' })).toBeVisible()

  await seedConsent(page, false)
  await page.reload()
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await expect(notice(page)).toHaveCount(0)
  const record = await consentRecordOf(page)
  expect(record, 'the seed never landed, so the count-0 assertion proved nothing').toContain('"analytics":false')
})

test('library consent: Accept is stored on the library origin', async ({ page }) => {
  await openLibrary(page)
  await notice(page).getByRole('button', { name: 'Accept' }).click()
  await expect(notice(page)).toHaveCount(0)

  const record = await consentRecordOf(page)
  expect(record, 'Accept stored nothing on the library origin').not.toBeNull()
  expect(JSON.parse(record!)).toMatchObject({ analytics: true, v: 1 })

  await page.reload()
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await expect(notice(page)).toHaveCount(0)
})

test('library consent: the policy link goes to the landing privacy page', async ({ page }) => {
  await openLibrary(page)
  const href = await notice(page).getByRole('link', { name: 'Read the privacy & cookie policy' }).getAttribute('href')
  expect(href, 'the notice has no policy link').not.toBeNull()
  const policy = new URL(href!) // throws on a relative href: the link must be absolute
  expect(policy.pathname).toBe('/privacy')

  const demo = page.getByRole('link', { name: 'Book the Demo' })
  const demoHref = (await demo.count()) > 0 ? await demo.first().getAttribute('href') : null
  const expectedOrigin = demoHref === null ? LANDING_FALLBACK_ORIGIN : new URL(demoHref).origin
  expect(policy.origin, 'the policy link is not on the origin of the Book the Demo link').toBe(expectedOrigin)

  await page.goto(policy.href)
  await expect(page.getByRole('heading', { level: 1, name: 'Privacy and cookies' })).toBeVisible()
})

test('library consent: the tag is requested after Accept on the production host only', async ({ page }) => {
  // Control: the predicate sees the tag host and refuses the font host.
  expect(isTagRequest('https://www.googletagmanager.com/gtag/js?id=x')).toBe(true)
  expect(isTagRequest('https://fonts.googleapis.com/css2?family=Manrope')).toBe(false)

  await openLibrary(page)
  const src = await page.locator('script[type="module"][src]').first().getAttribute('src')
  expect(src, 'index.html has no module entry script').not.toBeNull()
  const res = await page.request.get(new URL(src!, page.url()).href)
  expect(res.status()).toBe(200)
  expect(await res.text(), 'the fork bakes no GA4 id; the tag check would be vacuous').toMatch(GA_ID)

  const tagRequests: string[] = []
  page.on('request', (req) => {
    if (isTagRequest(req.url())) tagRequests.push(req.url())
  })
  await openLibrary(page)
  await page.waitForLoadState('networkidle')
  await notice(page).getByRole('button', { name: 'Accept' }).click()
  await expect(notice(page)).toHaveCount(0)
  const stored = await consentRecordOf(page)
  expect(stored, 'Accept stored no consent, so the post-Accept request check would be vacuous').toContain('"analytics":true')
  await openLibrary(page, '/rules', false)
  await page.waitForLoadState('networkidle')
  if (EXPECT_TAG) {
    await expect.poll(() => tagRequests.length, 'the production library never requested the GA4 tag after Accept').toBeGreaterThan(0)
  } else {
    expect(tagRequests, 'the library requested the GA4 tag on a non-production host').toEqual([])
  }
})

for (const width of WIDE_WIDTHS) {
  test(`library consent: the notice sits in the viewport clear of the sidebar (${width})`, async ({ page }) => {
    await setWidth(page, width)
    await openLibrary(page)
    const box = await rectOf(notice(page), 'the cookie notice')
    const aside = await rectOf(page.locator('aside'), 'the sidebar')
    expect(enclosesRect({ x: 0, y: 0, width, height: 1080 }, box, SLACK_PX), `the notice leaves the ${width}x1080 viewport`).toBe(true)
    expect(rectsOverlap(box, aside), 'the notice overlaps the sidebar').toBe(false)
  })

  test(`library consent: the spacer lets the band clear the notice (${width})`, async ({ page }) => {
    await setWidth(page, width)
    await openLibrary(page)
    const main = page.locator('#lib-main')
    await main.evaluate((el) => el.scrollTo({ top: el.scrollHeight, behavior: 'instant' }))
    await expect
      .poll(() => main.evaluate((el) => Math.round(el.scrollHeight - el.clientHeight - el.scrollTop)))
      .toBeLessThanOrEqual(1)
    const heading = page.getByRole('heading', { level: 2, name: /See it on your own invoices\./ })
    await expect(heading).toHaveCount(1)
    const h = await rectOf(heading, 'the home band heading')
    const n = await rectOf(notice(page), 'the cookie notice')
    expect(h.y + h.height, `the band heading (bottom ${h.y + h.height}) sits under the notice (top ${n.y})`).toBeLessThanOrEqual(n.y + SLACK_PX)
  })

  test(`library consent: Cookie choices sits in the sidebar footer (${width})`, async ({ page }) => {
    await setWidth(page, width)
    await seedConsent(page, true)
    await openLibrary(page, '/', false)
    const aside = page.locator('aside')
    const control = aside.getByRole('button', { name: 'Cookie choices' })
    const c = await rectOf(control, 'Cookie choices')
    expect(enclosesRect(await rectOf(aside, 'the sidebar'), c, SLACK_PX), 'Cookie choices is outside the sidebar').toBe(true)

    const demo = aside.getByRole('link', { name: 'Book the Demo' })
    if ((await demo.count()) > 0) {
      const d = await rectOf(demo, 'Book the Demo')
      expect(d.y + d.height, 'Book the Demo is not above Cookie choices').toBeLessThanOrEqual(c.y + SLACK_PX)
      expect(rectsOverlap(d, c), 'Cookie choices overlaps Book the Demo').toBe(false)
    }

    await control.click()
    await expect(notice(page).locator('.cn-setting')).toBeVisible()
    const n = await rectOf(notice(page), 'the reopened notice')
    expect(rectsOverlap(c, n), 'the reopened notice covers Cookie choices').toBe(false)
  })
}

// The phone card sits 12px above the viewport bottom; the band must reserve card plus inset.
const PHONE_INSET_PX = 12
for (const width of [390, 375]) {
  test(`library consent: on a phone the spacer reserves the notice band (${width})`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 })
    await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
    await openLibrary(page)
    const box = await rectOf(notice(page), 'the cookie notice')
    expect(enclosesRect({ x: 0, y: 0, width, height: 844 }, box, SLACK_PX), `the notice leaves the ${width}x844 viewport`).toBe(true)
    const spacer = await rectOf(page.locator('.cn-spacer'), 'the scroll spacer')
    expect(
      spacer.height,
      `the spacer reserves ${spacer.height}px but the notice covers ${box.height + PHONE_INSET_PX}px`,
    ).toBeGreaterThanOrEqual(box.height + PHONE_INSET_PX)
  })
}

// Shared choice, under the production hostnames served by the fork (E2E rules: no real GA or Sentry).
const WWW = 'https://www.ascomply.com'
const LIB = 'https://library.ascomply.com'
const COOKIE_DAYS = 400
const DAY_S = 24 * 60 * 60
const GA_NAME = /^_ga(_|$)/

const routedTest = test.extend<{ routed: Page }>({
  routed: async ({ browser }, use) => {
    const context = await browser.newContext()
    await routeProductionHosts(context, { landing: LANDING_URL, library: LIBRARY_URL })
    await use(await context.newPage())
    await context.close()
  },
})

const grant = (ts = new Date().toISOString()): string => encodeURIComponent(JSON.stringify({ analytics: true, ts, v: 1 }))
const grantCookie = { name: 'asc_consent', value: grant(), domain: '.ascomply.com', path: '/', secure: true, sameSite: 'Lax' as const }

async function cookiesNamed(page: Page, name: string | RegExp, url?: string) {
  const all = await page.context().cookies(url)
  return all.filter((c) => (typeof name === 'string' ? c.name === name : name.test(c.name)))
}

async function openWww(page: Page): Promise<void> {
  await page.goto(`${WWW}/`)
  await expect(page.getByRole('heading', { level: 1, name: 'Africa moves. Compliance keeps up.' })).toBeVisible()
}

async function openRoutedLibrary(page: Page): Promise<void> {
  await page.goto(`${LIB}/`)
  await expect(page.locator('#lib-main').getByRole('heading').first()).toBeVisible()
}

const landingChoices = (page: Page): Locator => page.getByRole('contentinfo').getByRole('button', { name: 'Cookie choices' })
const libraryChoices = (page: Page): Locator => page.locator('aside').getByRole('button', { name: 'Cookie choices' })

/** SC-00: a mismatch means the routing failed, not the consent code. */
async function controlRouting(page: Page, request: APIRequestContext): Promise<void> {
  for (const [host, origin] of [
    [WWW, LANDING_URL],
    [LIB, LIBRARY_URL],
  ] as const) {
    const routed = await page.goto(`${host}/build.txt`)
    const direct = await request.get(`${origin}/build.txt`)
    expect(direct.ok(), `routing, not consent: ${origin}/build.txt did not answer 2xx`).toBeTruthy()
    const expected = (await direct.text()).trim()
    expect(expected, 'routing, not consent: the fork serves an empty /build.txt').not.toBe('')
    expect((await routed?.text())?.trim(), `routing, not consent: ${host} does not serve the fork's build`).toBe(expected)
  }
  await openWww(page)
}

routedTest('SC-00 shared consent: the production hostnames serve the fork', async ({ routed, request }) => {
  await controlRouting(routed, request)
})

routedTest('SC-01 shared consent: Accept on the landing applies on the Library', async ({ routed: page, request }) => {
  await controlRouting(page, request)
  const errors = consoleGate(page)
  const stubbed: string[] = []
  const leaked: string[] = []
  page.on('request', (r) => {
    if (isStubbedHost(r.url())) stubbed.push(r.url())
  })
  page.on('requestfinished', async (r) => {
    if (isStubbedHost(r.url()) && (await r.response())?.headers()[STUB_HEADER] !== '1') leaked.push(r.url())
  })

  await openWww(page)
  await expect(notice(page)).toBeVisible()
  await notice(page).getByRole('button', { name: 'Accept' }).click()
  await expect(notice(page)).toHaveCount(0)

  const cookies = await cookiesNamed(page, 'asc_consent')
  expect(cookies, 'Accept must store exactly one asc_consent cookie').toHaveLength(1)
  expect(cookies[0]).toMatchObject({ domain: '.ascomply.com', secure: true, sameSite: 'Lax' })
  const wantExpires = Date.now() / 1000 + COOKIE_DAYS * DAY_S
  expect(Math.abs(cookies[0].expires - wantExpires)).toBeLessThan(DAY_S)
  expect(JSON.parse(decodeURIComponent(cookies[0].value))).toMatchObject({ analytics: true, v: 1 })
  await expect.poll(() => stubbed.length, 'the stubbed tag was never requested, so the leak check proves nothing').toBeGreaterThan(0)

  await openRoutedLibrary(page)
  await expect(notice(page)).toHaveCount(0)
  await libraryChoices(page).click()
  await expect(notice(page).locator('.cn-setting')).toHaveText('Analytics cookies are on.')
  await notice(page).getByRole('button', { name: 'Reject' }).click()

  await openWww(page)
  await expect(notice(page)).toHaveCount(0)
  await landingChoices(page).click()
  await expect(notice(page).locator('.cn-setting')).toHaveText('Analytics cookies are off.')

  await page.waitForLoadState('networkidle')
  expect(leaked, 'a GA or Sentry request was not answered by the stub').toEqual([])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

routedTest('SC-02 shared consent: two old per-origin answers settle on the later one', async ({ routed: page, request }) => {
  await controlRouting(page, request)
  const errors = consoleGate(page)
  const WWW_TS = '2026-01-01T00:00:00.000Z'
  const LIB_TS = '2026-02-01T00:00:00.000Z'
  // Seeds each origin once, so a later visit cannot resurrect the record the app removed.
  await page.addInitScript(
    ([wwwTs, libTs]) => {
      try {
        if (window.sessionStorage.getItem('seeded')) return
        window.sessionStorage.setItem('seeded', '1')
        const www = location.hostname === 'www.ascomply.com'
        const lib = location.hostname === 'library.ascomply.com'
        if (www || lib) {
          window.localStorage.setItem('asc_consent', JSON.stringify({ analytics: www, ts: www ? wwwTs : libTs, v: 1 }))
        }
      } catch {
        // about:blank has an opaque origin; the real navigation re-runs this.
      }
    },
    [WWW_TS, LIB_TS],
  )
  const localRecord = (): Promise<string | null> => page.evaluate(() => window.localStorage.getItem('asc_consent'))

  await openWww(page)
  await expect(notice(page)).toHaveCount(0)
  expect(JSON.parse((await consentRecordOf(page))!)).toMatchObject({ analytics: true, ts: WWW_TS })
  expect(await localRecord(), 'www localStorage still holds asc_consent').toBeNull()

  await openRoutedLibrary(page)
  await expect(notice(page)).toHaveCount(0)
  expect(JSON.parse((await consentRecordOf(page))!)).toMatchObject({ analytics: false, ts: LIB_TS })
  expect(await localRecord(), 'library localStorage still holds asc_consent').toBeNull()

  await openWww(page)
  await expect(notice(page)).toHaveCount(0)
  await landingChoices(page).click()
  await expect(notice(page).locator('.cn-setting')).toHaveText('Analytics cookies are off.')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

routedTest('SC-03 shared consent: Reject on the Library deletes the shared and the leftover _ga', async ({ routed: page, request }) => {
  await controlRouting(page, request)
  const errors = consoleGate(page)
  await page.context().addCookies([
    { name: '_ga', value: 'GA1.1.1.1', domain: '.ascomply.com', path: '/', secure: true },
    { name: '_ga', value: 'GA1.1.2.2', url: LIB, secure: true },
    { name: '_ga_TEST', value: 'GS1.1.3', domain: '.ascomply.com', path: '/', secure: true },
    grantCookie,
  ])

  await openRoutedLibrary(page)
  expect(await cookiesNamed(page, GA_NAME), 'control: the three _ga cookies must exist before Reject').toHaveLength(3)
  await libraryChoices(page).click()
  await notice(page).getByRole('button', { name: 'Reject' }).click()

  await expect.poll(async () => (await cookiesNamed(page, GA_NAME)).map((c) => `${c.name}@${c.domain}`)).toEqual([])
  expect(JSON.parse((await consentRecordOf(page))!)).toMatchObject({ analytics: false })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

routedTest('SC-04 shared consent: a landing Reject reaches the leftover _ga at the next Library load', async ({ routed: page, request }) => {
  await controlRouting(page, request)
  const errors = consoleGate(page)
  await page.context().addCookies([grantCookie, { name: '_ga', value: 'GA1.1.2.2', url: LIB, secure: true }])

  await openWww(page)
  await expect(notice(page)).toHaveCount(0)
  await landingChoices(page).click()
  await notice(page).getByRole('button', { name: 'Reject' }).click()
  await expect(notice(page)).toHaveCount(0)
  const leftover = await cookiesNamed(page, '_ga', LIB)
  expect(leftover.map((c) => c.domain), 'control: a landing Reject cannot reach the library-host _ga').toEqual(['library.ascomply.com'])

  await openRoutedLibrary(page)
  await expect(notice(page)).toHaveCount(0)
  await expect.poll(async () => (await cookiesNamed(page, GA_NAME)).length, 'the Library did not expire the leftover _ga').toBe(0)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})
