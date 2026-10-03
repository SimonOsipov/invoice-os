// @vitest-environment jsdom
// SSR markup, because jsdom drops shorthands. SSR runs no effect and no IntersectionObserver exists, so the card is in its end state.
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { GLYPHS } from '../icons'
import { Problem } from './Problem'

const V168 = "Nigeria's e-invoicing transition means businesses will need more than PDF invoices and manual approval chains."
const V169 =
  'Many companies still manage invoices across accounting software, Excel files, emails, ERP systems and manual approvals. This creates errors, delays and audit risk.'
const V759 = [
  ['Missing or incomplete tax fields', 'FAIL'],
  ['Incorrect customer or supplier information', 'FAIL'],
  ['Duplicate invoice numbers', 'FAIL'],
  ['Weak approval workflows', 'WARN'],
  ['Poor audit trails', 'WARN'],
  ['Manual invoice corrections', 'WARN'],
  ['Disconnected accounting and ERP systems', 'WARN'],
  ['Lack of readiness for structured e-invoicing requirements', 'FAIL'],
] as const
const V763_DONE = '4 errors · 4 warnings · Not ready to submit'

function ssr(): DocumentFragment {
  const tpl = document.createElement('template')
  tpl.innerHTML = renderToStaticMarkup(createElement(Problem))
  return tpl.content
}

const styleOf = (el: Element | null | undefined): Record<string, string> => {
  const out: Record<string, string> = {}
  for (const decl of (el?.getAttribute('style') ?? '').split(/;(?![^(]*\))/)) {
    const i = decl.indexOf(':')
    if (i > 0) out[decl.slice(0, i).trim()] = decl.slice(i + 1).trim()
  }
  return out
}
const norm = (s: string | null | undefined) => (s ?? '').replace(/\s+/g, ' ').trim()
const paths = (el: Element | null | undefined) => [...(el?.querySelectorAll('path') ?? [])].map((p) => p.getAttribute('d'))

describe('PR-01 #problem is the cream Section without the symptom grid', () => {
  it('is section.ds-section.band-cream whose first child is .container, with no .ios-grid', () => {
    const frag = ssr()
    const sections = frag.querySelectorAll('#problem')
    expect(sections.length).toBe(1)
    const section = sections[0]
    expect(section.tagName).toBe('SECTION')
    expect([...section.classList]).toEqual(['ds-section', 'band-cream'])
    expect(section.firstElementChild?.classList.contains('container')).toBe(true)
    expect(section.querySelectorAll('.ios-grid').length).toBe(0)
  })
})

describe('PR-02 the eyebrow and the two-line H2', () => {
  it('reads THE PROBLEM in the light eyebrow and a t-h2 whose second line is a teal span', () => {
    const frag = ssr()
    const eyebrows = frag.querySelectorAll('.t-eyebrow')
    expect(eyebrows.length).toBe(1)
    expect(eyebrows[0].classList.contains('ds-eyebrow--dark')).toBe(false)
    expect(norm(eyebrows[0].textContent)).toBe('THE PROBLEM')

    const h2s = frag.querySelectorAll('h2')
    expect(h2s.length).toBe(1)
    const h2 = h2s[0]
    expect(h2.classList.contains('t-h2')).toBe(true)
    expect(norm(h2.textContent)).toBe('The invoice is becoming a compliance checkpoint. Is your business ready?')
    const last = h2.lastElementChild
    expect(last?.tagName).toBe('SPAN')
    expect(norm(last?.textContent)).toBe('Is your business ready?')
    expect(styleOf(last).color).toBe('var(--teal)')
  })
})

describe('PR-03 the two V168-169 paragraphs', () => {
  it('holds exactly two .t-body paragraphs with the V168 and V169 text', () => {
    const ps = [...ssr().querySelectorAll('#problem p')]
    expect(ps.length).toBe(2)
    ps.forEach((p) => expect(p.classList.contains('t-body')).toBe(true))
    expect(ps.map((p) => norm(p.textContent))).toEqual([V168, V169])
  })
})

describe('PR-04 the card frame and header', () => {
  it('is one .card with the card shadow, a file-search tile, the title, the Sample data tag, 8 rows, a footer and a text button', () => {
    const cards = ssr().querySelectorAll('#problem-check')
    expect(cards.length).toBe(1)
    const card = cards[0]
    expect(card.classList.contains('card')).toBe(true)
    expect(styleOf(card)['box-shadow']).toBe('var(--shadow-card)')

    const tiles = card.querySelectorAll('.ds-icontile--primary')
    expect(tiles.length).toBe(1)
    expect(paths(tiles[0])).toEqual([...GLYPHS['file-search']])
    expect(norm(card.querySelector('.t-card-title')?.textContent)).toBe('Invoice check')
    expect(norm(card.querySelector('.t-meta')?.textContent)).toBe('Sample data')
    expect(card.querySelectorAll('[data-check="row"]').length).toBe(8)
    expect(card.querySelectorAll('[data-check="footer"]').length).toBe(1)
    const btns = card.querySelectorAll('button.ds-btn.ds-btn--text')
    expect(btns.length).toBe(1)
    expect(norm(btns[0].textContent)).toBe('Run check again')
  })
})

describe('PR-05 the SSR render is the end state', () => {
  it('shows the V759 labels, tags, glyphs and colours, and the V763 footer in destructive', () => {
    const card = ssr().querySelector('#problem-check')
    const rows = [...(card?.querySelectorAll('[data-check="row"]') ?? [])]
    expect(rows.length).toBe(8)
    expect(rows.map((r) => norm(r.children[0].textContent))).toEqual(V759.map(([label]) => label))
    expect(rows.map((r) => norm(r.children[1].textContent))).toEqual(V759.map(([, tag]) => tag))
    rows.forEach((r, i) => {
      const tag = r.children[1]
      const fail = V759[i][1] === 'FAIL'
      expect(paths(tag), `row ${i} glyph`).toEqual([...(fail ? GLYPHS.x : GLYPHS['triangle-alert'])])
      expect(styleOf(tag).color, `row ${i} colour`).toBe(fail ? 'var(--destructive)' : 'var(--status-progress-fg)')
    })
    const footer = card?.querySelector('[data-check="footer"]')
    expect(norm(footer?.textContent)).toBe(V763_DONE)
    expect(styleOf(footer).color).toBe('var(--destructive)')
  })
})
