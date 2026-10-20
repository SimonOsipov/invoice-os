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

const aside = (page: Page) => page.locator('.asc-app > aside')
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
      for (let k = 0; k < n; k++) await expectCardsInside(grids.nth(k), `band ${k}`)
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

const ARTICLE_WIDTHS = [...WIDE_WIDTHS, 1600]
const BAND_EDGE_MIN_WIDTH = 1440 // below it the row is narrower than the body measure and the band may wrap

type Pads = { left: number; right: number }

// Content edge of #blog-main: its box less the scrollbar.
async function contentRight(page: Page): Promise<number> {
  return main(page).evaluate((el) => el.getBoundingClientRect().left + el.clientWidth)
}

async function paddingsOf(loc: Locator): Promise<Pads> {
  return loc.evaluate((el) => {
    const cs = getComputedStyle(el)
    return { left: parseFloat(cs.paddingLeft), right: parseFloat(cs.paddingRight) }
  })
}

async function maxWidthOf(loc: Locator): Promise<number> {
  return loc.evaluate((el) => parseFloat(getComputedStyle(el).maxWidth))
}

const rightOf = (r: Rect) => r.x + r.width

test.describe('blog article at wide widths', () => {
  const article = (page: Page) => main(page).locator('article')
  const row = (page: Page) => article(page).locator(':scope > div').nth(2)
  const toc = (page: Page) => main(page).locator('aside[aria-label="On this page"]')

  async function open(page: Page, width: number): Promise<void> {
    await prepare(page)
    await resize(page, width, 1080)
    await visit(page, '/what-changes')
    await expect(article(page).locator('h1')).toBeVisible()
    await expect(toc(page), 'control: the seeded post has three headings').toBeVisible()
  }

  for (const width of WIDE_WIDTHS) {
    test(`blog article: body and TOC sit side by side (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await open(page, width)
      const body = await rectOf(row(page).locator(':scope > div').first(), 'the body column')
      const t = await rectOf(toc(page), 'the TOC')
      await attachJson(testInfo, `blog-article-row-${width}`, { body, toc: t })
      expect(rectsOverlap(body, t), 'the body and the TOC overlap').toBe(false)
      expect(t.x, 'the TOC sits right of the body').toBeGreaterThanOrEqual(body.x + body.width - SLACK_PX)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog article: the TOC stays in view under the header (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await open(page, width)
      await article(page).locator('#sec-2').scrollIntoViewIfNeeded()
      await main(page).evaluate((el) => {
        el.scrollTop = el.scrollHeight
      })
      const m = await rectOf(main(page), '#blog-main')
      const h = await rectOf(header(page), '#blog-header')
      const t = await rectOf(toc(page), 'the TOC')
      await attachJson(testInfo, `blog-article-toc-${width}`, { main: m, header: h, toc: t })
      expect(enclosesRect(m, t, SLACK_PX), 'the TOC leaves the visible main').toBe(true)
      expect(t.y, 'the TOC sits under the header').toBeGreaterThanOrEqual(h.y + h.height - SLACK_PX)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog article: the cover spans the body row (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await open(page, width)
      const a = await rectOf(article(page), 'the article')
      const cover = await rectOf(article(page).locator(':scope > div').nth(1), 'the cover')
      const r = await rectOf(row(page), 'the body row')
      await attachJson(testInfo, `blog-article-cover-${width}`, { article: a, cover, row: r })
      expect(enclosesRect(a, cover, SLACK_PX), 'the cover leaves the article').toBe(true)
      expect(Math.abs(cover.width - r.width), 'the cover and the body row differ in width').toBeLessThanOrEqual(SLACK_PX)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog article: the band buttons sit apart in the band (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await open(page, width)
      const band = main(page).locator('section').filter({ has: page.getByRole('link', { name: 'Book a demo' }) })
      const demo = await rectOf(band.getByRole('link', { name: 'Book a demo' }), 'Book a demo')
      const sub = await rectOf(band.getByRole('button', { name: 'Subscribe' }), 'Subscribe')
      const b = await rectOf(band, 'the band')
      await attachJson(testInfo, `blog-article-band-${width}`, { band: b, demo, sub })
      expect(enclosesRect(b, demo, SLACK_PX), 'Book a demo leaves the band').toBe(true)
      expect(enclosesRect(b, sub, SLACK_PX), 'Subscribe leaves the band').toBe(true)
      expect(rectsOverlap(demo, sub), 'the band buttons overlap').toBe(false)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })
  }

  for (const width of ARTICLE_WIDTHS) {
    test(`blog article: the header, cover, body row and band reach the content edge (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await open(page, width)
      const edge = await contentRight(page)
      const a = await rectOf(article(page), 'the article')
      const pad = await paddingsOf(article(page))
      const parts = {
        header: await rectOf(article(page).locator(':scope > div').nth(0), 'the header block'),
        cover: await rectOf(article(page).locator(':scope > div').nth(1), 'the cover'),
        row: await rectOf(row(page), 'the body row'),
      }
      const band = main(page).locator('section').filter({ has: page.getByRole('link', { name: 'Book a demo' }) })
      const b = await rectOf(band, 'the band')
      const inner = await rectOf(band.locator(':scope > div'), 'the band inner row')
      const bandPad = await paddingsOf(band)
      await attachJson(testInfo, `blog-article-edge-${width}`, { edge, article: a, pad, ...parts, band: b, inner, bandPad })
      expect(Math.abs(rightOf(a) - edge), 'the article stops short of the content edge').toBeLessThanOrEqual(SLACK_PX)
      for (const [name, r] of Object.entries(parts)) {
        expect(Math.abs(rightOf(r) - (rightOf(a) - pad.right)), `${name} stops short of the article padding edge`).toBeLessThanOrEqual(SLACK_PX)
        expect(Math.abs(r.x - (a.x + pad.left)), `${name} does not start at the article padding edge`).toBeLessThanOrEqual(SLACK_PX)
      }
      expect(Math.abs(rightOf(b) - edge), 'the band stops short of the content edge').toBeLessThanOrEqual(SLACK_PX)
      expect(Math.abs(rightOf(inner) - (rightOf(b) - bandPad.right)), 'the band inner row stops short of the band padding edge').toBeLessThanOrEqual(SLACK_PX)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog article: the contents list ends at the row's right edge (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await open(page, width)
      const body = await rectOf(row(page).locator(':scope > div').first(), 'the body column')
      const t = await rectOf(toc(page), 'the TOC')
      const r = await rectOf(row(page), 'the body row')
      await attachJson(testInfo, `blog-article-toc-edge-${width}`, { body, toc: t, row: r })
      expect(Math.abs(rightOf(t) - rightOf(r)), 'the TOC does not end at the row right edge').toBeLessThanOrEqual(SLACK_PX)
      expect(Math.abs(body.x - r.x), 'the body does not start at the row left edge').toBeLessThanOrEqual(SLACK_PX)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog article: the body column keeps its measure (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await open(page, width)
      const bodyLoc = row(page).locator(':scope > div').first()
      const body = await rectOf(bodyLoc, 'the body column')
      const maxWidth = await maxWidthOf(bodyLoc)
      const summary = await rectOf(article(page).locator('p.t-lead'), 'the summary')
      const r = await rectOf(row(page), 'the body row')
      await attachJson(testInfo, `blog-article-measure-${width}`, { body, maxWidth, summary, row: r })
      if (width >= BAND_EDGE_MIN_WIDTH) {
        expect(Math.abs(body.width - maxWidth), 'the body is narrower than its measure').toBeLessThanOrEqual(SLACK_PX)
      } else {
        expect(body.width, 'the body is wider than its measure').toBeLessThanOrEqual(maxWidth + SLACK_PX)
      }
      expect(summary.width, 'the summary is wider than the body measure').toBeLessThanOrEqual(maxWidth + SLACK_PX)
      expect(body.width, 'the body fills the whole row').toBeLessThan(r.width)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })
  }

  for (const width of ARTICLE_WIDTHS.filter((w) => w >= BAND_EDGE_MIN_WIDTH)) {
    test(`blog article: the band buttons reach the band's right edge (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await open(page, width)
      const band = main(page).locator('section').filter({ has: page.getByRole('link', { name: 'Book a demo' }) })
      const inner = await rectOf(band.locator(':scope > div'), 'the band inner row')
      const demo = await rectOf(band.getByRole('link', { name: 'Book a demo' }), 'Book a demo')
      const sub = await rectOf(band.getByRole('button', { name: 'Subscribe' }), 'Subscribe')
      await attachJson(testInfo, `blog-article-band-edge-${width}`, { inner, demo, sub })
      expect(Math.abs(Math.max(rightOf(demo), rightOf(sub)) - rightOf(inner)), 'the band buttons stop short of the inner row edge').toBeLessThanOrEqual(SLACK_PX)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })
  }
})

test.describe('blog article without a contents list at wide widths', () => {
  const article = (page: Page) => main(page).locator('article')
  const row = (page: Page) => article(page).locator(':scope > div').nth(2)

  for (const width of ARTICLE_WIDTHS) {
    test(`blog article without a contents list: the row reaches the content edge (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 1080)
      await visit(page, '/lagos-office')
      await expect(article(page).locator('h1')).toBeVisible()
      expect(await main(page).locator('aside[aria-label="On this page"]').count(), 'control: the post has no contents list').toBe(0)
      const a = await rectOf(article(page), 'the article')
      const pad = await paddingsOf(article(page))
      const r = await rectOf(row(page), 'the body row')
      const bodyLoc = row(page).locator(':scope > div').first()
      const body = await rectOf(bodyLoc, 'the body column')
      const maxWidth = await maxWidthOf(bodyLoc)
      await attachJson(testInfo, `blog-article-no-toc-${width}`, { article: a, pad, row: r, body, maxWidth })
      expect(Math.abs(rightOf(r) - (rightOf(a) - pad.right)), 'the row stops short of the article padding edge').toBeLessThanOrEqual(SLACK_PX)
      expect(Math.abs(body.x - r.x), 'the body does not start at the row left edge').toBeLessThanOrEqual(SLACK_PX)
      if (width >= BAND_EDGE_MIN_WIDTH) {
        expect(Math.abs(body.width - maxWidth), 'the body is narrower than its measure').toBeLessThanOrEqual(SLACK_PX)
      }
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

    test(`blog phone: no view scrolls sideways (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 800)
      const readings: Record<string, unknown> = {}
      const check = async (label: string): Promise<void> => {
        const r = await page.evaluate(() => {
          const m = document.getElementById('blog-main')
          return {
            doc: document.documentElement.scrollWidth - document.documentElement.clientWidth,
            main: m ? m.scrollWidth - m.clientWidth : 0,
          }
        })
        readings[label] = r
        expect(r.doc, `${label}: the page scrolls sideways`).toBeLessThanOrEqual(1)
        expect(r.main, `${label}: main scrolls sideways`).toBeLessThanOrEqual(1)
        const a = await rectOf(aside(page), 'aside')
        const m = await rectOf(main(page), '#blog-main')
        expect(a.y + a.height, `${label}: the sidebar sits above main`).toBeLessThanOrEqual(m.y + SLACK_PX)
      }

      await visit(page)
      await expect(main(page).locator(`section ${GRID} button`).first(), 'home: no band card').toBeVisible()
      await check('home')

      await visit(page, '/category/regulation')
      await expect(main(page).getByRole('button', { name: new RegExp(WHAT_CHANGES) }).first()).toBeVisible()
      await check('category')

      await visit(page, '/')
      await expect(main(page).locator('h1')).toBeVisible()
      await header(page).locator('label').getByRole('textbox').fill('invoice')
      await expect(main(page).locator('h2')).toContainText(/match/)
      expect(await main(page).locator('button.blog-row').count(), 'control: no result row').toBeGreaterThan(0)
      await check('search')

      await visit(page, '/what-changes')
      await expect(main(page).locator('article h1')).toBeVisible()
      await expect(main(page).locator('aside[aria-label="On this page"]'), 'control: no TOC').toBeVisible()
      await expect(main(page).getByTestId('blog-demo-scene'), 'control: no demo').toBeVisible()
      await check('article')
      await attachJson(testInfo, `blog-phone-sweep-${width}`, readings)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog phone: the article content sits inside the viewport (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 800)
      await visit(page, '/what-changes')
      const art = main(page).locator('article')
      await expect(art.locator('h1')).toBeVisible()
      const toc = main(page).locator('aside[aria-label="On this page"]')
      await expect(toc, 'control: no TOC').toBeVisible()
      const demo = main(page).getByTestId('blog-demo')
      await expect(demo, 'control: no demo frame').toBeVisible()
      const body = await rectOf(art.locator(':scope > div').nth(2).locator(':scope > div').first(), 'the body column')
      const t = await rectOf(toc, 'the TOC')
      const d = await rectOf(demo, 'the demo frame')
      await attachJson(testInfo, `blog-phone-article-${width}`, { body, toc: t, demo: d })
      const viewport = { x: 0, y: -1e6, width, height: 2e6 }
      expect(enclosesRect(viewport, body, SLACK_PX), 'the body column leaves the viewport width').toBe(true)
      expect(enclosesRect(viewport, t, SLACK_PX), 'the TOC leaves the viewport width').toBe(true)
      expect(enclosesRect(viewport, d, SLACK_PX), 'the demo frame leaves the viewport width').toBe(true)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog phone: the article aligns with home (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await prepare(page)
      await resize(page, width, 800)
      await visit(page)
      await expect(main(page).locator('h1')).toBeVisible()
      const home = await rectOf(main(page).locator('h1'), 'the home h1')
      await visit(page, '/what-changes')
      await expect(main(page).locator('article h1')).toBeVisible()
      const art = await rectOf(main(page).locator('article h1'), 'the article h1')
      await attachJson(testInfo, `blog-phone-align-${width}`, { home, article: art })
      expect(Math.abs(art.x - home.x), 'the article h1 and the home h1 start at different left edges').toBeLessThanOrEqual(SLACK_PX)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })
  }
})

// Words whose glyphs sit on more than one line inside `root`: a mid-word break.
async function brokenWords(root: Locator): Promise<string[]> {
  return root.evaluate((el) => {
    const broken: string[] = []
    const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const text = n.textContent ?? ''
      for (const m of text.matchAll(/[^\s-]+/g)) {
        const r = document.createRange()
        r.setStart(n, m.index ?? 0)
        r.setEnd(n, (m.index ?? 0) + m[0].length)
        const tops = Array.from(r.getClientRects()).map((c) => Math.round(c.top))
        if (tops.length > 0 && Math.max(...tops) - Math.min(...tops) > 2) broken.push(m[0])
      }
    }
    return broken
  })
}

test.describe('blog phone at 375: TOC and demo', () => {
  test('blog phone: a TOC click lands the heading below the sticky header and inside the viewport (375)', async ({ page }) => {
    const errors = collectErrors(page)
    await prepare(page)
    await resize(page, 375, 800)
    await visit(page, '/what-changes')
    const row = main(page).locator('aside[aria-label="On this page"] button.blog-toc').nth(1)
    await expect(row, 'control: no second TOC row').toBeVisible()
    await row.click()
    let last = -1
    await expect
      .poll(async () => {
        const y = await page.evaluate(() => window.scrollY)
        const settled = y === last && y > 0
        last = y
        return settled
      })
      .toBe(true)
    const h = await rectOf(main(page).locator('h2#sec-1'), 'the clicked heading')
    const hd = await rectOf(header(page), 'the header')
    const vh = await page.evaluate(() => window.innerHeight)
    expect(h.y, 'the heading top sits under the header').toBeGreaterThanOrEqual(hd.y + hd.height - SLACK_PX)
    expect(h.y + h.height, 'the heading leaves the viewport').toBeLessThanOrEqual(vh + SLACK_PX)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('blog phone: TOC numerals do not break mid-word (375)', async ({ page }) => {
    const errors = collectErrors(page)
    await prepare(page)
    await resize(page, 375, 800)
    await visit(page, '/what-changes')
    await expect(main(page).locator('aside[aria-label="On this page"] button.blog-toc span.mono').first(), 'control: no TOC numeral').toBeVisible()
    expect(await brokenWords(main(page).locator('aside[aria-label="On this page"]')), 'TOC words broken mid-word').toEqual([])
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('blog phone: demo words do not break mid-word (375)', async ({ page }) => {
    const errors = collectErrors(page)
    await prepare(page)
    await resize(page, 375, 800)
    for (const path of ['/what-changes', '/csv-import']) {
      await visit(page, path)
      const scene = main(page).getByTestId('blog-demo-scene')
      await expect(scene, `${path}: control: no demo scene`).toBeVisible()
      expect(await brokenWords(scene), `${path}: demo words broken mid-word`).toEqual([])
    }
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
})

const SEEDED_TITLES = [
  'The private pilot opens to 20 more businesses', // migrations/20261017135635_seed_blog.sql
  WHAT_CHANGES,
  'Document recognition now learns each supplier layout',
  'The Sage connector is now available',
]

test('blog home: live posts render', async ({ page }) => {
  const errors = collectErrors(page)
  await prepare(page)
  await visit(page)
  const label = main(page).locator('section').first().locator('.t-step').first()
  await expect(label, 'control: no featured card').toBeVisible()
  await expect(label).toHaveText(/^(Featured|Latest) · /)
  await expect(main(page).locator(`section ${GRID} button`).first(), 'no band card rendered').toBeVisible()
  const titles = await main(page).locator(`section ${GRID} button > span:nth-child(2)`).allTextContents()
  expect(titles.some((t) => SEEDED_TITLES.includes(t)), `no band holds a seeded post; saw ${titles.join(' | ')}`).toBe(true)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test.describe('blog routes', () => {
  // Seeded on forks (migrations/20261017135635_seed_blog.sql); the blog server answers 2xx for these.
  const COLD: [string, string[]][] = [
    ['/subscribe', ['Blog', 'Subscribe']],
    ['/subscribe/confirm', ['Blog', 'Subscribe']],
    ['/category/regulation', ['Blog']],
    ['/what-changes', ['Blog', 'Regulation & FIRS news', WHAT_CHANGES]],
  ]
  // Unknown paths answer 404 (BLOG-05) and still boot the SPA's not-found state.
  const MISSING = ['/category/x', '/a-post', '/a/b', '/what-changes/extra']

  test('blog routes: a cold deep link renders the shell', async ({ page }) => {
    const errors = collectErrors(page)
    await prepare(page)
    for (const [path] of COLD) {
      await visit(page, path)
      await expect(crumbs(page).getByRole('button').first(), `${path}: first crumb`).toHaveText('Blog')
      await expect(aside(page).getByRole('button', { name: 'Subscribe' }), `${path}: the Subscribe button`).toBeVisible()
    }
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  for (const path of MISSING) {
    test(`blog routes: a cold deep link to ${path} renders the not-found shell`, async ({ page }) => {
      const url = `${BLOG_URL}${path}`
      const errors = collectErrors(page, url)
      await prepare(page)
      const response = await page.goto(url)
      expect(response?.status(), `${path}: status`).toBe(404)
      await expect(main(page), `${path}: #blog-main never rendered`).toBeVisible()
      await expect(header(page), `${path}: #blog-header never rendered`).toBeVisible()
      await expect(crumbs(page).getByRole('button').first(), `${path}: first crumb`).toHaveText('Blog')
      await expect(aside(page).getByRole('button', { name: 'Subscribe' }), `${path}: the Subscribe button`).toBeVisible()
      await expect(main(page).getByRole('heading', { name: 'This page is not on the blog.' }), `${path}: not-found heading`).toBeVisible()
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })
  }

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

test.describe('blog demo at wide widths', () => {
  const frame = (page: Page) => main(page).getByTestId('blog-demo')
  const scene = (page: Page) => frame(page).getByTestId('blog-demo-scene')
  const scrub = (page: Page) => frame(page).getByTestId('blog-demo-scrub')

  async function open(page: Page, width: number): Promise<void> {
    await prepare(page)
    await resize(page, width, 1080)
    await visit(page, '/csv-import')
    await expect(frame(page).getByText('Import from CSV or Excel'), 'the demo title never rendered').toBeVisible()
  }

  for (const width of WIDE_WIDTHS) {
    test(`blog demo: the scene and controls sit in the frame (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await open(page, width)
      const f = await rectOf(frame(page), 'the demo frame')
      const sc = await rectOf(scene(page), 'the scene box')
      const play = await rectOf(frame(page).getByRole('button', { name: 'Play or pause' }), 'play')
      const time = await rectOf(frame(page).getByText(/^\d:\d\d \/ \d:\d\d$/), 'the time')
      const sb = await rectOf(scrub(page), 'the scrub bar')
      await attachJson(testInfo, `blog-demo-frame-${width}`, { frame: f, scene: sc, play, time, scrub: sb })
      for (const [name, r] of Object.entries({ scene: sc, play, time, scrub: sb }))
        expect(enclosesRect(f, r, SLACK_PX), `the ${name} leaves the demo frame`).toBe(true)
      expect(rectsOverlap(play, time), 'play and time overlap').toBe(false)
      expect(rectsOverlap(play, sb), 'play and scrub overlap').toBe(false)
      expect(rectsOverlap(time, sb), 'time and scrub overlap').toBe(false)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })

    test(`blog demo: the last row fits at the last step (${width})`, async ({ page }, testInfo) => {
      const errors = collectErrors(page)
      await open(page, width)
      await scrub(page).scrollIntoViewIfNeeded()
      const sb = await rectOf(scrub(page), 'the scrub bar')
      await page.mouse.click(sb.x + sb.width * 0.99, sb.y + sb.height / 2)
      await expect(frame(page).getByText('04 / 04')).toBeVisible()
      const sc = await rectOf(scene(page), 'the scene box')
      const row = await rectOf(scene(page).locator('div', { hasText: 'Needs fixing' }).last(), 'the last row')
      await attachJson(testInfo, `blog-demo-last-row-${width}`, { scene: sc, row })
      expect(enclosesRect(sc, row, SLACK_PX), 'the last row leaves the scene box').toBe(true)
      expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
    })
  }
})
