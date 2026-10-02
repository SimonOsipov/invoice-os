// @vitest-environment jsdom
// The sage v2 audience strip (RESKIN-02-04). SSR markup, because jsdom drops shorthands such as padding-block and gap.
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { TrustStrip } from './TrustStrip'

const V858 = ['Finance teams', 'Accounting firms', 'Growing businesses', 'Fintech', 'Technology partners']

function ssr(): DocumentFragment {
  const tpl = document.createElement('template')
  tpl.innerHTML = renderToStaticMarkup(createElement(TrustStrip))
  return tpl.content
}

const styleOf = (el: Element | null | undefined): Record<string, string> => {
  const out: Record<string, string> = {}
  for (const decl of (el?.getAttribute('style') ?? '').split(';')) {
    const i = decl.indexOf(':')
    if (i > 0) out[decl.slice(0, i).trim()] = decl.slice(i + 1).trim()
  }
  return out
}

function parts() {
  const frag = ssr()
  const strip = frag.querySelector<HTMLElement>('[data-strip="audience"]')
  expect(strip, 'control: the strip rendered').not.toBeNull()
  const [label, row] = [...strip!.children]
  return { frag, strip: strip!, label, row }
}

describe('TS-01 the strip is the sage band with 30px block padding', () => {
  it('renders one section.ds-section.band-sage with padding-block 30px, holding exactly one [data-strip="audience"]', () => {
    const frag = ssr()
    const bands = frag.querySelectorAll('section')
    expect(bands.length).toBe(1)
    const band = bands[0]
    expect([...band.classList]).toEqual(['ds-section', 'band-sage'])
    expect(band.getAttribute('style')).toBe('padding-block:30px')
    const strips = frag.querySelectorAll('[data-strip="audience"]')
    expect(strips.length).toBe(1)
    expect(band.contains(strips[0]), 'the strip sits inside the band').toBe(true)
  })
})

describe('TS-02 the label', () => {
  it('is the first of two div children of the strip, with the v2 text and eyebrow styling', () => {
    const { strip, label, row } = parts()
    expect(strip.children.length).toBe(2)
    expect([label.tagName, row.tagName]).toEqual(['DIV', 'DIV'])
    expect(label.textContent).toBe('Built for the way your business works')
    expect(styleOf(label)).toEqual({
      flex: 'none',
      'font-size': '10px',
      'font-weight': '700',
      'letter-spacing': 'var(--tracking-eyebrow)',
      'text-transform': 'uppercase',
      color: 'var(--primary)',
      'white-space': 'nowrap',
    })
  })
})

describe('TS-03 the five segments', () => {
  it('holds exactly five spans, all in the segment row, in V858 order, none in the label', () => {
    const { strip, label, row } = parts()
    const spans = [...strip.querySelectorAll('span')]
    expect(spans.length).toBe(V858.length)
    expect(spans.map((s) => s.textContent)).toEqual(V858)
    expect(spans.every((s) => s.parentElement === row), 'every span is a direct child of the segment row').toBe(true)
    expect(label.querySelectorAll('span').length).toBe(0)
  })

  it('styles every segment 16px/700 with the card tracking, ink colour and no wrap', () => {
    const { row } = parts()
    const spans = [...row.querySelectorAll('span')]
    expect(spans.length).toBeGreaterThan(0)
    for (const s of spans) {
      expect(styleOf(s), s.textContent ?? '').toEqual({
        'font-size': '16px',
        'font-weight': '700',
        'letter-spacing': 'var(--tracking-card)',
        color: 'var(--ink)',
        'white-space': 'nowrap',
      })
    }
  })
})

describe('TS-04 the strip layout wraps with the V152-160 gaps', () => {
  it('the strip and the segment row are wrapping flex boxes with the prototype flex, gaps and alignment', () => {
    const { strip, row } = parts()
    expect(styleOf(strip)).toEqual({
      display: 'flex',
      'flex-wrap': 'wrap',
      'align-items': 'center',
      gap: '16px 48px',
    })
    expect(styleOf(row)).toEqual({
      flex: '1 1 520px',
      display: 'flex',
      'flex-wrap': 'wrap',
      'justify-content': 'space-between',
      gap: '12px 32px',
    })
  })
})
