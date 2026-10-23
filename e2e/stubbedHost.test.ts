import { describe, expect, it } from 'vitest'
import { HUBSPOT_CHAT_DOMAINS, isStubbedHost } from './smoke/landingConsent'
import { HUBSPOT_CHAT_DOMAINS as LANDING_DOMAINS } from '../frontend/landing/src/hubspotChat'

describe('isStubbedHost', () => {
  it('stubs the GA tag host', () => {
    expect(isStubbedHost('https://www.googletagmanager.com/gtag/js?id=G-X')).toBe(true)
  })

  it('stubs GA collection hosts, regional included', () => {
    expect(isStubbedHost('https://www.google-analytics.com/g/collect')).toBe(true)
    expect(isStubbedHost('https://region1.google-analytics.com/g/collect')).toBe(true)
  })

  it('stubs Sentry hosts', () => {
    expect(isStubbedHost('https://o1.ingest.de.sentry.io/api/1/envelope/')).toBe(true)
  })

  it('leaves the app, fonts and a Sentry lookalike alone, and an unparsable URL', () => {
    expect(isStubbedHost('https://www.ascomply.com/')).toBe(false)
    expect(isStubbedHost('https://fonts.googleapis.com/css2')).toBe(false)
    expect(isStubbedHost('https://evil.example/?x=sentry.io')).toBe(false)
    expect(isStubbedHost('https://evilgoogle-analytics.com/g/collect')).toBe(false)
    expect(isStubbedHost('not a url')).toBe(false)
  })

  it('stubs every HubSpot chat domain', () => {
    expect(HUBSPOT_CHAT_DOMAINS.length).toBeGreaterThan(0)
    for (const d of HUBSPOT_CHAT_DOMAINS) {
      expect(isStubbedHost(`https://x.${d}/`), d).toBe(true)
      expect(isStubbedHost(`https://${d}/`), d).toBe(true)
    }
  })

  it('leaves a HubSpot lookalike alone', () => {
    expect(isStubbedHost('https://evilhubspot.com/')).toBe(false)
    expect(isStubbedHost('https://evil.example/?x=hs-scripts.com')).toBe(false)
    expect(isStubbedHost('https://www.ascomply.com/')).toBe(false)
  })

  it('keeps the retyped HubSpot domain list equal to the landing constant', () => {
    expect([...HUBSPOT_CHAT_DOMAINS]).toEqual([...LANDING_DOMAINS])
  })
})
