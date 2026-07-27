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
  it('appBase/opsBase/supportBase: return null when their VITE_* vars are unset', () => {
    // Every persona, so a newly added target can never quietly skip the null contract.
    for (const p of LANDING_PERSONAS) {
      expect(destUrl(p), `persona ${p.id}`).toBeNull()
    }
  })
})

describe('LANDING_PERSONAS', () => {
  it('ids are unique and each carries the persona id into the destination', () => {
    expect(new Set(LANDING_PERSONAS.map((p) => p.id)).size).toBe(LANDING_PERSONAS.length)
  })

  // The four shipped surfaces: two tenant workspaces on the app, the Ops Console on
  // the ops-console service, and the Support Console on its own. A persona pointing at the
  // wrong service is the one bug this list can have that nothing else would catch.
  it('routes each persona to its own console', () => {
    const byId = Object.fromEntries(LANDING_PERSONAS.map((p) => [p.id, p.target]))
    expect(byId).toEqual({ developer: 'ops', support: 'support', firm: 'app', inhouse: 'app' })
  })
})
