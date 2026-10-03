// @vitest-environment jsdom
// Coverage band, left column and panel shell: header copy, country switch, card, flags, legend (jsdom).
import { act, createElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { GLYPHS } from '../icons'
import { click, mountView, show, spyConsoleError, unmountView, type View } from './ds/dsDom.test.util'
import { Coverage } from './Coverage'

type Tone = 'success' | 'progress'
type VCountry = {
  name: string
  code: string
  pill: string
  pillTone: Tone
  title: string
  body: string
  context: string
  flowLabel: string
  steps: string[]
  disclaimer: string
  href: string
  linkLabel: string
}

// V783-785 COUNTRIES, retyped. countries.ts COVERAGE must equal it.
const V_COUNTRIES: Record<'NG' | 'KE' | 'ZA', VCountry> = {
  NG: {
    name: 'Nigeria',
    code: 'NG',
    pill: 'Launch market',
    pillTone: 'success',
    title: 'Our starting point. Your next step.',
    body: "Invoice validation, internal approvals and organised records, built around Nigeria's e-invoicing context.",
    context: 'NRS · Merchant Buyer Solution',
    flowLabel: 'INVOICE WORKFLOW',
    steps: ['Capture invoice data', 'Validate required fields', 'Approve internally', 'Submit to the NRS'],
    disclaimer: 'Custom integrations with your ERP, CRM or accounting system are available. Talk to our team about your setup.',
    href: 'https://einvoice.nrs.gov.ng/',
    linkLabel: 'NRS e-invoicing portal ↗',
  },
  KE: {
    name: 'Kenya',
    code: 'KE',
    pill: 'Planned expansion',
    pillTone: 'progress',
    title: 'A country workflow for Kenya.',
    body: "Our expansion plans follow Kenya's eTIMS environment, with country-specific invoice checks and review workflows.",
    context: 'KRA · eTIMS',
    flowLabel: 'PLANNED WORKFLOW',
    steps: ['Map invoice data', 'Check eTIMS requirements', 'Route exceptions for review', 'Prepare the country workflow'],
    disclaimer: 'Kenya is a planned market. Launch timing and production integration will be confirmed as the rollout develops.',
    href: 'https://www.kra.go.ke/',
    linkLabel: 'KRA eTIMS guidance ↗',
  },
  ZA: {
    name: 'South Africa',
    code: 'ZA',
    pill: 'Planned expansion',
    pillTone: 'progress',
    title: 'Prepare for what comes next.',
    body: "Our roadmap follows South Africa's evolving VAT landscape, including SARS proposals for a Digital VAT Model.",
    context: 'SARS · VAT modernisation',
    flowLabel: 'PLANNED WORKFLOW',
    steps: ['Follow SARS updates', 'Assess proposed changes', 'Review the impact on workflows', 'Prepare for confirmed rules'],
    disclaimer: 'South Africa is a planned market. The Digital VAT Model is a proposal under consultation, not a live ASComply connection.',
    href: 'https://www.sars.gov.za/',
    linkLabel: 'SARS Digital VAT consultation ↗',
  },
}
const ORDER = ['NG', 'KE', 'ZA'] as const

// V245's header paragraph.
const V_HEADER_PARAGRAPH =
  'Start with a focused launch. Build toward a connected African market, with workflows shaped around local requirements.'

let view: View
let consoleError: ReturnType<typeof spyConsoleError>

beforeEach(async () => {
  view = mountView()
  consoleError = spyConsoleError()
  await show(view, createElement(Coverage))
})

afterEach(() => {
  expect(consoleError, 'console.error was called').not.toHaveBeenCalled()
  unmountView(view)
  vi.restoreAllMocks()
})

const norm = (s: string | null | undefined) => (s ?? '').replace(/\s+/g, ' ').trim()
const section = () => view.container.querySelector('section')
const tabs = () => Array.from(view.container.querySelectorAll<HTMLButtonElement>('button[aria-pressed]'))
const pressedFlags = () => tabs().map((t) => t.getAttribute('aria-pressed'))
function tab(id: (typeof ORDER)[number]): HTMLButtonElement {
  expect(tabs(), 'three tab buttons').toHaveLength(3)
  return tabs()[ORDER.indexOf(id)]
}

/** Inline colour of the nearest ancestor-or-self of `el`, up to `stop`. */
function inlineColour(el: Element, stop: Element): string {
  for (let e: Element | null = el; e && e !== stop; e = e.parentElement) {
    const c = (e as HTMLElement).style.color
    if (c) return c
  }
  return ''
}

function readCard(id: (typeof ORDER)[number]) {
  const cards = view.container.querySelectorAll('[data-cov-card]')
  expect(cards, 'exactly one [data-cov-card]').toHaveLength(1)
  const card = cards[0]
  const badge = card.querySelector('.ds-badge')
  const steps = Array.from(card.querySelectorAll('.t-step'))
  expect(steps, 'the card holds two .t-step labels: Country context and the flow label').toHaveLength(2)
  const contextRow = steps[0].nextElementSibling
  const rows = Array.from(steps[1].nextElementSibling?.children ?? [])
  const links = Array.from(card.querySelectorAll('a'))
  expect(links, `the ${id} card holds exactly one link`).toHaveLength(1)
  return {
    badgeClass: badge?.className,
    badge: norm(badge?.textContent),
    dot: badge?.querySelector('.ds-badge-dot') !== null,
    code: norm(card.querySelector('.t-meta')?.textContent),
    title: norm(card.querySelector('h3.t-h3')?.textContent),
    body: norm(card.querySelector('p.t-body')?.textContent),
    contextLabel: norm(steps[0].textContent),
    context: norm(contextRow?.textContent),
    flowLabel: norm(steps[1].textContent),
    steps: rows.map((r) => norm(r.textContent)),
    disclaimer: norm(card.querySelector('p.t-caption')?.textContent),
    link: { text: norm(links[0].textContent), href: links[0].getAttribute('href'), target: links[0].getAttribute('target'), rel: links[0].getAttribute('rel') },
    text: norm(card.textContent),
  }
}

function expectedCard(id: (typeof ORDER)[number]) {
  const c = V_COUNTRIES[id]
  return {
    badgeClass: `ds-badge ds-badge--${c.pillTone}`,
    badge: c.pill,
    dot: true,
    code: `${c.code} / Africa`,
    title: c.title,
    body: c.body,
    contextLabel: 'Country context',
    context: c.context,
    flowLabel: c.flowLabel,
    steps: c.steps.map((t, i) => `0${i + 1}${t}`),
    disclaimer: c.disclaimer,
    link: { text: c.linkLabel, href: c.href, target: '_blank', rel: 'noopener noreferrer' },
  }
}

function expectCard(id: (typeof ORDER)[number]) {
  const { text, ...card } = readCard(id)
  expect(card, `${id} card`).toEqual(expectedCard(id))
  for (const other of ORDER.filter((o) => o !== id)) {
    expect(text, `the ${id} card holds ${other}'s title`).not.toContain(V_COUNTRIES[other].title)
  }
}

describe('Coverage band', () => {
  it('CV-01 the band is #coverage on the peach band', () => {
    expect(view.container.children, 'Coverage renders one root').toHaveLength(1)
    const root = view.container.firstElementChild!
    expect(root.localName).toBe('section')
    expect(root.id).toBe('coverage')
    expect(root.className).toBe('ds-section band-peach')
    expect(root.querySelectorAll('h2'), 'exactly one h2').toHaveLength(1)
    expect(root.querySelectorAll('.container'), 'exactly one .container').toHaveLength(1)
  })

  it('CV-02 the header copy is V’s', () => {
    const root = section()!
    const eyebrows = root.querySelectorAll('.t-eyebrow')
    expect(eyebrows, 'one eyebrow').toHaveLength(1)
    expect(norm(eyebrows[0].textContent)).toBe('NIGERIA FIRST. AFRICA IN VIEW.')
    expect(eyebrows[0].className, 'a light eyebrow, not ds-eyebrow--dark').toBe('t-eyebrow')

    const h2 = root.querySelector('h2')
    expect(h2, 'an h2').not.toBeNull()
    expect(h2!.className).toBe('t-h2')
    expect(norm(h2!.textContent)).toBe('One continent. Every country has its context.')
    expect(h2!.querySelectorAll('br'), 'one line break').toHaveLength(1)
    const spans = h2!.querySelectorAll('span.t-hl-peach')
    expect(spans, 'one span.t-hl-peach').toHaveLength(1)
    expect(norm(spans[0].textContent)).toBe('Every country has its context.')
    expect(h2!.style.margin).toMatch(/^0(px)?$/)

    const paragraphs = Array.from(root.querySelectorAll('p')).filter(
      (p) => norm(p.textContent) === V_HEADER_PARAGRAPH && !p.closest('[data-cov-card],[data-cov-panel]'),
    )
    expect(paragraphs, 'the header paragraph outside the card and panel').toHaveLength(1)
    const style = (paragraphs[0] as HTMLElement).style
    expect({ margin: style.margin, maxWidth: style.maxWidth, fontSize: style.fontSize, lineHeight: style.lineHeight, color: style.color }).toEqual({
      margin: '0px',
      maxWidth: '300px',
      fontSize: '16px',
      lineHeight: 'var(--lh-body)',
      color: 'var(--accent-foreground)',
    })
  })

  it('CV-03 NG is selected first and its card shows', () => {
    const t = tabs()
    expect(t.map((b) => b.textContent), 'tab names in order, untrimmed').toEqual(['Nigeria', 'Kenya', 'South Africa'])
    expect(pressedFlags()).toEqual(['true', 'false', 'false'])
    // The classes carry the focus ring and the hover dim (landing.css).
    expect(t.map((b) => b.className), 'tab classes').toEqual(Array(3).fill('a-card-btn a-tab-pill'))
    expect(t.map((b) => b.type), 'tab types').toEqual(Array(3).fill('button'))

    // V789: pressed and unpressed inline values.
    const [on, off] = [t[0], t[1]]
    expect({ background: on.style.background, color: on.style.color, border: on.style.border }).toEqual({
      background: 'var(--primary)',
      color: 'var(--primary-foreground)',
      border: '1px solid var(--primary)',
    })
    expect({ background: off.style.background, color: off.style.color, border: off.style.border }).toEqual({
      background: 'transparent',
      color: 'var(--primary)',
      border: '1px solid var(--primary-20)',
    })

    expectCard('NG')
    const card = view.container.querySelector<HTMLElement>('[data-cov-card]')!
    expect([card.className, card.style.background], 'V252 card surface').toEqual(['card-floating', 'var(--cream-card)'])
    const steps = Array.from(card.querySelectorAll('.t-step'))
    const glyphs = [steps[0].nextElementSibling, ...Array.from(steps[1].nextElementSibling?.children ?? [])].map((r) => r?.querySelector('svg') ?? null)
    expect(glyphs, 'a glyph in the context row and in each of the four step rows').toHaveLength(5)
    glyphs.forEach((g, i) => {
      expect(g, `glyph ${i} exists`).not.toBeNull()
      expect(g!.getAttribute('width'), `glyph ${i} size`).toBe('16')
      expect(inlineColour(g!, card), `glyph ${i} colour`).toBe('var(--teal)')
      const want = i === 0 ? GLYPHS['shield-check'] : GLYPHS.check
      expect(Array.from(g!.querySelectorAll('path')).map((p) => p.getAttribute('d')), `glyph ${i} drawing`).toEqual(want)
    })
  })

  it('CV-04 each country renders its own card', () => {
    for (const id of ['KE', 'ZA', 'NG'] as const) {
      click(tab(id))
      expect(pressedFlags(), `after clicking ${id}`).toEqual(ORDER.map((o) => String(o === id)))
      expectCard(id)
    }
  })

  it('CV-05 clicking the selected tab keeps it', () => {
    click(tab('NG'))
    click(tab('NG'))
    expect(pressedFlags()).toEqual(['true', 'false', 'false'])
    expectCard('NG')
  })

  it('CV-09 a click on a tab’s flag selects that country', () => {
    for (const id of ['ZA', 'KE'] as const) {
      const flag = tab(id).firstElementChild!
      expect(flag.localName, `${id} flag`).toBe('svg')
      act(() => void flag.dispatchEvent(new MouseEvent('click', { bubbles: true })))
      expect(pressedFlags(), `after a click on the ${id} flag`).toEqual(ORDER.map((o) => String(o === id)))
      expectCard(id)
    }
  })

  it('CV-06 the portal link opens a new tab without opener or referrer', () => {
    const seen: string[] = []
    for (const id of ORDER) {
      click(tab(id))
      const links = view.container.querySelectorAll('[data-cov-card] a')
      expect(links, `${id} card links`).toHaveLength(1)
      seen.push(links[0].getAttribute('href') ?? '')
      expect(links[0].getAttribute('target'), `${id} target`).toBe('_blank')
      expect(links[0].getAttribute('rel'), `${id} rel`).toBe('noopener noreferrer')
      const anchors = Array.from(view.container.querySelectorAll('a'))
      expect(anchors, `${id}: the card link is the only anchor in the band`).toHaveLength(1)
    }
    expect(seen).toEqual(['https://einvoice.nrs.gov.ng/', 'https://www.kra.go.ke/', 'https://www.sars.gov.za/'])
  })

  it('CV-07 each tab carries its V flag', () => {
    // V748-750 FLAGS: each shape's attributes, in paint order.
    const attrs = (el: Element) => Object.fromEntries(Array.from(el.attributes).map((a) => [a.name, a.value]))
    const ZA_PATH = { d: 'M0 0L12 10 0 20M12 10H30', fill: 'none' }
    const WANT: Record<(typeof ORDER)[number], { viewBox: string; shapes: Record<string, string>[] }> = {
      NG: {
        viewBox: '0 0 3 2',
        shapes: [
          { width: '3', height: '2', fill: '#008751' },
          { x: '1', width: '1', height: '2', fill: '#fff' },
        ],
      },
      KE: {
        viewBox: '0 0 30 20',
        shapes: [
          { width: '30', height: '20', fill: '#006600' },
          { width: '30', height: '14', fill: '#fff' },
          { width: '30', height: '6', fill: '#000' },
          { y: '7', width: '30', height: '6', fill: '#bb0000' },
        ],
      },
      ZA: {
        viewBox: '0 0 30 20',
        shapes: [
          { width: '30', height: '10', fill: '#de3831' },
          { y: '10', width: '30', height: '10', fill: '#002395' },
          { ...ZA_PATH, stroke: '#fff', 'stroke-width': '7' },
          { ...ZA_PATH, stroke: '#007749', 'stroke-width': '4.2' },
          { d: 'M0 3L8.5 10 0 17Z', fill: '#000' },
        ],
      },
    }
    const t = tabs()
    expect(t, 'three tabs').toHaveLength(3)
    ORDER.forEach((id, i) => {
      const flag = t[i].firstElementChild
      expect(t[i].firstChild, `${id} tab: the flag comes before the name`).toBe(flag)
      expect(flag?.localName, `${id} tab's first child`).toBe('svg')
      expect(flag!.getAttribute('aria-hidden')).toBe('true')
      expect(flag!.getAttribute('preserveAspectRatio')).toBe('xMidYMid slice')
      expect(flag!.getAttribute('viewBox')).toBe(WANT[id].viewBox)
      expect([flag!.getAttribute('width'), flag!.getAttribute('height')], `${id} size`).toEqual(['22', '15'])
      expect([(flag as unknown as HTMLElement).style.display, (flag as unknown as HTMLElement).style.borderRadius], `${id} box`).toEqual(['block', '2px'])
      expect(Array.from(flag!.children).map(attrs), `${id} shapes`).toEqual(WANT[id].shapes)
    })
  })

  it('CV-08 the dark panel has its title and legend', () => {
    const panels = view.container.querySelectorAll('[data-cov-panel]')
    expect(panels, 'exactly one [data-cov-panel]').toHaveLength(1)
    const panel = panels[0]
    const titles = Array.from(panel.querySelectorAll('*')).filter((e) => e.children.length === 0 && norm(e.textContent) === 'The ASComply Africa roadmap')
    expect(titles, 'the panel title').toHaveLength(1)

    // V791 legend: swatches are the 10px dots.
    const swatches = Array.from(panel.querySelectorAll<HTMLElement>('span')).filter((s) => s.style.width === '10px' && s.style.height === '10px')
    expect(swatches.map((s) => norm(s.parentElement?.textContent))).toEqual(['First launch', 'Planned expansion', 'Future vision'])
    expect(swatches.map((s) => s.style.background)).toEqual(['var(--accent)', 'var(--sage)', 'var(--on-dark-10)'])
    expect(swatches.map((s) => s.style.border)).toEqual(['1px solid transparent', '1px solid transparent', '1px solid var(--on-dark-20)'])
    expect(swatches.map((s) => s.style.borderRadius)).toEqual(['var(--radius-pill)', 'var(--radius-pill)', 'var(--radius-pill)'])
  })
})
