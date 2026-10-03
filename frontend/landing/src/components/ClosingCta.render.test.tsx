// @vitest-environment jsdom
// Closing CTA, SSR markup (jsdom drops shorthands). V446-457 is retyped below; fill and size are the topology job's.
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { ClosingCta } from './ClosingCta'

const V_EYEBROW = "LET'S MAKE COMPLIANCE CLEARER."
const V_H2 = 'A better way to move forward.'
const V452 = 'See how ASComply fits your invoices, your systems and your team.'
const V_FONT = 'clamp(35px, 4vw, 52px)'

function ssr(): DocumentFragment {
  const tpl = document.createElement('template')
  tpl.innerHTML = renderToStaticMarkup(createElement(ClosingCta, { onBookDemo: () => undefined }))
  return tpl.content
}

const norm = (s: string | null | undefined) => (s ?? '').replace(/\s+/g, ' ').trim()
/** Text with each <br> read as a space. */
function spaced(el: Element): string {
  const copy = el.cloneNode(true) as Element
  copy.querySelectorAll('br').forEach((br) => br.replaceWith(' '))
  return norm(copy.textContent)
}
const style = (el: Element, prop: string) => new RegExp(`(?:^|;)\\s*${prop}:\\s*([^;]+)`, 'i').exec(el.getAttribute('style') ?? '')?.[1].trim()

describe('CL-01 the closing panel sits in an id-less off-white section', () => {
  it('is one section.ds-section.band-cream with no id, holding exactly one [data-closing]', () => {
    const frag = ssr()
    const sections = frag.querySelectorAll('section')
    expect(sections, 'one section').toHaveLength(1)
    expect([...sections[0].classList]).toEqual(['ds-section', 'band-cream'])
    expect(sections[0].hasAttribute('id'), 'an id would add a scroll-spy and NV-12 target').toBe(false)
    expect(sections[0].querySelectorAll('[data-closing]'), 'one panel').toHaveLength(1)
  })
})

describe('CL-02 the closing copy is V’s', () => {
  it('has the eyebrow, the h2 with its clamp size and one break, V452, and one primary Book a demo button', () => {
    const panel = ssr().querySelector('[data-closing]')
    expect(panel, 'control: the panel rendered').not.toBeNull()

    const eyebrows = panel!.querySelectorAll('.t-eyebrow')
    expect(eyebrows, 'one eyebrow').toHaveLength(1)
    expect(norm(eyebrows[0].textContent)).toBe(V_EYEBROW)

    const h2s = panel!.querySelectorAll('h2.t-h2')
    expect(h2s, 'one h2').toHaveLength(1)
    expect(h2s[0].querySelectorAll('br'), 'one line break').toHaveLength(1)
    expect(spaced(h2s[0])).toBe(V_H2)
    expect(style(h2s[0], 'font-size')).toBe(V_FONT)

    const ps = panel!.querySelectorAll('p')
    expect(ps, 'one paragraph').toHaveLength(1)
    expect(norm(ps[0].textContent)).toBe(V452)

    const buttons = panel!.querySelectorAll('button')
    expect(buttons, 'one button').toHaveLength(1)
    expect(norm(buttons[0].textContent)).toBe('Book a demo')
    expect(buttons[0].classList.contains('ds-btn--primary')).toBe(true)
  })
})

describe('CL-03 the mark is hidden from assistive technology', () => {
  it('has one .cta-mark, aria-hidden="true", reading "All clear." over one break', () => {
    const marks = ssr().querySelectorAll('.cta-mark')
    expect(marks, 'one mark').toHaveLength(1)
    expect(marks[0].getAttribute('aria-hidden')).toBe('true')
    expect(marks[0].querySelectorAll('br'), 'one break').toHaveLength(1)
    expect(spaced(marks[0])).toBe('All clear.')
    expect(marks[0].closest('[data-closing]'), 'the mark is inside the panel').not.toBeNull()
  })
})
