// The four base resolvers: null when unset (mirrors gatewayBase()), trailing slashes trimmed.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { appBase, consoleBase, opsBase, supportBase } from './auth'

// Unset targets are stubbed to '' so a shell-exported VITE_* cannot leak in.
function stubTargets(env: Partial<Record<'VITE_APP_URL' | 'VITE_OPS_URL' | 'VITE_SUPPORT_URL', string>>): void {
  for (const k of ['VITE_APP_URL', 'VITE_OPS_URL', 'VITE_SUPPORT_URL'] as const) vi.stubEnv(k, env[k] ?? '')
}

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('base resolvers', () => {
  it('bases return null when unset', () => {
    stubTargets({})
    expect(appBase()).toBeNull()
    expect(opsBase()).toBeNull()
    expect(supportBase()).toBeNull()
    // Whitespace-only is unset too.
    stubTargets({ VITE_APP_URL: '  ', VITE_OPS_URL: ' ', VITE_SUPPORT_URL: '\t' })
    expect([appBase(), opsBase(), supportBase()]).toEqual([null, null, null])
  })

  it('bases trim trailing slashes', () => {
    stubTargets({ VITE_APP_URL: 'https://a.x//', VITE_OPS_URL: 'https://o.x/', VITE_SUPPORT_URL: 'https://s.x' })
    expect(appBase()).toBe('https://a.x')
    expect(opsBase()).toBe('https://o.x')
    expect(supportBase()).toBe('https://s.x')
  })

  it('consoleBase picks by target', () => {
    stubTargets({ VITE_APP_URL: 'https://a.x', VITE_OPS_URL: 'https://ops.x', VITE_SUPPORT_URL: 'https://support.x' })
    expect(consoleBase('ops')).toBe('https://ops.x')
    expect(consoleBase('support')).toBe('https://support.x')
  })

  it('consoleBase is null when its target is unset', () => {
    // The app base is set so a fallback from an unset console to the app would show.
    stubTargets({ VITE_APP_URL: 'https://a.x', VITE_OPS_URL: 'https://ops.x' })
    expect(consoleBase('ops'), 'control: the set target resolves').toBe('https://ops.x')
    expect(consoleBase('support')).toBeNull()
    stubTargets({ VITE_APP_URL: 'https://a.x', VITE_SUPPORT_URL: 'https://support.x' })
    expect(consoleBase('ops')).toBeNull()
  })
})
