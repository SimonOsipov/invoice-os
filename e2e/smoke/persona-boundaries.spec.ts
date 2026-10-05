import { test, expect } from '@playwright/test'
import { BOUNDARY_MATRIX } from '../personas'
import { collectErrors, expectRefused } from '../personaSession'

// The cross-persona boundary matrix: every destination visited with `?persona=<id>` and no
// session is sent back to the landing page. A present-but-wrong param is refused exactly as an
// absent one is. One case per BOUNDARY_MATRIX row; personas.test.ts row 10 keeps the matrix at
// the 12 pairs.
//
// A green matrix proves where the browser ends up, not that no console chrome painted first.
// Each case asserts the landing h1 and eyebrow, so a blank page or a URL-prefix match fails.
// smoke/apps.ts is the positive control: each destination draws for a visitor it admits.
//
// Smoke, not topology: a refusal reaches no gateway and touches no database, so it is safe under
// smoke's `fullyParallel: true`.

for (const { persona, destination } of BOUNDARY_MATRIX) {
  test(`${destination}: refuses the ${persona} persona and returns it to the landing page`, async ({ page }) => {
    // Attached before the navigation inside expectRefused, so load-time errors are caught.
    const errors = collectErrors(page)

    await expectRefused(page, persona, destination)

    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
    await expect(page.locator('#top .t-eyebrow')).toContainText(/e-invoicing/i)

    expect(errors, `console errors refusing ${persona} at ${destination}:\n${errors.join('\n')}`).toEqual([])
  })
}
