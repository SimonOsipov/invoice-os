// RED-then-GREEN specs (M4-21-10, AC-2) — pin resolveTarget's fail-loud contract before
// topology/targets.ts, smoke/apps.ts and api/client.ts are pointed at it.
import { afterEach, describe, expect, it } from 'vitest'

import { resolveTarget } from './targets'

const ENV_VAR = 'GATEWAY_URL'

afterEach(() => {
  delete process.env[ENV_VAR]
})

describe('resolveTarget', () => {
  it('throws when the env var is unset', () => {
    delete process.env[ENV_VAR]

    expect(() => resolveTarget(ENV_VAR)).toThrow(/GATEWAY_URL/)
  })

  it('throws when the env var is whitespace-only', () => {
    process.env[ENV_VAR] = '   '

    expect(() => resolveTarget(ENV_VAR)).toThrow(/GATEWAY_URL/)
  })

  it('trims whitespace and strips trailing slashes', () => {
    process.env[ENV_VAR] = '  https://gw///  '

    expect(resolveTarget(ENV_VAR)).toBe('https://gw')
  })
})
