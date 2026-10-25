import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

vi.mock('../sitePaths', () => ({ LIBRARY_BASE: '/lib-x', BLOG_BASE: '/blog-x' }))

describe('Privacy quotes the base paths from sitePaths', () => {
  it('PB-01 the Library and Blog addresses follow the constants, not a literal', async () => {
    const { Privacy } = await import('./Privacy')
    const html = renderToStaticMarkup(createElement(Privacy))
    const hits = (s: string) => html.split(s).length - 1
    expect(hits('www.ascomply.com/lib-x'), 'control: the mocked base reaches the page').toBeGreaterThanOrEqual(4)
    expect(hits('www.ascomply.com/blog-x')).toBeGreaterThanOrEqual(4)
    expect(html).not.toContain('www.ascomply.com/library')
    expect(html).not.toContain('www.ascomply.com/blog<')
    expect(html).not.toContain('www.ascomply.com/blog ')
  })
})
