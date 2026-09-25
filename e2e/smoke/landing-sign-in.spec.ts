import { test, expect } from '@playwright/test'
import { resolveTarget } from '../targets'
import { seedConsent } from './landingConsent'

// The sign-in modal carries the email form above the persona list (AUTH-05 D7), so it is
// taller than the viewport on a short phone. Its card must stay inside the viewport and
// scroll its own content. Geometry has no jsdom oracle; this is behaviour, not a visual diff.

const LANDING_URL = resolveTarget('LANDING_URL')

const VIEWPORTS = [
  { width: 390, height: 667 },
  { width: 1280, height: 800 },
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

    // signin=ready opens the modal with the form, as the app's start bounce does (D25 step 3).
    await page.goto(`${LANDING_URL}/?state=${STATE}&signin=ready`)
    const dialog = page.getByRole('dialog', { name: 'Sign in' })
    await expect(dialog).toBeVisible()

    const card = dialog.locator(':scope > div')
    await expect(card).toHaveCount(1)
    // The entry animation moves the card by 10px; wait for its box to settle inside the viewport.
    await expect
      .poll(async () => {
        const box = await card.boundingBox()
        return box != null && box.x >= 0 && box.y >= 0 && box.x + box.width <= viewport.width && box.y + box.height <= viewport.height
      }, { message: `the card overflows ${viewport.width}x${viewport.height}` })
      .toBe(true)

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
