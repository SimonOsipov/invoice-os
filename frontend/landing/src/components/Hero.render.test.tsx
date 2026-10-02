// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The v2 dark hero band (RESKIN-02-02): frame, eyebrow, h1, CTAs, notes, bottom strip.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { GLYPHS } from '../icons'
import { Hero } from './Hero'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const textOf = (el: Element | null): string => (el?.textContent ?? '').replace(/\s+/g, ' ').trim()

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.restoreAllMocks()
})

// jsdom drops shorthands such as padding-block, so inline styles are read from the SSR markup.
function ssrTop(): HTMLElement {
  const ssr = document.createElement('template')
  ssr.innerHTML = renderToStaticMarkup(createElement(Hero, { onBookDemo: () => undefined }))
  return ssr.content.querySelector<HTMLElement>('#top')!
}

const styleOf = (el: Element | null | undefined): Record<string, string> => {
  const out: Record<string, string> = {}
  for (const decl of (el?.getAttribute('style') ?? '').split(';')) {
    const i = decl.indexOf(':')
    if (i > 0) out[decl.slice(0, i).trim()] = decl.slice(i + 1).trim()
  }
  return out
}

async function mountHero(onBookDemo: () => void = () => undefined): Promise<HTMLElement> {
  await act(async () => {
    root.render(createElement(Hero, { onBookDemo }))
  })
  const top = document.querySelector<HTMLElement>('#top')
  expect(top, 'expected #top to render').not.toBeNull()
  return top!
}

describe('HB-01 #top is the dark Section', () => {
  it('is a section.ds-section.band-dark with the clamp padding-block, its first child a .container', async () => {
    const top = await mountHero()
    expect(top.tagName).toBe('SECTION')
    expect([...top.classList]).toEqual(['ds-section', 'band-dark'])
    expect(top.firstElementChild?.classList.contains('container'), 'first child is .container').toBe(true)

    expect(ssrTop().getAttribute('style')).toBe('padding-block:clamp(48px, 6vw, 80px) 0')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('HB-02 the dark eyebrow', () => {
  it('holds exactly one .t-eyebrow, dark, with the v2 text', async () => {
    const top = await mountHero()
    const eyebrows = top.querySelectorAll('.t-eyebrow')
    expect(eyebrows.length).toBe(1)
    expect(eyebrows[0].classList.contains('ds-eyebrow--dark')).toBe(true)
    expect(textOf(eyebrows[0])).toBe('E-INVOICING SOLUTION FOR NIGERIA AND AFRICA')
  })
})

describe('HB-03 the h1 and its highlighted line', () => {
  it('is one h1.t-h1 reading the headline, ending in span.t-hl "keeps up."', async () => {
    await mountHero()
    const h1s = document.querySelectorAll('h1')
    expect(h1s.length).toBe(1)
    const h1 = h1s[0]
    expect(h1.classList.contains('t-h1')).toBe(true)
    expect(textOf(h1)).toBe('Africa moves. Compliance keeps up.')

    const hl = h1.querySelectorAll('span.t-hl')
    expect(hl.length).toBe(1)
    expect(textOf(hl[0])).toBe('keeps up.')
    expect(h1.lastChild).toBe(hl[0])

    // D-16: the collapsing textOf cannot see a missing space after a <br />.
    expect(h1.querySelectorAll('br').length).toBe(2)
    expect(h1.textContent).toBe('Africa moves. Compliance keeps up.')
  })
})

describe('HB-05 the accent CTA books a demo', () => {
  it('"Book a demo" is a lg accent button that calls onBookDemo once', async () => {
    const onBookDemo = vi.fn()
    const top = await mountHero(onBookDemo)
    const matches = [...top.querySelectorAll('button')].filter((b) => textOf(b) === 'Book a demo')
    expect(matches.length, 'expected exactly one "Book a demo" button in #top').toBe(1)
    const button = matches[0]
    expect(button.className).toBe('ds-btn ds-btn--accent ds-btn--lg')
    expect(button.getAttribute('type')).toBe('button')

    expect(onBookDemo).not.toHaveBeenCalled()
    await act(async () => {
      button.click()
    })
    expect(onBookDemo).toHaveBeenCalledTimes(1)
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('HB-06 Explore the platform is an anchor to #platform', () => {
  it('is a ghostDark <a href="#platform"> and does not book a demo', async () => {
    const onBookDemo = vi.fn()
    const top = await mountHero(onBookDemo)
    const matches = [...top.querySelectorAll('a, button')].filter((el) => textOf(el) === 'Explore the platform')
    expect(matches.length, 'expected exactly one "Explore the platform" control').toBe(1)
    const el = matches[0]
    expect(el.tagName).toBe('A')
    expect(el.getAttribute('href')).toBe('#platform')
    expect(el.classList.contains('ds-btn--ghostDark')).toBe(true)
    // D-40: no hrefPrefix on either hero anchor.
    expect([...top.querySelectorAll('a')].map((a) => a.getAttribute('href'))).toEqual(['#platform', '#platform'])

    await act(async () => {
      ;(el as HTMLElement).click()
    })
    expect(onBookDemo).not.toHaveBeenCalled()
  })
})

describe('HB-07 the hero has no sign-in control', () => {
  it('#top holds exactly one button, "Book a demo"', async () => {
    const top = await mountHero()
    const buttons = [...top.querySelectorAll('button')]
    expect(buttons.map(textOf)).toEqual(['Book a demo'])
    const controls = [...top.querySelectorAll('a, button, [role="button"]')].map(textOf)
    expect(controls, 'control: the CTAs are in the scan').toContain('Book a demo')
    expect(controls.filter((t) => /log\s?in|sign\s?in/i.test(t))).toEqual([])
  })
})

describe('HB-08 the two notes, each with a check', () => {
  it('reads the V856 notes in order, each holding the check glyph', async () => {
    const top = await mountHero()
    const notes = ['Your systems, connected', 'Audit-ready invoice records']
    const items = [...top.querySelectorAll('div')].filter((d) => notes.includes(textOf(d)))
    expect(items.map(textOf)).toEqual(notes)
    for (const item of items) {
      const svgs = item.querySelectorAll('svg')
      expect(svgs.length, `"${textOf(item)}" holds one svg`).toBe(1)
      expect([...svgs[0].querySelectorAll('path')].map((p) => p.getAttribute('d'))).toEqual([...GLYPHS.check])
    }
  })
})

describe('HB-09 the bottom strip', () => {
  it('the last child of .container holds the tagline span and the "Explore the platform ↓" anchor', async () => {
    const top = await mountHero()
    const container = top.querySelector('.container')
    expect(container, 'expected #top .container').not.toBeNull()
    const strip = container!.lastElementChild!
    expect(strip, 'the strip follows the split').not.toBe(container!.firstElementChild)
    expect(textOf(strip.querySelector('span'))).toBe('Local expertise. Pan-African ambition.')
    const anchor = strip.querySelector('a[href="#platform"]')
    expect(anchor, 'expected the strip anchor').not.toBeNull()
    expect(textOf(anchor)).toBe('Explore the platform ↓')
  })
})

describe('HB-10 the timeline and the floating tag are gone', () => {
  it('#top text holds none of the retired strings, and does hold the v2 headline', async () => {
    const top = await mountHero()
    const text = textOf(top)
    const RETIRED = ['Large taxpayers', 'Medium taxpayers', 'Small / SME', 'NRS REFERENCE', 'CSID · pending transmit']
    expect(RETIRED.filter((s) => text.includes(s))).toEqual([])
    expect(text).toContain('Africa moves.')
  })
})

describe('HB-15 the band lays out as the v2 prototype', () => {
  it('the frame, the text column order and every inline style match the prototype values', () => {
    const top = ssrTop()
    const container = top.querySelector('.container')!
    expect([...container.children].map((c) => c.className || c.tagName)).toEqual(['split', 'DIV'])
    const [split, strip] = [...container.children]
    expect(styleOf(split)).toEqual({ 'grid-template-columns': 'minmax(0, 1fr) minmax(0, 1.05fr)', gap: '64px', 'align-items': 'start' })

    const [textCol, cardCol] = [...split.children]
    expect(styleOf(cardCol)).toEqual({ 'min-width': '0' })
    expect(styleOf(textCol)).toEqual({ display: 'grid', gap: '28px', 'justify-items': 'start' })
    expect([...textCol.children].map((c) => c.tagName)).toEqual(['SPAN', 'H1', 'P', 'DIV', 'DIV'])
    const [, h1, lead, ctas, notes] = [...textCol.children]

    expect(styleOf(h1)).toEqual({ margin: '0', color: 'var(--surface-foreground)' })
    expect(lead.className).toBe('t-lead')
    expect(styleOf(lead)).toEqual({ margin: '0', 'max-width': '480px', color: 'var(--surface-body)' })
    expect(styleOf(ctas)).toEqual({ display: 'flex', 'flex-wrap': 'wrap', 'align-items': 'center', gap: '16px 28px' })
    expect([...ctas.children].map(textOf)).toEqual(['Book a demo', 'Explore the platform'])
    expect(styleOf(notes)).toEqual({ display: 'flex', 'flex-wrap': 'wrap', gap: '12px 32px', 'margin-top': '4px' })

    expect(notes.children.length).toBe(2)
    for (const note of notes.children) {
      expect(styleOf(note)).toEqual({
        display: 'flex',
        'align-items': 'center',
        gap: '10px',
        'font-size': '14px',
        'font-weight': '600',
        color: 'var(--surface-body)',
      })
      expect(styleOf(note.firstElementChild)).toEqual({ display: 'inline-flex', color: 'var(--accent)' })
      const svg = note.querySelector('svg')!
      expect([svg.getAttribute('width'), svg.getAttribute('height'), svg.getAttribute('stroke-width')]).toEqual(['16', '16', '2'])
    }

    expect(styleOf(strip)).toEqual({
      'margin-top': '64px',
      padding: '28px 0',
      'border-top': '1px solid var(--on-dark-10)',
      display: 'flex',
      'flex-wrap': 'wrap',
      'justify-content': 'space-between',
      gap: '12px 24px',
      'font-size': '10px',
      'font-weight': '700',
      'letter-spacing': 'var(--tracking-eyebrow)',
      'text-transform': 'uppercase',
      color: 'var(--eyebrow-on-dark)',
    })
    expect(styleOf(strip.querySelector('a'))).toEqual({ color: 'var(--eyebrow-on-dark)' })
  })
})
