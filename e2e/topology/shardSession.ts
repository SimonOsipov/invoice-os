import { expect, type Page } from '@playwright/test'

import { login, me } from '../api/client'
import { APP_URL } from './targets'

const SESSION_KEY = 'invoice-os.session'
const SEEDED_FLAG = 'e2e.seeded'

type ShardLogin = { id: string; subject: string; name: string; kind: string; role: string }

// Stores the session record the SPA keeps after a real sign-in (lib/session.ts serializeSession):
// a hand-off record for firm, a persona record for in-house (a hand-off record is firm-mode only).
// The init script writes once per tab, so a reload or a deep link keeps what the app stored.
export async function seedShardSession(page: Page, persona: 'firm' | 'inhouse', tenant: ShardLogin): Promise<void> {
  const token = await login({ ...tenant, tenantId: tenant.id })
  const profile = await me(token)
  expect(profile.tenant.id, 'the mock issuer minted a token for another tenant').toBe(tenant.id)
  const record = JSON.stringify({
    v: 1,
    personaId: persona,
    token,
    me: profile,
    verified: true,
    ...(persona === 'firm' ? { handoff: true } : {}),
  })
  await page.context().addInitScript(
    ({ origin, key, flag, value }) => {
      if (location.origin !== origin || sessionStorage.getItem(flag)) return
      sessionStorage.setItem(flag, '1')
      localStorage.setItem(key, value)
    },
    { origin: new URL(APP_URL).origin, key: SESSION_KEY, flag: SEEDED_FLAG, value: record },
  )
}

// Fails a sign-in that landed in another tenant, such as a `?persona=` navigation that re-minted the seeded 1111 / 2222.
export async function assertShardSession(page: Page, tenantId: string): Promise<void> {
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
  const raw = await page.evaluate((key) => localStorage.getItem(key), SESSION_KEY)
  expect(raw, 'no stored session after sign-in').not.toBeNull()
  expect(JSON.parse(raw!).me?.tenant?.id, 'the session is bound to another tenant than the shard').toBe(tenantId)
}
