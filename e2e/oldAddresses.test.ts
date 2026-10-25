import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { libraryPathsOf, newAddressOf, oldUrls } from './smoke/oldAddresses'

describe('oldAddresses', () => {
  it.each([
    ['https://blog.ascomply.com/', 'https://www.ascomply.com/blog/'],
    ['https://blog.ascomply.com/x', 'https://www.ascomply.com/blog/x'],
    ['https://blog.ascomply.com/blog', 'https://www.ascomply.com/blog'],
    ['https://blog.ascomply.com/blog/x', 'https://www.ascomply.com/blog/x'],
    ['https://blog.ascomply.com/subscribe/confirm?token=t', 'https://www.ascomply.com/blog/subscribe/confirm?token=t'],
    ['http://library.ascomply.com/invoices', 'https://www.ascomply.com/library/invoices'],
    ['https://library.ascomply.com/library/rules', 'https://www.ascomply.com/library/rules'],
  ])('newAddressOf maps %s', (from, to) => {
    expect(newAddressOf(from)).toBe(to)
  })

  it('a lookalike is not a base', () => {
    expect(newAddressOf('https://blog.ascomply.com/blogger')).toBe('https://www.ascomply.com/blog/blogger')
  })

  it('a host that is not an old host throws', () => {
    expect(() => newAddressOf('https://www.ascomply.com/blog/')).toThrow()
  })

  it('oldUrls strips the base from sitemap locs', () => {
    const urls = oldUrls(['https://www.ascomply.com/blog/', 'https://www.ascomply.com/blog/p'], ['/rules'])
    expect(urls).toContain('https://blog.ascomply.com/')
    expect(urls).toContain('https://blog.ascomply.com/p')
    expect(urls).toContain('https://blog.ascomply.com/blog/p')
    expect(urls).toContain('https://library.ascomply.com/rules')
    expect(urls).toContain('http://blog.ascomply.com/')
    expect(urls).toContain('http://library.ascomply.com/')
  })

  it('every url oldUrls builds maps to a www address', () => {
    for (const url of oldUrls(['https://www.ascomply.com/blog/p'], ['/a/b'])) {
      expect(newAddressOf(url).startsWith('https://www.ascomply.com/')).toBe(true)
    }
  })

  it('libraryPathsOf lists each group and each feature under its group', () => {
    const src = "{ id: 'invoices', n: '01', name: 'x' }\n F('import-files', 'T')\n { id: 'rules', n: '02' }\n F('validate', 'V')"
    expect(libraryPathsOf(src)).toEqual(['/invoices', '/invoices/import-files', '/rules', '/rules/validate'])
  })

  it('oldUrls asks every fixed blog and library address', () => {
    const urls = oldUrls(['https://www.ascomply.com/blog/', 'https://www.ascomply.com/blog/p'], ['/rules'])
    for (const path of ['/', '/robots.txt', '/sitemap.xml', '/subscribe', '/blog/']) expect(urls).toContain(`https://blog.ascomply.com${path}`)
    expect(urls).toContain(`https://blog.ascomply.com/subscribe/confirm?token=${'t'.repeat(43)}`)
    for (const path of ['/', '/library/invoices']) expect(urls).toContain(`https://library.ascomply.com${path}`)
  })

  it('libraryPathsOf reads every group and feature of the real content.ts', () => {
    const src = readFileSync(new URL('../frontend/library/src/content.ts', import.meta.url), 'utf8')
    const paths = libraryPathsOf(src)
    expect(paths.filter((p) => p.split('/').length === 2)).toHaveLength(11)
    expect(paths.filter((p) => p.split('/').length === 3)).toHaveLength(26)
    expect(paths).toContain('/invoices/import-files')
  })
})
