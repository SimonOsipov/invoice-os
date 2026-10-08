// Library layout, phone layout, tour and outbound links on the PR fork. Each case asserts a
// relationship (containment, side by side, alignment, no overflow), never a raw dimension, and
// nothing is exempt. Groups and features are discovered from the DOM (`nav-<gid>`, `fc-<fid>`);
// nothing is imported from frontend/.
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { resolveTarget } from '../targets'
import { enclosesRect, PHONE_WIDTHS, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'

const LIBRARY_URL = resolveTarget('LIBRARY_URL')
const BAKED_APP_URL = resolveTarget('BAKED_APP_URL')
const BAKED_LANDING_URL = resolveTarget('BAKED_LANDING_URL')

const MIN_GROUPS = 11
const MIN_FEATURES = 26
const TOUR_STEPS = 14
// The tour's seven stops, in order: [group id, feature id]. Odd steps spotlight the group's nav
// button, even steps its feature card.
const TOUR_STOPS: [string, string][] = [
  ['invoices', 'import-files'],
  ['recognition', 'read-documents'],
  ['rules', 'validate'],
  ['approvals', 'approval-queue'],
  ['clearance', 'submit-clear'],
  ['audit', 'audit-trail'],
  ['reports', 'overview'],
]

type Discovered = { gid: string; fids: string[] }
let GROUPS: Discovered[] = []
const FEATURES = () => GROUPS.flatMap((g) => g.fids.map((fid) => ({ gid: g.gid, fid })))

type LibWin = Window & { __libOver: (el: Element) => number }

// Seeds a consent answer (no cookie notice over the page) and a helper that reads how far a box's
// non-empty descendants poke past its own edges.
async function prepare(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.localStorage.setItem('asc_consent', JSON.stringify({ analytics: false, ts: '2026-01-01T00:00:00Z', v: 1 }))
    ;(window as unknown as LibWin).__libOver = (box: Element): number => {
      const b = box.getBoundingClientRect()
      let worst = 0
      for (const el of Array.from(box.querySelectorAll('*'))) {
        const r = el.getBoundingClientRect()
        if (r.width <= 0 || r.height <= 0) continue
        worst = Math.max(worst, b.left - r.left, r.right - b.right, b.top - r.top, r.bottom - b.bottom)
      }
      return Math.round(worst * 10) / 10
    }
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

async function visit(page: Page, path: string): Promise<void> {
  await page.goto(`${LIBRARY_URL}${path}`)
  await expect(page.locator('#lib-main'), `${path}: #lib-main never rendered`).toBeVisible()
}

async function resize(page: Page, width: number, height: number): Promise<void> {
  await page.setViewportSize({ width, height })
  await page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))))
}

const rectOf = async (loc: Locator, label: string): Promise<Rect> => {
  const box = await loc.boundingBox()
  if (!box) throw new Error(`${label} has no box`)
  return box
}

// Reads the groups from the sidebar and each group's features from its page (D10).
test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  try {
    await prepare(page)
    await visit(page, '/')
    const gids = await page.locator('aside [id^="nav-"]').evaluateAll((els) => els.map((e) => e.id.slice(4)))
    expect(gids.length, `the sidebar lists ${gids.length} groups`).toBeGreaterThanOrEqual(MIN_GROUPS)
    GROUPS = []
    for (const gid of gids) {
      await visit(page, `/${gid}`)
      const fids = await page.locator('[id^="fc-"]').evaluateAll((els) => els.map((e) => e.id.slice(3)))
      expect(fids.length, `group ${gid} lists no feature cards`).toBeGreaterThan(0)
      GROUPS.push({ gid, fids })
    }
    expect(FEATURES().length, 'the library lists too few features').toBeGreaterThanOrEqual(MIN_FEATURES)
  } finally {
    await page.close()
  }
})

const aside = (page: Page) => page.locator('aside')
const main = (page: Page) => page.locator('#lib-main')
const tourButton = (page: Page) => page.getByRole('button', { name: 'Take the tour' })

test.describe('library layout at wide widths', () => {
  test('library shell: the sidebar and main sit side by side and fill the viewport', async ({ page }, testInfo) => {
    test.setTimeout(120_000)
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    for (const path of ['/', '/rules', '/rules/validate']) {
      await visit(page, path)
      for (const width of WIDE_WIDTHS) {
        await resize(page, width, 1080)
        await settleAnimations(aside(page), main(page))
        const a = await rectOf(aside(page), 'aside')
        const m = await rectOf(main(page), '#lib-main')
        const win = await page.evaluate(() => ({ w: window.innerWidth, h: window.innerHeight }))
        const gap = m.x - (a.x + a.width)
        measured.push({ path, width, aside: a, main: m, gap, win })
        const at = `${path} @${width}`
        expect.soft(a.x, `${at}: the sidebar starts at the left edge`).toBeLessThanOrEqual(1)
        expect.soft(gap, `${at}: the sidebar ends before main starts`).toBeGreaterThanOrEqual(-1)
        expect.soft(gap, `${at}: nothing sits between the sidebar and main`).toBeLessThanOrEqual(1)
        expect.soft(m.x + m.width, `${at}: main reaches the right edge`).toBeGreaterThanOrEqual(win.w - 1)
        expect.soft(Math.abs(a.height - win.h), `${at}: the sidebar fills the viewport height`).toBeLessThanOrEqual(1)
        expect.soft(Math.abs(m.height - win.h), `${at}: main fills the viewport height`).toBeLessThanOrEqual(1)
      }
    }
    await attachJson(testInfo, 'library-shell', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('library shell: #lib-main never scrolls sideways', async ({ page }, testInfo) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    const paths = ['/', ...GROUPS.map((g) => `/${g.gid}`), '/rules/validate']
    for (const path of paths) {
      await visit(page, path)
      for (const width of WIDE_WIDTHS) {
        await resize(page, width, 1080)
        const r = await main(page).evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
        measured.push({ path, width, ...r })
        expect.soft(r.clientWidth, `${path} @${width}: #lib-main has no box`).toBeGreaterThan(0)
        expect.soft(r.scrollWidth - r.clientWidth, `${path} @${width}: #lib-main scrolls sideways`).toBeLessThanOrEqual(1)
      }
    }
    await attachJson(testInfo, 'library-no-sideways', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('library cards stay inside the main column', async ({ page }, testInfo) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    const read = (selector: string) =>
      page.evaluate((sel) => {
        const m = document.getElementById('lib-main')!
        const mr = m.getBoundingClientRect()
        const left = mr.left + m.clientLeft
        return {
          left,
          right: left + m.clientWidth,
          cards: Array.from(document.querySelectorAll(sel)).map((c, i) => {
            const r = c.getBoundingClientRect()
            return { id: c.id || `#${i}`, x: r.x, y: r.y, width: r.width, height: r.height }
          }),
        }
      }, selector)
    const pages: { path: string; selector: string; floor: number }[] = [
      { path: '/', selector: '#lib-main .lib-card', floor: MIN_GROUPS },
      ...GROUPS.map((g) => ({ path: `/${g.gid}`, selector: '[id^="fc-"]', floor: g.fids.length })),
    ]
    let featureCards = 0
    for (const { path, selector, floor } of pages) {
      await visit(page, path)
      for (const width of WIDE_WIDTHS) {
        await resize(page, width, 1080)
        const { left, right, cards } = await read(selector)
        const at = `${path} @${width}`
        measured.push({ path, width, left, right, cards })
        expect.soft(cards.length, `${at}: card count`).toBeGreaterThanOrEqual(floor)
        if (path !== '/' && width === WIDE_WIDTHS[0]) featureCards += cards.length
        for (const c of cards) {
          expect.soft(c.x, `${at}: ${c.id} starts inside main`).toBeGreaterThanOrEqual(left - 1)
          expect.soft(c.x + c.width, `${at}: ${c.id} ends inside main`).toBeLessThanOrEqual(right + 1)
        }
        for (let i = 0; i < cards.length; i++) {
          for (let j = i + 1; j < cards.length; j++) {
            expect.soft(rectsOverlap(cards[i], cards[j]), `${at}: ${cards[i].id} and ${cards[j].id} overlap`).toBe(false)
          }
        }
      }
    }
    expect.soft(featureCards, 'feature cards seen across the group pages').toBeGreaterThanOrEqual(MIN_FEATURES)
    await attachJson(testInfo, 'library-cards', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('library stepper stays at the top of #lib-main after a scroll', async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    await visit(page, '/rules')
    for (const width of WIDE_WIDTHS) {
      // A short viewport so the page is taller than it and the scroll control can move.
      await resize(page, width, 500)
      const scrolled = await main(page).evaluate((el) => {
        el.scrollTop = 800
        return el.scrollTop
      })
      const stepper = page.getByTestId('lib-stepper')
      const s = await rectOf(stepper, 'lib-stepper')
      const m = await rectOf(main(page), '#lib-main')
      measured.push({ width, scrolled, stepper: s, main: m })
      expect(scrolled, `@${width}: control, #lib-main scrolled`).toBeGreaterThan(0)
      expect.soft(Math.abs(s.y - m.y), `@${width}: the stepper stays at the top of #lib-main`).toBeLessThanOrEqual(1)
    }
    await attachJson(testInfo, 'library-stepper', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('library player scene stays inside its 350px box at every step', async ({ page }, testInfo) => {
    test.setTimeout(480_000)
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    for (const { gid, fid } of FEATURES()) {
      await visit(page, `/${gid}/${fid}`)
      const player = page.getByTestId('lib-player')
      const label = player.getByText(/^\d{2} \/ \d{2}$/)
      await expect(label).toBeVisible()
      const n = Number((await label.textContent())!.slice(5))
      await page.getByRole('button', { name: 'Play or pause' }).click()
      const scene = page.getByTestId('lib-scene')
      const scrub = page.locator('#lib-scrub')
      for (const width of WIDE_WIDTHS) {
        await resize(page, width, 1080)
        for (let i = 0; i < n; i++) {
          const bar = await rectOf(scrub, '#lib-scrub')
          await scrub.click({ position: { x: ((i + 0.5) / n) * bar.width, y: bar.height / 2 } })
          const want = `${String(i + 1).padStart(2, '0')} / ${String(n).padStart(2, '0')}`
          await expect(label, `${gid}/${fid} @${width}: seek to step ${i + 1}`).toHaveText(want)
          await settleAnimations(scene)
          const over = await scene.evaluate((el) => (window as unknown as LibWin).__libOver(el))
          measured.push({ gid, fid, width, step: i + 1, steps: n, over })
          expect.soft(over, `${gid}/${fid} @${width} step ${i + 1}: the scene pokes out of its box`).toBeLessThanOrEqual(1)
        }
      }
    }
    await attachJson(testInfo, 'library-scene', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('library card thumbnails stay inside their window, above the play disc', async ({ page }, testInfo) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    for (const { gid } of GROUPS) {
      await visit(page, `/${gid}`)
      for (const width of WIDE_WIDTHS) {
        await resize(page, width, 1080)
        const cards = await page.evaluate(() =>
          Array.from(document.querySelectorAll('[id^="fc-"]')).map((card) => {
            const rect = (el: Element | null) => {
              const r = el!.getBoundingClientRect()
              return { x: r.x, y: r.y, width: r.width, height: r.height }
            }
            const win = card.querySelector('[data-testid="lib-thumb-window"]')
            const disc = card.querySelector('[data-testid="lib-play-disc"]')
            const scene = card.querySelector('[data-testid="lib-thumb-scene"]')
            return {
              id: card.id,
              found: !!(win && disc && scene),
              frame: win ? rect(win.parentElement) : null,
              window: win ? rect(win) : null,
              disc: disc ? rect(disc) : null,
              over: scene ? (window as unknown as LibWin).__libOver(scene) : null,
            }
          }),
        )
        expect.soft(cards.length, `/${gid} @${width}: card count`).toBeGreaterThan(0)
        measured.push({ gid, width, cards })
        for (const c of cards) {
          const at = `/${gid} @${width} ${c.id}`
          expect.soft(c.found, `${at}: thumbnail hooks present`).toBe(true)
          if (!c.found || !c.frame || !c.window || !c.disc || c.over === null) continue
          expect.soft(enclosesRect(c.frame, c.window, 1), `${at}: the thumbnail window pokes out of the card's 192px window`).toBe(true)
          expect.soft(c.window.y + c.window.height, `${at}: the thumbnail window ends above the play disc`).toBeLessThanOrEqual(c.disc.y + 1)
          expect.soft(c.over, `${at}: the thumbnail scene pokes out of its box`).toBeLessThanOrEqual(1)
        }
      }
    }
    await attachJson(testInfo, 'library-thumbnails', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('library feature panels sit side by side inside #lib-main', async ({ page }, testInfo) => {
    test.setTimeout(240_000)
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    for (const { gid, fid } of FEATURES()) {
      await visit(page, `/${gid}/${fid}`)
      for (const width of WIDE_WIDTHS) {
        await resize(page, width, 1080)
        const read = await page.evaluate(() => {
          const m = document.getElementById('lib-main')!
          const mr = m.getBoundingClientRect()
          const left = mr.left + m.clientLeft
          const panels = document.querySelector('[data-testid="lib-panels"]')
          return {
            left,
            right: left + m.clientWidth,
            panels: Array.from(panels?.children ?? []).map((c) => {
              const r = c.getBoundingClientRect()
              return { x: r.x, y: r.y, width: r.width, height: r.height }
            }),
          }
        })
        const at = `${gid}/${fid} @${width}`
        measured.push({ gid, fid, width, ...read })
        expect.soft(read.panels.length, `${at}: panel count`).toBe(3)
        const p = read.panels
        for (let i = 0; i < p.length; i++) {
          expect.soft(p[i].x, `${at}: panel ${i + 1} starts inside main`).toBeGreaterThanOrEqual(read.left - 1)
          expect.soft(p[i].x + p[i].width, `${at}: panel ${i + 1} ends inside main`).toBeLessThanOrEqual(read.right + 1)
          expect.soft(Math.abs(p[i].y - p[0].y), `${at}: panel ${i + 1} top matches panel 1`).toBeLessThanOrEqual(1)
          if (i > 0) {
            expect.soft(p[i].x, `${at}: panel ${i + 1} sits right of panel ${i}`).toBeGreaterThan(p[i - 1].x)
            expect.soft(rectsOverlap(p[i - 1], p[i]), `${at}: panels ${i} and ${i + 1} overlap`).toBe(false)
          }
        }
      }
    }
    await attachJson(testInfo, 'library-panels', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test("library links carry this fork's landing and app URLs", async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown> = {}
    await visit(page, '/')
    const demo = page.getByRole('link', { name: 'Book the Demo' })
    expect(await demo.count(), '/: Book the Demo links').toBeGreaterThanOrEqual(1)
    const demoHrefs = await demo.evaluateAll((els) => els.map((e) => e.getAttribute('href')))
    measured.demo = demoHrefs
    for (const href of demoHrefs) expect.soft(href, '/: Book the Demo href').toBe(`${BAKED_LANDING_URL}/?demo`)

    await visit(page, '/rules/validate')
    const open = page.getByRole('link', { name: 'Open in Platform' })
    await expect(open, '/rules/validate: Open in Platform links').toHaveCount(1)
    measured.open = await open.getAttribute('href')
    expect.soft(measured.open, '/rules/validate: Open in Platform href').toBe(`${BAKED_APP_URL}/invoices?via=library`)

    await visit(page, '/notifications/alerts')
    await expect(page.getByRole('link', { name: 'Open in Platform' }), '/notifications/alerts: a coming-soon feature has no platform link').toHaveCount(0)

    await attachJson(testInfo, 'library-links', { ...measured, BAKED_APP_URL, BAKED_LANDING_URL })
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
})

test.describe('library at phone widths, 768 and the tour', () => {
  const PAGES = ['/', '/rules', '/rules/validate']

  test('library phone: no sideways scroll, aside above main, content inside the viewport', async ({ page }, testInfo) => {
    test.setTimeout(120_000)
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    for (const width of PHONE_WIDTHS) {
      await page.setViewportSize({ width, height: 800 })
      for (const path of PAGES) {
        await visit(page, path)
        await resize(page, width, 800)
        const read = await page.evaluate(() => {
          const rect = (el: Element) => {
            const r = el.getBoundingClientRect()
            return { id: el.id || el.getAttribute('data-testid') || el.className, left: r.left, right: r.right, top: r.top, bottom: r.bottom }
          }
          const doc = document.documentElement
          return {
            scrollWidth: doc.scrollWidth,
            clientWidth: doc.clientWidth,
            aside: rect(document.querySelector('aside')!),
            main: rect(document.getElementById('lib-main')!),
            homeCards: Array.from(document.querySelectorAll('#lib-main .lib-card')).map(rect),
            featureCards: Array.from(document.querySelectorAll('[id^="fc-"]')).map(rect),
            stepper: Array.from(document.querySelectorAll('[data-testid="lib-stepper"]')).map(rect),
            player: Array.from(document.querySelectorAll('[data-testid="lib-player"]')).map(rect),
          }
        })
        const at = `${path} @${width}`
        measured.push({ path, width, ...read })
        expect.soft(read.scrollWidth - read.clientWidth, `${at}: the document scrolls sideways`).toBeLessThanOrEqual(1)
        expect.soft(read.aside.bottom, `${at}: the sidebar sits above main`).toBeLessThanOrEqual(read.main.top + 1)
        if (path === '/') expect.soft(read.homeCards.length, `${at}: home group cards`).toBeGreaterThanOrEqual(MIN_GROUPS)
        if (path === '/rules') expect.soft(read.featureCards.length, `${at}: feature cards`).toBeGreaterThanOrEqual(3)
        expect.soft(read.stepper.length, `${at}: the stepper renders`).toBe(1)
        if (path === '/rules/validate') expect.soft(read.player.length, `${at}: the player renders`).toBe(1)
        for (const box of [...read.homeCards, ...read.featureCards, ...read.stepper, ...read.player]) {
          expect.soft(box.left, `${at}: ${box.id} starts inside the viewport`).toBeGreaterThanOrEqual(-1)
          expect.soft(box.right, `${at}: ${box.id} ends inside the viewport`).toBeLessThanOrEqual(read.clientWidth + 1)
        }
      }
    }
    await attachJson(testInfo, 'library-phone', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('library phone: no tour button, Book the Demo visible', async ({ page }, testInfo) => {
    test.setTimeout(120_000)
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    for (const width of [...PHONE_WIDTHS, 768]) {
      await page.setViewportSize({ width, height: 800 })
      for (const path of PAGES) {
        await visit(page, path)
        const tour = await tourButton(page).count()
        const asideTour = await page.locator('aside .lib-tour').count()
        const demo = page.getByRole('link', { name: 'Book the Demo' }).first()
        const demoVisible = await demo.isVisible()
        measured.push({ path, width, tour, asideTour, demoVisible })
        const at = `${path} @${width}`
        if (width === 768) {
          expect.soft(tour, `${at}: control, the tour button shows at 768`).toBeGreaterThanOrEqual(1)
        } else {
          expect.soft(tour, `${at}: no tour button`).toBe(0)
          expect.soft(asideTour, `${at}: no tour button in the sidebar`).toBe(0)
        }
        expect.soft(demoVisible, `${at}: Book the Demo is visible`).toBe(true)
      }
    }
    await attachJson(testInfo, 'library-phone-tour-button', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('library phone: the player is full width', async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    for (const width of PHONE_WIDTHS) {
      await page.setViewportSize({ width, height: 800 })
      await visit(page, '/rules/validate')
      const read = await page.getByTestId('lib-player').evaluate((el) => {
        const r = el.getBoundingClientRect()
        const section = el.closest('section')!
        const sr = section.getBoundingClientRect()
        const cs = getComputedStyle(section)
        return {
          left: r.left,
          right: r.right,
          contentLeft: sr.left + parseFloat(cs.borderLeftWidth) + parseFloat(cs.paddingLeft),
          contentRight: sr.right - parseFloat(cs.borderRightWidth) - parseFloat(cs.paddingRight),
        }
      })
      measured.push({ width, ...read })
      expect.soft(Math.abs(read.left - read.contentLeft), `@${width}: the player starts at its section's content edge`).toBeLessThanOrEqual(1)
      expect.soft(Math.abs(read.right - read.contentRight), `@${width}: the player ends at its section's content edge`).toBeLessThanOrEqual(1)
    }
    await attachJson(testInfo, 'library-phone-player', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('library at 768: the desktop layout holds', async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    await page.setViewportSize({ width: 768, height: 800 })
    for (const path of PAGES) {
      await visit(page, path)
      const a = await rectOf(aside(page), 'aside')
      const m = await rectOf(main(page), '#lib-main')
      const sc = await main(page).evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
      const tour = await tourButton(page).count()
      measured.push({ path, aside: a, main: m, ...sc, tour })
      expect.soft(a.x + a.width, `${path}: the sidebar sits left of main`).toBeLessThanOrEqual(m.x + 1)
      expect.soft(Math.abs(a.y - m.y), `${path}: the sidebar and main tops line up`).toBeLessThanOrEqual(1)
      expect.soft(sc.scrollWidth - sc.clientWidth, `${path}: #lib-main scrolls sideways`).toBeLessThanOrEqual(1)
      expect.soft(tour, `${path}: the tour button shows`).toBeGreaterThanOrEqual(1)
    }
    await attachJson(testInfo, 'library-768', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('library phone: the stepper sticks to the viewport top after a scroll', async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await prepare(page)
    await page.setViewportSize({ width: 375, height: 800 })
    await visit(page, '/rules')
    const scrollY = await page.evaluate(() => {
      window.scrollTo(0, 900)
      return window.scrollY
    })
    const s = await rectOf(page.getByTestId('lib-stepper'), 'lib-stepper')
    await attachJson(testInfo, 'library-phone-stepper', { scrollY, stepper: s })
    expect(scrollY, 'control: the document scrolled').toBeGreaterThan(0)
    expect.soft(Math.abs(s.y), 'the stepper sits at the viewport top').toBeLessThanOrEqual(1)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })

  test('library tour: each spotlight encloses its target and the callout clears it', async ({ page }, testInfo) => {
    test.setTimeout(240_000)
    const errors = collectErrors(page)
    await prepare(page)
    const measured: Record<string, unknown>[] = []
    for (const [width, height] of [[1280, 600], [1280, 700], [1440, 900]] as const) {
      await page.setViewportSize({ width, height })
      await visit(page, '/')
      await main(page).getByRole('button', { name: 'Take the tour' }).click()
      const spot = page.getByTestId('lib-tour-spot')
      const callout = page.getByTestId('lib-tour-callout')
      for (let n = 1; n <= TOUR_STEPS; n++) {
        const [gid, fid] = TOUR_STOPS[Math.ceil(n / 2) - 1]
        const targetId = n % 2 === 1 ? `nav-${gid}` : `fc-${fid}`
        const at = `${width}x${height} step ${n} (#${targetId})`
        const want = `STEP ${String(n).padStart(2, '0')} OF ${TOUR_STEPS}`
        const read = async () => {
          await settleAnimations(spot, callout)
          const reading = await page.evaluate((id) => {
            const rect = (el: Element | null) => {
              if (!el) return null
              const r = el.getBoundingClientRect()
              return { x: r.x, y: r.y, width: r.width, height: r.height }
            }
            return {
              step: document.querySelector('[data-testid="lib-tour-callout"] .t-step')?.textContent ?? null,
              target: rect(document.getElementById(id)),
              spot: rect(document.querySelector('[data-testid="lib-tour-spot"]')),
              callout: rect(document.querySelector('[data-testid="lib-tour-callout"]')),
              win: { x: 0, y: 0, width: window.innerWidth, height: window.innerHeight },
            }
          }, targetId)
          const problems: string[] = []
          if (reading.step !== want) problems.push(`step label reads ${reading.step}, want ${want}`)
          if (!reading.target) problems.push(`#${targetId} has no box`)
          else if (!reading.spot || !enclosesRect(reading.spot, reading.target, 1)) problems.push('the spotlight does not enclose its target')
          if (!reading.callout) problems.push('the callout has no box')
          else {
            if (!enclosesRect(reading.win, reading.callout, 1)) problems.push('the callout is not inside the window')
            if (reading.spot && rectsOverlap(reading.callout, reading.spot)) problems.push('the callout overlaps the spotlight')
          }
          return { reading, problems }
        }
        let last = await read()
        try {
          await expect.poll(async () => (last = await read()).problems.length, { timeout: 5_000, message: at }).toBe(0)
        } catch {
          // The soft assertion below reports the last reading.
        }
        measured.push({ width, height, n, targetId, ...last.reading, problems: last.problems })
        expect.soft(last.problems, at).toEqual([])
        if (n < TOUR_STEPS) {
          await callout.getByRole('button', { name: 'Next', exact: true }).click()
        } else {
          await expect(callout.getByRole('button', { name: 'Finish', exact: true }), `${at}: the last step offers Finish`).toBeVisible()
        }
      }
    }
    await attachJson(testInfo, 'library-tour', measured)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
})
