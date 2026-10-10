// RED-then-GREEN spec (M4-21-10, AC-1) — pins landingBase()'s null-when-unset contract,
// mirroring gatewayBase()'s C8b behaviour (packages/api-client/src/client.test.ts), before
// its hardcoded dev-landing fallback is removed.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { landingBase, libraryBase } from './auth'

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('landingBase', () => {
  it('returns null when VITE_LANDING_URL is unset', () => {
    expect(landingBase()).toBeNull()
  })
})

describe('libraryBase', () => {
  it('libraryBase_trimsTheSlashAndNullsBlank', () => {
    vi.stubEnv('VITE_LIBRARY_URL', ' https://lib.x/ ')
    expect(libraryBase()).toBe('https://lib.x')
    vi.stubEnv('VITE_LIBRARY_URL', '')
    expect(libraryBase()).toBeNull()
    vi.stubEnv('VITE_LIBRARY_URL', '  ')
    expect(libraryBase()).toBeNull()
    vi.unstubAllEnvs()
    expect(libraryBase()).toBeNull()
  })
})
