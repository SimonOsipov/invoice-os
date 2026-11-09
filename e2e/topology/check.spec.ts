// The /check page layout on the PR preview. Each case asserts a relationship, never a raw dimension.
// It never presses Send: no run, no IP count, no mail.
import { test, expect, type Locator, type Page } from '@playwright/test'
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
})
