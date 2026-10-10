// SSR contract of the v2 header: frame tokens, Logo lockup, nav list, closed burger.
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { GLYPHS } from '../icons'
import { NAV_LINKS, Nav } from './Nav'

const noop = () => {}
const render = (props: { hrefPrefix?: string } = {}) =>
  renderToStaticMarkup(createElement(Nav, { onSignIn: noop, onBookDemo: noop, ...props }))

type Link = { label: string; href: string }

// The v2 file's nav list (V851), retyped.
const V2_NAV: Link[] = [
  { label: 'The problem', href: '#problem' },
  { label: 'The solution', href: '#solution' },
  { label: 'Platform', href: '#platform' },
  { label: "Who it's for", href: '#solutions' },
  { label: 'Integrations', href: '#integrations' },
]

function isOrderedSubsequence(list: readonly Link[], of: readonly Link[]): boolean {
  let at = 0
  for (const item of list) {
    while (at < of.length && !(of[at].label === item.label && of[at].href === item.href)) at++
    if (at === of.length) return false
    at++
  }
  return true
}

const decode = (s: string) => s.replace(/&#x27;/g, "'").replace(/&amp;/g, '&')

function attrsOf(openTag: string): Record<string, string> {
  return Object.fromEntries([...openTag.matchAll(/\s([^\s=>/]+)(?:="([^"]*)")?/g)].map((m) => [m[1], m[2] ?? '']))
}

function primaryNavLinks(html: string): Link[] {
  const nav = /<nav\b[^>]*aria-label="Primary"[^>]*>([\s\S]*?)<\/nav>/.exec(html)
  expect(nav, 'expected <nav aria-label="Primary">').not.toBeNull()
  return [...nav![1].matchAll(/<a\b([^>]*)>([\s\S]*?)<\/a>/g)].map((m) => ({
    label: decode(m[2].replace(/<[^>]*>/g, '')),
    href: attrsOf(m[1]).href,
  }))
}

describe('HD-01 the header frame uses the v2 header tokens', () => {
  it('the <header> style holds the sticky frame, the three header tokens and the 1px border', () => {
    const open = /<header\b[^>]*>/.exec(render())?.[0]
    expect(open, 'expected a <header> element').toBeDefined()
    const style = attrsOf(open!).style
    expect(style, 'the <header> has no inline style').toBeTruthy()
    const decls = new Map(style.split(';').map((d) => [d.slice(0, d.indexOf(':')), d.slice(d.indexOf(':') + 1)]))

    expect(decls.get('position'), 'position').toBe('sticky')
    expect(decls.get('top'), 'top').toBe('0')
    expect(decls.get('height'), 'height').toBe('var(--header-h)')
    expect(decls.get('background'), 'background').toBe('var(--header-bg)')
    expect(decls.get('backdrop-filter'), 'backdrop-filter').toBe('blur(var(--header-blur))')
    expect(decls.get('border-bottom'), 'border-bottom').toBe('1px solid var(--header-border)')
    expect(style, 'no raw oklch colour').not.toContain('oklch(')
    expect(style, 'no calc() height').not.toContain('calc(')
  })
})

describe('HD-02 the lockup is the DS Logo linking to #top', () => {
  it.each([
    ['', '#top'],
    ['/', '/#top'],
  ])('with hrefPrefix %j the first <a> is the Logo and its href is %s', (hrefPrefix, href) => {
    const m = /<a\b([^>]*)>([\s\S]*?)<\/a>/.exec(render(hrefPrefix ? { hrefPrefix } : {}))
    expect(m, 'expected an <a> in the header').not.toBeNull()
    const attrs = attrsOf(m![1])
    expect(attrs['aria-label'], 'lockup aria-label').toBe('ASComply Africa')
    expect(attrs.href, 'lockup href').toBe(href)
    expect(m![2], 'lockup holds the DS Logo').toContain('class="ds-logo"')
    const img = /<img\b[^>]*>/.exec(m![2])?.[0]
    expect(img, 'lockup holds the mark <img>').toBeDefined()
    expect(attrsOf(img!).width, 'mark width').toBe('32')
  })
})

describe('HD-03 NAV_LINKS is an in-order subsequence of V851 and renders in order', () => {
  it('the list is non-empty, a subsequence of V2_NAV, and the Primary nav renders exactly it', () => {
    expect(isOrderedSubsequence(V2_NAV, V2_NAV), 'control: V2_NAV is its own subsequence').toBe(true)
    expect(isOrderedSubsequence([V2_NAV[0], V2_NAV[3]], V2_NAV), 'control: a gap-skipping pick is in order').toBe(true)
    expect(
      isOrderedSubsequence([V2_NAV[1], V2_NAV[0]], V2_NAV),
      'control: [#solution, #problem] is out of order',
    ).toBe(false)
    expect(
      isOrderedSubsequence([{ label: 'The Problem', href: '#problem' }], V2_NAV),
      'control: a label-case change is not in V851',
    ).toBe(false)

    expect(NAV_LINKS.length, 'NAV_LINKS is empty').toBeGreaterThanOrEqual(1)
    expect(
      isOrderedSubsequence(
        NAV_LINKS.map((l) => ({ label: l.label, href: l.href })),
        V2_NAV,
      ),
      `NAV_LINKS is not an ordered subsequence of V851: ${JSON.stringify(NAV_LINKS)}`,
    ).toBe(true)
    expect(primaryNavLinks(render())).toEqual(NAV_LINKS.map((l) => ({ label: l.label, href: l.href })))
  })
})

describe('HD-06 the closed burger names a menu that is not rendered', () => {
  it('button.a-burger has the Menu label, aria-expanded false, a dangling aria-controls and the menu glyph', () => {
    const html = render()
    const tags = [...html.matchAll(/<button\b[^>]*>/g)].filter((m) =>
      (attrsOf(m[0]).class ?? '').split(/\s+/).includes('a-burger'),
    )
    expect(tags.length, 'expected exactly one button.a-burger').toBe(1)
    const attrs = attrsOf(tags[0][0])
    expect(attrs['aria-label'], 'burger aria-label').toBe('Menu')
    expect(attrs['aria-expanded'], 'burger aria-expanded').toBe('false')
    expect(attrs['aria-controls'], 'burger aria-controls').toBeTruthy()
    expect(html, 'the controlled menu must not be in the closed markup').not.toContain(`id="${attrs['aria-controls']}"`)

    const inner = html.slice(tags[0].index!, html.indexOf('</button>', tags[0].index!))
    expect([...inner.matchAll(/<path d="([^"]*)"/g)].map((m) => m[1])).toEqual([...GLYPHS.menu])
  })
})

describe('NV-01 the nav reaches the five sections', () => {
  it("the nav reaches V851's five sections: NAV_LINKS is exactly them, and the Primary nav renders them", () => {
    const expected = V2_NAV.map((l) => [l.label, l.href])
    expect(expected, 'control: V2_NAV holds five entries').toHaveLength(5)
    expect(NAV_LINKS.map((l) => [l.label, l.href])).toEqual(expected)
    expect(primaryNavLinks(render()).map((l) => [l.label, l.href])).toEqual(expected)
    expect(primaryNavLinks(render({ hrefPrefix: '/' })).map((l) => [l.label, l.href])).toEqual(
      expected.map(([label, href]) => [label, `/${href}`]),
    )
  })
})

describe('HD-03b the Primary nav renders every entry in list order', () => {
  // Planted: the render must grow with the list, whatever the live list holds.
  const EXTRA: Link[] = [
    { label: 'Extra A', href: '#extra-a' },
    { label: 'Extra B', href: '#extra-b' },
  ]
  let before: number
  beforeEach(() => {
    before = NAV_LINKS.length
    NAV_LINKS.push(...EXTRA)
  })
  afterEach(() => void NAV_LINKS.splice(before))

  it.each([
    ['', ''],
    ['/', '/'],
  ])('with hrefPrefix %j the anchors are the prefixed NAV_LINKS pairs, in order, one per entry', (hrefPrefix, p) => {
    expect(NAV_LINKS.length, 'control: the fixture grew the list to seven').toBe(7)
    expect(primaryNavLinks(render(hrefPrefix ? { hrefPrefix } : {}))).toEqual(
      NAV_LINKS.map((l) => ({ label: l.label, href: `${p}${l.href}` })),
    )
  })

  it('NV-09 the grown list holds distinct hrefs and renders without a duplicate-key warning', () => {
    const hrefs = NAV_LINKS.map((l) => l.href)
    expect(hrefs.length, 'control: the fixture grew the list to seven').toBe(7)
    expect(new Set(hrefs).size, `duplicate href in ${JSON.stringify(hrefs)}`).toBe(hrefs.length)
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      expect(primaryNavLinks(render()).map((l) => l.href)).toEqual(hrefs)
      expect(spy.mock.calls).toEqual([])
    } finally {
      spy.mockRestore()
    }
  })
})

describe('the header action group', () => {
  it('the header action group carries .a-actions and no inline style', () => {
    const html = render()
    const at = html.indexOf('Book a demo')
    expect(at, 'expected a Book a demo button').toBeGreaterThan(-1)
    const parent = [...html.slice(0, at).matchAll(/<div\b[^>]*>/g)].at(-1)?.[0]
    expect(parent, 'expected an enclosing <div>').toBeDefined()
    const attrs = attrsOf(parent!)
    expect((attrs.class ?? '').split(/\s+/), 'class list').toContain('a-actions')
    expect(attrs, 'no inline style').not.toHaveProperty('style')
  })
})

describe('the Library link', () => {
  const LIB = 'https://lib.x'
  const withLib = () =>
    renderToStaticMarkup(createElement(Nav, { onSignIn: noop, onBookDemo: noop, libraryHref: LIB }))

  it('Nav_endsTheNavWithLibraryWhenSet', () => {
    const html = withLib()
    const nav = /<nav\b[^>]*aria-label="Primary"[^>]*>([\s\S]*?)<\/nav>/.exec(html)![1]
    const anchors = [...nav.matchAll(/<a\b([^>]*)>([\s\S]*?)<\/a>/g)]
    expect(anchors.map((m) => attrsOf(m[1]).href)).toEqual([...V2_NAV.map((l) => l.href), LIB])
    const last = anchors.at(-1)!
    const attrs = attrsOf(last[1])
    expect(last[2]).toBe('Library')
    expect(attrs.class).toBe('ios-nav-link')
    expect(attrs['aria-current']).toBeUndefined()
    expect(attrs.style).toContain('color:var(--ink)')
    expect(attrs.style).toContain('border-bottom:2px solid transparent')
  })
})
