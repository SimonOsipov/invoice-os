import { test, expect } from '@playwright/test'

import { collectErrors, signInAs } from '../personaSession'
import { FORBIDDEN_STRINGS } from '../envCopyStrings'

// The env pill has no persona branch (Header.tsx reads ctx.sandbox alone), so one persona
// is the whole contract.
//
// The forbidden strings are also asserted at the node tier (frontend/app/src/envPosture.test.ts).
// That tier is not redundant: with LIVE disabled, ENV_BANNER.live can never render here.

test('deployed app: the environment pill is SANDBOX by default and LIVE is unselectable', async ({ page }) => {
  const errors = collectErrors(page)

  await signInAs(page, 'firm')

  const pill = page.getByTestId('env-pill')
  const live = page.getByTestId('env-pill-live')
  const sbx = pill.getByRole('button', { name: 'SANDBOX' })

  // Fresh load, no interaction — the end-to-end oracle for the SANDBOX default.
  await expect(sbx).toHaveAttribute('aria-pressed', 'true')
  await expect(live).toHaveAttribute('aria-pressed', 'false')
  await expect(live).toBeDisabled()

  // force:true is mandatory — actionability would hang on a disabled element rather than
  // report the click as refused.
  await live.click({ force: true })
  await expect(sbx).toHaveAttribute('aria-pressed', 'true')
  await expect(live).toHaveAttribute('aria-pressed', 'false')

  // LIVE skipped by Tab ⇒ out of the tab order and not a focus trap. Strictly stronger
  // than "Enter does nothing", and the reason no assertion here reads `title`: a disabled
  // control is unreachable by keyboard, so its tooltip never fires.
  await sbx.focus()
  await page.keyboard.press('Tab')
  await expect(page.getByRole('button', { name: 'New invoice' })).toBeFocused()

  const banner = page.getByTestId('env-banner')
  await expect(banner).toBeVisible()
  await expect(banner).toContainText('accreditation')
  for (const phrase of FORBIDDEN_STRINGS) {
    // ignoreCase: the live defect this list guards was a capital-S "Sent to NRS".
    await expect(banner, `the environment banner claims "${phrase}"`).not.toContainText(phrase, { ignoreCase: true })
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
