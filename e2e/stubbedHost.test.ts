import { describe, expect, it } from 'vitest'
import { isStubbedHost } from './smoke/landingConsent'

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
    expect(isStubbedHost('not a url')).toBe(false)
  })
})
