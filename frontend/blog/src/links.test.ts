import { afterEach, describe, expect, it, vi } from 'vitest'
import { landingHref } from './links'

afterEach(() => vi.unstubAllEnvs())

describe('links', () => {
  it('links_goToAscomplyUsesTheLandingUrl', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing-pr-1.up.railway.app/')
    expect(landingHref()).toBe('https://landing-pr-1.up.railway.app')
    for (const v of ['', '   ']) {
      vi.stubEnv('VITE_LANDING_URL', v)
      expect(landingHref(), JSON.stringify(v)).toBe('https://www.ascomply.com')
    }
  })
})
