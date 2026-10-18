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

const WHAT_CHANGES = 'What structured e-invoicing changes for a Nigerian finance team'
const GRID = 'div[style*="grid-template-columns"]'

async function expectCardsInside(grid: Locator, label: string): Promise<string[]> {
  const g = await rectOf(grid, `${label} grid`)
  const cards = grid.locator(':scope > div')
  const n = await cards.count()
  const rects: Rect[] = []
  for (let i = 0; i < n; i++) rects.push(await rectOf(cards.nth(i), `${label} card ${i}`))
  rects.forEach((r, i) => expect(enclosesRect(g, r, SLACK_PX), `${label} card ${i} leaves its grid`).toBe(true))
  for (let i = 0; i < n; i++) {
    for (let j = i + 1; j < n; j++) expect(rectsOverlap(rects[i], rects[j]), `${label} cards ${i} and ${j} overlap`).toBe(false)
  }
  return cards.locator('button > span:nth-child(2)').allTextContents()
}

test.describe('blog home, category and search at wide widths', () => {
  for (const width of WIDE_WIDTHS) {
    test(`blog home: the featured card's image and text sit apart (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 1080)
      await visit(page)
      await expect(main(page).locator('h1')).toBeVisible()
      const text = main(page).locator('section').first().getByRole('button', { name: /Read the post/ })
      await expect(text, 'control: no featured card; seeded posts are published').toBeVisible()
      const card = text.locator('..')
      const image = card.locator(':scope > div').first()
      const c = await rectOf(card, 'the featured card')
      const i = await rectOf(image, 'the featured image box')
      const t = await rectOf(text, 'the featured text')
      await attachJson(testInfo, `blog-featured-${width}`, { card: c, image: i, text: t })
      expect(enclosesRect(c, i, SLACK_PX), 'the image box leaves the card').toBe(true)
      expect(enclosesRect(c, t, SLACK_PX), 'the text leaves the card').toBe(true)
      expect(rectsOverlap(i, t), 'the image box overlaps the text').toBe(false)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog home: band cards sit inside their grid and apart (${width})`, async ({ page }) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 1080)
      await visit(page)
      await expect(main(page).locator('h1')).toBeVisible()
      await expect(main(page).locator(`section ${GRID} button`).first()).toBeVisible()
      const grids = main(page).locator(`section ${GRID}`)
      const n = await grids.count()
      expect(n, 'control: no band rendered').toBeGreaterThan(0)
      const titles: string[] = []
      for (let k = 0; k < n; k++) titles.push(...(await expectCardsInside(grids.nth(k), `band ${k}`)))
      expect(titles, 'control: no band holds a seeded title').toContain(WHAT_CHANGES)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog category: cards sit inside the grid and apart (${width})`, async ({ page }) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 1080)
      await visit(page, '/category/regulation')
      await expect(main(page).getByRole('button', { name: new RegExp(WHAT_CHANGES) }).first()).toBeVisible()
      const titles = await expectCardsInside(main(page).locator(`section ${GRID}`), 'category')
      expect(titles, 'control: the seeded card is missing').toContain(WHAT_CHANGES)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog search: date and text columns sit apart (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 1080)
      await visit(page)
      await expect(main(page).locator('h1')).toBeVisible()
      const field = header(page).locator('label')
      const input = field.getByRole('textbox')
      await input.fill('invoice')
      await expect(main(page).locator('h2')).toContainText(/match/)
      const rows = main(page).locator('button.blog-row')
      const n = await rows.count()
      expect(n, 'control: no result row').toBeGreaterThan(0)
      for (let k = 0; k < n; k++) {
        const r = await rectOf(rows.nth(k), `row ${k}`)
        const d = await rectOf(rows.nth(k).locator(':scope > span').first(), `row ${k} date`)
        const x = await rectOf(rows.nth(k).locator(':scope > span').nth(1), `row ${k} text`)
        expect(enclosesRect(r, d, SLACK_PX), `row ${k}: the date leaves the row`).toBe(true)
        expect(enclosesRect(r, x, SLACK_PX), `row ${k}: the text leaves the row`).toBe(true)
        expect(rectsOverlap(d, x), `row ${k}: date and text overlap`).toBe(false)
      }
      const clear = field.getByRole('button', { name: 'Clear search' })
      const f = await rectOf(field, 'the search field')
      const cl = await rectOf(clear, 'the clear button')
      const inp = await rectOf(input, 'the search input')
      await attachJson(testInfo, `blog-search-${width}`, { field: f, clear: cl, input: inp })
      expect(enclosesRect(f, cl, SLACK_PX), 'the clear button leaves the search field').toBe(true)
      expect(rectsOverlap(cl, inp), 'the clear button overlaps the input').toBe(false)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })
  }
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
