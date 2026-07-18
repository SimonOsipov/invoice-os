// RED-then-GREEN spec (M4-21-10, AC-1) — pins landingBase()'s null-when-unset contract,
// mirroring gatewayBase()'s C8b behaviour (packages/api-client/src/client.test.ts), before
// its hardcoded dev-landing fallback is removed.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { landingBase } from './auth'

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('landingBase', () => {
  it('returns null when VITE_LANDING_URL is unset', () => {
    expect(landingBase()).toBeNull()
  })
})
