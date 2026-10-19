import { afterEach, describe, expect, it, vi } from 'vitest'
import { PRODUCTION_HOSTNAMES } from './analytics'
import { demoHref, landingHref, privacyHref } from './links'

afterEach(() => vi.unstubAllEnvs())

describe('links', () => {
  it('links_goToAscomplyUsesTheLandingUrl', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing-pr-1.up.railway.app/')
    expect(landingHref()).toBe('https://landing-pr-1.up.railway.app')
    for (const v of ['', '   ']) {
      vi.stubEnv('VITE_LANDING_URL', v)
      expect(landingHref(), JSON.stringify(v)).toBe(`https://${PRODUCTION_HOSTNAMES[0]}`)
    }
  })

  it('links_privacyHrefIsOnTheLandingOrigin', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing-pr-1.up.railway.app/')
    expect(privacyHref()).toBe('https://landing-pr-1.up.railway.app/privacy')
    vi.stubEnv('VITE_LANDING_URL', '')
    expect(privacyHref()).toBe(`https://${PRODUCTION_HOSTNAMES[0]}/privacy`)
  })

  it('links_demoHrefIsTheLandingDeepLink', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing-pr-1.up.railway.app/')
    expect(demoHref()).toBe('https://landing-pr-1.up.railway.app/?demo')
    vi.stubEnv('VITE_LANDING_URL', '')
    expect(demoHref()).toBe(`https://${PRODUCTION_HOSTNAMES[0]}/?demo`)
  })

  it('links_fallbackFollowsTheFirstProductionHostname', async () => {
    vi.stubEnv('VITE_LANDING_URL', '')
    vi.resetModules()
    vi.doMock('./analytics', () => ({ PRODUCTION_HOSTNAMES: ['host-from-the-list.test'] }))
    const links = await import('./links')
    expect(links.landingHref()).toBe('https://host-from-the-list.test')
    vi.doUnmock('./analytics')
  })
})
