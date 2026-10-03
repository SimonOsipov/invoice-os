// @vitest-environment jsdom
// Integrations band, SSR markup (jsdom drops shorthands). V822 and V381-392 are retyped below.
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { Integrations } from './Integrations'

const V_EYEBROW = 'INTEGRATIONS & PARTNERS'
const V_H2 = 'Your systems. A connected future.'
const V_HL = 'A connected future.'
const V_CTA = 'Discuss your integration →'
const V822 = [
  ['SAP', 'Enterprise resource planning'],
  ['ORACLE', 'Enterprise applications'],
  ['Microsoft', 'Dynamics 365'],
  ['QuickBooks', 'Business accounting'],
  ['sage', 'Accounting and ERP'],
  ['odoo', 'Business applications'],
] as const
// D-10: the glyph's four squares, in order.
const GLYPH_FILLS = ['var(--ink)', 'var(--teal)', 'var(--teal)', 'var(--ink)']

function ssr(): DocumentFragment {
  const tpl = document.createElement('template')
  tpl.innerHTML = renderToStaticMarkup(createElement(Integrations, { onBookDemo: () => undefined }))
  return tpl.content
}

const norm = (s: string | null | undefined) => (s ?? '').replace(/\s+/g, ' ').trim()
const bg = (el: Element) => /(?:^|;)\s*background:\s*([^;]+)/.exec(el.getAttribute('style') ?? '')?.[1].trim()
const stops = (root: Element) => [...root.querySelectorAll('a[href], button, input, select, textarea, [tabindex]')].map((e) => e.outerHTML)
const cards = (frag: DocumentFragment) => [...frag.querySelectorAll('[data-partner]')]
// The aria-hidden grid span with four child spans (D-10).
const glyphsIn = (card: Element) =>
  [...card.querySelectorAll('span[aria-hidden="true"]')].filter((s) => s.children.length === 4 && [...s.children].every((c) => c.localName === 'span'))

describe('IG-01 the band is #integrations on the peach band', () => {
  it('is one section.ds-section.band-peach with one h2 and one .cols6[data-partners]', () => {
    const frag = ssr()
    const sections = frag.querySelectorAll('#integrations')
    expect(sections, 'one #integrations').toHaveLength(1)
    expect(sections[0].localName).toBe('section')
    expect([...sections[0].classList]).toEqual(['ds-section', 'band-peach'])
    expect(sections[0].querySelectorAll('h2'), 'one h2').toHaveLength(1)
    const grids = sections[0].querySelectorAll('[data-partners]')
    expect(grids, 'one partner grid').toHaveLength(1)
    expect(grids[0].classList.contains('cols6')).toBe(true)
  })
})

describe('IG-02 the header copy is V’s', () => {
  it('reads the eyebrow and the two-line h2 whose second line is span.t-hl-peach', () => {
    const frag = ssr()
    expect(frag.querySelector('#integrations'), 'control: the band rendered').not.toBeNull()
    const eyebrows = frag.querySelectorAll('.t-eyebrow')
    expect(eyebrows, 'one eyebrow').toHaveLength(1)
    expect(norm(eyebrows[0].textContent)).toBe(V_EYEBROW)

    const h2 = frag.querySelector('h2')!
    h2.querySelectorAll('br').forEach((br) => br.replaceWith(' '))
    expect(norm(h2.textContent)).toBe(V_H2)
    const hl = h2.querySelectorAll('span.t-hl-peach')
    expect(hl, 'one highlight span').toHaveLength(1)
    expect(norm(hl[0].textContent)).toBe(V_HL)
  })
})

describe('IG-03 six partner cards in V822 order', () => {
  it('holds six .card partners with V’s wordmark and description, each with one progress badge', () => {
    const list = cards(ssr())
    expect(list, 'six partners').toHaveLength(V822.length)
    expect(list.every((c) => c.classList.contains('card')), 'each is a .card').toBe(true)
    const wordmark = (c: Element) => norm(c.firstElementChild?.lastElementChild?.textContent)
    expect(list.map(wordmark), 'wordmarks, in order').toEqual(V822.map(([mark]) => mark))
    expect(list.map((c) => norm(c.querySelector('.t-body-sm')?.textContent)), 'descriptions, in order').toEqual(V822.map(([, desc]) => desc))
    for (const [i, c] of list.entries()) {
      const badges = c.querySelectorAll('.ds-badge.ds-badge--progress')
      expect(badges, `${V822[i][0]}: one progress badge`).toHaveLength(1)
      expect(norm(badges[0].textContent), `${V822[i][0]}: badge text`).toBe('In progress')
    }
  })
})

describe('IG-04 only Microsoft has the four-square glyph', () => {
  it('renders the glyph on the Microsoft card alone, filled ink, teal, teal, ink', () => {
    const list = cards(ssr())
    expect(list, 'six partners').toHaveLength(V822.length)
    const glyphs = list.map((c) => glyphsIn(c))
    expect(glyphs.map((g) => g.length), 'glyphs per card').toEqual(V822.map(([mark]) => (mark === 'Microsoft' ? 1 : 0)))
    expect([...glyphs[2][0].children].map(bg), 'square fills, in order').toEqual(GLYPH_FILLS)
  })
})

describe('IG-05 the CTA is a text button on the peach rule', () => {
  it('has one button, with the V text, ds-btn--text and btn-on-peach', () => {
    const buttons = ssr().querySelectorAll('#integrations button')
    expect(buttons, 'one button in the band').toHaveLength(1)
    expect(norm(buttons[0].textContent)).toBe(V_CTA)
    expect(buttons[0].classList.contains('ds-btn--text')).toBe(true)
    expect(buttons[0].classList.contains('btn-on-peach')).toBe(true)
    expect(stops(ssr().querySelector('#integrations')!), 'the CTA is the only Tab stop').toEqual([buttons[0].outerHTML])
  })
})
