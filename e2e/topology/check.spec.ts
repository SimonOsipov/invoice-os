// The /check page layout on the PR preview: relationships, plus the design's computed values per breakpoint.
// It never presses Send: no run, no IP count, no mail.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { argosOn, shoot } from '../argos'
import { seedConsent } from '../smoke/landingConsent'
import { resolveTarget } from '../targets'
import { enclosesRect, gaps, PHONE_WIDTHS, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'

const CHECK_URL = resolveTarget('CHECK_URL')

// Long unbroken text (D15): a 120-character file name, a 60-character header, an 80-character sample.
const FILE_NAME = `${'f'.repeat(116)}.csv`
const LONG_HEADER = 'h'.repeat(60)
const LONG_SAMPLE = 's'.repeat(80)
const FIXTURE = Buffer.from(`invoice_number,${LONG_HEADER},total\nINV-1,${LONG_SAMPLE},100\n`)
const EMAIL_200 = `${'e'.repeat(187)}@example.com`

const card = (page: Page) => page.locator('.check-card')
const rectOf = async (loc: Locator, label: string): Promise<Rect> => {
  await settleAnimations(loc)
  const box = await loc.boundingBox()
  if (!box) throw new Error(`${label} has no box`)
  return box
}

async function resize(page: Page, width: number): Promise<void> {
  await page.setViewportSize({ width, height: 1080 })
  await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(width)
}

async function noSidewaysScroll(page: Page, label: string): Promise<void> {
  const r = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth }))
  expect(r.scrollWidth - r.clientWidth, `${label}: the page scrolls sideways (${r.scrollWidth} vs ${r.clientWidth})`).toBeLessThanOrEqual(1)
}

// Opens /check, uploads the fixture and returns once the Map step shows. `onUpload` runs while the mapping call is held.
async function toMap(page: Page, onUpload?: () => Promise<void>): Promise<void> {
  await seedConsent(page, false)
  let release!: () => void
  const held = new Promise<void>((r) => (release = r))
  await page.route('**/readiness-check/mapping', async (route) => {
    await held
    await route.continue()
  })
  const response = await page.goto(`${CHECK_URL}/`)
  expect(response?.ok(), '/check did not answer 2xx').toBeTruthy()
  await page.locator('#check-file').setInputFiles({ name: FILE_NAME, mimeType: 'text/csv', buffer: FIXTURE })
  await expect(page.locator('.check-file')).toBeVisible()
  await onUpload?.()
  release()
  await expect(page.getByRole('heading', { name: 'Match your columns' }), 'the Map step never showed').toBeVisible()
  // The suggestion may leave the invoice number unplaced; Continue needs it.
  await page.getByLabel('Invoice number', { exact: true }).selectOption('invoice_number')
}

test.describe('check layout', () => {
  test('check layout: the page and the Map step fit at wide widths', async ({ page }) => {
    await resize(page, WIDE_WIDTHS[0])
    await toMap(page, async () => {
      expect(enclosesRect(await rectOf(card(page), 'card'), await rectOf(page.locator('.check-file'), 'file name'), 1), 'Upload: the file name is outside the card').toBe(true)
    })
    for (const width of WIDE_WIDTHS) {
      await resize(page, width)
      const label = `${width}px`
      await noSidewaysScroll(page, label)
      const cardRect = await rectOf(card(page), 'card')
      const vw = await page.evaluate(() => document.documentElement.clientWidth)
      const g = gaps(cardRect, { x: 0, width: vw })
      expect(Math.abs(g.left - g.right), `${label}: card gutters ${g.left} / ${g.right}`).toBeLessThanOrEqual(1)

      const rows = page.locator('.check-field')
      const lefts: number[] = []
      for (let i = 0; i < (await rows.count()); i++) {
        const label_ = await rectOf(rows.nth(i).locator('label'), `field ${i} label`)
        const select = await rectOf(rows.nth(i).locator('select'), `field ${i} select`)
        expect(rectsOverlap(label_, select), `${label}: field row ${i} label overlaps its select`).toBe(false)
        lefts.push(select.x)
      }
      expect(lefts.length, 'no field rows').toBeGreaterThan(0)
      expect(Math.max(...lefts) - Math.min(...lefts), `${label}: selects share no left edge`).toBeLessThanOrEqual(1)

      const scroller = await rectOf(page.locator('.check-scroll'), 'columns scroller')
      expect(enclosesRect(cardRect, scroller, 1), `${label}: the columns scroller is outside the card`).toBe(true)
      const table = await rectOf(page.locator('.check-columns'), 'columns table')
      const scrolls = await page.locator('.check-scroll').evaluate((el) => el.scrollWidth > el.clientWidth)
      expect(enclosesRect(scroller, table, 1) || scrolls, `${label}: the columns table escapes its scroller`).toBe(true)
      expect(enclosesRect(cardRect, await rectOf(page.locator('.check-file'), 'file name'), 1), `${label}: the file name is outside the card`).toBe(true)
    }
  })

  test('check layout: the email step keeps the box and Send inside the card', async ({ page }) => {
    await resize(page, WIDE_WIDTHS[0])
    await toMap(page)
    await page.getByRole('button', { name: 'Continue' }).click()
    await page.getByLabel('Email address', { exact: true }).fill(EMAIL_200)
    for (const width of WIDE_WIDTHS) {
      await resize(page, width)
      const label = `${width}px`
      const cardRect = await rectOf(card(page), 'card')
      const box = await rectOf(page.locator('.check-consent input'), 'consent box')
      const text = await rectOf(page.locator('.check-consent span'), 'consent label')
      expect(rectsOverlap(box, text), `${label}: the consent box overlaps its label`).toBe(false)
      expect(enclosesRect(cardRect, await rectOf(page.getByLabel('Email address', { exact: true }), 'email'), 1), `${label}: the email input is outside the card`).toBe(true)
      expect(enclosesRect(cardRect, await rectOf(page.getByRole('button', { name: 'Send' }), 'Send'), 1), `${label}: Send is outside the card`).toBe(true)
    }
  })

  test('check layout: no sideways scroll on a phone', async ({ page }) => {
    await resize(page, PHONE_WIDTHS[0])
    await toMap(page, async () => {
      for (const width of PHONE_WIDTHS) {
        await resize(page, width)
        await noSidewaysScroll(page, `Upload at ${width}px`)
      }
      await resize(page, PHONE_WIDTHS[0])
    })
    for (const width of PHONE_WIDTHS) {
      await resize(page, width)
      await noSidewaysScroll(page, `Map at ${width}px`)
    }
  })

  // Frame rows. A pdf pick reaches the Email step with no network call.
  const PDF = { name: 'a.pdf', mimeType: 'application/pdf', buffer: Buffer.from('%PDF-1.4') }
  const ALL_WIDTHS = [...WIDE_WIDTHS, ...PHONE_WIDTHS]

  async function openCheck(page: Page, width: number): Promise<void> {
    await seedConsent(page, false)
    await resize(page, width)
    const response = await page.goto(`${CHECK_URL}/`)
    expect(response?.ok(), '/check did not answer 2xx').toBeTruthy()
    await expect(page.locator('.check-tool')).toBeVisible()
    await settleAnimations(page.locator('.check-tool .check-card'))
  }

  const css = (loc: Locator, prop: string) => loc.evaluate((el, p) => getComputedStyle(el).getPropertyValue(p), prop)
  const px = async (loc: Locator, prop: string) => parseFloat(await css(loc, prop))
  const tracks = async (loc: Locator) => (await css(loc, 'grid-template-columns')).trim().split(/\s+/).length
  const resolved = (page: Page, token: string, prop: 'color' | 'outline-color') =>
    page.evaluate(
      ([t, p]) => {
        const el = document.createElement('i')
        el.style.setProperty(p, `var(${t})`)
        document.body.appendChild(el)
        const v = getComputedStyle(el).getPropertyValue(p)
        el.remove()
        return v
      },
      [token, prop],
    )
  const noOverlap = (rects: Rect[], label: string) => {
    for (let i = 0; i < rects.length; i++)
      for (let j = i + 1; j < rects.length; j++) expect(rectsOverlap(rects[i], rects[j]), `${label}: items ${i} and ${j} overlap`).toBe(false)
  }

  test('check layout: the header stays on top and fits', async ({ page }) => {
    await openCheck(page, ALL_WIDTHS[0])
    for (const width of ALL_WIDTHS) {
      await resize(page, width)
      // html has scroll-behavior: smooth; an instant scroll moves before the measure.
      await page.evaluate(() => window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' }))
      expect(await page.evaluate(() => window.scrollY), `${width}px: page scrolled`).toBeGreaterThan(0)
      const label = `${width}px`
      const head = await rectOf(page.locator('.check-head'), 'header')
      expect(Math.abs(head.y), `${label}: header top`).toBeLessThanOrEqual(1)
      const logo = await rectOf(page.getByRole('link', { name: 'ASComply Africa home' }), 'logo')
      const signUp = await rectOf(page.locator('.check-head').getByRole('link', { name: 'Sign up' }), 'Sign up')
      expect(enclosesRect(head, logo, 1), `${label}: logo outside the header`).toBe(true)
      expect(enclosesRect(head, signUp, 1), `${label}: Sign up outside the header`).toBe(true)
      const rects = [logo, signUp]
      for (const name of ['What you get', 'Platform']) {
        const l = page.locator('.check-head').getByRole('link', { name, exact: true })
        if (await l.isVisible()) rects.push(await rectOf(l, name))
      }
      noOverlap(rects, label)
      await noSidewaysScroll(page, label)
    }
  })

  test('check layout: a phone shows only the logo and Sign up', async ({ page }) => {
    await openCheck(page, PHONE_WIDTHS[0])
    const nav = page.locator('.check-head nav')
    for (const width of [320, 375, 390, 414]) {
      await resize(page, width)
      await expect(nav.getByRole('link', { name: 'What you get' }), `${width}px`).toBeHidden()
      await expect(nav.getByRole('link', { name: 'Platform' }), `${width}px`).toBeHidden()
      await expect(nav.getByRole('link', { name: 'Sign up' }), `${width}px`).toBeVisible()
    }
    for (const width of [767, ...WIDE_WIDTHS]) {
      await resize(page, width)
      await expect(nav.getByRole('link', { name: 'What you get' }), `${width}px`).toBeVisible()
      await expect(nav.getByRole('link', { name: 'Platform' }), `${width}px`).toBeVisible()
    }
  })

  test('check layout: the strip fits the card', async ({ page }) => {
    await openCheck(page, WIDE_WIDTHS[0])
    for (const width of [...WIDE_WIDTHS, 767]) {
      await resize(page, width)
      const tool = await rectOf(page.locator('.check-tool'), 'tool card')
      const lis = page.locator('.check-progress li')
      const rects: Rect[] = []
      for (let i = 0; i < (await lis.count()); i++) rects.push(await rectOf(lis.nth(i), `item ${i}`))
      expect(rects).toHaveLength(4)
      for (const r of rects) {
        expect(enclosesRect(tool, r, 1), `${width}px: an item is outside the card`).toBe(true)
        expect(Math.abs(r.y - rects[0].y), `${width}px: items share no top`).toBeLessThanOrEqual(1)
      }
      noOverlap(rects, `${width}px`)
    }
    for (const width of PHONE_WIDTHS.filter((w) => w < 760)) {
      await resize(page, width)
      await expect(page.locator('.check-progress'), `${width}px`).toBeHidden()
      const tool = await rectOf(page.locator('.check-tool'), 'tool card')
      expect(enclosesRect(tool, await rectOf(page.locator('.check-progress-m'), 'phone strip'), 1), `${width}px: phone strip`).toBe(true)
      const segs = page.locator('.check-segs span')
      for (let i = 0; i < (await segs.count()); i++) expect(enclosesRect(tool, await rectOf(segs.nth(i), `segment ${i}`), 1), `${width}px: segment ${i}`).toBe(true)
    }
  })

  test('check layout: copy left, card right, stacked on a phone', async ({ page }) => {
    await openCheck(page, WIDE_WIDTHS[0])
    for (const width of WIDE_WIDTHS) {
      await resize(page, width)
      const copy = await rectOf(page.locator('.check-copy'), 'copy')
      const tool = await rectOf(page.locator('.check-tool'), 'card')
      expect(copy.x + copy.width, `${width}px: copy right edge past the card`).toBeLessThanOrEqual(tool.x)
      expect(Math.abs(copy.y - tool.y), `${width}px: tops`).toBeLessThanOrEqual(1)
    }
    for (const width of PHONE_WIDTHS) {
      await resize(page, width)
      const copy = await rectOf(page.locator('.check-copy'), 'copy')
      const tool = await rectOf(page.locator('.check-tool'), 'card')
      expect(copy.y + copy.height, `${width}px: copy not above the card`).toBeLessThanOrEqual(tool.y + 1)
      await noSidewaysScroll(page, `${width}px`)
    }
  })

  test('check layout: the Map step widens the card and lines up the trust cues', async ({ page }) => {
    await resize(page, WIDE_WIDTHS[0])
    await toMap(page)
    for (const width of WIDE_WIDTHS) {
      await resize(page, width)
      const label = `${width}px`
      const tool = await rectOf(page.locator('.check-tool'), 'card')
      const h1 = await rectOf(page.locator('.check-h1'), 'h1')
      expect(Math.abs(tool.x - h1.x), `${label}: card and h1 left edges`).toBeLessThanOrEqual(1)
      const vw = await page.evaluate(() => document.documentElement.clientWidth)
      const g = gaps(tool, { x: 0, width: vw })
      expect(Math.abs(g.left - g.right), `${label}: card gutters ${g.left} / ${g.right}`).toBeLessThanOrEqual(1)
      const cues = page.locator('.check-trust li')
      expect(await cues.count()).toBe(4)
      const tops: number[] = []
      for (let i = 0; i < 4; i++) tops.push((await rectOf(cues.nth(i), `cue ${i}`)).y)
      expect(Math.max(...tops) - Math.min(...tops), `${label}: trust cues share no top`).toBeLessThanOrEqual(1)
    }
  })

  test('check layout: the footer items do not overlap', async ({ page }) => {
    await openCheck(page, ALL_WIDTHS[0])
    for (const width of ALL_WIDTHS) {
      await resize(page, width)
      const foot = page.locator('.check-foot')
      const rects = [
        await rectOf(foot.locator('.ds-logo'), 'logo'),
        await rectOf(foot.locator('.check-foot-id > span').last(), 'company line'),
        await rectOf(foot.getByRole('link', { name: 'Privacy' }), 'Privacy'),
        await rectOf(foot.getByRole('button', { name: 'Cookie choices' }), 'Cookie choices'),
      ]
      noOverlap(rects, `${width}px`)
      for (const r of rects) expect(r.x >= 0 && r.x + r.width <= width + 1, `${width}px: outside the viewport`).toBe(true)
    }
  })

  test('check layout: reduced motion stops the frame\'s motion', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await resize(page, WIDE_WIDTHS[0])
    await toMap(page)
    await expect(page.locator('.check-card')).toHaveCSS('animation-name', 'none')
    await expect(page.locator('html')).toHaveCSS('scroll-behavior', 'auto')
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    expect(await css(page.locator('.check-card'), 'animation-name')).not.toBe('none')
    await expect(page.locator('html')).toHaveCSS('scroll-behavior', 'smooth')
  })

  test('check layout: the frame takes the design\'s values at each breakpoint', async ({ page }) => {
    const near = (a: number, b: number, what: string) => expect(Math.abs(a - b), `${what}: ${a} vs ${b}`).toBeLessThanOrEqual(0.5)
    await openCheck(page, 1280)
    near(await px(page.locator('.check-head'), 'height'), 72, '1280 header')
    // Computed width, not the box: .asc-mark scales the drawn mark by 128/144.
    near(await px(page.locator('.check-head .ds-logo-mark'), 'width'), 30, '1280 header mark')
    near(await px(page.locator('.check-foot .ds-logo-mark'), 'width'), 24, '1280 footer mark')
    near(await px(page.locator('.check-hero-pad'), 'padding-top'), 64, '1280 hero top')
    near(await px(page.locator('.check-hero-pad'), 'padding-bottom'), 76.8, '1280 hero bottom')
    near(await px(page.locator('.check-lead'), 'font-size'), 16, '1280 lead')
    expect(await css(page.locator('.check-trust'), 'display')).toBe('grid')
    expect(await tracks(page.locator('.check-trust'))).toBe(2)
    expect(await css(page.locator('.check-foot-row'), 'flex-direction')).toBe('row')
    await resize(page, 700)
    near(await px(page.locator('.check-wrap').first(), 'padding-left'), 64, '700 gutter left')
    near(await px(page.locator('.check-wrap').first(), 'padding-right'), 64, '700 gutter right')
    for (const width of [900, 767]) {
      await resize(page, width)
      expect(await css(page.locator('.check-trust'), 'display'), `${width}px`).toBe('flex')
      expect(await css(page.locator('.check-trust'), 'flex-wrap'), `${width}px`).toBe('wrap')
    }
    await resize(page, 390)
    near(await px(page.locator('.check-head'), 'height'), 64, '390 header')
    near(await px(page.locator('.check-what'), 'padding-top'), 56, '390 what top')
    near(await px(page.locator('.check-what'), 'padding-bottom'), 56, '390 what bottom')
    near(await px(page.locator('.check-wrap').first(), 'padding-left'), 16, '390 gutter')
    near(await px(page.locator('.check-h1'), 'font-size'), 34, '390 h1')
    near(await px(page.locator('.check-lead'), 'font-size'), 15, '390 lead')
    near(await px(page.locator('.check-hero-pad'), 'padding-top'), 28, '390 hero top')
    near(await px(page.locator('.check-hero-pad'), 'padding-bottom'), 32, '390 hero bottom')
    expect(await css(page.locator('.check-trust'), 'display')).toBe('grid')
    expect(await tracks(page.locator('.check-trust'))).toBe(2)
    near(await px(page.locator('.check-trust li').first(), 'font-size'), 13, '390 cue')
    expect(await css(page.locator('.check-foot-row'), 'flex-direction')).toBe('column')
    const logo = await rectOf(page.locator('.check-foot .ds-logo'), 'footer logo')
    const privacy = await rectOf(page.locator('.check-foot').getByRole('link', { name: 'Privacy' }), 'Privacy')
    expect(Math.abs(logo.x - privacy.x), 'footer: Privacy left vs logo left').toBeLessThanOrEqual(1)

    // Map at 390: the Map rule outranks the 600 px trust-cue rule.
    await page.setViewportSize({ width: 390, height: 1080 })
    await page.locator('#check-file').setInputFiles({ name: 'm.csv', mimeType: 'text/csv', buffer: FIXTURE })
    await expect(page.getByRole('heading', { name: 'Match your columns' })).toBeVisible()
    near(await px(page.locator('.check-h1'), 'font-size'), 34, '390 Map h1')
    expect(await css(page.locator('.check-trust'), 'display')).toBe('flex')
  })

  test('check layout: the header links show the design\'s focus ring', async ({ page }) => {
    await openCheck(page, 1280)
    const ring = await resolved(page, '--ring', 'outline-color')
    await page.keyboard.press('Tab')
    for (const target of [page.getByRole('link', { name: 'ASComply Africa home' }), page.locator('.check-head').getByRole('link', { name: 'What you get' })]) {
      await expect(target).toBeFocused()
      expect(await css(target, 'outline-style')).toBe('solid')
      expect(await css(target, 'outline-width')).toBe('2px')
      expect(await css(target, 'outline-offset')).toBe('2px')
      expect(await css(target, 'outline-color')).toBe(ring)
      await page.keyboard.press('Tab')
    }
  })

  test('check layout: the hero eyebrow takes the dark-band colour', async ({ page }) => {
    await openCheck(page, 1280)
    const color = await css(page.locator('.check-hero .t-eyebrow'), 'color')
    expect(color).toBe(await resolved(page, '--text-eyebrow-dark', 'color'))
    expect(color).not.toBe(await resolved(page, '--text-eyebrow', 'color'))
  })

  test('check layout: every strip label shows in full', async ({ page }) => {
    await openCheck(page, 1440)
    for (const pdf of [false, true]) {
      if (pdf) {
        await page.locator('#check-file').setInputFiles(PDF)
        await expect(page.locator('.check-progress li.is-skipped')).toBeVisible()
      }
      for (const width of [1440, 1280, 1100, 1024, 981, 900, 761]) {
        await resize(page, width)
        const label = `${width}px${pdf ? ' pdf' : ''}`
        const lis = page.locator('.check-progress li')
        const rects: Rect[] = []
        for (let i = 0; i < (await lis.count()); i++) {
          const li = await rectOf(lis.nth(i), `item ${i}`)
          rects.push(li)
          const texts = lis.nth(i).locator('.check-step-lbl, .check-step-sub')
          for (let j = 0; j < (await texts.count()); j++) {
            const t = texts.nth(j)
            expect(await t.evaluate((el) => el.scrollWidth <= el.clientWidth), `${label}: item ${i} text is clipped`).toBe(true)
            expect(enclosesRect(li, await rectOf(t, `item ${i} text`), 1), `${label}: item ${i} text outside its item`).toBe(true)
          }
        }
        noOverlap(rects, label)
      }
    }
  })

  // What you get.
  test('check layout: anchors land below the header', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    for (const width of [1280, 390]) {
      await openCheck(page, width)
      const headBottom = async () => (await rectOf(page.locator('.check-head'), 'header')).y + (await rectOf(page.locator('.check-head'), 'header')).height
      if (width === 1280) {
        await page.locator('.check-head').getByRole('link', { name: 'What you get', exact: true }).click()
        await expect.poll(async () => (await page.locator('#check-what-h').boundingBox())!.y, `${width}px: #what heading`).toBeGreaterThanOrEqual(await headBottom() - 1)
        const h = await rectOf(page.locator('#check-what-h'), 'what heading')
        expect(h.y + h.height, `${width}px: #what heading is outside the viewport`).toBeLessThanOrEqual(1080)
      }
      await page.locator('#what').getByRole('link', { name: 'Check a file' }).click()
      await expect.poll(async () => (await page.locator('#check').boundingBox())!.y, `${width}px: #check`).toBeGreaterThanOrEqual((await headBottom()) - 1)
    }
  })

  test('check layout: What you get side by side, stacked on a phone', async ({ page }) => {
    await openCheck(page, WIDE_WIDTHS[0])
    const check = async (width: number, stacked: boolean) => {
      await resize(page, width)
      const label = `${width}px`
      const copy = await rectOf(page.locator('.check-what-copy'), 'copy')
      const example = await rectOf(page.locator('.check-example'), 'example')
      if (stacked) expect(copy.y + copy.height, `${label}: copy not above the example`).toBeLessThanOrEqual(example.y + 1)
      else expect(copy.x + copy.width, `${label}: copy right edge past the example`).toBeLessThanOrEqual(example.x)
      const limits = await rectOf(page.locator('.check-limits'), 'limits')
      const cells = page.locator('.check-limits > div')
      const rects: Rect[] = []
      for (let i = 0; i < (await cells.count()); i++) rects.push(await rectOf(cells.nth(i), `limit ${i}`))
      expect(rects).toHaveLength(4)
      for (const r of rects) expect(enclosesRect(limits, r, 1), `${label}: a limit cell is outside the grid`).toBe(true)
      noOverlap(rects, label)
      expect(example.x >= 0 && example.x + example.width <= width + 1, `${label}: the example is outside the viewport`).toBe(true)
      // The fixed label column: each label fits it in full and ends before its message.
      const labels = page.locator('.check-mail-row b')
      expect(await labels.count(), `${label}: no example rows`).toBeGreaterThan(0)
      for (let i = 0; i < (await labels.count()); i++) {
        const b = labels.nth(i)
        expect(await b.evaluate((el) => el.scrollWidth <= el.clientWidth), `${label}: example label ${i} is clipped`).toBe(true)
        const row = b.locator('xpath=..')
        const [lr, vr, rr] = [await rectOf(b, 'label'), await rectOf(row.locator('span'), 'message'), await rectOf(row, 'row')]
        expect(rectsOverlap(lr, vr), `${label}: example label ${i} overlaps its message`).toBe(false)
        expect(enclosesRect(rr, lr, 1) && enclosesRect(rr, vr, 1), `${label}: example row ${i} content leaves the row`).toBe(true)
      }
      await noSidewaysScroll(page, label)
    }
    for (const width of WIDE_WIDTHS) await check(width, false)
    for (const width of PHONE_WIDTHS) await check(width, true)
  })

  test('check layout: What you get takes the design\'s phone values', async ({ page }) => {
    const side = async (loc: Locator) => [await px(loc, 'padding-left'), await px(loc, 'padding-right')]
    for (const [width, num, pad] of [[390, 22, 14], [1280, 26, 20]] as const) {
      await openCheck(page, width)
      expect(await px(page.locator('.check-limits b').first(), 'font-size'), `${width}px limit number`).toBe(num)
      for (const part of ['.check-mail-head', '.check-mail-row', '.check-mail-no']) {
        expect(await side(page.locator(part).first()), `${width}px ${part} side padding`).toEqual([pad, pad])
      }
    }
  })

  test('check layout: the Argos shots of the Upload and Map steps', async ({ page }) => {
    test.skip(!argosOn(), 'Argos shots are taken on the nightly only')
    await openCheck(page, 1440)
    await shoot(page, 'check', 'upload')
    await toMap(page)
    await shoot(page, 'check', 'map')
  })
})
