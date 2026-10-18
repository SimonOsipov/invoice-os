import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import { enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from '../topology/layout'
import { consentRecordOf, consoleGate, routeProductionHosts, seedConsent } from './landingConsent'

// The consent notice and GA4 tag on the deployed blog. The tag loads after Accept on the
// production blog host only; on a fork it stays dark whatever the visitor answers.
// Relationship assertions only (topology/layout.ts). The SC tests route the production hostnames
// to the fork to prove the shared cookie.

const BLOG_URL = resolveTarget('BLOG_URL')
const BLOG_HOST = new URL(BLOG_URL).hostname.toLowerCase()
const BLOG_PRODUCTION_HOST = 'blog.ascomply.com' // retyped from BLOG_HOSTNAMES in frontend/landing/src/hubspot.ts
const EXPECT_TAG = BLOG_HOST === BLOG_PRODUCTION_HOST
const LANDING_URL = resolveTarget('LANDING_URL')
const LIBRARY_URL = resolveTarget('LIBRARY_URL') // routeProductionHosts routes it too
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
const breadcrumb = (page: Page): Locator => page.getByRole('navigation', { name: 'Breadcrumb' })

async function openBlog(page: Page, path = '/', expectNotice = true): Promise<void> {
  const response = await page.goto(`${BLOG_URL}${path}`)
  expect(response?.ok(), `${BLOG_URL}${path} did not answer 2xx`).toBeTruthy()
  await expect(breadcrumb(page)).toBeVisible()
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

test('blog consent: the notice mounts with no answer and not with one', async ({ page }) => {
  await openBlog(page)
  await expect(notice(page).getByRole('button', { name: 'Accept' })).toBeVisible()
  await expect(notice(page).getByRole('button', { name: 'Reject' })).toBeVisible()

  await seedConsent(page, false)
  await page.reload()
  await expect(breadcrumb(page)).toBeVisible()
  await expect(notice(page)).toHaveCount(0)
  const record = await consentRecordOf(page)
  expect(record, 'the seed never landed, so the count-0 assertion proved nothing').toContain('"analytics":false')
})

test('blog consent: Accept is stored on the blog origin', async ({ page }) => {
  await openBlog(page)
  await notice(page).getByRole('button', { name: 'Accept' }).click()
  await expect(notice(page)).toHaveCount(0)

  const record = await consentRecordOf(page)
  expect(record, 'Accept stored nothing on the blog origin').not.toBeNull()
  expect(JSON.parse(record!)).toMatchObject({ analytics: true, v: 1 })

  await page.reload()
  await expect(breadcrumb(page)).toBeVisible()
  await expect(notice(page)).toHaveCount(0)
})

test('blog consent: the policy link goes to the landing privacy page', async ({ page }) => {
  await openBlog(page)
  const href = await notice(page).getByRole('link', { name: 'Read the privacy & cookie policy' }).getAttribute('href')
  expect(href, 'the notice has no policy link').not.toBeNull()
  const policy = new URL(href!) // throws on a relative href: the link must be absolute
  expect(policy.pathname).toBe('/privacy')

  const home = await page.locator('aside').getByRole('link', { name: 'Go to ascomply.com' }).getAttribute('href')
  expect(home, 'the sidebar has no Go to ascomply.com link').not.toBeNull()
  expect(policy.origin, 'the policy link is not on the origin of the Go to ascomply.com link').toBe(new URL(home!).origin)

  await page.goto(policy.href)
  await expect(page.getByRole('heading', { level: 1, name: 'Privacy and cookies' })).toBeVisible()
})

test('blog consent: the tag is requested after Accept on the production host only', async ({ page }) => {
  // Control: the predicate sees the tag host and refuses the font host.
  expect(isTagRequest('https://www.googletagmanager.com/gtag/js?id=x')).toBe(true)
  expect(isTagRequest('https://fonts.googleapis.com/css2?family=Manrope')).toBe(false)

  await openBlog(page)
  const src = await page.locator('script[type="module"][src]').first().getAttribute('src')
  expect(src, 'index.html has no module entry script').not.toBeNull()
  const res = await page.request.get(new URL(src!, page.url()).href)
  expect(res.status()).toBe(200)
  expect(await res.text(), 'the fork bakes no GA4 id; the tag check would be vacuous').toMatch(GA_ID)

  const tagRequests: string[] = []
  page.on('request', (req) => {
    if (isTagRequest(req.url())) tagRequests.push(req.url())
  })
  await openBlog(page)
  await page.waitForLoadState('networkidle')
  await notice(page).getByRole('button', { name: 'Accept' }).click()
  await expect(notice(page)).toHaveCount(0)
  const stored = await consentRecordOf(page)
  expect(stored, 'Accept stored no consent, so the post-Accept request check would be vacuous').toContain('"analytics":true')
  await openBlog(page, '/subscribe', false)
  await page.waitForLoadState('networkidle')
  if (EXPECT_TAG) {
    await expect.poll(() => tagRequests.length, 'the production blog never requested the GA4 tag after Accept').toBeGreaterThan(0)
  } else {
    expect(tagRequests, 'the blog requested the GA4 tag on a non-production host').toEqual([])
  }
})

for (const width of WIDE_WIDTHS) {
  test(`blog consent: the notice sits in the viewport clear of the sidebar (${width})`, async ({ page }) => {
    await setWidth(page, width)
    await openBlog(page)
    const box = await rectOf(notice(page), 'the cookie notice')
    const aside = await rectOf(page.locator('aside'), 'the sidebar')
    expect(enclosesRect({ x: 0, y: 0, width, height: 1080 }, box, SLACK_PX), `the notice leaves the ${width}x1080 viewport`).toBe(true)
    expect(rectsOverlap(box, aside), 'the notice overlaps the sidebar').toBe(false)
  })

  test(`blog consent: Cookie choices sits in the sidebar footer (${width})`, async ({ page }) => {
    await setWidth(page, width)
    await seedConsent(page, true)
    await openBlog(page, '/', false)
    const aside = page.locator('aside')
    const control = aside.getByRole('button', { name: 'Cookie choices' })
    const c = await rectOf(control, 'Cookie choices')
    expect(enclosesRect(await rectOf(aside, 'the sidebar'), c, SLACK_PX), 'Cookie choices is outside the sidebar').toBe(true)

    const home = await rectOf(aside.getByRole('link', { name: 'Go to ascomply.com' }), 'Go to ascomply.com')
    expect(home.y + home.height, 'Go to ascomply.com is not above Cookie choices').toBeLessThanOrEqual(c.y + SLACK_PX)
    expect(rectsOverlap(home, c), 'Cookie choices overlaps Go to ascomply.com').toBe(false)

    await control.click()
    await expect(notice(page).locator('.cn-setting')).toBeVisible()
    const n = await rectOf(notice(page), 'the reopened notice')
    expect(rectsOverlap(c, n), 'the reopened notice covers Cookie choices').toBe(false)
  })
}

// Shared choice, under the production hostnames served by the fork (E2E rules: no real GA or Sentry).
const WWW = 'https://www.ascomply.com'
const BLOG = 'https://blog.ascomply.com'
const COOKIE_NAME = 'asc_consent'

const routedTest = test.extend<{ routed: Page }>({
  routed: async ({ browser }, use) => {
    const context = await browser.newContext()
    await routeProductionHosts(context, { landing: LANDING_URL, library: LIBRARY_URL, blog: BLOG_URL })
    await use(await context.newPage())
    // in-flight route.fetch calls reject once the context closes
    await context.unrouteAll({ behavior: 'ignoreErrors' })
    await context.close()
  },
})

async function consentCookies(page: Page) {
  return (await page.context().cookies()).filter((c) => c.name === COOKIE_NAME)
}

async function openWww(page: Page): Promise<void> {
  await page.goto(`${WWW}/`)
  await expect(page.getByRole('heading', { level: 1, name: 'Africa moves. Compliance keeps up.' })).toBeVisible()
}

async function openRoutedBlog(page: Page): Promise<void> {
  await page.goto(`${BLOG}/`)
  await expect(breadcrumb(page)).toBeVisible()
}

/** SC-00: a mismatch means the routing failed, not the consent code. */
async function controlRouting(page: Page, request: APIRequestContext): Promise<void> {
  for (const [host, origin] of [
    [WWW, LANDING_URL],
    [BLOG, BLOG_URL],
  ] as const) {
    const routed = await page.goto(`${host}/build.txt`)
    const direct = await request.get(`${origin}/build.txt`)
    expect(direct.ok(), `routing, not consent: ${origin}/build.txt did not answer 2xx`).toBeTruthy()
    const expected = (await direct.text()).trim()
    expect(expected, 'routing, not consent: the fork serves an empty /build.txt').not.toBe('')
    expect((await routed?.text())?.trim(), `routing, not consent: ${host} does not serve the fork's build`).toBe(expected)
  }
}

routedTest('SC-00 shared consent: the production hostnames serve the fork (blog)', async ({ routed, request }) => {
  await controlRouting(routed, request)
})

routedTest('SC-01 shared consent: Accept on the landing applies on the blog', async ({ routed: page, request }) => {
  await controlRouting(page, request)
  const errors = consoleGate(page)

  await openWww(page)
  await expect(notice(page)).toBeVisible()
  await notice(page).getByRole('button', { name: 'Accept' }).click()
  await expect(notice(page)).toHaveCount(0)

  await openRoutedBlog(page)
  await expect(notice(page)).toHaveCount(0)
  const cookies = await consentCookies(page)
  expect(cookies, 'Accept must store exactly one asc_consent cookie').toHaveLength(1)
  expect(cookies[0]).toMatchObject({ domain: '.ascomply.com', secure: true, sameSite: 'Lax' })
  expect(JSON.parse((await consentRecordOf(page))!)).toMatchObject({ analytics: true, v: 1 })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

routedTest('SC-02 shared consent: Reject on the blog is stored for the landing', async ({ routed: page, request }) => {
  await controlRouting(page, request)
  const errors = consoleGate(page)
  expect(await consentCookies(page), 'control: the jar must start empty').toEqual([])

  await openRoutedBlog(page)
  await expect(notice(page)).toBeVisible()
  await notice(page).getByRole('button', { name: 'Reject' }).click()
  await expect(notice(page)).toHaveCount(0)

  const cookies = await consentCookies(page)
  expect(cookies, 'Reject must store exactly one asc_consent cookie').toHaveLength(1)
  expect(cookies[0]).toMatchObject({ domain: '.ascomply.com', secure: true, sameSite: 'Lax' })
  expect(JSON.parse(decodeURIComponent(cookies[0].value))).toMatchObject({ analytics: false, v: 1 })

  await openWww(page)
  await expect(notice(page)).toHaveCount(0)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})
