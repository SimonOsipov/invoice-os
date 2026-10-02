import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const h = vi.hoisted(() => ({ initMonitoring: vi.fn() }))

vi.mock('@invoice-os/monitoring', () => ({ initMonitoring: h.initMonitoring }))

const PROD = 'www.ascomply.com'

async function load(hostname: string) {
  vi.resetModules()
  vi.stubGlobal('window', { location: { hostname } })
  await import('./instrument')
}

beforeEach(() => {
  h.initMonitoring.mockClear()
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.doUnmock('./hubspot')
  vi.resetModules()
})

describe('instrument', () => {
  it('instrument_productionHostStartsLandingMonitoring', async () => {
    await load(PROD)
    const { landingRouteName } = await import('./route')

    expect(h.initMonitoring.mock.calls).toEqual([['landing', { routeName: landingRouteName }]])
    expect((h.initMonitoring.mock.calls[0]?.[1] as { routeName: unknown }).routeName).toBe(landingRouteName)
  })

  it.each([
    'landing-pr-301.up.railway.app',
    'ascomply.com',
    'www.ascomply.com.attacker.example',
    'wwwascomply.com',
    'localhost',
    '',
  ])('instrument_otherHostsStayDark %j', async (hostname) => {
    await load(hostname)
    expect(h.initMonitoring).not.toHaveBeenCalled()

    // Control: the same load path on the production host does start monitoring.
    await load(PROD)
    expect(h.initMonitoring).toHaveBeenCalledTimes(1)
  })

  it('instrument_reusesTheLandingAllowlist', async () => {
    const spy = vi.fn()
    vi.resetModules()
    vi.doMock('./hubspot', async () => {
      const actual = await vi.importActual<typeof import('./hubspot')>('./hubspot')
      return {
        ...actual,
        isProductionHost: (...args: Parameters<typeof actual.isProductionHost>) => {
          spy(...args)
          return actual.isProductionHost(...args)
        },
      }
    })
    vi.stubGlobal('window', { location: { hostname: PROD } })
    await import('./instrument')

    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy.mock.calls[0]?.[0]).toBe(PROD)
  })

  describe('consent', () => {
    it.each([
      ['absent', null],
      ['denied', '{"analytics":false,"ts":"","v":1}'],
      ['granted', '{"analytics":true,"ts":"","v":1}'],
    ])('instrument_consentDoesNotGate %s', async (_label, stored) => {
      const getItem = vi.fn((k: string) => (k === 'asc_consent' ? stored : null))
      vi.stubGlobal('localStorage', { getItem })
      vi.stubGlobal('sessionStorage', { getItem })
      await load(PROD)

      expect(h.initMonitoring).toHaveBeenCalledTimes(1)
      expect(getItem).toHaveBeenCalledTimes(0)
    })

    it('instrument_consentDoesNotOpenTheGate', async () => {
      vi.stubGlobal('localStorage', { getItem: () => '{"analytics":true,"ts":"","v":1}' })
      await load('landing-pr-301.up.railway.app')
      expect(h.initMonitoring).not.toHaveBeenCalled()

      // Control: the production host with the same storage does start monitoring.
      await load(PROD)
      expect(h.initMonitoring).toHaveBeenCalledTimes(1)
    })
  })
})
