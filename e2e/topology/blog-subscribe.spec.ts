// The blog /subscribe page layout on the PR fork. Each case asserts a relationship, never a raw dimension.
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { resolveTarget } from '../targets'
import { enclosesRect, PHONE_WIDTHS, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'

const BLOG_URL = resolveTarget('BLOG_URL')
const SLACK_PX = 0.5 // sub-pixel rounding only

// Seeds a consent answer so no cookie notice covers the page.
async function prepare(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.localStorage.setItem('asc_consent', JSON.stringify({ analytics: false, ts: '2026-01-01T00:00:00Z', v: 1 }))
  })
}

function collectErrors(page: Page): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
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
const control = (page: Page) => card(page).getByRole('button', { name: 'Weekly digest' }).locator('xpath=..')
const freqButtons = (page: Page) => control(page).getByRole('button')

async function visit(page: Page, width: number): Promise<void> {
  await page.setViewportSize({ width, height: 1080 })
  await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
  const response = await page.goto(`${BLOG_URL}/subscribe`)
  expect(response?.ok(), '/subscribe did not answer 2xx').toBeTruthy()
  await expect(main(page).getByRole('heading', { level: 1 }), '/subscribe never rendered the form').toBeVisible()
  await expect(main(page).getByRole('button', { name: 'Subscribe' }), 'the form Subscribe button').toBeVisible()
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
