// Blog shell layout, phone layout and cold deep links on the PR fork. Each case asserts a
// relationship (side by side, containment, alignment, no overflow), never a raw dimension.
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { resolveTarget } from '../targets'
import {
  assertFillsColumn,
  enclosesRect,
  PHONE_WIDTHS,
  rectsOverlap,
  settleAnimations,
  WIDE_WIDTHS,
  type Rect,
} from './layout'

const BLOG_URL = resolveTarget('BLOG_URL')
const SLACK_PX = 0.5 // sub-pixel rounding only
const SCROLL_BLOCK_PX = 3000
const SCROLL_TO_PX = 1000

// Seeds a consent answer so no cookie notice covers the shell.
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

const aside = (page: Page) => page.locator('aside')
const main = (page: Page) => page.locator('#blog-main')
const header = (page: Page) => page.locator('#blog-header')
const crumbs = (page: Page) => page.getByRole('navigation', { name: 'Breadcrumb' })

async function visit(page: Page, path = '/'): Promise<void> {
  const response = await page.goto(`${BLOG_URL}${path}`)
  expect(response?.ok(), `${path}: did not answer 2xx`).toBeTruthy()
  await expect(main(page), `${path}: #blog-main never rendered`).toBeVisible()
  await expect(header(page), `${path}: #blog-header never rendered`).toBeVisible()
}

async function resize(page: Page, width: number, height: number): Promise<void> {
  await page.setViewportSize({ width, height })
  await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
}

async function rectOf(loc: Locator, label: string): Promise<Rect> {
  await settleAnimations(loc)
  const box = await loc.boundingBox()
  if (!box) throw new Error(`${label} has no box`)
  return box
}

test.describe('blog layout at wide widths', () => {
  for (const width of WIDE_WIDTHS) {
    test(`blog layout: sidebar and main sit side by side and fill the viewport (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 1080)
      await visit(page)
      const a = await rectOf(aside(page), 'aside')
      const m = await rectOf(main(page), '#blog-main')
      const win = await page.evaluate(() => ({ w: window.innerWidth, h: window.innerHeight }))
      await attachJson(testInfo, `blog-shell-${width}`, { aside: a, main: m, win })
      expect(rectsOverlap(a, m), 'the sidebar overlaps main').toBe(false)
      expect(Math.abs(a.x), 'the sidebar starts at the left edge').toBeLessThanOrEqual(SLACK_PX)
      expect(m.x, 'main starts at or after the sidebar edge').toBeGreaterThanOrEqual(a.x + a.width - SLACK_PX)
      expect(Math.abs(m.x + m.width - win.w), 'main reaches the right edge').toBeLessThanOrEqual(SLACK_PX)
      expect(Math.abs(a.height - win.h), 'the sidebar fills the viewport height').toBeLessThanOrEqual(1)
      expect(Math.abs(m.height - win.h), 'main fills the viewport height').toBeLessThanOrEqual(1)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog layout: the header stays at main's top after a scroll (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 1080)
      await visit(page)
      // The empty shell is shorter than the viewport, so inject height for the scroll to move.
      const scrolled = await main(page).evaluate(
        (el, [block, to]) => {
          const pad = document.createElement('div')
          pad.style.height = `${block}px`
          el.appendChild(pad)
          el.scrollTop = to
          return el.scrollTop
        },
        [SCROLL_BLOCK_PX, SCROLL_TO_PX],
      )
      const h = await rectOf(header(page), '#blog-header')
      const m = await rectOf(main(page), '#blog-main')
      await attachJson(testInfo, `blog-header-scroll-${width}`, { scrolled, header: h, main: m })
      expect(scrolled, 'control: main did not scroll, so the stick check proved nothing').toBeGreaterThan(h.height)
      expect(Math.abs(h.y - m.y), "the header stays at main's top").toBeLessThanOrEqual(SLACK_PX)
      expect(enclosesRect(m, h, SLACK_PX), 'the header leaves main').toBe(true)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog layout: crumbs and search sit in the header apart (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 1080)
      await visit(page)
      const h = await rectOf(header(page), '#blog-header')
      const nav = await rectOf(crumbs(page), 'the breadcrumb')
      const search = await rectOf(header(page).locator('label'), 'the search field')
      await attachJson(testInfo, `blog-header-parts-${width}`, { header: h, nav, search })
      expect(enclosesRect(h, nav, SLACK_PX), 'the breadcrumb leaves the header').toBe(true)
      expect(enclosesRect(h, search, SLACK_PX), 'the search field leaves the header').toBe(true)
      expect(rectsOverlap(nav, search), 'the breadcrumb overlaps the search field').toBe(false)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })
  }

  test('blog layout: the header fills main', async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await prepare(page)
    await visit(page)
    const fits = await assertFillsColumn(page, header(page), main(page), 'blog header')
    await attachJson(testInfo, 'blog-header-fills-main', fits)
    expect(fits, 'no width was measured').toHaveLength(WIDE_WIDTHS.length)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
})

test.describe('blog at phone widths', () => {
  for (const width of PHONE_WIDTHS) {
    test(`blog layout: on a phone the sidebar stacks above main (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 800)
      await visit(page)
      const a = await rectOf(aside(page), 'aside')
      const m = await rectOf(main(page), '#blog-main')
      const doc = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
      }))
      await attachJson(testInfo, `blog-phone-${width}`, { aside: a, main: m, ...doc })
      expect(a.y + a.height, 'the sidebar sits above main').toBeLessThanOrEqual(m.y + SLACK_PX)
      expect(doc.scrollWidth - doc.clientWidth, 'the page scrolls sideways').toBeLessThanOrEqual(1)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })
  }
})

test.describe('blog routes', () => {
  const COLD: [string, string[]][] = [
    ['/subscribe', ['Blog', 'Subscribe']],
    ['/category/x', ['Blog']],
    ['/a-post', ['Blog']],
  ]

  test('blog routes: a cold deep link renders the shell', async ({ page }) => {
    const errors = collectErrors(page)
    await prepare(page)
    for (const [path, expected] of COLD) {
      await visit(page, path)
      await expect(crumbs(page).getByRole('button'), `${path}: crumbs`).toHaveText(expected)
      await expect(aside(page).getByRole('button', { name: 'Subscribe' }), `${path}: the Subscribe button`).toBeVisible()
    }
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
})
