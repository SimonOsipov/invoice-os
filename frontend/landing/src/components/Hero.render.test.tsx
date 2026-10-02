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

    // jsdom drops the padding-block shorthand, so the style attribute is read from the SSR markup.
    const ssr = document.createElement('template')
    ssr.innerHTML = renderToStaticMarkup(createElement(Hero, { onBookDemo: () => undefined }))
    expect(ssr.content.querySelector('#top')?.getAttribute('style')).toBe('padding-block:clamp(48px, 6vw, 80px) 0')
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
