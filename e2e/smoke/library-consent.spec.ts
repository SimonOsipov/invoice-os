import { expect, test, type Locator, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import { enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from '../topology/layout'
import { seedConsent } from './landingConsent'

// The consent notice and dark GA4 tag on the deployed Feature Library. The library host is not
// the production allowlist, so the tag stays dark on a fork whatever the visitor answers.
// Relationship assertions only (topology/layout.ts); consent lives in per-origin localStorage,
// so parallel tests cannot reach each other.

const LIBRARY_URL = resolveTarget('LIBRARY_URL')
const LANDING_FALLBACK_ORIGIN = 'https://www.ascomply.com'
const CONSENT_KEY = 'asc_consent' // retyped from frontend/landing/src/consent.ts
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
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
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
  const record = await page.evaluate((key) => window.localStorage.getItem(key), CONSENT_KEY)
  expect(record, 'the seed never landed, so the count-0 assertion proved nothing').toContain('"analytics":false')
})

test('library consent: Accept is stored on the library origin', async ({ page }) => {
  await openLibrary(page)
  await notice(page).getByRole('button', { name: 'Accept' }).click()
  await expect(notice(page)).toHaveCount(0)

  const record = await page.evaluate((key) => window.localStorage.getItem(key), CONSENT_KEY)
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

test('library consent: the fork bakes an id and still never requests the tag', async ({ page }) => {
  // Control: the predicate sees the tag host and refuses the font host.
  expect(isTagRequest('https://www.googletagmanager.com/gtag/js?id=x')).toBe(true)
  expect(isTagRequest('https://fonts.googleapis.com/css2?family=Manrope')).toBe(false)

  await openLibrary(page)
  const src = await page.locator('script[type="module"][src]').first().getAttribute('src')
  expect(src, 'index.html has no module entry script').not.toBeNull()
  const res = await page.request.get(new URL(src!, page.url()).href)
  expect(res.status()).toBe(200)
  expect(await res.text(), 'the fork bakes no GA4 id; the dark-tag check would be vacuous (U1)').toMatch(GA_ID)

  const tagRequests: string[] = []
  page.on('request', (req) => {
    if (isTagRequest(req.url())) tagRequests.push(req.url())
  })
  await openLibrary(page)
  await page.waitForLoadState('networkidle')
  await notice(page).getByRole('button', { name: 'Accept' }).click()
  await expect(notice(page)).toHaveCount(0)
  await openLibrary(page, '/rules', false)
  await page.waitForLoadState('networkidle')
  expect(tagRequests, 'the library requested the GA4 tag on a non-production host').toEqual([])
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
