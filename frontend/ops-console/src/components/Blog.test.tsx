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
})
