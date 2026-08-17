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

// The attributes are the assertion surface for the a11y specs, so they get read from
// the opening tag alone — a whole-element scan matches the version span's attributes too.
function openingTag(elementHtml: string): string {
  const at = elementHtml.indexOf('>')
  expect(at, 'no opening tag in this markup').toBeGreaterThan(-1)
  return elementHtml.slice(0, at + 1)
}

// directChildren counts ELEMENTS. A bare text node is invisible to it and is still an
// anonymous flex item, so three-way space-between ships with T4-2 green. Concatenating
// the children back must reproduce the element byte for byte.
function expectNoStrayText(elementHtml: string, children: string[]): void {
  let cursor = openingTag(elementHtml).length
  for (const [i, child] of children.entries()) {
    expect(elementHtml.slice(cursor, cursor + child.length), `stray content before direct child ${i}`).toBe(child)
    cursor += child.length
  }
  expect(elementHtml.slice(cursor), 'stray content after the last direct child').toMatch(/^<\//)
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

    // Element count alone lets a text node in as a third anonymous flex item.
    expectNoStrayText(row, children)
    expectNoStrayText(wrapper, inner)
  })

  it('AC-2/AC-3: the grouping MECHANISM is present, not only the markup shape', () => {
    const row = copyrightRowSlice(html)
    const rowTag = openingTag(row)
    // Control needle: the tag reader sees the declarations that are there.
    expect(rowTag, 'control: the row carries no inline style at all').toContain('style=')
    expect(rowTag, 'the row stopped being a flex container').toContain('display:flex')
    expect(rowTag, 'without space-between the two-child grouping buys nothing').toContain(
      'justify-content:space-between',
    )

    const wrapperTag = openingTag(directChildren(row)[1])
    expect(wrapperTag, 'control: the wrapper carries no inline style at all').toContain('style=')
    // flex-wrap is INERT on a block box: the AC-3 string check below passes on it.
    expect(wrapperTag, 'flex-wrap:wrap is inert unless the wrapper is a flex container').toContain('display:flex')
    expect(wrapperTag).toContain('flex-wrap:wrap')
  })

  it('AC-1/AC-7: the accessible name is the visible label, and nothing hides or unfocuses the control', () => {
    const control = buttonWithLabel(html, LABEL)
    const tag = openingTag(control)
    // Control needle: the scan can see an attribute that IS present.
    expect(tag, 'control: the control tag carries no attributes to scan').toContain('class=')

    // Every one of these overrides or removes the accessible name AC #1 pins, or takes
    // the control out of the keyboard order, without touching the visible text.
    for (const attr of ['aria-label', 'aria-labelledby', 'title=', 'aria-hidden', 'tabindex', 'inert']) {
      expect(tag, `the control carries ${attr}`).not.toContain(attr)
    }

    // An ancestor can hide it just as completely as the control itself can.
    const row = copyrightRowSlice(html)
    expect(openingTag(row), 'the copyright row is hidden from assistive technology').not.toContain('aria-hidden')
    expect(openingTag(row), 'the copyright row is inert').not.toContain('inert')
    const wrapperTag = openingTag(directChildren(row)[1])
    expect(wrapperTag, 'the wrapper hides the control from assistive technology').not.toContain('aria-hidden')
    expect(wrapperTag, 'the wrapper makes the control unreachable').not.toContain('inert')
  })

  it('AC-7: focus order — the control follows Book a demo and precedes nothing focusable', () => {
    const sibling = buttonWithLabel(html, 'Book a demo')
    const control = buttonWithLabel(html, LABEL)
    expect(html.indexOf(sibling), 'control: the sibling is not on the page').toBeGreaterThan(-1)
    expect(
      html.indexOf(control),
      'the control moved ahead of the Company column, changing the footer tab order',
    ).toBeGreaterThan(html.indexOf(sibling))
    // Nothing focusable may follow it inside the footer: it is the last stop.
    const after = html.slice(html.indexOf(control) + control.length)
    expect(after, 'a focusable element now follows the control in the footer').not.toMatch(/<(?:a|button|input)\b/)
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
