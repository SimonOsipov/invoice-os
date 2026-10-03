// @vitest-environment jsdom
// SSR markup, because jsdom drops shorthands and applies no CSS.
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import App from '../App'
import { GLYPHS } from '../icons'
import { Platform } from './Platform'
import { PLATFORM_CAPABILITIES, PLATFORM_COPY, PLATFORM_SIDE_COPY } from './Platform.copy.test.util'

function ssr(): DocumentFragment {
  const tpl = document.createElement('template')
  tpl.innerHTML = renderToStaticMarkup(createElement(Platform, { onBookDemo: () => undefined }))
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
/** Nearest ancestor-or-self that sets an inline `color`. */
const colorOf = (el: Element | null): string | undefined => {
  for (let n = el; n; n = n.parentElement) {
    const c = styleOf(n).color
    if (c) return c
  }
  return undefined
}
const paths = (el: Element | null | undefined) => [...(el?.querySelectorAll('path') ?? [])].map((p) => p.getAttribute('d'))

describe('PL-01 the cream Platform section and its header', () => {
  it('is section#platform.ds-section.band-cream with the light eyebrow, the teal-ended H2 and the side copy', () => {
    const frag = ssr()
    const sections = frag.querySelectorAll('#platform')
    expect(sections.length).toBe(1)
    expect(sections[0].tagName).toBe('SECTION')
    expect([...sections[0].classList]).toEqual(['ds-section', 'band-cream'])

    const eyebrows = frag.querySelectorAll('.t-eyebrow')
    expect(eyebrows.length).toBe(1)
    expect(eyebrows[0].classList.contains('ds-eyebrow--dark')).toBe(false)
    expect(norm(eyebrows[0].textContent)).toBe('THE ASCOMPLY PLATFORM')

    const h2s = frag.querySelectorAll('h2')
    expect(h2s.length).toBe(1)
    const h2 = h2s[0]
    expect(h2.classList.contains('t-h2')).toBe(true)
    expect(norm(h2.textContent)).toBe('From invoice chaos to complete clarity.')
    expect(h2.querySelectorAll('br').length).toBe(1)
    const last = h2.lastElementChild
    expect(last?.tagName).toBe('SPAN')
    expect(norm(last?.textContent)).toBe('to complete clarity.')
    expect(styleOf(last).color).toBe('var(--teal)')

    const side = [...frag.querySelectorAll('p.t-body')].filter((p) => norm(p.textContent) === PLATFORM_SIDE_COPY)
    expect(side.length).toBe(1)
    expect(styleOf(side[0])['max-width']).toBe('300px')
    const header = side[0].parentElement
    expect(header?.contains(h2), 'the side copy shares the header row with the H2').toBe(true)
    expect(header?.contains(eyebrows[0])).toBe(true)
    expect(styleOf(header)).toMatchObject({
      display: 'flex',
      'flex-wrap': 'wrap',
      'justify-content': 'space-between',
      'align-items': 'flex-end',
      gap: '24px 48px',
      'margin-bottom': '48px',
    })
  })
})

describe('PL-02 one named tablist with three icon tabs', () => {
  it('has one tablist inside .ds-tabs.a-tabs, named, with the three V tabs, icons and Validate selected', () => {
    const frag = ssr()
    const lists = frag.querySelectorAll('[role=tablist]')
    expect(lists.length).toBe(1)
    const root = lists[0].closest('.ds-tabs')
    expect(root, 'the tablist sits in .ds-tabs').not.toBeNull()
    expect(root?.classList.contains('a-tabs')).toBe(true)
    expect(lists[0].getAttribute('aria-label')).toBe('ASComply platform steps')

    const tabs = [...lists[0].querySelectorAll('button[role=tab]')]
    expect(tabs.length).toBe(3)
    expect(tabs.map((t) => t.textContent)).toEqual(PLATFORM_COPY.map((c) => c.tabText))
    tabs.forEach((tab, i) => {
      expect(paths(tab), `tab ${i} icon`).toEqual([...GLYPHS[PLATFORM_COPY[i].tabIcon]])
      expect(tab.querySelector('svg')?.getAttribute('width'), `tab ${i} icon size`).toBe('18')
    })
    expect(tabs.map((t) => t.getAttribute('aria-selected'))).toEqual(['true', 'false', 'false'])
  })
})

describe('PL-06 the three capabilities sit after the tabs', () => {
  it('renders .cols3 after .ds-tabs with three items: 40px primary tile, card title and body-sm', () => {
    const frag = ssr()
    const tabs = frag.querySelector('.ds-tabs')
    const grids = frag.querySelectorAll('.cols3')
    expect(tabs, 'control: the tabs rendered').not.toBeNull()
    expect(grids.length).toBe(1)
    expect(tabs?.contains(grids[0]), 'the grid is outside the tabs').toBe(false)
    expect(Boolean(tabs!.compareDocumentPosition(grids[0]) & Node.DOCUMENT_POSITION_FOLLOWING), 'the grid follows the tabs').toBe(true)
    expect(styleOf(grids[0])).toMatchObject({ gap: '24px 48px', 'margin-top': '48px' })

    const items = [...grids[0].querySelectorAll(':scope > div')]
    expect(items.length).toBe(3)
    items.forEach((item, i) => {
      const [icon, title, body] = PLATFORM_CAPABILITIES[i]
      const tiles = item.querySelectorAll('.ds-icontile--primary')
      expect(tiles.length, `item ${i} tile`).toBe(1)
      expect(styleOf(tiles[0]), `item ${i} tile size`).toMatchObject({ width: '40px', height: '40px' })
      expect(paths(tiles[0]), `item ${i} glyph`).toEqual([...GLYPHS[icon]])
      expect(norm(item.querySelector('.t-card-title')?.textContent), `item ${i} title`).toBe(title)
      const bodies = item.querySelectorAll('p.t-body-sm')
      expect(bodies.length).toBe(1)
      expect(norm(bodies[0].textContent), `item ${i} body`).toBe(body)
      expect(styleOf(item)['border-top'], `item ${i} rule`).toBe('1px solid var(--tab-border)')
    })
  })
})

describe('PL-07 the panel padding follows the prototype', () => {
  it('sets padding: clamp(24px, 4vw, 48px) on the tabpanel', () => {
    const panels = ssr().querySelectorAll('[role=tabpanel]')
    expect(panels.length).toBe(1)
    expect(styleOf(panels[0]).padding).toBe('clamp(24px, 4vw, 48px)')
  })
})

describe('PL-03r the Validate result card keeps the card frame', () => {
  it('is a .card with the card shadow and 22px 24px padding holding the 28px shield and four 17px check rows', () => {
    const panel = ssr().querySelector('[role=tabpanel]')
    const cards = panel?.querySelectorAll('.card') ?? []
    expect(cards.length).toBe(1)
    expect(styleOf(cards[0])).toMatchObject({ 'box-shadow': 'var(--shadow-card)', padding: '22px 24px' })

    const tiles = cards[0].querySelectorAll('.ds-icontile')
    expect(tiles.length).toBe(1)
    expect(tiles[0].classList.contains('ds-icontile--primary')).toBe(true)
    expect(styleOf(tiles[0])).toMatchObject({ width: '36px', height: '36px' })

    const shield = [...cards[0].querySelectorAll('svg')].filter((s) => s.getAttribute('width') === '28')
    expect(shield.length).toBe(1)
    expect(paths(shield[0])).toEqual([...GLYPHS['shield-check']])
    expect(colorOf(shield[0])).toBe('var(--teal)')

    const ticks = [...cards[0].querySelectorAll('svg')].filter((s) => s.getAttribute('width') === '17')
    expect(ticks.length).toBe(4)
    ticks.forEach((t) => {
      expect(paths(t)).toEqual([...GLYPHS['circle-check']])
      expect(colorOf(t)).toBe('var(--teal)')
    })
  })
})

describe('PL-09 the page places #platform after #solution and before #how', () => {
  it('lists the three section ids consecutively, with #platform once', () => {
    const tpl = document.createElement('template')
    tpl.innerHTML = renderToStaticMarkup(createElement(App))
    const ids = [...tpl.content.querySelectorAll('section[id]')].map((s) => s.id)
    expect(ids.filter((id) => id === 'platform')).toHaveLength(1)
    const i = ids.indexOf('solution')
    expect(i, 'control: #solution rendered').toBeGreaterThan(-1)
    expect(ids.slice(i, i + 3)).toEqual(['solution', 'platform', 'how'])
  })

  it('the rendered page makes no cryptographic claim, and the control page does render the Platform copy', () => {
    const tpl = document.createElement('template')
    tpl.innerHTML = renderToStaticMarkup(createElement(App))
    const text = norm(tpl.content.textContent)
    expect(text, 'control: the Platform band rendered').toContain('THE ASCOMPLY PLATFORM')
    expect(text).not.toMatch(/cryptograph/i)
  })
})
