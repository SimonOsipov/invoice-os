// Same-origin privacy link, no stubs, prefix-blind Book a demo.
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { Footer } from './Footer'

function noop() {}

const COPYRIGHT_ROW = '2026 ASComply Africa Limited'

function connectSlice(html: string): string {
  const start = html.indexOf('>Connect<')
  expect(start, 'expected to find the Connect column heading').toBeGreaterThan(-1)
  const end = html.indexOf(COPYRIGHT_ROW)
  expect(end, 'expected the bottom row to follow the Connect column').toBeGreaterThan(start)
  return html.slice(start, end)
}

describe('Footer adversarial coverage', () => {
  it('FT-09: the privacy link is same-origin, no anchor is a stub, and Book a demo ignores the prefix', () => {
    const plain = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop }))
    const prefixed = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix: '/' }))

    for (const html of [plain, prefixed]) {
      const tag = html.match(/<a[^>]*href="\/privacy"[^>]*>/)
      expect(tag, 'expected to find the /privacy anchor tag').not.toBeNull()
      expect(tag![0]).not.toMatch(/target=/)
      expect(tag![0]).not.toMatch(/rel=/)
      expect(html, 'a bare # stub is back').not.toContain('href="#"')
      expect(html).not.toContain('href="//privacy"')
    }

    const buttonOf = (html: string) => connectSlice(html).match(/<button[^>]*>Book a demo<\/button>/)?.[0]
    expect(buttonOf(plain)).toBeTruthy()
    expect(buttonOf(plain)).toBe(buttonOf(prefixed))
  })
})
