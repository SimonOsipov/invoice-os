// Blog shell layout, phone layout and cold deep links on the PR fork. Each case asserts a
// relationship (side by side, containment, alignment, no overflow), never a raw dimension.
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { resolveTarget } from '../targets'
import { GATEWAY_URL } from './targets'
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

// expectedUrl: the one resource a test requests knowing it answers an error status.
function collectErrors(page: Page, expectedUrl?: string): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error' && msg.location().url !== expectedUrl) errors.push(msg.text())
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
  // Seeded on forks (migrations/20261017135635_seed_blog.sql); a not-found path never goes here, visit() needs 2xx.
  const COLD = ['/subscribe', '/category/regulation', '/what-changes']

  test('blog routes: a cold deep link renders the shell', async ({ page }) => {
    const errors = collectErrors(page)
    await prepare(page)
    for (const path of COLD) {
      await visit(page, path)
      await expect(crumbs(page).getByRole('button').first(), `${path}: first crumb`).toHaveText('Blog')
      await expect(aside(page).getByRole('button', { name: 'Subscribe' }), `${path}: the Subscribe button`).toBeVisible()
    }
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('blog routes: an unknown slug answers 404 and still boots the SPA', async ({ page }) => {
    const url = `${BLOG_URL}/no-such-post-${crypto.randomUUID().slice(0, 8)}`
    const errors = collectErrors(page, url)
    await prepare(page)
    const response = await page.goto(url)
    expect(response?.status(), 'status of an unknown slug').toBe(404)
    await expect(main(page), '#blog-main never rendered').toBeVisible()
    await expect(header(page), '#blog-header never rendered').toBeVisible()
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
})

// Seeded post served as crawler HTML (internal/blogweb); the SPA replaces the block on mount.
test.describe('blog crawler HTML', () => {
  const SEEDED = '/what-changes'
  const JS_DELAY_MS = 1500
  const MIN_FRAMES = 5

  test('blog crawler HTML: a JavaScript visitor never sees it before the SPA mounts', async ({ page }) => {
    const errors = collectErrors(page)
    await prepare(page)
    await page.route('**/assets/*.js', async (route) => {
      await new Promise((r) => setTimeout(r, JS_DELAY_MS))
      await route.continue()
    })
    await page.addInitScript(() => {
      const stats = { frames: 0, present: 0, boxed: 0 }
      ;(window as unknown as { __crawl: typeof stats }).__crawl = stats
      const tick = () => {
        stats.frames++
        const block = document.querySelector('#root > [data-crawl]')
        if (block) {
          stats.present++
          const r = block.getBoundingClientRect()
          if (r.width > 0 || r.height > 0) stats.boxed++
        }
        if (!document.getElementById('blog-main')) requestAnimationFrame(tick)
      }
      requestAnimationFrame(tick)
    })
    const response = await page.goto(`${BLOG_URL}${SEEDED}`)
    expect(response?.status(), 'status').toBe(200)
    await expect(main(page), '#blog-main never rendered').toBeVisible()
    const stats = await page.evaluate(() => (window as unknown as { __crawl: { frames: number; present: number; boxed: number } }).__crawl)
    expect(stats.frames, 'the sampler ran').toBeGreaterThanOrEqual(MIN_FRAMES)
    expect(stats.present, 'control: the sampler saw the server block').toBeGreaterThanOrEqual(1)
    expect(stats.boxed, 'frames where the block had a box').toBe(0)
    await expect(page.locator('[data-crawl]'), 'the block leaves the DOM').toHaveCount(0)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('blog crawler HTML: without JavaScript the post shows as HTML', async ({ browser }) => {
    const live = await fetch(`${GATEWAY_URL}/blog/posts/what-changes`)
    expect(live.status, 'live post').toBe(200)
    const { title } = (await live.json()) as { title: string }
    const context = await browser.newContext({ javaScriptEnabled: false })
    try {
      const page = await context.newPage()
      const response = await page.goto(`${BLOG_URL}${SEEDED}`)
      expect(response?.status(), 'status').toBe(200)
      await expect(page.locator('#root > [data-crawl] h1')).toHaveText(title)
    } finally {
      await context.close()
    }
  })
})
