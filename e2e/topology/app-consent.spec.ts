// The app's cookie notice and HubSpot chat on the deployed PR app. The PR host is no chat host, so Accept loads no chat.
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import { createEntity, createInvoice } from '../api/client'
import { freshTin } from '../api/fixtures'
import { browserToken, collectErrors, DESTINATION_READY, signInAs, storedSession } from '../personaSession'
import { HUBSPOT_CHAT_DOMAINS, routeAppHost, watchStubLeaks } from '../smoke/landingConsent'
import { resolveTarget } from '../targets'
import { approvalRun404Dropper, collectSourcedErrors } from './consoleGate'
import { rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { APP_URL, GATEWAY_URL, shardTenants } from './targets'

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
  expect(hosts.length, 'the request log is empty').toBeGreaterThan(0)
  expect(hosts, 'the log holds no request to the app host').toContain(new URL(APP_URL).hostname.toLowerCase())
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

// Every visible control outside the notice, clipped to its overflow ancestors. Exclusions: the notice itself and tooltip, status and dialog roles.
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
  // The detail page's approval-run GET 404s for an invoice with no run (consoleGate.ts).
  const errors = collectSourcedErrors(page, approvalRun404Dropper(page))
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
      // CreateFlow draws no h1; its step label is the landmark.
      const ready = route === '/create' ? page.getByText('New invoice ·').first() : page.locator('main.pf-main .pf-scroll h1:visible').first()
      await expect(ready, `${at}: the screen drew no landmark`).toBeVisible({ timeout: 30_000 })
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

test('AC-L2 the band clears the notice, first visit and reopened, at 1280 and 390', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)
  await signInAs(page, 'firm', { tenantId: SHARD.a.id, consent: 'none' })
  const entry = page.viewportSize()
  const measured: unknown[] = []
  // The inset is measured (viewport height minus card bottom), not retyped from landing.css.
  const read = async (at: string, height: number) => {
    await settleAnimations(notice(page))
    const card = await notice(page).boundingBox()
    const spacer = await page.locator('.cn-spacer').boundingBox()
    expect(card, `${at}: the notice has no box`).not.toBeNull()
    expect(spacer, `${at}: the spacer has no box`).not.toBeNull()
    const inset = height - (card!.y + card!.height)
    expect(inset, `${at}: the card is not anchored inside the viewport bottom`).toBeGreaterThan(0)
    expect(spacer!.height, `${at}: the spacer is ${spacer!.height}px for a ${card!.height + inset}px band`).toBeGreaterThanOrEqual(card!.height + inset)
    measured.push({ at, card, spacer, inset })
  }
  try {
    for (const { width, height } of [{ width: 1280, height: 720 }, { width: 390, height: 844 }]) {
      await page.setViewportSize({ width, height })
      await page.goto(`${APP_URL}/`)
      await expect(notice(page)).toBeVisible({ timeout: 30_000 })
      await expect(notice(page).locator('.cn-setting')).toHaveCount(0)
      await read(`first visit at ${width}`, height)
      await accept(page).click()
      await expect(notice(page)).toBeHidden()
      await page.locator('aside').getByRole('button', { name: 'Cookie choices' }).click()
      await expect(notice(page).locator('.cn-setting')).toHaveText('Analytics cookies are on.')
      await read(`reopened at ${width}`, height)
      // Hosts under ascomply.com keep the choice in the cookie of the same name too.
      await page.evaluate((key) => localStorage.removeItem(key), CONSENT_KEY)
      await page.context().clearCookies({ name: CONSENT_KEY })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  await attachJson(testInfo, 'app-consent-band', measured)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

const PROD_APP = 'https://app.ascomply.com'

// Copy of cleanInvoiceFields in invoice-surfaces.spec.ts: a draft with no rule violation.
function cleanInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

test('AC-GA the routed production app loads the Google tag and sends events only after Accept', async ({ page, browser, request }) => {
  test.setTimeout(300_000)
  await signInAs(page, 'firm', { tenantId: SHARD.a.id, consent: 'none' })
  const token = await browserToken(page)
  const session = await storedSession(page)
  const entity = await createEntity(token, { name: `APP-GA ${Date.now()}`, tin: freshTin() })
  const invoice = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(`INV-GA-${Date.now()}`) })

  const context = await browser.newContext()
  try {
    await routeAppHost(context, { app: APP_URL, gateway: GATEWAY_URL })
    // The routed origin starts empty: carry the fork session over, once.
    await context.addInitScript(({ key, blob, origin }) => {
      if (location.origin === origin && localStorage.getItem(key) === null) localStorage.setItem(key, blob)
    }, { ...session, origin: PROD_APP })
    const app = await context.newPage()
    const errors = collectSourcedErrors(app, approvalRun404Dropper(app))
    const { leaked, pending } = watchStubLeaks(app)
    const gaRequests: string[] = []
    app.on('request', (r) => {
      if (hostMatches(new URL(r.url()).hostname.toLowerCase(), TAG_HOSTS)) gaRequests.push(r.url())
    })
    const layer = () => app.evaluate(() => ((window as unknown as { dataLayer?: ArrayLike<unknown>[] }).dataLayer ?? []).map((e) => Array.from(e)))
    const pageViews = async () => (await layer()).filter((e) => e[0] === 'event' && e[1] === 'page_view')
    const gaCookies = async () => (await context.cookies()).filter((c) => /^_ga(_|$)/.test(c.name))

    // routing control: a mismatch means the routing failed, not the analytics
    const served = await app.goto(`${PROD_APP}/build.txt`)
    const direct = await request.get(`${APP_URL}/build.txt`)
    expect(direct.ok(), `routing, not analytics: ${APP_URL}/build.txt did not answer 2xx`).toBeTruthy()
    expect((await served?.text())?.trim(), `routing, not analytics: ${PROD_APP} does not serve the fork's build`).toBe((await direct.text()).trim())

    await app.goto(`${PROD_APP}/invoices/${invoice.id}`)
    await DESTINATION_READY.app(app, 30_000)
    await expect(app.locator('aside.pf-sidebar')).toBeVisible()

    // before Accept: a validation is held
    await expect(notice(app)).toBeVisible()
    await app.getByTestId('revalidate').click()
    await expect(app.getByTestId('invoice-status-badge')).toContainText('VALIDATED', { timeout: 30_000 })
    expect(gaRequests, 'a GA request left before Accept').toEqual([])
    expect(await app.evaluate(() => typeof (window as unknown as { dataLayer?: unknown }).dataLayer)).toBe('undefined')

    // after Accept
    await accept(app).click()
    await expect(notice(app)).toBeHidden()
    await expect.poll(() => gaRequests.some((u) => new URL(u).hostname === 'www.googletagmanager.com'), 'the tag was never requested').toBe(true)
    await expect.poll(async () => (await layer()).map((e) => e[0]), 'the tag never loaded on the routed app').toContain('config')
    const first = await layer()
    expect(first.map((e) => e[0])).toEqual(['js', 'config', 'event'])
    expect(first[1][2]).toEqual({ page_location: `${PROD_APP}/invoices/:id` })
    expect(first[2].slice(1)).toEqual(['invoice_validated', {}])

    // route changes send the bounded pattern
    const before = (await pageViews()).length
    await app.locator('aside').getByRole('button', { name: 'Invoices', exact: true }).click()
    await expect.poll(async () => (await pageViews()).slice(before), 'the sidebar navigation sent no page_view').toEqual([['event', 'page_view', { page_location: `${PROD_APP}/invoices` }]])
    const term = freshTin()
    await app.getByPlaceholder('Search invoices, TINs…').fill(term)
    await app.getByPlaceholder('Search invoices, TINs…').press('Enter')
    await expect.poll(async () => (await pageViews()).slice(before + 1), 'the search sent no page_view').toEqual([['event', 'page_view', { page_location: `${PROD_APP}/invoices` }]])
    const sent = JSON.stringify(await layer())
    expect(sent, 'a query string reached the dataLayer').not.toContain('?')
    expect(sent, 'the search term reached the dataLayer').not.toContain(term)
    expect(sent, 'the invoice id reached the dataLayer').not.toContain(invoice.id)

    // Reject stops sending and expires the _ga cookies
    await context.addCookies([
      { name: '_ga', value: 'GA1.1.1.1', domain: '.ascomply.com', path: '/', secure: true },
      { name: '_ga_TEST', value: 'GS1.1.3', domain: '.ascomply.com', path: '/', secure: true },
    ])
    expect((await gaCookies()).map((c) => c.name).sort(), 'control: both _ga cookies must exist before Reject').toEqual(['_ga', '_ga_TEST'])
    await app.locator('aside').getByRole('button', { name: 'Cookie choices' }).click()
    await notice(app).getByRole('button', { name: 'Reject', exact: true }).click()
    await expect.poll(async () => (await gaCookies()).map((c) => c.name)).toEqual([])
    const held = (await layer()).length
    await app.locator('aside').getByRole('button', { name: 'Rules', exact: true }).click()
    await expect(app).toHaveURL(`${PROD_APP}/rules`)
    await app.waitForLoadState('networkidle')
    expect((await layer()).length, 'an event was sent after Reject').toBe(held)

    // stub and console gates
    await Promise.all(pending)
    expect(leaked, 'a GA, Sentry or chat request was not answered by the stub').toEqual([])
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  } finally {
    await context.unrouteAll({ behavior: 'ignoreErrors' })
    await context.close()
  }
})
