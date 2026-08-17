// RED specs (task-563, LAND-05-04) — pin the footer Cookie choices control before
// Footer.tsx grows it. SSR-only, same idiom as Footer.render.test.tsx.
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { Footer } from './Footer'

function noop() {}

const LABEL = 'Cookie choices'
// ASCII sub-needle: sidesteps the © / · escaping question and occurs exactly once.
const COPYRIGHT = '2026 ASCOMPLY AFRICA'

const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop }))

// Bounded at the copyright row: Company is the LAST column, so an open-ended slice
// swallows the row the control lives in.
function companySlice(markup: string): string {
  const start = markup.indexOf('>Company<')
  expect(start, 'expected to find the Company column heading').toBeGreaterThan(-1)
  const end = markup.indexOf(COPYRIGHT)
  expect(end, 'expected the copyright row to follow the Company column').toBeGreaterThan(start)
  return markup.slice(start, end)
}

function copyrightRowSlice(markup: string): string {
  const idx = markup.indexOf(COPYRIGHT)
  expect(idx, 'expected to find the copyright row').toBeGreaterThan(-1)
  const open = markup.lastIndexOf('<div', idx)
  expect(open, 'expected an opening div for the copyright row').toBeGreaterThan(-1)
  return markup.slice(open)
}

// Depth walk, not a regex: "exactly two direct children" is the whole grouping claim
// and a flat count cannot see nesting.
function directChildren(elementHtml: string): string[] {
  const TAG = /<(\/?)([a-zA-Z][a-zA-Z0-9-]*)([^>]*)>/g
  const children: string[] = []
  let depth = 0
  let openAt = -1
  let first = true
  for (const m of elementHtml.matchAll(TAG)) {
    const at = m.index ?? 0
    if (first) {
      first = false
      continue
    }
    if (m[1] === '/') {
      if (depth === 0) break
      depth -= 1
      if (depth === 0) children.push(elementHtml.slice(openAt, at + m[0].length))
      continue
    }
    if (m[3].trimEnd().endsWith('/')) {
      if (depth === 0) children.push(m[0])
      continue
    }
    if (depth === 0) openAt = at
    depth += 1
  }
  return children
}

function buttonWithLabel(markup: string, label: string): string {
  const m = markup.match(new RegExp(`<button[^>]*>${label}</button>`))
  expect(m, `expected a <button> labelled "${label}"`).not.toBeNull()
  return m![0]
}

describe('Footer Cookie choices control (LAND-05-04)', () => {
  it('control: the render resolved and the walker sees the copyright row', () => {
    expect(html.length).toBeGreaterThan(0)
    const row = copyrightRowSlice(html)
    expect(row).toContain(COPYRIGHT)
    expect(directChildren(row).length).toBeGreaterThan(0)
  })

  it('T4-1: it is a button whose accessible name is exactly "Cookie choices"', () => {
    expect(html).toMatch(/<button[^>]*>Cookie choices</)
    expect(html, 'the control is an anchor, not a button').not.toMatch(/<a[^>]*>Cookie choices</)
  })

  it('T4-2: it shares one wrapper with the version string, and the row keeps two direct children', () => {
    const row = copyrightRowSlice(html)
    const children = directChildren(row)
    expect(children.length, 'the copyright row must stay a two-item space-between row').toBe(2)
    expect(children[0], 'the first child is the copyright string').toContain(COPYRIGHT)

    const wrapper = children[1]
    expect(wrapper, 'the control is not grouped with the version string').toContain(LABEL)
    expect(wrapper, 'the version string left the wrapper').toContain('v 1.0')

    const inner = directChildren(wrapper)
    expect(inner.length, 'the wrapper holds the control and the version string, nothing else').toBe(2)
    expect(inner.join(''), 'the wrapper direct children are not the control and the version string').toContain(LABEL)
    expect(inner.join('')).toContain('v 1.0')
  })

  it('T4-3: it is not a fourth Company link', () => {
    // Control needle first: without the control on the page the exclusion below is vacuous.
    expect(html, 'the control is not on the page at all').toContain(LABEL)
    const company = companySlice(html)
    const items = Array.from(company.matchAll(/<(?:a|button)[^>]*>([^<]*)</g)).map((m) => m[1])
    expect(items.length).toBeGreaterThan(0)
    expect(items).toEqual(['Book a demo', 'Security', 'Status', 'Privacy &amp; cookies'])
    expect(company, 'the control leaked into the Company column').not.toContain(LABEL)
  })

  it('T4-4: never hidden, never disabled — the same contract as its Book a demo sibling', () => {
    const sibling = buttonWithLabel(html, 'Book a demo')
    expect(sibling, 'the sibling contract already broke').not.toMatch(/\bdisabled\b/)
    expect(sibling).not.toMatch(/\bhidden\b/)

    const control = buttonWithLabel(html, LABEL)
    expect(control, 'the control ships disabled beside always-enabled siblings').not.toMatch(/\bdisabled\b/)
    expect(control, 'the control ships hidden beside always-visible siblings').not.toMatch(/\bhidden\b/)
  })

  it('T4-5: it renders var(--primary), never --fg-3 / --muted-foreground', () => {
    // Control needle: --fg-3 IS in this row, on both spans, so the exclusion is not vacuous.
    expect(copyrightRowSlice(html), 'control: the row spans no longer carry --fg-3').toContain('var(--fg-3)')
    const control = buttonWithLabel(html, LABEL)
    expect(control).toContain('var(--primary)')
    expect(control).not.toContain('var(--fg-3)')
    expect(control).not.toContain('var(--muted-foreground)')
  })

  it('AC-3: the wrapper wraps, keeping the break point the row has today', () => {
    const row = copyrightRowSlice(html)
    expect(row, 'control: the outer row already wraps').toContain('flex-wrap:wrap')
    const children = directChildren(row)
    expect(children.length).toBe(2)
    expect(children[1], 'a non-wrapping wrapper overflows into App.tsx overflow-x: clip, silently').toContain(
      'flex-wrap:wrap',
    )
  })

  it('T4-9: with no onCookieChoices the render does not throw and still emits the control', () => {
    let markup = ''
    expect(() => {
      markup = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop }))
    }, 'the default handler is not a noop').not.toThrow()
    expect(markup.length).toBeGreaterThan(0)
    expect(markup, 'the control is conditional on the optional prop').toMatch(/<button[^>]*>Cookie choices</)
  })
})
