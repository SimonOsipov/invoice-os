import type { BrowserContext, Cookie } from '@playwright/test'

import { resolveTarget } from './targets'

export type Audience = 'app' | 'ops' | 'support'

// The context jar includes HttpOnly cookies, which no page script can read.
export async function refreshCookie(context: BrowserContext, audience: Audience): Promise<Cookie | null> {
  const cookies = await context.cookies(resolveTarget('GATEWAY_URL'))
  return cookies.find((c) => c.name === `__Host-rt-${audience}`) ?? null
}

export async function clearRefreshCookie(context: BrowserContext, audience: Audience): Promise<void> {
  await context.clearCookies({ name: `__Host-rt-${audience}` })
}
