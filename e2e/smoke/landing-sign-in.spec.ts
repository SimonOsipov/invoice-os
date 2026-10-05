import { test, expect } from '@playwright/test'
import { resolveTarget } from '../targets'
import { enclosesRect, gaps, settleAnimations, type Rect } from '../topology/layout'
import { seedConsent } from './landingConsent'

// The sign-in dialog holds the form and no account chooser. Its card must stay inside the
// viewport, stay centred, and keep the form's controls inside it on a short phone. Geometry
// has no jsdom oracle; this is behaviour, not a visual diff.

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
    const viewportRect: Rect = { x: 0, y: 0, width: viewport.width, height: viewport.height }
    // The entry animation moves the card; wait for its box to settle inside the viewport.
    await expect
      .poll(async () => {
        const box = await card.boundingBox()
        return box != null && enclosesRect(viewportRect, box)
      }, { message: `the card overflows ${viewport.width}x${viewport.height}` })
      .toBe(true)

    await settleAnimations(card)
    const cardBox = await card.boundingBox()
    expect(cardBox, 'the card has no box').not.toBeNull()
    const side = gaps(cardBox!, viewportRect)
    expect(Math.abs(side.left - side.right), `the card is off-centre at ${viewport.width}x${viewport.height}: ${JSON.stringify(side)}`).toBeLessThanOrEqual(1)

    // The card scrolls its own content, so x is the only axis a control must stay inside it on.
    const xOnly = (r: Rect): Rect => ({ x: r.x, y: 0, width: r.width, height: 1 })
    const controls = {
      'email input': dialog.getByLabel('Work email', { exact: true }),
      'password input': dialog.getByLabel('Password', { exact: true }),
      'Sign in button': dialog.getByRole('button', { name: 'Sign in →', exact: true }),
    }
    for (const [name, control] of Object.entries(controls)) {
      await control.scrollIntoViewIfNeeded()
      await expect(control, `the ${name} is not visible`).toBeVisible()
      const box = await control.boundingBox()
      expect(box, `the ${name} has no box`).not.toBeNull()
      expect(enclosesRect(xOnly(cardBox!), xOnly(box!), 0.5), `the ${name} leaves the card at ${viewport.width}x${viewport.height}: ${JSON.stringify({ cardBox, box })}`).toBe(true)
    }
    await expect(controls['Sign in button']).toBeInViewport()

    const overflow = await card.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
    expect(overflow.scrollWidth, `the card scrolls sideways at ${viewport.width}x${viewport.height}: ${JSON.stringify(overflow)}`).toBeLessThanOrEqual(overflow.clientWidth + 1)

    // The form is visible above, so these absences are not vacuous.
    await expect(dialog.locator('[data-persona]')).toHaveCount(0)
    await expect(dialog.getByText('Choose an account')).toHaveCount(0)

    expect(errors, `console errors on the landing page:\n${errors.join('\n')}`).toEqual([])
  })
}
