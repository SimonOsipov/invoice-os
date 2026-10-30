import { expect, type Page } from '@playwright/test'

import { me, type Me } from './api/client'
import { VERIFIED, browserToken } from './personaSession'
import { seedConsent } from './smoke/landingConsent'
import { resolveTarget } from './targets'

const LANDING_URL = resolveTarget('LANDING_URL')

// The accept page of an invite link (frontend/landing/src/components/InvitePage.tsx).
export const inviteUrl = (token: string) => `${LANDING_URL}/invite#token=${token}`

// A fragment-only goto is a same-document navigation: the page would keep its view and its address-bar fragment. Load afresh.
export async function openInvite(page: Page, token: string): Promise<void> {
  await page.goto('about:blank')
  await page.goto(inviteUrl(token))
}

// Registers the invitee on the accept page and ends on "Check your email".
export async function registerOnAcceptPage(page: Page, token: string): Promise<void> {
  await seedConsent(page, false)
  await openInvite(page, token)
  await page.getByRole('button', { name: 'Create account', exact: true }).click()
  await page.getByRole('button', { name: 'Create account →', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Check your email', exact: true })).toBeVisible({ timeout: 30_000 })
}

// Waits for the app to draw, then returns the browser session's /v1/me.
export async function readSessionIdentity(page: Page): Promise<Me> {
  await expect(page.locator(VERIFIED)).toBeAttached({ timeout: 30_000 })
  return me(await browserToken(page))
}
