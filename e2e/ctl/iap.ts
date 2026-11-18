// STUB (SECAUD-35-09 red phase): signatures only, wrong behaviour. The executor replaces the bodies.
import type { BrowserContext } from '@playwright/test'

export const IAP_ORIGINS = [] as readonly string[]
export const CTL_IAP_ACCOUNT = ''
export const isIapOrigin = (_url: URL): boolean => false

export async function mintIapToken(): Promise<{ token: string; expiresAt: string }> {
  return { token: '', expiresAt: '' }
}

export async function installIapRoute(_context: BrowserContext, _token: string, _origins: readonly string[]): Promise<void> {}

export const iapRouteScript = (_token: string): string => 'async page => {}'
