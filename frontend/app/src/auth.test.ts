// RED-then-GREEN spec (M4-21-10, AC-1) — pins landingBase()'s null-when-unset contract,
// mirroring gatewayBase()'s C8b behaviour (packages/api-client/src/client.test.ts), before
// its hardcoded dev-landing fallback is removed.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { landingBase, libraryBase, supportBase } from './auth'

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

describe('supportBase', () => {
  it('supportBase_trimsAndNullsBlank', () => {
    vi.stubEnv('VITE_SUPPORT_URL', ' https://sup.x/ ')
    expect(supportBase()).toBe('https://sup.x')
    vi.stubEnv('VITE_SUPPORT_URL', '')
    expect(supportBase()).toBeNull()
    vi.stubEnv('VITE_SUPPORT_URL', '  ')
    expect(supportBase()).toBeNull()
  })
})
