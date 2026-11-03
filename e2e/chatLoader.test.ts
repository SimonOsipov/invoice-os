import { afterEach, describe, expect, it, vi } from 'vitest'

import { chatLoaderUrl } from './smoke/landingConsent'

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('chatLoaderUrl', () => {
  it('chatLoaderUrl builds the loader URL from HUBSPOT_PORTAL_ID', () => {
    vi.stubEnv('HUBSPOT_PORTAL_ID', ' 1234567 ')

    expect(chatLoaderUrl()).toBe('https://js-eu1.hs-scripts.com/1234567.js')
  })

  it('chatLoaderUrl fails loud without HUBSPOT_PORTAL_ID', () => {
    vi.stubEnv('HUBSPOT_PORTAL_ID', '')

    expect(() => chatLoaderUrl()).toThrow(/HUBSPOT_PORTAL_ID is not set/)
  })

  it('landingConsent imports with no portal id', async () => {
    vi.stubEnv('HUBSPOT_PORTAL_ID', '')
    vi.resetModules()

    const mod = await import('./smoke/landingConsent')

    expect(mod.HUBSPOT_CHAT_DOMAINS).toBeDefined()
  })
})
