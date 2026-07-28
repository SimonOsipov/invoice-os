// Pins the aria-current attribute contract (LAND-01-03, AC #5) mechanically, without a DOM.
// React 19's setValueForAttribute passes booleans straight to setAttribute for any aria-
// prefixed name, so `aria-current={active}` (active: boolean) would render the literal
// string aria-current="false" on every inactive link instead of leaving the attribute off.
// Nav.tsx guards this with `aria-current={active ? 'true' : undefined}`. Confirmed against
// the actual react-dom@19.2.7 installed here (not assumed from changelog prose).
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { Nav } from './Nav'

describe('React aria-* boolean-passthrough mechanism (justifies the ternary in Nav.tsx)', () => {
  it('undefined is omitted, the string "true" is literal, but a raw boolean false leaks as aria-current="false"', () => {
    const html = renderToStaticMarkup(
      createElement(
        'div',
        null,
        createElement('a', { 'aria-current': undefined, id: 'omitted' }),
        createElement('a', { 'aria-current': 'true', id: 'literal' }),
        createElement('a', { 'aria-current': false, id: 'trap' }),
      ),
    )
    expect(html).not.toContain('aria-current="undefined"')
    expect(html).toContain('aria-current="true" id="literal"')
    // This is the trap Nav.tsx's ternary avoids: a raw boolean false is NOT dropped.
    expect(html).toContain('aria-current="false" id="trap"')
  })
})

describe('Nav SSR (no scroll-spy effect runs under renderToStaticMarkup, so activeHref stays its initial null)', () => {
  it('every link is inactive on first render and none of them carry aria-current at all — not "false"', () => {
    // If Nav.tsx used the boolean form (`aria-current={active}`) instead of the ternary,
    // this render would emit aria-current="false" on all six links, since useState's
    // initial value (null) makes `l.href === activeHref` false for every link and SSR
    // never runs the useEffect that could later flip one of them true.
    const html = renderToStaticMarkup(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {} }))
    expect(html).not.toMatch(/aria-current/)
  })

  it('the nav landmark carries aria-label="Primary"', () => {
    const html = renderToStaticMarkup(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {} }))
    expect(html).toContain('<nav aria-label="Primary" class="ios-hide-mobile"')
  })
})
