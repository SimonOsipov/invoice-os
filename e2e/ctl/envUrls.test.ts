import { describe, expect, it } from 'vitest'
import { resolveEnv } from './envUrls'

describe('resolveEnv', () => {
  it('names a PR preview by its run.app services', () => {
    const r = resolveEnv('pr-12')
    expect(r.urls.GATEWAY_URL).toBe('https://gateway-pr-12-944095690109.africa-south1.run.app')
    expect(r.urls.SUPPORT_CONSOLE_URL).toBe('https://support-console-pr-12-944095690109.africa-south1.run.app')
    expect(Object.keys(r.urls)).toHaveLength(7)
  })

  it('names a preview\'s site addresses', () => {
    const r = resolveEnv('pr-12')
    expect(r.urls.LIBRARY_URL).toBe('https://library-pr-12-944095690109.africa-south1.run.app/library')
    expect(r.urls.BLOG_URL).toBe('https://blog-pr-12-944095690109.africa-south1.run.app/blog')
    expect(r.urls.LANDING_URL).toBe('https://landing-pr-12-944095690109.africa-south1.run.app')
    expect(r.urls.APP_URL).toBe('https://app-pr-12-944095690109.africa-south1.run.app')
  })

  it('names production by its hosts', () => {
    const r = resolveEnv('production')
    expect(r.urls.GATEWAY_URL).toBe('https://api.ascomply.com')
    expect(r.urls.LANDING_URL).toBe('https://www.ascomply.com')
    expect(r.urls.SUPPORT_CONSOLE_URL).toBe('https://sup.ascomply.com')
  })

  it('names production\'s site addresses', () => {
    const r = resolveEnv('production')
    expect(r.urls.LIBRARY_URL).toBe('https://www.ascomply.com/library')
    expect(r.urls.BLOG_URL).toBe('https://www.ascomply.com/blog')
    expect(r.urls.LANDING_URL).toBe('https://www.ascomply.com')
  })
})
