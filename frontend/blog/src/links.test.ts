import { afterEach, describe, expect, it, vi } from 'vitest'
import { PRODUCTION_HOSTNAMES } from './analytics'
import { demoHref, landingHref, privacyHref } from './links'

afterEach(() => vi.unstubAllEnvs())

describe('links', () => {
  it('links_landingHrefUsesTheLandingUrl', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing-pr-1-944095690109.africa-south1.run.app/')
    expect(landingHref()).toBe('https://landing-pr-1-944095690109.africa-south1.run.app')
    for (const v of ['', '   ']) {
      vi.stubEnv('VITE_LANDING_URL', v)
      expect(landingHref(), JSON.stringify(v)).toBe(`https://${PRODUCTION_HOSTNAMES[0]}`)
    }
  })

  it('links_privacyHrefIsOnTheLandingOrigin', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing-pr-1-944095690109.africa-south1.run.app/')
    expect(privacyHref()).toBe('https://landing-pr-1-944095690109.africa-south1.run.app/privacy')
    vi.stubEnv('VITE_LANDING_URL', '')
    expect(privacyHref()).toBe(`https://${PRODUCTION_HOSTNAMES[0]}/privacy`)
  })

  it('links_demoHrefIsTheLandingDeepLink', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing-pr-1-944095690109.africa-south1.run.app/')
    expect(demoHref()).toBe('https://landing-pr-1-944095690109.africa-south1.run.app/?demo')
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
