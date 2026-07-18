// RED-then-GREEN spec (M4-21-10, AC-1) — pins appBase()/opsBase()'s null-when-unset
// contract, mirroring gatewayBase()'s C8b behaviour (packages/api-client/src/client.test.ts),
// before their hardcoded dev-deploy fallbacks are removed. destUrl() must degrade to the
// documented no-gateway path (return null) rather than pointing at `development`.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { destUrl, LANDING_PERSONAS } from './auth'

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('destUrl', () => {
  it('appBase/opsBase: return null when their VITE_* vars are unset', () => {
    const firm = LANDING_PERSONAS.find((p) => p.id === 'firm')!
    const support = LANDING_PERSONAS.find((p) => p.id === 'support')!

    expect(destUrl(firm)).toBeNull()
    expect(destUrl(support)).toBeNull()
  })
})
