import { afterEach, describe, expect, it, vi } from 'vitest'
import { APP_PATHS } from './appPaths.ts'
import { FEATURES, GROUPS } from './content.ts'
import { demoHref, featurePlatformHref, groupPlatformHref, platformHref, privacyHref } from './links.ts'

afterEach(() => vi.unstubAllEnvs())

describe('links', () => {
  it('platformHref_prefixesTheAppAndTagsTheVisit', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.example//')
    expect(platformHref('/create')).toBe('https://app.example/create?via=library')
    expect(platformHref('/')).toBe('https://app.example/?via=library')
  })

  it('platformHref_isNullWhenTheAppUrlIsUnset', () => {
    for (const v of ['', '   ']) {
      vi.stubEnv('VITE_APP_URL', v)
      expect(platformHref('/create')).toBeNull()
    }
  })

  it('featurePlatformHref_isNullForEveryComingSoonFeature', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.example')
    let soon = 0
    let shipped = 0
    for (const f of FEATURES) {
      if (f.status === 'soon') {
        expect(featurePlatformHref(f)).toBeNull()
        soon++
      } else {
        expect(featurePlatformHref(f)).toBe(platformHref(f.path))
        shipped++
      }
    }
    expect([soon, shipped]).toEqual([11, 15])
    const clear = FEATURES.find((f) => f.id === 'submit-clear')!
    expect(featurePlatformHref(clear)).toBe('https://app.example/invoices?via=library')
  })

  it('groupPlatformHref_isNullForAGroupWithNoShippedFeature', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.example')
    const nulls = GROUPS.filter((g) => groupPlatformHref(g) === null).map((g) => g.id)
    expect(nulls).toEqual(['notifications', 'settings'])
    for (const g of GROUPS.filter((x) => !nulls.includes(x.id))) {
      expect(groupPlatformHref(g)).toBe(platformHref(APP_PATHS[g.view]))
    }
    const reports = GROUPS.find((g) => g.id === 'reports')!
    expect(groupPlatformHref(reports)).toBe('https://app.example/?via=library')
  })

  it('demoHref_isTheLandingDemoDeepLink', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://www.example/')
    expect(demoHref()).toBe('https://www.example/?demo')
    vi.stubEnv('VITE_LANDING_URL', '')
    expect(demoHref()).toBeNull()
  })
})

describe('privacyHref', () => {
  it('LK-PH-01 privacyHref follows the landing URL', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://l.example/')
    expect(privacyHref()).toBe('https://l.example/privacy')
    vi.stubEnv('VITE_LANDING_URL', '')
    expect(privacyHref()).toBe('https://www.ascomply.com/privacy')
    vi.unstubAllEnvs()
    expect(privacyHref()).toBe('https://www.ascomply.com/privacy')
  })
})
