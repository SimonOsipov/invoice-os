// The footer's bottom row and Cookie choices control (LAND-05-04, re-targeted to the v2 footer in
// RESKIN-02-05). SSR-only, same idiom as Footer.render.test.tsx.
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { Footer } from './Footer'

function noop() {}

const LABEL = 'Cookie choices'
// ASCII sub-needle: sidesteps the © / · escaping question and occurs exactly once.
const COPYRIGHT = '2026 ASComply Africa Limited'

const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop }))

// Bounded at the copyright row: Connect is the LAST column, so an open-ended slice
// swallows the row the control lives in.
function connectSlice(markup: string): string {
  const start = markup.indexOf('>Connect<')
  expect(start, 'expected to find the Connect column heading').toBeGreaterThan(-1)
  const end = markup.indexOf(COPYRIGHT)
  expect(end, 'expected the copyright row to follow the Connect column').toBeGreaterThan(start)
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
// the opening tag alone.
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

const attrsOf = (tag: string): Record<string, string> =>
  Object.fromEntries([...tag.matchAll(/\s([^\s=>/]+)(?:="([^"]*)")?/g)].map((m) => [m[1], m[2] ?? '']))
const textOfHtml = (markup: string) => markup.replace(/<[^>]*>/g, '')

describe('Footer bottom row and Cookie choices control (LAND-05-04, FT-06 to FT-08)', () => {
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

  it('FT-06 / T4-2: the row keeps two direct children, the group holds Privacy policy then Cookie choices and nothing else', () => {
    const row = copyrightRowSlice(html)
    const children = directChildren(row)
    expect(children.length, 'the bottom row must stay a two-item space-between row').toBe(2)
    expect(textOfHtml(children[0]), 'the first child is the copyright string').toBe(
      '© 2026 ASComply Africa Limited · Lagos, Nigeria',
    )

    const group = children[1]
    const inner = directChildren(group)
    expect(inner.length, 'the group holds the privacy link and the control, nothing else').toBe(2)
    expect(inner[0], 'the first group child is the privacy link').toMatch(/^<a\b/)
    expect(attrsOf(openingTag(inner[0])).href).toBe('/privacy')
    expect(attrsOf(openingTag(inner[0])).class).toBe('a-link')
    expect(textOfHtml(inner[0])).toBe('Privacy policy')
    expect(inner[1], 'the second group child is the control').toMatch(/^<button\b/)
    expect(attrsOf(openingTag(inner[1])).class).toBe('a-link')
    expect(textOfHtml(inner[1])).toBe(LABEL)

    // Element count alone lets a text node in as a further anonymous flex item.
    expectNoStrayText(row, children)
    expectNoStrayText(group, inner)
  })

  it('FT-06 / AC-3: the row and the group are wrapping flex containers, the row space-between', () => {
    const row = copyrightRowSlice(html)
    const rowTag = openingTag(row)
    // Control needle: the tag reader sees the declarations that are there.
    expect(rowTag, 'control: the row carries no inline style at all').toContain('style=')
    expect(rowTag, 'the row stopped being a flex container').toContain('display:flex')
    expect(rowTag, 'the row stopped wrapping').toContain('flex-wrap:wrap')
    expect(rowTag, 'without space-between the two-child grouping buys nothing').toContain(
      'justify-content:space-between',
    )

    const groupTag = openingTag(directChildren(row)[1])
    expect(groupTag, 'control: the group carries no inline style at all').toContain('style=')
    // flex-wrap is INERT on a block box.
    expect(groupTag, 'flex-wrap:wrap is inert unless the group is a flex container').toContain('display:flex')
    expect(groupTag, 'the group would not wrap its second child, overflowing the row').toContain('flex-wrap:wrap')
  })

  it('AC-1/AC-7: the accessible name is the visible label, and nothing hides or unfocuses the control', () => {
    const control = buttonWithLabel(html, LABEL)
    const tag = openingTag(control)
    // Control needle: the scan can see an attribute that IS present.
    expect(tag, 'control: the control tag carries no attributes to scan').toContain('class=')

    // Every one of these overrides or removes the accessible name, or takes the control out
    // of the keyboard order, without touching the visible text.
    for (const attr of ['aria-label', 'aria-labelledby', 'title=', 'aria-hidden', 'tabindex', 'inert']) {
      expect(tag, `the control carries ${attr}`).not.toContain(attr)
    }

    // An ancestor can hide it just as completely as the control itself can.
    const row = copyrightRowSlice(html)
    expect(openingTag(row), 'the bottom row is hidden from assistive technology').not.toContain('aria-hidden')
    expect(openingTag(row), 'the bottom row is inert').not.toContain('inert')
    const groupTag = openingTag(directChildren(row)[1])
    expect(groupTag, 'the group hides the control from assistive technology').not.toContain('aria-hidden')
    expect(groupTag, 'the group makes the control unreachable').not.toContain('inert')
  })

  it('FT-07: focus order, the control follows Book a demo and Privacy policy and precedes nothing focusable', () => {
    const sibling = buttonWithLabel(html, 'Book a demo')
    const control = buttonWithLabel(html, LABEL)
    const privacy = html.match(/<a\b[^>]*>Privacy policy<\/a>/)?.[0]
    expect(privacy, 'control: the Privacy policy anchor is not on the page').toBeDefined()
    expect(html.indexOf(sibling), 'control: the sibling is not on the page').toBeGreaterThan(-1)
    expect(html.indexOf(control), 'the control moved ahead of Book a demo').toBeGreaterThan(html.indexOf(sibling))
    expect(html.indexOf(control), 'the control moved ahead of Privacy policy').toBeGreaterThan(html.indexOf(privacy!))
    // Nothing focusable may follow it inside the footer: it is the last stop.
    const after = html.slice(html.indexOf(control) + control.length)
    expect(after, 'a focusable element now follows the control in the footer').not.toMatch(/<(?:a|button|input)\b/)
  })

  it('FT-07 / T4-3: it is not a Connect control', () => {
    // Control needle first: without the control on the page the exclusion below is vacuous.
    expect(html, 'the control is not on the page at all').toContain(LABEL)
    const connect = connectSlice(html)
    const items = Array.from(connect.matchAll(/<(?:a|button)[^>]*>([^<]*)</g)).map((m) => m[1])
    expect(items.length).toBeGreaterThan(0)
    expect(items).toEqual(['Book a demo', 'Open the cockpit', 'Contact ASComply'])
    expect(connect, 'the control leaked into the Connect column').not.toContain(LABEL)
  })

  it('FT-07 / T4-4: never hidden, never disabled, the same contract as its Book a demo sibling', () => {
    const sibling = buttonWithLabel(html, 'Book a demo')
    expect(sibling, 'the sibling contract already broke').not.toMatch(/\bdisabled\b/)
    expect(sibling).not.toMatch(/\bhidden\b/)

    const control = buttonWithLabel(html, LABEL)
    expect(control, 'the control ships disabled beside always-enabled siblings').not.toMatch(/\bdisabled\b/)
    expect(control, 'the control ships hidden beside always-visible siblings').not.toMatch(/\bhidden\b/)
  })

  it('FT-08 / T4-5: Cookie choices and Privacy policy share one a-link style, with no inline colour', () => {
    // Control needle: the muted colour IS on this row, so the exclusion below is not vacuous.
    expect(openingTag(copyrightRowSlice(html)), 'control: the row no longer carries --muted-foreground').toContain(
      'var(--muted-foreground)',
    )
    const privacy = html.match(/<a\b[^>]*>Privacy policy<\/a>/)?.[0]
    expect(privacy, 'control: the Privacy policy anchor is not on the page').toBeDefined()
    for (const [name, tag] of [
      ['Privacy policy', openingTag(privacy!)],
      [LABEL, openingTag(buttonWithLabel(html, LABEL))],
    ] as const) {
      expect(attrsOf(tag).class, `${name} class`).toBe('a-link')
      expect(tag, `${name} carries an inline colour`).not.toMatch(/(?:^|[\s";])color:/)
      expect(tag, `${name} names --muted-foreground`).not.toContain('--muted-foreground')
      expect(tag, `${name} names --fg-3`).not.toContain('--fg-3')
    }
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
