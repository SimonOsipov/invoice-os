// The blog /subscribe page layout on the PR fork. Each case asserts a relationship, never a raw dimension.
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { randomBytes, randomUUID } from 'node:crypto'
import { resolveTarget } from '../targets'
import { expectedStatusDropper, type Dropper } from './consoleGate'
import { enclosesRect, PHONE_WIDTHS, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { GATEWAY_URL } from './targets'

const BLOG_URL = resolveTarget('BLOG_URL')
const SLACK_PX = 0.5 // sub-pixel rounding only

// Seeds a consent answer so no cookie notice covers the page.
async function prepare(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.localStorage.setItem('asc_consent', JSON.stringify({ analytics: false, ts: '2026-01-01T00:00:00Z', v: 1 }))
  })
}

function collectErrors(page: Page, drop?: Dropper): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error' && !drop?.(msg.text(), msg.location().url)) errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })
  return errors
}

async function attachJson(testInfo: TestInfo, name: string, body: unknown): Promise<void> {
  await testInfo.attach(`${name}.json`, { body: JSON.stringify(body, null, 2), contentType: 'application/json' })
}

const main = (page: Page) => page.locator('#blog-main')
// Structure: intro = the h1's column; card = three divs above the email field; control = the frequency buttons' parent.
const intro = (page: Page) => main(page).locator('h1').locator('xpath=..')
const card = (page: Page) => main(page).getByLabel('Work email').locator('xpath=ancestor::div[3]')
const chips = (page: Page) => card(page).locator('button[aria-pressed]').filter({ hasNotText: /^(Weekly digest|Every post|Monthly)$/ })
// The breadcrumb also renders a "Subscribe" button; the form's lives in the section.
const subscribeButton = (page: Page) => main(page).locator('section').getByRole('button', { name: 'Subscribe', exact: true })
const control = (page: Page) => card(page).getByRole('button', { name: 'Weekly digest' }).locator('xpath=..')
const freqButtons = (page: Page) => control(page).getByRole('button')

async function visit(page: Page, width: number): Promise<void> {
  await page.setViewportSize({ width, height: 1080 })
  await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
  const response = await page.goto(`${BLOG_URL}/subscribe`)
  expect(response?.ok(), '/subscribe did not answer 2xx').toBeTruthy()
  await expect(main(page).getByRole('heading', { level: 1 }), '/subscribe never rendered the form').toBeVisible()
  await expect(subscribeButton(page), 'the form Subscribe button').toBeVisible()
}

async function rectOf(loc: Locator, label: string): Promise<Rect> {
  await settleAnimations(loc)
  const box = await loc.boundingBox()
  if (!box) throw new Error(`${label} has no box`)
  return box
}

async function freqRects(page: Page): Promise<Rect[]> {
  const n = await freqButtons(page).count()
  expect(n, 'the frequency control holds three buttons').toBe(3)
  return Promise.all([0, 1, 2].map((i) => rectOf(freqButtons(page).nth(i), `frequency button ${i}`)))
}

for (const width of WIDE_WIDTHS) {
  test(`blog subscribe: intro and card sit side by side and fit main (${width})`, async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await prepare(page)
    await visit(page, width)
    const i = await rectOf(intro(page), 'the intro column')
    const c = await rectOf(card(page), 'the card')
    const m = await rectOf(main(page), '#blog-main')
    await attachJson(testInfo, `blog-subscribe-columns-${width}`, { intro: i, card: c, main: m })
    expect(rectsOverlap(i, c), 'the intro overlaps the card').toBe(false)
    expect(c.x, 'the card sits right of the intro').toBeGreaterThanOrEqual(i.x + i.width - SLACK_PX)
    expect(enclosesRect(m, i, SLACK_PX), 'the intro leaves main').toBe(true)
    expect(enclosesRect(m, c, SLACK_PX), 'the card leaves main').toBe(true)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
}

for (const width of [...WIDE_WIDTHS, ...PHONE_WIDTHS]) {
  test(`blog subscribe: the three frequency buttons fit the segmented control (${width})`, async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await prepare(page)
    await visit(page, width)
    const c = await rectOf(card(page), 'the card')
    const ctl = await rectOf(control(page), 'the frequency control')
    const buttons = await freqRects(page)
    await attachJson(testInfo, `blog-subscribe-frequency-${width}`, { card: c, control: ctl, buttons })
    expect(enclosesRect(c, ctl, SLACK_PX), 'the control leaves the card').toBe(true)
    buttons.forEach((b, k) => expect(enclosesRect(ctl, b, SLACK_PX), `frequency button ${k} leaves the control`).toBe(true))
    expect(rectsOverlap(buttons[0], buttons[1]) || rectsOverlap(buttons[1], buttons[2]), 'frequency buttons overlap').toBe(false)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
}

for (const width of PHONE_WIDTHS) {
  test(`blog subscribe: chips wrap inside the card (${width})`, async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await prepare(page)
    await visit(page, width)
    const i = await rectOf(intro(page), 'the intro column')
    const c = await rectOf(card(page), 'the card')
    const n = await chips(page).count()
    expect(n, 'no topic chip rendered, so the containment check would be vacuous').toBeGreaterThan(0)
    const rects = await Promise.all(Array.from({ length: n }, (_, k) => rectOf(chips(page).nth(k), `chip ${k}`)))
    const scroll = await main(page).evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
    await attachJson(testInfo, `blog-subscribe-chips-${width}`, { intro: i, card: c, chips: rects, ...scroll })
    rects.forEach((r, k) => expect(enclosesRect(c, r, SLACK_PX), `chip ${k} leaves the card`).toBe(true))
    expect(c.y, 'the card sits below the intro').toBeGreaterThanOrEqual(i.y + i.height - SLACK_PX)
    expect(scroll.scrollWidth - scroll.clientWidth, '#blog-main scrolls sideways').toBeLessThanOrEqual(1)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
}

// No token in the URL, so the card shows the expired-link state with no request made.
const confirmCard = (page: Page) => main(page).getByText('This link has expired or was already used.').locator('xpath=../..')

async function visitConfirm(page: Page, width: number): Promise<void> {
  await page.setViewportSize({ width, height: 1080 })
  await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
  const response = await page.goto(`${BLOG_URL}/subscribe/confirm`)
  expect(response?.ok(), '/subscribe/confirm did not answer 2xx').toBeTruthy()
  await expect(main(page).getByRole('heading', { level: 1 }), '/subscribe/confirm never rendered the page').toBeVisible()
  await expect(main(page).getByRole('button', { name: 'Subscribe again' }), 'the confirm card Subscribe again button').toBeVisible()
}

for (const width of WIDE_WIDTHS) {
  test(`blog confirm: card and intro side by side, card fits main (${width})`, async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await prepare(page)
    await visitConfirm(page, width)
    const i = await rectOf(intro(page), 'the intro column')
    const c = await rectOf(confirmCard(page), 'the card')
    const m = await rectOf(main(page), '#blog-main')
    await attachJson(testInfo, `blog-confirm-columns-${width}`, { intro: i, card: c, main: m })
    expect(rectsOverlap(i, c), 'the intro overlaps the card').toBe(false)
    expect(c.x, 'the card sits right of the intro').toBeGreaterThanOrEqual(i.x + i.width - SLACK_PX)
    expect(enclosesRect(m, i, SLACK_PX), 'the intro leaves main').toBe(true)
    expect(enclosesRect(m, c, SLACK_PX), 'the card leaves main').toBe(true)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
}

for (const width of PHONE_WIDTHS) {
  test(`blog confirm: card sits below the intro and fits main (${width})`, async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await prepare(page)
    await visitConfirm(page, width)
    const i = await rectOf(intro(page), 'the intro column')
    const c = await rectOf(confirmCard(page), 'the card')
    const m = await rectOf(main(page), '#blog-main')
    const scroll = await main(page).evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
    await attachJson(testInfo, `blog-confirm-phone-${width}`, { intro: i, card: c, main: m, ...scroll })
    expect(c.y, 'the card sits below the intro').toBeGreaterThanOrEqual(i.y + i.height - SLACK_PX)
    expect(enclosesRect(m, c, SLACK_PX), 'the card leaves main').toBe(true)
    expect(scroll.scrollWidth - scroll.clientWidth, '#blog-main scrolls sideways').toBeLessThanOrEqual(1)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
}

const isSubscribePost = (url: string) => /\/blog\/subscriptions$/.test(url)

test('blog subscribe: a valid submit reaches the fork gateway and shows the done state', async ({ page }) => {
  const errors = collectErrors(page)
  await prepare(page)
  await visit(page, WIDE_WIDTHS[0])
  const email = `blog-e2e-${randomUUID().slice(0, 8)}@corp.example`
  const n = await chips(page).count()
  expect(n, 'no topic chip rendered').toBeGreaterThan(0)
  await main(page).getByLabel('Work email').fill(email)
  const [res] = await Promise.all([
    page.waitForResponse((r) => r.request().method() === 'POST' && isSubscribePost(r.url())),
    subscribeButton(page).click(),
  ])
  expect(res.status(), 'subscribe answers 202 (http.StatusAccepted)').toBe(202)
  await expect(main(page).getByText('Check your inbox.')).toBeVisible()
  await expect(main(page)).toContainText(email)
  await expect(main(page)).toContainText(`across ${n} ${n === 1 ? 'topic' : 'topics'}`)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('blog subscribe: validation messages', async ({ page }) => {
  const errors = collectErrors(page)
  const posts: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && isSubscribePost(r.url())) posts.push(r.url())
  })
  await prepare(page)
  await visit(page, WIDE_WIDTHS[0])
  const field = main(page).getByLabel('Work email')
  await subscribeButton(page).click()
  await expect(main(page).getByText('Enter your work email.')).toBeVisible()
  await field.fill('a@b')
  await expect(main(page).getByText('Enter a valid email address, such as name@company.com.')).toBeVisible()
  await field.fill('blog-e2e-novalidate@corp.example')
  const n = await chips(page).count()
  for (let k = 0; k < n; k++) await chips(page).nth(k).click()
  await subscribeButton(page).click()
  await expect(main(page).getByText('Choose at least one topic.')).toBeVisible()
  expect(posts, 'a rejected form must not POST').toEqual([])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('blog subscribe: topics are the live categories', async ({ page }) => {
  const errors = collectErrors(page)
  const live = (await (await fetch(`${GATEWAY_URL}/blog/categories`)).json()) as { categories: { name: string }[] }
  expect(live.categories.length, 'the gateway lists no category').toBeGreaterThan(0)
  await prepare(page)
  await visit(page, WIDE_WIDTHS[0])
  await expect.poll(() => chips(page).count()).toBe(live.categories.length)
  const names = (await chips(page).allTextContents()).map((t) => t.trim())
  expect(names).toEqual(live.categories.map((c) => c.name))
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

// 43 URL-safe chars (blogConfirmTokenRE in internal/gateway/blog_subscribe.go), never issued.
const unknownToken = () => randomBytes(32).toString('base64url')
const CONFIRM_POST = /\/blog\/subscriptions\/confirm$/

test('blog confirm: an unknown token shows the expired state', async ({ page }) => {
  const errors = collectErrors(page, expectedStatusDropper(page, 410, CONFIRM_POST)) // http.StatusGone
  await prepare(page)
  const token = unknownToken()
  expect(token).toHaveLength(43)
  await page.goto(`${BLOG_URL}/subscribe/confirm?token=${token}`)
  await expect(page, 'the token stays in the address bar').not.toHaveURL(/token=/)
  const [res] = await Promise.all([
    page.waitForResponse((r) => CONFIRM_POST.test(r.url())),
    main(page).getByRole('button', { name: 'Confirm subscription' }).click(),
  ])
  expect(res.status(), 'an unknown token answers 410 (http.StatusGone)').toBe(410)
  await expect(main(page).getByText('This link has expired or was already used.')).toBeVisible()
  await expect(main(page).getByRole('button', { name: 'Subscribe again' })).toBeVisible()
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('blog confirm: loading the link sends nothing', async ({ page }) => {
  const errors = collectErrors(page)
  const seen: string[] = []
  page.on('request', (r) => {
    if (CONFIRM_POST.test(r.url())) seen.push(r.url())
  })
  await prepare(page)
  await page.goto(`${BLOG_URL}/subscribe/confirm?token=${unknownToken()}`)
  await expect(main(page).getByRole('button', { name: 'Confirm subscription' })).toBeVisible()
  await page.waitForLoadState('networkidle')
  expect(seen, 'the confirm POST fired before the click').toEqual([])
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})
