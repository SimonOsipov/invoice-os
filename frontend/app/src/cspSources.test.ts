import { afterEach, describe, expect, it, vi } from 'vitest'
import { tagSrc } from '../../landing/src/analytics'
import { chatScriptUrl, HUBSPOT_CHAT_DOMAINS } from '../../landing/src/hubspotChat'
import { APP_CSP_SOURCES } from './cspSources'

afterEach(() => vi.unstubAllEnvs())

describe('APP_CSP_SOURCES', () => {
  it('appSources_coverTheGoogleTag', () => {
    expect(APP_CSP_SOURCES.script).toContain(new URL(tagSrc('G-TEST')).origin)
  })

  it('appSources_coverTheChatLoaderAndEveryChatDomain', () => {
    vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '1')
    for (const d of HUBSPOT_CHAT_DOMAINS) expect(APP_CSP_SOURCES.script).toContain(`https://*.${d}`)
    const host = new URL(chatScriptUrl() ?? '').hostname
    expect(HUBSPOT_CHAT_DOMAINS.some((d) => host.endsWith(`.${d}`))).toBe(true)
  })

  it('appSources_connectNamesAnalyticsAndChat', () => {
    for (const s of ['https://*.google-analytics.com', 'https://*.analytics.google.com', 'https://*.hubspot.com', 'wss://*.hubspot.com'])
      expect(APP_CSP_SOURCES.connect).toContain(s)
    expect(APP_CSP_SOURCES.object).toEqual(['blob:'])
  })
})
