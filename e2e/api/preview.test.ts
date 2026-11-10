import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const exec = vi.hoisted(() => vi.fn(() => 'value'))
vi.mock('node:child_process', () => ({ execFileSync: exec }))

import { previewSecret } from './preview'

describe('previewSecret', () => {
  beforeEach(() => {
    vi.stubEnv('GATEWAY_URL', 'https://gateway-nightly-944095690109.africa-south1.run.app')
    exec.mockClear()
  })
  afterEach(() => vi.unstubAllEnvs())

  const accessArgs = () => {
    expect(exec).toHaveBeenCalledTimes(1)
    return (exec.mock.calls[0] as unknown as [string, string[]])[1]
  }

  it('reads the version it is given, never latest', () => {
    previewSecret('hubspot-token', '7')
    expect(accessArgs()).toEqual([
      'secrets', 'versions', 'access', '7', '--secret', 'nightly-hubspot-token', '--project', 'ascomply-preview',
    ])
  })

  it('reads latest when no version is given', () => {
    previewSecret('mock-issuer-key')
    expect(accessArgs()[3]).toBe('latest')
  })
})
