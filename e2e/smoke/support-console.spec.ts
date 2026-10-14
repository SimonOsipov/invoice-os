import { expect } from '@playwright/test'

import { collectErrors } from '../personaSession'
import { seedStaffSession, test } from '../staffSession'

// Console shell smoke on a real staff session. The Rules screen's contract lives in api/contract-validation.spec.ts.
// Assert the h1, not the TopBar crumb: they can differ.
const SCREENS: { nav: string; h1: string }[] = [
  { nav: 'Rules', h1: 'Rules admin' },
  { nav: 'Tenants', h1: 'Tenants' },
  { nav: 'System health', h1: 'System health' },
]

test('support-console: every screen is reachable from the sidebar and renders its own h1', async ({ page, staffAccount }) => {
  const errors = collectErrors(page)
  await seedStaffSession(page, 'support', staffAccount)

  const sidebar = page.locator('aside.ops-sidebar')
  // A new screen must turn this red rather than being skipped by the loop.
  await expect(sidebar.locator('nav button.ops-nav')).toHaveCount(SCREENS.length)

  for (const screen of SCREENS) {
    await sidebar.getByRole('button', { name: screen.nav }).click()
    await expect(page.getByRole('heading', { level: 1 }), `clicking "${screen.nav}" should open "${screen.h1}"`).toHaveText(screen.h1)
  }

  expect(errors, `console errors sweeping the support console:\n${errors.join('\n')}`).toEqual([])
})
