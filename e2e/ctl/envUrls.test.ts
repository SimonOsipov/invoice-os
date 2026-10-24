import { describe, expect, it } from 'vitest'
import { resolveEnv } from './envUrls'

describe('resolveEnv', () => {
  it('names a PR preview by its run.app services', () => {
    const r = resolveEnv('pr-12')
    expect(r.urls.GATEWAY_URL).toBe('https://gateway-pr-12-944095690109.africa-south1.run.app')
    expect(r.urls.SUPPORT_CONSOLE_URL).toBe('https://support-console-pr-12-944095690109.africa-south1.run.app')
    expect(Object.keys(r.urls)).toHaveLength(7)
  })

  it('names production by its hosts', () => {
    const r = resolveEnv('production')
    expect(r.urls.GATEWAY_URL).toBe('https://api.ascomply.com')
    expect(r.urls.LANDING_URL).toBe('https://www.ascomply.com')
    expect(r.urls.SUPPORT_CONSOLE_URL).toBe('https://sup.ascomply.com')
  })
})
