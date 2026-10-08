import { describe, expect, it } from 'vitest'
import { isProductionHost } from './smoke/sentryHost'

describe('isProductionHost', () => {
  it('names every production custom domain, library included', () => {
    for (const host of ['www', 'app', 'ops', 'library']) {
      expect(isProductionHost(`https://${host}.ascomply.com/`), host).toBe(true)
    }
    expect(isProductionHost('https://LIBRARY.ascomply.com')).toBe(true)
  })

  it('refuses a PR environment host and a lookalike', () => {
    expect(isProductionHost('https://library-pr-365.up.railway.app')).toBe(false)
    expect(isProductionHost('https://library.ascomply.com.evil.example')).toBe(false)
    expect(isProductionHost('https://sup.ascomply.com')).toBe(false)
  })
})
