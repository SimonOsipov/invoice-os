// The app's cookie notice and HubSpot chat on the deployed PR app. The PR host is no chat host, so Accept loads no chat.
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import { createEntity, createInvoice } from '../api/client'
import { freshTin } from '../api/fixtures'
import { browserToken, collectErrors, signInAs } from '../personaSession'
import { HUBSPOT_CHAT_DOMAINS } from '../smoke/landingConsent'
import { resolveTarget } from '../targets'
import { rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { APP_URL, shardTenants } from './targets'

const SHARD = shardTenants('app-consent.spec.ts')
test.beforeAll(() => { expect(test.info().project.name, 'app-consent.spec.ts belongs to the ui-surfaces shard').toBe('ui-surfaces') })

const CONSENT_KEY = 'asc_consent' // retyped from frontend/landing/src/consent.ts
// retyped from HUBSPOT_COOKIE_NAMES in frontend/landing/src/hubspotChat.ts
const HUBSPOT_COOKIES = ['__hstc', 'hubspotutk', '__hssrc', '__hssc', 'messagesUtk', 'hs-messages-is-open', 'hs-messages-hide-welcome-message']
const TAG_HOSTS = ['googletagmanager.com', 'google-analytics.com']

async function attachJson(testInfo: TestInfo, name: string, body: unknown): Promise<void> {
  await testInfo.attach(`${name}.json`, { body: JSON.stringify(body, null, 2), contentType: 'application/json' })
}

const notice = (page: Page) => page.getByRole('region', { name: 'Cookie notice' })
const accept = (page: Page) => notice(page).getByRole('button', { name: 'Accept', exact: true })
const hostMatches = (host: string, domains: readonly string[]) => domains.some((d) => host === d || host.endsWith(`.${d}`))

test('AC-01 a user with no stored choice sees the notice, and Accept stores it', async ({ page }) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  await signInAs(page, 'firm', { tenantId: SHARD.a.id, consent: 'none' })
  await expect(notice(page)).toBeVisible()
  await accept(page).click()
  await expect(notice(page)).toBeHidden()
  const stored = JSON.parse((await page.evaluate((key) => localStorage.getItem(key), CONSENT_KEY)) ?? 'null') as { analytics?: boolean; v?: number } | null
  expect(stored, 'Accept stored no record').not.toBeNull()
  expect([stored?.analytics, stored?.v]).toEqual([true, 1])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AC-02 Cookie choices in the sidebar brings the notice back with the setting', async ({ page }) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  await signInAs(page, 'firm', { tenantId: SHARD.a.id, consent: 'none' })
  await accept(page).click()
  await expect(notice(page)).toBeHidden()
  await page.locator('aside').getByRole('button', { name: 'Cookie choices' }).click()
  await expect(notice(page)).toBeVisible()
  await expect(notice(page)).toContainText('Analytics cookies are on.')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AC-03 the PR app host loads no HubSpot chat and no Google tag after Accept', async ({ page }) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  const hosts: string[] = []
  page.on('request', (r) => hosts.push(new URL(r.url()).hostname.toLowerCase()))
  await signInAs(page, 'firm', { tenantId: SHARD.a.id, consent: 'none' })
  await accept(page).click()
  await expect(notice(page)).toBeHidden()
  await page.waitForLoadState('networkidle')
  expect(hosts.filter((h) => hostMatches(h, [...HUBSPOT_CHAT_DOMAINS, ...TAG_HOSTS])), 'a chat or tag host was requested').toEqual([])
  await expect(page.locator('#hs-script-loader')).toHaveCount(0)
  expect(await page.evaluate(() => typeof (window as Window & { _hsq?: unknown })._hsq)).toBe('undefined')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AC-04 Sign out deletes the HubSpot cookies on the app host', async ({ page, context }) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await context.addCookies(HUBSPOT_COOKIES.map((name) => ({ name, value: 'e2e', url: APP_URL })))
  const held = (await context.cookies(APP_URL)).map((c) => c.name)
  expect(held, 'the seeded cookies were not stored').toEqual(expect.arrayContaining(HUBSPOT_COOKIES))
  await page.getByRole('button', { name: 'Sign out' }).click()
  await page.waitForURL((u) => u.href.startsWith(resolveTarget('LANDING_URL')), { timeout: 20_000 })
  const left = (await context.cookies(APP_URL)).map((c) => c.name).filter((n) => HUBSPOT_COOKIES.includes(n))
  expect(left, 'HubSpot cookies survived the sign-out').toEqual([])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

const ROUTES = ['/', '/invoices', '/approvals', '/rules', '/customers', '/reports', '/workflows', '/clients', '/audit', '/settings', '/create']
const SIZES = [{ width: 1280, height: 720 }, { width: 390, height: 844 }]
const SELECTOR = 'button, a[href], input, select, textarea'

// Every visible control outside the notice, clipped to its overflow ancestors. DC-18 exclusions: the notice itself and tooltip, status and dialog roles.
async function controlRects(page: Page): Promise<{ notice: Rect | null; controls: { label: string; rect: Rect }[] }> {
  return page.evaluate((selector) => {
    const note = document.querySelector('[role="region"][aria-label="Cookie notice"]')
    const nr = note?.getBoundingClientRect()
    const clip = (el: Element): DOMRect | null => {
      let r: { l: number; t: number; r: number; b: number } = (({ left, top, right, bottom }) => ({ l: left, t: top, r: right, b: bottom }))(el.getBoundingClientRect())
      for (let p = el.parentElement; p; p = p.parentElement) {
        const cs = getComputedStyle(p)
        if (cs.overflowX === 'visible' && cs.overflowY === 'visible') continue
        const pr = p.getBoundingClientRect()
        r = { l: Math.max(r.l, pr.left), t: Math.max(r.t, pr.top), r: Math.min(r.r, pr.right), b: Math.min(r.b, pr.bottom) }
      }
      return r.r > r.l && r.b > r.t ? new DOMRect(r.l, r.t, r.r - r.l, r.b - r.t) : null
    }
    const controls = [...document.querySelectorAll(selector)]
      .filter((el) => !note?.contains(el) && !el.closest('[role="tooltip"], [role="status"], [role="dialog"]'))
      .filter((el) => { const cs = getComputedStyle(el); return cs.display !== 'none' && cs.visibility === 'visible' })
      .flatMap((el) => {
        const r = clip(el)
        const label = (el.getAttribute('aria-label') || el.textContent || el.tagName).trim().slice(0, 40)
        return r ? [{ label, rect: { x: r.x, y: r.y, width: r.width, height: r.height } }] : []
      })
    return { notice: nr ? { x: nr.x, y: nr.y, width: nr.width, height: nr.height } : null, controls }
  }, SELECTOR)
}

test('AC-L the cookie notice overlaps no app control on every screen at 1280 and 390', async ({ page }, testInfo) => {
  test.setTimeout(600_000)
  const errors = collectErrors(page)
  await signInAs(page, 'firm', { tenantId: SHARD.a.id, consent: 'none' })
  const token = await browserToken(page)
  const entity = await createEntity(token, { name: `APP-CONSENT ${Date.now()}`, tin: freshTin() })
  const invoice = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-CONSENT-${Date.now()}` })

  const cases: { route: string; width: number; height: number }[] = []
  for (const route of [...ROUTES, `/invoices/${invoice.id}`]) for (const size of SIZES) cases.push({ route, ...size })
  for (const route of ['/', '/invoices']) for (const width of WIDE_WIDTHS) if (!cases.some((c) => c.route === route && c.width === width && c.height === 720)) cases.push({ route, width, height: 720 })

  const entry = page.viewportSize()
  const measured: unknown[] = []
  try {
    for (const { route, width, height } of cases) {
      const at = `${route} at ${width}x${height}`
      await page.setViewportSize({ width, height })
      await page.goto(`${APP_URL}${route}`)
      await expect(notice(page), `${at}: no notice`).toBeVisible({ timeout: 30_000 })
      await expect(page.locator('main.pf-main .pf-scroll h1:visible').first(), `${at}: the screen drew no h1`).toBeVisible({ timeout: 30_000 })
      await settleAnimations(notice(page))
      await page.evaluate((wide) => {
        if (wide) document.querySelector('main.pf-main .pf-scroll')?.scrollTo(0, 1e9)
        else window.scrollTo(0, 1e9)
      }, width > 860)
      await settleAnimations(notice(page))
      const read = await controlRects(page)
      expect(read.notice, `${at}: the notice has no box`).not.toBeNull()
      expect(read.controls.length, `${at}: no control was measured`).toBeGreaterThan(0)
      const hit = read.controls.filter((c) => rectsOverlap(c.rect, read.notice!)).map((c) => c.label)
      expect(hit, `${at}: controls under the notice`).toEqual([])
      measured.push({ at, notice: read.notice, controls: read.controls.length })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  await attachJson(testInfo, 'app-consent-layout', measured)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})
