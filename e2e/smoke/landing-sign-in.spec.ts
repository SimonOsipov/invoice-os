import { test, expect } from '@playwright/test'
import { resolveTarget } from '../targets'
import { enclosesRect, gaps, settleAnimations, type Rect } from '../topology/layout'
import { seedConsent } from './landingConsent'

// The sign-in modal carries the email form above the persona list, so it is
// taller than the viewport on a short phone. Its card must stay inside the viewport and
// scroll its own content. Geometry has no jsdom oracle; this is behaviour, not a visual diff.

const LANDING_URL = resolveTarget('LANDING_URL')

const VIEWPORTS = [
  { width: 390, height: 667 },
  { width: 1280, height: 800 },
  { width: 1440, height: 900 },
] as const

// A state in the app's shape (43 base64url characters): landing only checks the format.
const STATE = 'A'.repeat(43)

for (const viewport of VIEWPORTS) {
  test(`landing sign-in modal: usable at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    const errors: string[] = []
    page.on('console', (msg) => {
      if (msg.type() === 'error') errors.push(msg.text())
    })
    page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`))
    await page.setViewportSize(viewport)
    await seedConsent(page, false)

    // signin=ready opens the modal with the form, as the app's start bounce does.
    await page.goto(`${LANDING_URL}/?state=${STATE}&signin=ready`)
    const dialog = page.getByRole('dialog', { name: 'Platform login' })
    await expect(dialog).toBeVisible()

    const card = dialog.locator(':scope > div')
    await expect(card).toHaveCount(1)
    // The entry animation moves the card; wait for its box to settle inside the viewport.
    await expect
      .poll(async () => {
        const box = await card.boundingBox()
        return box != null && box.x >= 0 && box.y >= 0 && box.x + box.width <= viewport.width && box.y + box.height <= viewport.height
      }, { message: `the card overflows ${viewport.width}x${viewport.height}` })
      .toBe(true)

    // O1: equal side gaps, read after the entry animation has finished.
    await settleAnimations(card)
    const cardBox = await card.boundingBox()
    expect(cardBox, 'the card has no box').not.toBeNull()
    const side = gaps(cardBox!, { x: 0, width: viewport.width })
    expect(Math.abs(side.left - side.right), `the card is off-centre at ${viewport.width}x${viewport.height}: ${JSON.stringify(side)}`).toBeLessThanOrEqual(1)

    // The card scrolls its own content, so x is the only axis a row must stay inside it on.
    const xOnly = (r: Rect): Rect => ({ x: r.x, y: 0, width: r.width, height: 1 })
    const grid = dialog.getByTestId('persona-picker').locator(':scope > div')
    await expect(grid).toHaveCount(1)
    const rows = dialog.locator('[data-persona]')
    const rowCount = await rows.count()
    expect(rowCount, 'the persona picker has no rows').toBeGreaterThan(0)
    const gridBox = (await grid.boundingBox())!
    expect(gridBox, 'the persona grid has no box').not.toBeNull()
    for (let i = 0; i < rowCount; i++) {
      const row = rows.nth(i)
      const chevron = row.locator('svg').last()
      const [rowBox, chevronBox] = await Promise.all([row.boundingBox(), chevron.boundingBox()])
      expect(rowBox, `persona row ${i} has no box`).not.toBeNull()
      expect(chevronBox, `persona row ${i} has no chevron box`).not.toBeNull()
      const at = `persona row ${i} at ${viewport.width}x${viewport.height}`
      expect(enclosesRect(xOnly(gridBox), xOnly(rowBox!), 0.5), `${at} overflows the persona grid: ${JSON.stringify({ gridBox, rowBox })}`).toBe(true)
      expect(enclosesRect(xOnly(cardBox!), xOnly(rowBox!), 0.5), `${at} overflows the card: ${JSON.stringify({ cardBox, rowBox })}`).toBe(true)
      expect(enclosesRect(rowBox!, chevronBox!, 0.5), `${at}: the chevron leaves its row: ${JSON.stringify({ rowBox, chevronBox })}`).toBe(true)
      expect(enclosesRect(xOnly(cardBox!), xOnly(chevronBox!), 0.5), `${at}: the chevron is clipped by the card: ${JSON.stringify({ cardBox, chevronBox })}`).toBe(true)
    }
    const overflow = await card.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
    expect(overflow.scrollWidth, `the card scrolls sideways at ${viewport.width}x${viewport.height}: ${JSON.stringify(overflow)}`).toBeLessThanOrEqual(overflow.clientWidth + 1)

    const submit = dialog.getByRole('button', { name: 'Sign in →', exact: true })
    await submit.scrollIntoViewIfNeeded()
    await expect(submit).toBeVisible()
    await expect(submit).toBeInViewport()

    const lastPersona = dialog.locator('[data-persona]').last()
    await lastPersona.scrollIntoViewIfNeeded()
    await expect(lastPersona).toBeVisible()
    await expect(lastPersona).toBeInViewport()

    expect(errors, `console errors on the landing page:\n${errors.join('\n')}`).toEqual([])
  })
}
