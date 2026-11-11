// The /check page layout on the PR preview: relationships, plus the design's computed values per breakpoint.
// It never sends a run to the server: no run, no IP count, no mail.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { argosOn, shoot } from '../argos'
import { seedConsent } from '../smoke/landingConsent'
import { resolveTarget } from '../targets'
import { enclosesRect, gaps, PHONE_WIDTHS, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'

const CHECK_URL = resolveTarget('CHECK_URL')
const GATEWAY_URL = resolveTarget('GATEWAY_URL')

// Long unbroken text (D15): a 120-character file name, a 60-character header, an 80-character sample.
const FILE_NAME = `${'f'.repeat(116)}.csv`
const LONG_HEADER = 'h'.repeat(60)
const LONG_SAMPLE = 's'.repeat(80)
const FIXTURE = Buffer.from(`invoice_number,${LONG_HEADER},total\nINV-1,${LONG_SAMPLE},100\n`)
const EMAIL_200 = `${'e'.repeat(187)}@example.com`

// A well-shaped token nothing issued: the ask page renders without looking it up.
const unknownConfirmLink = () => `${GATEWAY_URL}/readiness-check/confirm?t=${'A'.repeat(43)}`

const card = (page: Page) => page.locator('.check-card')
const rectOf = async (loc: Locator, label: string): Promise<Rect> => {
  await settleAnimations(loc)
  const box = await loc.boundingBox()
  if (!box) throw new Error(`${label} has no box`)
  return box
}

async function resize(page: Page, width: number, height = 1080): Promise<void> {
  await page.setViewportSize({ width, height })
  await expect.poll(() => page.evaluate(() => [window.innerWidth, window.innerHeight])).toEqual([width, height])
}

async function noSidewaysScroll(page: Page, label: string): Promise<void> {
  const r = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth }))
  expect(r.scrollWidth - r.clientWidth, `${label}: the page scrolls sideways (${r.scrollWidth} vs ${r.clientWidth})`).toBeLessThanOrEqual(1)
}

// Fixture check: the gateway's mapping answer is canned, so the columns, samples and matches are known.
const SUGGESTION = {
  columns: ['Inv', LONG_HEADER, 'Total', 'Buyer', 'Date', 'Currency'],
  sample_rows: [['INV-1', LONG_SAMPLE, '100', 'Acme', '2026-01-01', 'NGN']],
  header_row: 1,
  rows_total: 1,
  mapping: { invoice_number: 'Inv', total: 'Total', buyer_name: 'Buyer', issue_date: 'Date' } as Record<string, string>,
  source: 'heuristic',
}
const BUYER_KEYS = ['buyer_tin', 'buyer_name', 'buyer_email', 'buyer_telephone', 'buyer_street', 'buyer_city', 'buyer_postal_zone', 'buyer_country', 'buyer_state', 'buyer_lga']
// Every Buyer field on one column: the Buyer subtitle lists all ten labels.
const ALL_BUYER = { ...SUGGESTION, mapping: { ...SUGGESTION.mapping, ...Object.fromEntries(BUYER_KEYS.map((k) => [k, 'Buyer'])) } }

// Fixture check: opens /check, uploads the fixture, answers the mapping call with `suggestion`.
async function toMapWith(page: Page, suggestion: typeof SUGGESTION = SUGGESTION): Promise<void> {
  await seedConsent(page, false)
  await page.route('**/readiness-check/mapping', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', headers: { 'access-control-allow-origin': '*' }, body: JSON.stringify(suggestion) }),
  )
  const response = await page.goto(`${CHECK_URL}/`)
  expect(response?.ok(), '/check did not answer 2xx').toBeTruthy()
  await page.locator('#check-file').setInputFiles({ name: FILE_NAME, mimeType: 'text/csv', buffer: FIXTURE })
  await expect(page.getByRole('heading', { name: 'Tell us which column is which' }), 'the Map step never showed').toBeVisible()
}

// Fixture check: from the Map step, Continue and Send; the run route answers 400, so the page returns to step 2 with the sentence.
async function sendRejected(page: Page, runs = { n: 0 }): Promise<void> {
  await page.route('**/readiness-check/run', (route) => {
    runs.n++
    return route.fulfill({ status: 400, contentType: 'application/json', headers: { 'access-control-allow-origin': '*' }, body: JSON.stringify({ error: 'mapping rejected' }) })
  })
  await page.getByRole('button', { name: 'Continue' }).click()
  await page.getByLabel('Email address', { exact: true }).fill('check-fixture@example.com')
  await page.locator('.check-consent input').check()
  await page.getByRole('button', { name: 'Send' }).click()
  await expect(page.getByRole('heading', { name: 'Tell us which column is which' })).toBeVisible()
  await expect(page.locator('.check-alert')).toBeVisible()
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
  await expect(page.locator('.check-upload-name')).toBeVisible()
  await onUpload?.()
  release()
  await expect(page.getByRole('heading', { name: 'Tell us which column is which' }), 'the Map step never showed').toBeVisible()
  // The suggestion may leave the invoice number unplaced; Continue needs it.
  await page.getByLabel('Invoice number', { exact: true }).selectOption('invoice_number')
}

test.describe('check layout', () => {
  test('check layout: the page and the Map step fit at wide widths', async ({ page }) => {
    await resize(page, WIDE_WIDTHS[0])
    await toMap(page, async () => {
      expect(enclosesRect(await rectOf(card(page), 'card'), await rectOf(page.locator('.check-upload-name'), 'file name'), 1), 'Upload: the file name is outside the card').toBe(true)
    })
    for (const width of WIDE_WIDTHS) {
      await resize(page, width)
      const label = `${width}px`
      await noSidewaysScroll(page, label)
      const cardRect = await rectOf(card(page), 'card')
      const vw = await page.evaluate(() => document.documentElement.clientWidth)
      const g = gaps(cardRect, { x: 0, width: vw })
      expect(Math.abs(g.left - g.right), `${label}: card gutters ${g.left} / ${g.right}`).toBeLessThanOrEqual(1)

      const rows = page.locator('.check-row')
      expect(await rows.count(), 'no field rows').toBeGreaterThan(0)
      const groups = page.locator('.check-grp')
      for (let g = 0; g < (await groups.count()); g++) {
        const groupRows = groups.nth(g).locator('.check-row')
        const lefts: number[] = []
        for (let i = 0; i < (await groupRows.count()); i++) {
          const lbl = await rectOf(groupRows.nth(i).locator('.check-row-lbl'), `group ${g} row ${i} label`)
          const select = await rectOf(groupRows.nth(i).locator('select'), `group ${g} row ${i} select`)
          expect(rectsOverlap(lbl, select), `${label}: group ${g} row ${i} label overlaps its select`).toBe(false)
          lefts.push(select.x)
        }
        if (lefts.length > 0) {
          expect(Math.max(...lefts) - Math.min(...lefts), `${label}: group ${g} selects share no left edge`).toBeLessThanOrEqual(1)
        }
      }

      const scroller = await rectOf(page.locator('.check-scroll'), 'columns scroller')
      expect(enclosesRect(cardRect, scroller, 1), `${label}: the columns scroller is outside the card`).toBe(true)
      const table = await rectOf(page.locator('.check-columns'), 'columns table')
      const scrolls = await page.locator('.check-scroll').evaluate((el) => el.scrollWidth > el.clientWidth)
      expect(enclosesRect(scroller, table, 1) || scrolls, `${label}: the columns table escapes its scroller`).toBe(true)
      expect(enclosesRect(cardRect, await rectOf(page.locator('.check-map-file'), 'file name'), 1), `${label}: the file name is outside the card`).toBe(true)
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
  const probeBorder = (page: Page) =>
    page.evaluate(() => {
      const el = document.createElement('i')
      el.style.border = '1.5px solid'
      document.body.appendChild(el)
      const v = getComputedStyle(el).borderTopWidth
      el.remove()
      return v
    })
  const resolved = (page: Page, token: string, prop: string) =>
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

  // Map step rows (fixture checks): a canned mapping answer, so the geometry is the page's alone.
  const headToTop = (page: Page) => page.locator('.check-map .check-step-head').evaluate((el) => el.scrollIntoView({ block: 'start', behavior: 'instant' }))
  const isPhone = (w: number) => (PHONE_WIDTHS as readonly number[]).includes(w)
  const pageEnd = (page: Page) => page.evaluate(() => window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' }))

  test('check layout: the Map step head, summary and preview fit the card', async ({ page }) => {
    await resize(page, ALL_WIDTHS[0])
    await toMapWith(page)
    for (const width of ALL_WIDTHS) {
      await resize(page, width)
      const label = `${width}px`
      const tool = await rectOf(page.locator('.check-tool'), 'tool')
      const stepNo = await rectOf(page.locator('.check-step-no'), 'step label')
      const title = await rectOf(page.locator('.check-step-title'), 'title')
      expect(stepNo.y + stepNo.height, `${label}: the step label is not above the title`).toBeLessThanOrEqual(title.y + 1)
      expect(enclosesRect(tool, await rectOf(page.locator('.check-map-file'), 'file meta'), 1), `${label}: the file meta is outside the card`).toBe(true)
      const sum = await rectOf(page.locator('.check-sum'), 'summary')
      expect(enclosesRect(await rectOf(card(page), 'card'), sum, 1), `${label}: the summary is outside the card`).toBe(true)
      noOverlap([await rectOf(page.locator('.check-sum-n'), 'summary number'), await rectOf(page.locator('.check-sum-text'), 'summary sentence')], `${label}: summary`)
      expect(enclosesRect(sum, await rectOf(page.locator('.check-meter'), 'meter'), 1), `${label}: the meter is outside the summary`).toBe(true)
      expect(enclosesRect(sum, await rectOf(page.locator('.check-unused-line'), 'unused line'), 1), `${label}: the unused line is outside the summary`).toBe(true)
      noOverlap([await rectOf(page.locator('.check-preview-head .check-sub'), 'preview title'), await rectOf(page.getByRole('button', { name: 'Show all 6 columns' }), 'toggle')], `${label}: preview head`)
      await noSidewaysScroll(page, label)
    }
  })

  test('check layout: the columns table scrolls sideways on a phone', async ({ page }) => {
    await resize(page, ALL_WIDTHS[0])
    await toMapWith(page)
    await page.getByRole('button', { name: 'Show all 6 columns' }).click()
    const scroll = page.locator('.check-scroll')
    for (const width of ALL_WIDTHS) {
      await resize(page, width)
      const label = `${width}px`
      const scroller = await rectOf(scroll, 'columns scroller')
      expect(enclosesRect(await rectOf(card(page), 'card'), scroller, 1), `${label}: the scroll region is outside the card`).toBe(true)
      const scrolls = await scroll.evaluate((el) => el.scrollWidth > el.clientWidth)
      if (isPhone(width)) {
        expect(scrolls, `${label}: the table does not overflow its region`).toBe(true)
        await scroll.evaluate((el) => {
          el.scrollLeft = 10000
        })
        await expect.poll(() => scroll.evaluate((el) => el.scrollLeft), `${label}: scrollLeft did not move`).toBeGreaterThan(0)
        await scroll.evaluate((el) => {
          el.scrollLeft = 0
        })
      } else {
        const table = await rectOf(page.locator('.check-columns'), 'columns table')
        expect(enclosesRect(scroller, table, 1) || scrolls, `${label}: the table escapes its region`).toBe(true)
      }
      await noSidewaysScroll(page, label)
    }
  })

  test('check layout: the field groups and rows fit', async ({ page }) => {
    await resize(page, ALL_WIDTHS[0])
    await toMapWith(page)
    for (const title of ['Amounts', 'Buyer']) await page.locator('.check-acc', { hasText: title }).first().click()
    await page.locator('#check-g-invoice .check-subhead .check-link').click()
    for (const width of ALL_WIDTHS) {
      await resize(page, width)
      const label = `${width}px`
      const cardRect = await rectOf(card(page), 'card')
      const groups = page.locator('.check-grp')
      for (let g = 0; g < (await groups.count()); g++) {
        const grp = groups.nth(g)
        expect(enclosesRect(cardRect, await rectOf(grp, `group ${g}`), 1), `${label}: group ${g} is outside the card`).toBe(true)
        const acc = await rectOf(grp.locator('.check-acc'), `group ${g} header`)
        const parts = [await rectOf(grp.locator('.check-acc-title'), 'title'), await rectOf(grp.locator('.check-count'), 'count'), await rectOf(grp.locator('.check-chev'), 'chevron')]
        for (const p of parts) expect(enclosesRect(acc, p, 1), `${label}: group ${g} header part is outside the header`).toBe(true)
        noOverlap(parts, `${label}: group ${g} header`)
        expect(enclosesRect(acc, await rectOf(grp.locator('.check-acc-sub'), 'subtitle'), 1), `${label}: group ${g} subtitle is outside the header`).toBe(true)
        const rows = grp.locator('.check-row')
        const lefts: number[] = []
        for (let i = 0; i < (await rows.count()); i++) {
          const lbl = await rectOf(rows.nth(i).locator('.check-row-lbl'), `group ${g} row ${i} label`)
          const select = await rectOf(rows.nth(i).locator('select'), `group ${g} row ${i} select`)
          if (isPhone(width)) {
            expect(select.y, `${label}: group ${g} row ${i} select starts above its label's bottom`).toBeGreaterThanOrEqual(lbl.y + lbl.height - 1)
            expect(Math.abs(select.x - lbl.x), `${label}: group ${g} row ${i} select and label share no left edge`).toBeLessThanOrEqual(1)
          } else {
            expect(rectsOverlap(lbl, select), `${label}: group ${g} row ${i} label overlaps its select`).toBe(false)
            lefts.push(select.x)
          }
        }
        if (lefts.length > 0) expect(Math.max(...lefts) - Math.min(...lefts), `${label}: group ${g} selects share no left edge`).toBeLessThanOrEqual(1)
      }
      const row = page.locator('.check-row').filter({ has: page.locator('#check-field-issue_date') })
      const rowRect = await rectOf(row, 'Issue date row')
      const tag = await rectOf(row.locator('.check-tag'), 'Suggested tag')
      expect(enclosesRect(rowRect, tag, 1), `${label}: the tag is outside its row`).toBe(true)
      expect(rectsOverlap(tag, await rectOf(row.locator('label'), 'Issue date label')), `${label}: the tag overlaps the label`).toBe(false)
      const supplier = page.locator('.check-grp').filter({ has: page.locator('#check-g-supplier') })
      expect(enclosesRect(await rectOf(supplier, 'Supplier group'), await rectOf(page.locator('.check-hint'), 'hint'), 1), `${label}: the hint is outside the Supplier group`).toBe(true)
    }

    // Boundary: the long Buyer subtitle.
    await resize(page, ALL_WIDTHS[0])
    await toMapWith(page, ALL_BUYER)
    await page.locator('.check-acc', { hasText: 'Buyer' }).first().click()
    const buyer = page.locator('.check-grp').filter({ has: page.locator('#check-g-buyer') })
    for (const width of ALL_WIDTHS) {
      await resize(page, width)
      const label = `${width}px`
      const acc = await rectOf(buyer.locator('.check-acc'), 'Buyer header')
      const sub = await rectOf(buyer.locator('.check-acc-sub'), 'Buyer subtitle')
      expect(enclosesRect(acc, sub, 1), `${label}: the Buyer subtitle is outside its header`).toBe(true)
      // Allowed deviation (Decision [Long Buyer subtitle truncation]): the cut is asserted at 1280 and on phones only.
      if (width === 1280 || isPhone(width)) {
        expect(await buyer.locator('.check-acc-sub').evaluate((el) => el.scrollWidth > el.clientWidth), `${label}: the long subtitle is not cut`).toBe(true)
      }
      const end = await rectOf(buyer.locator(isPhone(width) ? '.check-chev' : '.check-count'), 'subtitle neighbour')
      expect(sub.x + sub.width, `${label}: the subtitle runs into its neighbour`).toBeLessThanOrEqual(end.x + 1)
    }
  })

  test('check layout: the action bar sticks to the bottom of the viewport', async ({ page }) => {
    await resize(page, ALL_WIDTHS[0], 600)
    await toMapWith(page)
    await page.getByLabel('Invoice number', { exact: true }).selectOption('')
    const bar = page.locator('.check-sticky')
    const buttons = () => [page.getByRole('button', { name: 'Choose another file' }), page.locator('.check-need'), page.getByRole('button', { name: 'Continue' })]
    for (const width of ALL_WIDTHS) {
      await resize(page, width, 600)
      const label = `${width}px`
      await headToTop(page)
      const tool = await rectOf(page.locator('.check-tool'), 'tool')
      let b = await rectOf(bar, 'bar')
      expect(Math.abs(b.y + b.height - 600), `${label}: the bar's bottom is not the viewport's bottom`).toBeLessThanOrEqual(1)
      expect(Math.abs(b.x - tool.x), `${label}: the bar's left edge is not the card's`).toBeLessThanOrEqual(2)
      expect(Math.abs(b.x + b.width - (tool.x + tool.width)), `${label}: the bar's right edge is not the card's`).toBeLessThanOrEqual(2)
      const rects: Rect[] = []
      for (const [i, loc] of buttons().entries()) {
        const r = await rectOf(loc, `bar item ${i}`)
        expect(enclosesRect(b, r, 1), `${label}: bar item ${i} is outside the bar`).toBe(true)
        rects.push(r)
      }
      noOverlap(rects, `${label}: bar`)
      if (isPhone(width)) {
        const inner = b.width - (await px(bar, 'padding-left')) - (await px(bar, 'padding-right'))
        expect(rects[2].y + rects[2].height, `${label}: Continue is not above Choose another file`).toBeLessThanOrEqual(rects[0].y + 1)
        for (const i of [0, 2]) expect(Math.abs(rects[i].width - inner), `${label}: bar item ${i} does not fill the bar`).toBeLessThanOrEqual(1)
      }
      await pageEnd(page)
      b = await rectOf(bar, 'resting bar')
      const lastGroup = await rectOf(page.locator('.check-grp').last(), 'last group')
      const cardNow = await rectOf(page.locator('.check-tool'), 'tool')
      expect(b.y, `${label}: the resting bar is over the last group`).toBeGreaterThanOrEqual(lastGroup.y + lastGroup.height - 1)
      expect(Math.abs(b.y + b.height - (cardNow.y + cardNow.height)), `${label}: the resting bar's bottom is not the card's`).toBeLessThanOrEqual(2)
    }
  })

  test('check layout: a rejected mapping shows in the pinned bar', async ({ page }) => {
    const runs = { n: 0 }
    await resize(page, ALL_WIDTHS[0], 600)
    await toMapWith(page)
    await sendRejected(page, runs)
    for (const width of ALL_WIDTHS) {
      await resize(page, width, 600)
      const label = `${width}px`
      await headToTop(page)
      const bar = await rectOf(page.locator('.check-sticky'), 'bar')
      const alert = await rectOf(page.locator('.check-alert'), 'alert')
      const actions = await rectOf(page.locator('.check-actions'), 'actions')
      expect(enclosesRect(bar, alert, 1), `${label}: the alert is outside the bar`).toBe(true)
      expect(alert.y + alert.height, `${label}: the alert is not above the buttons`).toBeLessThanOrEqual(actions.y + 1)
      expect(Math.abs(bar.y + bar.height - 600), `${label}: the bar's bottom is not the viewport's bottom`).toBeLessThanOrEqual(1)
      expect(enclosesRect(bar, await rectOf(page.locator('.check-alert span'), 'alert text'), 1), `${label}: the alert text is outside the bar`).toBe(true)
      await noSidewaysScroll(page, label)
    }
    expect(runs.n, 'the run request did not reach the route once').toBe(1)
  })

  test('check layout: the Map step takes the design\'s sizes and colours', async ({ page }) => {
    type Row = [string, string, string]
    const read = async (rows: Row[]) => {
      for (const [sel, prop, want] of rows) {
        const loc = page.locator(sel).first()
        const expected = want.startsWith('--') ? await resolved(page, want, prop) : want
        expect(await css(loc, prop), `${sel} ${prop}`).toBe(expected)
      }
    }
    const next = page.getByRole('button', { name: 'Continue' })
    await resize(page, 1280)
    await toMapWith(page)
    // First, before any pointer use: a script focus shows :focus-visible only then.
    const acc = page.locator('.check-acc').first()
    await acc.focus()
    await read([
      ['.check-acc', 'outline-style', 'solid'],
      ['.check-acc', 'outline-width', '2px'],
      ['.check-acc', 'outline-offset', '2px'],
      ['.check-acc', 'outline-color', '--ring'],
    ])
    await page.getByRole('button', { name: 'Show all 6 columns' }).click()
    await page.locator('#check-g-invoice .check-subhead .check-link').click()
    await read([
      ['.check-map', 'row-gap', '22px'],
      ['.check-sum', 'padding-top', '18px'],
      ['.check-sum', 'padding-left', '20px'],
      ['.check-sum', 'column-gap', '20px'],
      ['.check-sum', 'background-color', '--secondary'],
      ['.check-sum-n', 'font-size', '34px'],
      ['.check-sum-n', 'font-weight', '800'],
      ['.check-sum-text', 'font-size', '15px'],
      ['.check-sum-text', 'font-weight', '700'],
      ['.check-meter', 'height', '6px'],
      ['.check-meter i', 'background-color', '--teal'],
      ['.check-unused-line', 'font-size', '13px'],
      ['.check-columns th', 'font-size', '10.5px'],
      ['.check-columns th', 'text-transform', 'uppercase'],
      ['.check-columns td', 'padding-top', '9px'],
      ['.check-columns td', 'padding-left', '12px'],
      ['.check-columns td', 'white-space', 'nowrap'],
      ['.check-col', 'color', '--teal'],
      ['.check-unused', 'font-size', '10px'],
      ['.check-unused', 'background-color', '--status-progress-bg'],
      ['.check-acc', 'padding-top', '14px'],
      ['.check-acc', 'padding-left', '16px'],
      ['.check-acc-text', 'min-width', '0px'],
      ['.check-acc-sub', 'text-overflow', 'ellipsis'],
      ['.check-acc-sub', 'white-space', 'nowrap'],
      ['.check-chev', 'display', 'inline-flex'],
      ['.check-chev', 'color', '--muted-foreground'],
      ['.check-acc-title', 'font-size', '15px'],
      ['.check-acc-title', 'font-weight', '700'],
      ['.check-count', 'font-size', '12px'],
      ['.check-row', 'padding-top', '10px'],
      ['.check-row', 'padding-left', '16px'],
      ['.check-row-lbl', 'font-size', '14px'],
      ['.check-row-lbl', 'font-weight', '600'],
      ['.check-row select', 'height', '40px'],
      ['.check-row select', 'font-size', '14px'],
      ['.check-fld.is-empty', 'color', '--muted-foreground'],
      ['.check-fld.is-empty', 'background-color', '--background'],
      ['.check-tag', 'font-size', '10px'],
      ['.check-tag', 'background-color', '--mint-soft'],
      ['.check-tag', 'color', '--status-success-fg'],
      ['.check-subhead', 'padding-top', '10px'],
      ['.check-subhead', 'padding-left', '16px'],
      ['.check-subhead', 'background-color', '--background'],
      ['.check-hint', 'font-size', '13.5px'],
      ['.check-hint', 'font-weight', '600'],
      ['.check-hint', 'background-color', '--status-progress-bg'],
      ['.check-sticky', 'padding-top', '16px'],
      ['.check-sticky', 'padding-left', '28px'],
      ['.check-sticky', 'border-top-color', '--cream-card-border'],
      ['.check-sticky', 'position', 'sticky'],
      ['.check-req', 'color', '--destructive'],
    ])
    expect(await tracks(page.locator('.check-row').first()), '1280 row tracks').toBe(2)
    // The fill is rounded to a whole percent in the page, so compare with the rounded share.
    const of = Number((await page.locator('.check-sum-n small').innerText()).replace(/\D/g, ''))
    const meter = await rectOf(page.locator('.check-meter'), 'meter')
    const fill = await rectOf(page.locator('.check-meter i'), 'meter fill')
    expect(Math.abs(fill.width - (meter.width * Math.round((4 / of) * 100)) / 100), `meter fill ${fill.width} of ${meter.width}`).toBeLessThanOrEqual(1)
    expect(await px(next, 'height')).toBe(46)
    expect(await px(next, 'min-width')).toBe(160)
    expect(await css(next, 'font-weight')).toBe('700')
    await page.getByLabel('Invoice number', { exact: true }).selectOption('')
    expect(await css(next, 'background-color'), 'disabled Continue background').toBe(await resolved(page, '--input', 'background-color'))
    expect(await css(next, 'color'), 'disabled Continue colour').toBe(await resolved(page, '--muted-foreground', 'color'))
    await page.getByLabel('Invoice number', { exact: true }).selectOption('Inv')
    await sendRejected(page)
    await read([
      ['.check-alert', 'padding-top', '14px'],
      ['.check-alert', 'padding-left', '16px'],
      ['.check-alert', 'color', '--destructive'],
    ])
    await resize(page, 390)
    await read([
      ['.check-map', 'row-gap', '18px'],
      ['.check-sum', 'padding-top', '16px'],
      ['.check-sum', 'padding-left', '16px'],
      ['.check-sum-n', 'font-size', '28px'],
      ['.check-row', 'padding-top', '12px'],
      ['.check-row', 'padding-left', '14px'],
      ['.check-sticky', 'padding-top', '12px'],
      ['.check-sticky', 'padding-left', '16px'],
      ['.check-actions', 'flex-direction', 'column-reverse'],
    ])
    expect(await tracks(page.locator('.check-row').first()), '390 row tracks').toBe(1)
    expect(await tracks(page.locator('.check-acc').first()), '390 header tracks').toBe(2)
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
    await expect(page.getByRole('heading', { name: 'Tell us which column is which' })).toBeVisible()
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

  // Upload step. `dragEvent` carries a file, so dataTransfer.types includes 'Files'.
  const dragEvent = (page: Page, type: 'dragenter' | 'dragleave') =>
    page.evaluateHandle((t) => {
      const dt = new DataTransfer()
      dt.items.add(new File(['x'], 'notes.txt'))
      document.querySelector('.check-drop')!.dispatchEvent(new DragEvent(t, { bubbles: true, cancelable: true, dataTransfer: dt }))
    }, type)
  const dragOver = (page: Page) => dragEvent(page, 'dragenter')
  const TXT = { name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('x') }
  const BIG = { name: 'big.csv', mimeType: 'text/csv', buffer: Buffer.alloc(15 * 1024 * 1024 + 1) }
  const near1 = (a: number, b: number, what: string) => expect(Math.abs(a - b), `${what}: ${a} vs ${b}`).toBeLessThanOrEqual(1)
  const bottom = (r: Rect) => r.y + r.height
  const pads = async (loc: Locator) => [await px(loc, 'padding-top'), await px(loc, 'padding-right'), await px(loc, 'padding-bottom')]

  test('check layout: the drop zone and its contents fit the card', async ({ page }) => {
    await openCheck(page, ALL_WIDTHS[0])
    for (const width of ALL_WIDTHS) {
      await resize(page, width)
      const label = `${width}px`
      const cardRect = await rectOf(card(page), 'card')
      const zone = await rectOf(page.locator('.check-drop'), 'zone')
      expect(enclosesRect(cardRect, zone, 1), `${label}: the zone is outside the card`).toBe(true)
      const chips = page.locator('.check-chip')
      expect(await chips.count()).toBe(4)
      const chipRects: Rect[] = []
      for (let i = 0; i < 4; i++) chipRects.push(await rectOf(chips.nth(i), `chip ${i}`))
      noOverlap(chipRects, `${label}: chips`)
      const stack = [
        await rectOf(page.locator('.check-drop-icon'), 'icon'),
        await rectOf(page.locator('.check-drop-title'), 'title'),
        await rectOf(page.locator('.check-drop-or'), 'or'),
        await rectOf(page.locator('.check-fake-btn'), 'button'),
        await rectOf(page.locator('.check-chips'), 'chips'),
        await rectOf(page.locator('.check-drop-note'), 'help'),
      ]
      for (const [i, r] of [...stack, ...chipRects].entries()) expect(enclosesRect(zone, r, 1), `${label}: part ${i} is outside the zone`).toBe(true)
      for (let i = 1; i < stack.length; i++) expect(stack[i].y, `${label}: part ${i} starts above part ${i - 1}'s bottom`).toBeGreaterThanOrEqual(bottom(stack[i - 1]) - 1)
      const lefts = [await rectOf(page.locator('.check-upload .check-label'), 'label'), zone, await rectOf(page.locator('.check-upload-note'), 'note')].map((r) => r.x)
      expect(Math.max(...lefts) - Math.min(...lefts), `${label}: label, zone and note share no left edge`).toBeLessThanOrEqual(1)
      await noSidewaysScroll(page, label)
    }
  })

  test('check layout: holding a file over the zone moves nothing', async ({ page }) => {
    await openCheck(page, 1280)
    for (const width of [1280, 390]) {
      await resize(page, width)
      const zone = page.locator('.check-drop')
      const [z0, n0] = [await rectOf(zone, 'zone'), await rectOf(page.locator('.check-upload-note'), 'note')]
      await dragOver(page)
      await expect(page.locator('.check-drop-title')).toHaveText('Drop it here to start')
      const [z1, n1] = [await rectOf(zone, 'zone'), await rectOf(page.locator('.check-upload-note'), 'note')]
      for (const k of ['x', 'y', 'width', 'height'] as const) near1(z1[k], z0[k], `${width}px zone ${k}`)
      near1(n1.y, n0.y, `${width}px note top`)
      await dragEvent(page, 'dragleave')
      await expect(page.locator('.check-drop-title')).toHaveText('Drag your invoice file here')
    }
  })

  test('check layout: a refusal sits under the zone and fits', async ({ page }) => {
    await openCheck(page, ALL_WIDTHS[0])
    for (const file of [TXT, BIG]) {
      await page.locator('#check-file').setInputFiles(file)
      await expect(page.locator('.check-err')).toBeVisible()
      for (const width of ALL_WIDTHS) {
        await resize(page, width)
        const label = `${width}px ${file.name}`
        const cardRect = await rectOf(card(page), 'card')
        const zone = await rectOf(page.locator('.check-drop'), 'zone')
        const err = await rectOf(page.locator('.check-err'), 'error')
        expect(err.y, `${label}: the error starts above the zone's bottom`).toBeGreaterThanOrEqual(bottom(zone) - 1)
        expect(enclosesRect(cardRect, err, 1) && enclosesRect(cardRect, zone, 1), `${label}: the error or zone is outside the card`).toBe(true)
        const icon = await rectOf(page.locator('.check-err svg'), 'error icon')
        const text = await rectOf(page.locator('.check-err span'), 'error text')
        expect(rectsOverlap(icon, text), `${label}: the error icon overlaps its text`).toBe(false)
        expect(enclosesRect(cardRect, text, 1), `${label}: the error text is outside the card`).toBe(true)
        expect((await rectOf(page.locator('.check-upload-note'), 'note')).y, `${label}: the note starts above the error's bottom`).toBeGreaterThanOrEqual(bottom(err) - 1)
        await noSidewaysScroll(page, label)
      }
    }
  })

  test('check layout: the reading row fits', async ({ page }) => {
    await resize(page, ALL_WIDTHS[0])
    await toMap(page, async () => {
      await expect(page.locator('.check-drop')).toHaveCount(0)
      for (const width of ALL_WIDTHS) {
        await resize(page, width)
        const label = `${width}px`
        const cardRect = await rectOf(card(page), 'card')
        const row = await rectOf(page.locator('.check-upload-file'), 'file row')
        const name = await rectOf(page.locator('.check-upload-name'), 'file name')
        const bar = await rectOf(page.locator('.check-bar'), 'bar')
        expect(enclosesRect(cardRect, row, 1), `${label}: the row is outside the card`).toBe(true)
        expect(enclosesRect(row, name, 1), `${label}: the name is outside the row`).toBe(true)
        expect(rectsOverlap(await rectOf(page.locator('.check-spinner'), 'spinner'), name), `${label}: the spinner overlaps the name`).toBe(false)
        expect(enclosesRect(cardRect, bar, 1), `${label}: the bar is outside the card`).toBe(true)
        expect(bar.y, `${label}: the bar is above the row`).toBeGreaterThanOrEqual(bottom(row) - 1)
        expect((await rectOf(page.getByRole('status'), 'status')).y, `${label}: the status is above the bar`).toBeGreaterThanOrEqual(bottom(bar) - 1)
      }
      await resize(page, ALL_WIDTHS[0])
    })
  })

  test('check layout: the upload step takes the design\'s sizes and gaps', async ({ page }) => {
    await openCheck(page, 1280)
    const zone = page.locator('.check-drop')
    const title = page.locator('.check-step-title')
    expect(await px(title, 'font-size'), '1280 step title').toBe(22)
    expect(await px(page.locator('.check-upload'), 'row-gap'), '1280 row gap').toBe(22)
    expect(await pads(zone), '1280 zone padding').toEqual([36, 24, 30])
    expect(await px(page.locator('.check-drop-title'), 'font-size')).toBe(17)
    expect(await px(page.locator('.check-fake-btn'), 'height')).toBe(44)
    expect(await px(page.locator('.check-chip').first(), 'font-size')).toBe(11)
    expect(await css(zone, 'border-top-style')).toBe('dashed')
    near1((await rectOf(title, 'h2')).y, bottom(await rectOf(page.locator('.check-step-no'), 'step no')) + 6, 'step-no to h2')
    near1((await rectOf(zone, 'zone')).y, bottom(await rectOf(page.locator('.check-upload .check-label'), 'label')) + 10, 'label to zone')
    await dragOver(page)
    await expect.poll(() => css(zone, 'border-top-color')).toBe(await resolved(page, '--teal', 'border-top-color'))
    expect(await css(zone, 'border-top-style')).toBe('solid')
    await expect.poll(() => css(zone, 'background-color')).toBe(await resolved(page, '--mint-soft', 'background-color'))
    await dragEvent(page, 'dragleave')
    await page.locator('#check-file').setInputFiles(TXT)
    await expect(page.locator('.check-err')).toBeVisible()
    await page.mouse.move(0, 0)
    await expect.poll(() => css(zone, 'border-top-color')).toBe(await resolved(page, '--destructive', 'border-top-color'))
    near1((await rectOf(page.locator('.check-err'), 'error')).y, bottom(await rectOf(zone, 'zone')) + 10, 'zone to error')
    await resize(page, 390)
    expect(await px(title, 'font-size'), '390 step title').toBe(20)
    expect(await px(page.locator('.check-upload'), 'row-gap'), '390 row gap').toBe(18)
    expect(await pads(zone), '390 zone padding').toEqual([26, 16, 22])

    await resize(page, 1280)
    await toMap(page, async () => {
      expect(await px(page.locator('.check-spinner'), 'width')).toBe(18)
      expect(await px(page.locator('.check-bar'), 'height')).toBe(4)
      const label = await rectOf(page.locator('.check-upload .check-label'), 'label')
      const row = await rectOf(page.locator('.check-upload-file'), 'file row')
      const bar = await rectOf(page.locator('.check-bar'), 'bar')
      const status = await rectOf(page.getByRole('status'), 'status')
      near1(row.y, bottom(label) + 14, 'label to file row')
      near1(bar.y, bottom(row) + 14, 'file row to bar')
      near1(status.y, bottom(bar) + 14, 'bar to status')
    })
  })

  test('check layout: the upload step takes the design\'s type and colour', async ({ page }) => {
    await openCheck(page, 1280)
    // [selector, property, expected]; a '--token' expectation compares the resolved value.
    type Row = [string, string, string]
    const read = async (rows: Row[]) => {
      for (const [sel, prop, want] of rows) {
        const loc = page.locator(sel).first()
        const expected = want.startsWith('--') ? await resolved(page, want, prop) : want
        expect(await css(loc, prop), `${sel} ${prop}`).toBe(expected)
      }
    }
    await read([
      ['.check-step-no', 'font-size', '11px'],
      ['.check-step-no', 'letter-spacing', '0.44px'],
      ['.check-step-no', 'text-transform', 'uppercase'],
      ['.check-step-no', 'color', '--step-label'],
      ['.check-upload .check-label', 'font-size', '14px'],
      ['.check-upload .check-label', 'font-weight', '700'],
      ['.check-upload .check-label', 'color', '--ink'],
      ['.check-drop', 'row-gap', '12px'],
      ['.check-drop-icon', 'width', '52px'],
      ['.check-drop-icon', 'height', '52px'],
      ['.check-drop-title', 'font-weight', '700'],
      ['.check-drop-or', 'font-size', '14px'],
      ['.check-drop-or', 'color', '--muted-foreground'],
      ['.check-fake-btn', 'padding-top', '0px'],
      ['.check-fake-btn', 'padding-left', '20px'],
      ['.check-fake-btn', 'background-color', '--primary'],
      ['.check-fake-btn', 'color', 'rgb(255, 255, 255)'],
      ['.check-fake-btn', 'font-size', '14px'],
      ['.check-fake-btn', 'font-weight', '700'],
      ['.check-chip', 'padding-top', '3px'],
      ['.check-chip', 'padding-left', '7px'],
      ['.check-chip', 'border-top-width', '1px'],
      ['.check-drop-note', 'font-size', '13px'],
      ['.check-upload-note', 'font-size', '13px'],
    ])
    // Chrome rounds a 1.5px border to whole px, so compare it with a 1.5px probe.
    expect(await css(page.locator('.check-drop'), 'border-top-width'), '.check-drop border-top-width').toBe(await probeBorder(page))
    await page.locator('.check-drop').hover()
    await expect.poll(() => css(page.locator('.check-drop'), 'background-color')).toBe(await resolved(page, '--sage-card', 'background-color'))
    await expect.poll(() => css(page.locator('.check-drop'), 'border-top-color')).toBe(await resolved(page, '--teal', 'border-top-color'))
    await page.locator('#check-file').setInputFiles(TXT)
    await expect(page.locator('.check-err')).toBeVisible()
    await read([
      ['.check-err', 'font-size', '13.5px'],
      ['.check-err', 'font-weight', '600'],
      ['.check-err', 'color', '--destructive'],
      ['.check-err', 'column-gap', '8px'],
    ])
    await toMap(page, async () => {
      await read([
        ['.check-upload-file', 'padding-top', '12px'],
        ['.check-upload-file', 'padding-left', '14px'],
        ['.check-upload-file', 'border-top-width', '1px'],
        ['.check-upload-name', 'font-size', '14px'],
        ['.check-upload-name', 'font-weight', '700'],
        ['.check-upload-size', 'font-size', '13px'],
      ])
    })
  })

  test('check layout: the drop zone shows the design\'s focus ring', async ({ page }) => {
    await openCheck(page, 1280)
    const zone = page.locator('.check-drop')
    expect(await css(zone, 'outline-style'), 'unfocused').toBe('none')
    await page.locator('#check-file').focus()
    expect(await css(zone, 'outline-style')).toBe('solid')
    expect(await css(zone, 'outline-width')).toBe('2px')
    expect(await css(zone, 'outline-offset')).toBe('2px')
    expect(await css(zone, 'outline-color')).toBe(await resolved(page, '--ring', 'outline-color'))
  })

  test('check layout: reduced motion stops the spinner and the bar', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await resize(page, WIDE_WIDTHS[0])
    await toMap(page, async () => {
      await expect(page.locator('.check-spinner')).toHaveCSS('animation-name', 'none')
      await expect(page.locator('.check-bar i')).toHaveCSS('animation-name', 'none')
      await page.emulateMedia({ reducedMotion: 'no-preference' })
      expect(await css(page.locator('.check-spinner'), 'animation-name')).not.toBe('none')
      expect(await css(page.locator('.check-bar i'), 'animation-name')).not.toBe('none')
    })
  })

  test('check layout: the Argos shots of the Upload and Map steps', async ({ page }) => {
    test.skip(!argosOn(), 'Argos shots are taken on the nightly only')
    await openCheck(page, 1440)
    await shoot(page, 'check', 'upload')
    await dragOver(page)
    await shoot(page, 'check', 'upload-drag')
    await dragEvent(page, 'dragleave')
    await page.locator('#check-file').setInputFiles(TXT)
    await expect(page.locator('.check-err')).toBeVisible()
    await shoot(page, 'check', 'upload-error-type')
    await toMap(page, async () => {
      await shoot(page, 'check', 'upload-reading')
    })
    await shoot(page, 'check', 'map')
    await toMapWith(page)
    await sendRejected(page)
    await shoot(page, 'check', 'map-error')
  })
})

test.describe('check confirm page', () => {
  test('check confirm page layout: the card and button fit at every width', async ({ page }) => {
    for (const width of [...WIDE_WIDTHS, ...PHONE_WIDTHS]) {
      const height = 1080
      await page.setViewportSize({ width, height })
      await page.goto(unknownConfirmLink())
      const button = page.getByRole('button')
      await expect(button, `one button at ${width}px`).toHaveCount(1)
      const main = page.locator('main')
      const cardBox = await rectOf(main, `the card at ${width}px`)
      const buttonBox = await rectOf(button, `the button at ${width}px`)
      expect(enclosesRect({ x: 0, y: 0, width, height }, cardBox, 1), `the card leaves the viewport at ${width}px`).toBe(true)
      expect(enclosesRect(cardBox, buttonBox, 1), `the button leaves the card at ${width}px`).toBe(true)
      const paragraphs = main.locator('p')
      const count = await paragraphs.count()
      expect(count, `paragraphs at ${width}px`).toBeGreaterThan(0)
      for (let i = 0; i < count; i++) {
        expect(enclosesRect(cardBox, await rectOf(paragraphs.nth(i), `paragraph ${i} at ${width}px`), 1), `paragraph ${i} leaves the card at ${width}px`).toBe(true)
      }
      expect(Math.abs(cardBox.x - (width - cardBox.x - cardBox.width)), `the card is off-centre at ${width}px`).toBeLessThanOrEqual(1)
      await noSidewaysScroll(page, `the confirm page at ${width}px`)
    }
  })

  test('check confirm page: one press sends one POST', async ({ page }) => {
    const posts: string[] = []
    page.on('request', (req) => {
      if (req.method() === 'POST' && new URL(req.url()).pathname === '/readiness-check/confirm') posts.push(req.url())
    })
    // The page reports its button on submit: a test call into a page with a held navigation never returns.
    const reports: unknown[] = []
    await page.exposeBinding('__confirmReport', (_source, value) => reports.push(value))
    // A window listener runs after the form's own submit handler, so it sees the pressed state.
    await page.addInitScript(() => {
      window.addEventListener('submit', () => {
        const b = document.querySelector('button')
        ;(window as unknown as { __confirmReport: (v: unknown) => void }).__confirmReport([b?.textContent?.trim(), b?.disabled])
      })
    })
    let release!: () => void
    const held = new Promise<void>((r) => (release = r))
    await page.route((u) => u.pathname === '/readiness-check/confirm', async (route) => {
      if (route.request().method() === 'POST') await held
      await route.continue()
    })
    try {
      await page.goto(unknownConfirmLink())
      const button = page.getByRole('button')
      await expect(button).toHaveText('Get my results')
      // The mouse API does not wait for the held navigation the way a locator click would.
      const box = await rectOf(button, 'the button')
      await page.mouse.dblclick(box.x + box.width / 2, box.y + box.height / 2)
      await expect.poll(() => reports[0], { message: 'the button as the first submit left it' }).toEqual(['Sending…', true])
      await expect.poll(() => posts.length, { message: 'POST /readiness-check/confirm requests sent by the double-click' }).toBe(1)
    } finally {
      release()
    }
    // The token was never issued, so the press lands on the expired page.
    await expect(page.getByText('This link has expired or was already used.')).toBeVisible()
    expect(posts.length, 'POST requests after the page answered').toBe(1)
  })
})
