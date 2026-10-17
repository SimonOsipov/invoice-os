import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { BlogList } from './BlogList'

describe('BlogList', () => {
  it('the Blog screen has its eyebrow and one h1', () => {
    const html = renderToStaticMarkup(<BlogList sub="" />)
    expect(html.match(/class="eyebrow"[^>]*>07 — CONTENT</g)).toHaveLength(1)
    expect(html.match(/<h1[^>]*>Blog<\/h1>/g)).toHaveLength(1)
    expect(html.match(/<h1/g)).toHaveLength(1)
  })

  it('the header row keeps the prototype margins inside the screen pad', () => {
    const html = renderToStaticMarkup(<BlogList sub="3 posts" />)
    expect(html.startsWith('<div class="ops-screen-pad">')).toBe(true)
    expect(html).toMatch(/<div style="[^"]*margin-bottom:20px[^"]*">/)
    expect(html).toMatch(/class="eyebrow" style="margin-bottom:10px"/)
    expect(html).toMatch(/<h1 style="[^"]*font-size:28px[^"]*letter-spacing:-0\.04em[^"]*">Blog<\/h1>/)
    expect(html).toMatch(/class="mono"[^>]*>3 posts</)
  })
})
