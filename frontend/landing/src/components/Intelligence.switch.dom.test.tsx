// @vitest-environment jsdom
// Intelligence band (jsdom): header, chips, aside, steps, stacked panels, closing row, panel contrast.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { Fragment, createElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { GLYPHS, type GlyphName } from '../icons'
import { LANDING_SRC, customPropValues, readV2Css, resolveTextContrast, stripSource, type ContrastRow } from '../cssScan.test.util'
import { click, mountView, show, spyConsoleError, unmountView, type View } from './ds/dsDom.test.util'
import { Coverage } from './Coverage'
import { Intelligence } from './Intelligence'

type Id = 'NG' | 'KE' | 'ZA'
const ORDER: readonly Id[] = ['NG', 'KE', 'ZA']

// V796-798 INTEL, retyped.
const V_INTEL: Record<Id, { name: string; authority: string; focus: string; href: string; tag: string; cardTitle: string; cardSub: string }> = {
  NG: { name: 'Nigeria', authority: 'NRS · Merchant Buyer Solution', focus: 'MBS requirements & invoice validation', href: 'https://einvoice.nrs.gov.ng/', tag: 'First launch', cardTitle: "Nigeria's e-invoicing requirements", cardSub: 'Required invoice fields, validation rules and transmission readiness.' },
  KE: { name: 'Kenya', authority: 'KRA · eTIMS', focus: 'eTIMS system requirements', href: 'https://www.kra.go.ke/', tag: 'Planned', cardTitle: "Kenya's eTIMS guidance", cardSub: 'Invoice data mapping, system requirements and exception handling.' },
  ZA: { name: 'South Africa', authority: 'SARS · VAT modernisation', focus: 'VAT developments & proposed digital reporting', href: 'https://www.sars.gov.za/', tag: 'Planned', cardTitle: "South Africa's VAT modernisation proposals", cardSub: 'Proposed changes, their scope and implementation status.' },
}

// V803-806 STEPS, retyped.
const V_STEPS: { n: string; label: string; icon: GlyphName; step: string; title: string; body: string; card: string; status: string }[] = [
  { n: '01', label: 'Monitor', icon: 'file-search', step: '01 / SOURCE REVIEW', title: 'Start at the official source.', body: 'Follow tax authority guidance and regulatory notices by country. Keep the source, publication date and jurisdiction attached to each update.', card: 'Source review', status: 'Official guidance enters the review queue.' },
  { n: '02', label: 'Understand', icon: 'sparkles', step: '02 / AI-ASSISTED ANALYSIS', title: 'Explain the change clearly.', body: 'Use AI assistance to summarise the update and identify potential effects on invoice data, validation rules and business workflows.', card: 'AI-assisted analysis', status: 'A draft summary and impact assessment are prepared.' },
  { n: '03', label: 'Review', icon: 'user-check', step: '03 / HUMAN REVIEW', title: 'People make the decision.', body: 'A compliance specialist checks the source, confirms the interpretation and decides what needs action. AI supports the review; a person approves it.', card: 'Human review', status: 'Interpretation and business impact are checked.' },
  { n: '04', label: 'Apply', icon: 'workflow', step: '04 / APPROVED WORKFLOW UPDATE', title: 'Turn approvals into workflow.', body: 'Prepare country-specific checks and tasks, assign an owner and keep a record of the approved update. Give finance and technology teams the same context.', card: 'Approved workflow update', status: 'Reviewed changes become clear, accountable tasks.' },
]

// V300 and V336, retyped.
const V_HEADER_PARAGRAPH = 'Our roadmap connects official updates, AI-assisted analysis and human review. A clearer way to follow regulatory change across Africa.'
const V_CLOSING = 'AI helps your team review. People approve the changes.'
const V_FOOT = 'Source-linked. Human-reviewed.'

// V748-750: the first rect's fill identifies each flag.
const FLAG = { NG: { viewBox: '0 0 3 2', fill: '#008751' }, KE: { viewBox: '0 0 30 20', fill: '#006600' }, ZA: { viewBox: '0 0 30 20', fill: '#de3831' } } as const

const v2 = readV2Css()
const landingCss = (f: string) => stripSource(f, readFileSync(join(LANDING_SRC, 'styles', f), 'utf8'))
const CSS = [v2['tokens/colors.css'], v2['utilities.css'], landingCss('ds.css'), landingCss('landing.css')]
const TOKENS = customPropValues(v2['tokens/colors.css'])
const hex = (name: string) => TOKENS.get(name)!.toLowerCase()

let view: View
let consoleError: ReturnType<typeof spyConsoleError>

beforeEach(async () => {
  view = mountView()
  consoleError = spyConsoleError()
  await show(view, createElement(Intelligence))
})

afterEach(() => {
  expect(consoleError, 'console.error was called').not.toHaveBeenCalled()
  unmountView(view)
  vi.restoreAllMocks()
})

const norm = (s: string | null | undefined) => (s ?? '').replace(/\s+/g, ' ').trim()
const ownText = (el: Element) => norm(Array.from(el.childNodes).filter((n) => n.nodeType === 3).map((n) => n.textContent).join(''))
/** Own text of `el` and every descendant, in document order, blanks dropped. */
const leafTexts = (el: Element) => [el, ...Array.from(el.querySelectorAll('*'))].map(ownText).filter(Boolean)
const paths = (el: Element | null | undefined) => Array.from(el?.querySelectorAll('path') ?? []).map((p) => p.getAttribute('d'))

const root = () => view.container.querySelector<HTMLElement>('#intelligence')
const chips = () => Array.from(view.container.querySelectorAll<HTMLButtonElement>('#intelligence button[aria-pressed]:not([data-intel-step])'))
const steps = () => Array.from(view.container.querySelectorAll<HTMLButtonElement>('[data-intel-step]'))
const panels = () => Array.from(view.container.querySelectorAll<HTMLElement>('[data-intel-panel]'))
const pressed = (els: Element[]) => els.map((e) => e.getAttribute('aria-pressed'))
const only = (n: number, i: number) => Array.from({ length: n }, (_, k) => String(k === i))

function chip(id: Id): HTMLButtonElement {
  expect(chips(), 'three country chips').toHaveLength(3)
  return chips()[ORDER.indexOf(id)]
}
function step(i: number): HTMLButtonElement {
  expect(steps(), 'four step buttons').toHaveLength(4)
  return steps()[i]
}
function workspace() {
  const ws = view.container.querySelectorAll<HTMLElement>('[data-intel-workspace]')
  expect(ws, 'exactly one [data-intel-workspace]').toHaveLength(1)
  expect(ws[0].children, 'the workspace holds the aside and the main column').toHaveLength(2)
  return { ws: ws[0], aside: ws[0].firstElementChild as HTMLElement, main: ws[0].lastElementChild as HTMLElement }
}

function expectAside(id: Id) {
  const c = V_INTEL[id]
  const { aside } = workspace()
  expect(leafTexts(aside), `${id} aside, in order`).toEqual(['Regulatory workspace', c.name, c.authority, 'Monitoring focus', c.focus, 'View official source ↗', V_FOOT])
  const links = aside.querySelectorAll('a')
  expect(links, `${id} aside holds exactly one link`).toHaveLength(1)
  expect([links[0].getAttribute('href'), links[0].getAttribute('target'), links[0].getAttribute('rel')], `${id} official-source link`).toEqual([c.href, '_blank', 'noopener noreferrer'])
  const flag = aside.querySelector('svg[aria-hidden="true"]')
  expect([flag?.getAttribute('width'), flag?.getAttribute('height'), flag?.getAttribute('viewBox'), flag?.querySelector('rect')?.getAttribute('fill')], `${id} aside flag`).toEqual(['30', '20', FLAG[id].viewBox, FLAG[id].fill])
}

function expectPanelCards(id: Id) {
  const c = V_INTEL[id]
  expect(panels(), 'four panels').toHaveLength(4)
  panels().forEach((p, i) => {
    const card = p.querySelector('.card')
    expect(card, `panel ${i} card`).not.toBeNull()
    const [, title, sub] = leafTexts(card!)
    expect([title, sub], `${id}: panel ${i} card title and sub`).toEqual([c.cardTitle, c.cardSub])
  })
}

describe('Intelligence band', () => {
  it('IN-01 the band is #intelligence on the dark-2 band with V’s copy', () => {
    expect(view.container.children, 'Intelligence renders one root').toHaveLength(1)
    const r = view.container.firstElementChild!
    expect([r.localName, r.id, r.className]).toEqual(['section', 'intelligence', 'ds-section band-dark2'])
    expect(r.querySelectorAll('.container'), 'exactly one .container').toHaveLength(1)

    const eyebrows = r.querySelectorAll('.t-eyebrow')
    expect(eyebrows, 'one eyebrow').toHaveLength(1)
    expect([eyebrows[0].className, norm(eyebrows[0].textContent)]).toEqual(['t-eyebrow ds-eyebrow--dark', 'AI-SUPPORTED REGULATORY INTELLIGENCE'])

    const h2s = r.querySelectorAll('h2')
    expect(h2s, 'exactly one h2').toHaveLength(1)
    const h2 = h2s[0]
    expect(h2.className).toBe('t-h2')
    expect(norm(h2.textContent)).toBe('Regulations move. Keep your next step clear.')
    expect(h2.querySelectorAll('br'), 'one line break').toHaveLength(1)
    const spans = h2.querySelectorAll('span.t-hl-dark2')
    expect(spans, 'one span.t-hl-dark2').toHaveLength(1)
    expect(norm(spans[0].textContent)).toBe('Keep your next step clear.')
    expect([h2.style.margin, h2.style.color], 'V297 h2 style').toEqual(['0px', 'var(--surface-foreground)'])

    const paragraphs = Array.from(r.querySelectorAll('p')).filter((p) => norm(p.textContent) === V_HEADER_PARAGRAPH && !p.closest('[data-intel-workspace]'))
    expect(paragraphs, 'the header paragraph outside the workspace').toHaveLength(1)

    const { ws } = workspace()
    expect(ws.className).toBe('split')
    const s = ws.style
    expect(
      { cols: s.gridTemplateColumns, background: s.background, border: s.border, radius: s.borderRadius, overflow: s.overflow, color: s.color },
      'V306 workspace',
    ).toEqual({
      cols: '300px minmax(0, 1fr)',
      background: 'var(--cream-card)',
      border: '1px solid var(--cream-card-border)',
      radius: 'var(--radius-md)',
      overflow: 'hidden',
      color: 'var(--ink)',
    })
  })

  it('IN-02 NG is the first chip and fills the aside', () => {
    const c = chips()
    expect(c, 'three chips').toHaveLength(3)
    expect(c.map((b) => b.type), 'chip types').toEqual(Array(3).fill('button'))
    expect(c.map((b) => b.className), 'chip classes').toEqual(Array(3).fill('a-card-btn'))
    expect(pressed(c)).toEqual(['true', 'false', 'false'])

    c.forEach((b, i) => {
      const id = ORDER[i]
      const flag = b.firstElementChild
      expect(b.firstChild, `${id} chip: the flag comes first`).toBe(flag)
      expect([flag?.localName, flag?.getAttribute('aria-hidden')], `${id} chip flag`).toEqual(['svg', 'true'])
      expect([flag!.getAttribute('width'), flag!.getAttribute('height'), flag!.getAttribute('viewBox'), flag!.querySelector('rect')?.getAttribute('fill')], `${id} chip flag`).toEqual(['22', '15', FLAG[id].viewBox, FLAG[id].fill])
      const tag = b.lastElementChild as HTMLElement
      expect(tag.localName, `${id} chip tag`).toBe('span')
      expect([ownText(b), norm(tag.textContent)], `${id} chip name and tag`).toEqual([V_INTEL[id].name, V_INTEL[id].tag])
      expect([tag.style.fontSize, tag.style.fontWeight, tag.style.opacity], `${id} tag style`).toEqual(['11px', '600', '0.85'])
      expect([b.style.height, b.style.fontSize, b.style.fontWeight, b.style.borderRadius], `${id} V804 chip`).toEqual(['44px', '14px', '700', 'var(--radius-btn)'])
    })

    // V801: pressed and unpressed inline values.
    const [on, off] = [c[0], c[1]]
    expect({ background: on.style.background, color: on.style.color, border: on.style.border }).toEqual({
      background: 'var(--accent)',
      color: 'var(--accent-foreground)',
      border: '1px solid var(--accent)',
    })
    expect({ background: off.style.background, color: off.style.color, border: off.style.border }).toEqual({
      background: 'var(--on-dark-5)',
      color: 'var(--surface-foreground)',
      border: '1px solid var(--on-dark-10)',
    })

    expectAside('NG')
    const { aside } = workspace()
    expect([aside.style.background, aside.style.padding, aside.style.display, aside.style.flexDirection, aside.style.gap], 'V307 aside').toEqual(['var(--sage-panel)', '28px 24px', 'flex', 'column', '20px'])
    const link = aside.querySelector('a')!
    expect(link.className).toBe('ds-btn ds-btn--text')
    expect(link.getAttribute('href')).toBe('https://einvoice.nrs.gov.ng/')
    const foot = [aside, ...Array.from(aside.querySelectorAll<HTMLElement>('*'))].filter((e) => ownText(e) === V_FOOT)
    expect(foot, 'the foot line').toHaveLength(1)
    const glyph = foot[0].querySelector('svg')
    expect(glyph, 'the foot line holds a glyph').not.toBeNull()
    expect([glyph!.getAttribute('width'), paths(glyph)], 'V312 link glyph').toEqual(['15', GLYPHS.link])
    expect([foot[0].style.fontSize, foot[0].style.fontWeight, foot[0].style.color], 'V312 foot line').toEqual(['13px', '700', 'var(--step-label)'])
  })

  it('IN-03 each chip fills the aside and the panel cards', () => {
    expectPanelCards('NG')
    for (const id of ['KE', 'ZA', 'NG'] as const) {
      click(chip(id))
      expect(pressed(chips()), `after clicking ${id}`).toEqual(ORDER.map((o) => String(o === id)))
      expectAside(id)
      expectPanelCards(id)
      const text = norm(root()!.textContent)
      for (const other of ORDER.filter((o) => o !== id)) {
        expect(text, `the ${id} view holds ${other}'s card title`).not.toContain(V_INTEL[other].cardTitle)
        expect(text, `the ${id} view holds ${other}'s focus`).not.toContain(V_INTEL[other].focus)
      }
    }
  })

  it('IN-04 the four steps and step 01 first', () => {
    const { main } = workspace()
    expect([main.style.display, main.style.gap, main.style.minWidth], 'V313 main column').toEqual(['grid', '24px', '0'])
    expect(norm(main.querySelector('.t-step')?.textContent), 'V315 header').toBe('Change → Understanding → Action')

    const s = steps()
    expect(s, 'four step buttons').toHaveLength(4)
    const labelOf = (b: Element) => Array.from(b.children).find((c) => !c.classList.contains('ds-icontile')) as HTMLElement
    expect(s.map((b) => `${norm(labelOf(b).firstElementChild?.textContent)} ${ownText(labelOf(b))}`), 'step buttons').toEqual(['01 Monitor', '02 Understand', '03 Review', '04 Apply'])
    expect(pressed(s)).toEqual(['true', 'false', 'false', 'false'])
    expect(s.map((b) => [b.type, b.className])).toEqual(Array(4).fill(['button', 'a-card-btn']))

    s.forEach((b, i) => {
      const num = labelOf(b).firstElementChild as HTMLElement
      expect([labelOf(b).style.whiteSpace, num.style.fontFamily, num.style.fontSize, num.style.color], `step ${i + 1} label`).toEqual(['nowrap', 'var(--font-mono)', '11px', 'var(--step-label)'])
      const tile = b.querySelector('.ds-icontile')
      expect(tile, `step ${i + 1} IconTile`).not.toBeNull()
      expect([(tile as HTMLElement).style.width, paths(tile)], `step ${i + 1} IconTile size and icon`).toEqual(['36px', GLYPHS[V_STEPS[i].icon]])
      expect(tile!.className, `step ${i + 1} tone`).toBe(`ds-icontile ds-icontile--${i === 0 ? 'accent' : 'primary'}`)
    })

    // V808: pressed and unpressed inline values.
    expect({ line: s[0].style.borderBottom, color: s[0].style.color, background: s[0].style.background }, 'pressed step').toEqual({
      line: '2px solid var(--accent-foreground)',
      color: 'var(--ink)',
      background: 'transparent',
    })
    expect({ line: s[1].style.borderBottom, color: s[1].style.color, background: s[1].style.background }, 'unpressed step').toEqual({
      line: '2px solid transparent',
      color: 'var(--muted-foreground)',
      background: 'transparent',
    })

    const row = s[0].parentElement!
    expect(s.every((b) => b.parentElement === row), 'the steps share one row').toBe(true)
    expect([row.style.display, row.style.minWidth], 'V316 row').toEqual(['flex', '520px'])
    expect((row.parentElement as HTMLElement).style.overflowX, 'V316 scroller').toBe('auto')
    expect(row.parentElement!.hasAttribute('data-intel-steps'), 'the scroller is [data-intel-steps]').toBe(true)
    const chevs = Array.from(row.children).filter((c) => c.localName === 'span' && !c.classList.contains('ds-icontile') && (c as HTMLElement).style.display !== 'none')
    expect(chevs, 'three chevrons').toHaveLength(3)
    chevs.forEach((c, i) => {
      expect(c.previousElementSibling, `chevron ${i} follows step ${i + 1}`).toBe(s[i])
      expect(c.nextElementSibling, `chevron ${i} precedes step ${i + 2}`).toBe(s[i + 1])
      const svg = c.querySelector('svg')
      expect([svg?.getAttribute('width'), paths(svg), (c as HTMLElement).style.color], `chevron ${i}`).toEqual(['16', GLYPHS['chevron-right'], 'var(--step-label)'])
    })

    const p = panels()
    expect(p, 'four panels').toHaveLength(4)
    expect(new Set(p.map((x) => x.parentElement)).size, 'panels share one grid wrapper').toBe(1)
    expect(p[0].parentElement!.style.display, 'V319 wrapper').toBe('grid')
    p.forEach((x, i) => {
      expect([x.className, x.style.gridArea, x.style.gridTemplateColumns, x.style.gap, x.style.alignItems], `panel ${i} V320`).toEqual([
        'split',
        '1 / 1',
        'minmax(0, 1.3fr) minmax(0, 0.9fr)',
        '32px',
        'stretch',
      ])
    })
    expect(p.map((x) => [x.style.visibility, x.getAttribute('aria-hidden')])).toEqual([
      ['visible', null],
      ['hidden', 'true'],
      ['hidden', 'true'],
      ['hidden', 'true'],
    ])
  })

  it('IN-05 a step click shows its panel alone', () => {
    const expectPanels = (at: number) => {
      expect(pressed(steps()), `step ${at + 1} pressed`).toEqual(only(4, at))
      expect(panels().map((x) => [x.style.visibility, x.getAttribute('aria-hidden')]), `step ${at + 1} panels`).toEqual(
        V_STEPS.map((_, i) => (i === at ? ['visible', null] : ['hidden', 'true'])),
      )
      panels().forEach((x, i) => {
        const v = V_STEPS[i]
        const card = x.querySelector('.card')!
        expect(norm(x.querySelector('.t-step')?.textContent), `panel ${i} step`).toBe(v.step)
        expect(norm(x.querySelector('h3')?.textContent), `panel ${i} title`).toBe(v.title)
        expect(norm(x.querySelector('p.t-body')?.textContent), `panel ${i} body`).toBe(v.body)
        const [label, , , status] = leafTexts(card)
        expect([label, status], `panel ${i} card label and status`).toEqual([v.card, v.status])
        expect(paths(card.querySelector('svg')), `panel ${i} card icon`).toEqual(GLYPHS[v.icon])
        expect(card.querySelector('svg')!.getAttribute('width'), `panel ${i} card icon size`).toBe('15')
      })
    }
    expectPanels(0)
    for (const at of [2, 3, 1, 0]) {
      click(step(at))
      expectPanels(at)
    }
  })

  it('IN-06 chips and steps are independent', () => {
    click(step(2))
    expect(pressed(steps())).toEqual(only(4, 2))
    click(chip('KE'))
    expect(pressed(chips()), 'Kenya is pressed').toEqual(only(3, 1))
    expect(pressed(steps()), 'step 03 stays pressed').toEqual(only(4, 2))
    expect(panels().map((x) => x.style.visibility), 'step 03 stays visible').toEqual(['hidden', 'hidden', 'visible', 'hidden'])
    click(step(0))
    expect(pressed(steps()), 'step 01 pressed').toEqual(only(4, 0))
    expect(pressed(chips()), 'Kenya stays the pressed chip').toEqual(only(3, 1))
    expectAside('KE')
  })

  it('IN-07 hidden panels hold nothing focusable', () => {
    const p = panels()
    expect(p, 'four panels').toHaveLength(4)
    p.forEach((x, i) => {
      expect(norm(x.textContent), `panel ${i} is not empty`).not.toBe('')
      expect(x.querySelectorAll('a[href], button, input, select, textarea, [tabindex]'), `panel ${i} focusables`).toHaveLength(0)
    })
    // The whole band: three chips, the source link, four steps, the roadmap link, in DOM order.
    const stops = Array.from(root()!.querySelectorAll<HTMLElement>('a[href], button, input, select, textarea, [tabindex]'))
    expect(stops.map((e) => leafTexts(e).join(' '))).toEqual([
      'Nigeria First launch',
      'Kenya Planned',
      'South Africa Planned',
      'View official source ↗',
      'Monitor 01',
      'Understand 02',
      'Review 03',
      'Apply 04',
      'Explore the roadmap →',
    ])
  })

  it('IN-08 the closing line and the roadmap link', () => {
    const r = root()!
    const lines = Array.from(r.querySelectorAll('p')).filter((p) => norm(p.textContent) === V_CLOSING)
    expect(lines, 'the closing paragraph').toHaveLength(1)
    expect(lines[0].closest('[data-intel-workspace]'), 'the closing row sits below the workspace').toBeNull()
    expect([lines[0].style.fontSize, lines[0].style.fontWeight, lines[0].style.color], 'V336 paragraph').toEqual(['16px', '700', 'var(--surface-foreground)'])

    const links = Array.from(r.querySelectorAll('a')).filter((a) => norm(a.textContent) === 'Explore the roadmap →')
    expect(links, 'the roadmap link').toHaveLength(1)
    const a = links[0]
    expect(a.localName).toBe('a')
    expect(a.getAttribute('href')).toBe('#coverage')
    expect(a.className).toBe('ds-btn ds-btn--text btn-on-dark')
    expect(a.hasAttribute('target'), 'no target').toBe(false)
    expect(a.parentElement, 'the paragraph and the link share one row').toBe(lines[0].parentElement)
  })

  it('IN-12 every chip and step pair resolves, and a repeat click keeps the selection', () => {
    let prev = 0
    for (const id of ORDER) {
      for (let at = 0; at < 4; at++) {
        click(chip(id))
        expect(pressed(steps()), `${id}: the chip click leaves step ${prev + 1}`).toEqual(only(4, prev))
        click(step(at))
        prev = at
        expect(pressed(chips()), `${id}/${at + 1} chips`).toEqual(ORDER.map((o) => String(o === id)))
        expect(pressed(steps()), `${id}/${at + 1} steps`).toEqual(only(4, at))
        expect(panels().map((x) => x.style.visibility), `${id}/${at + 1} panels`).toEqual(V_STEPS.map((_, i) => (i === at ? 'visible' : 'hidden')))
        expectAside(id)
        expectPanelCards(id)
      }
    }
    // Repeat clicks and clicks on a child of the button: no toggle-off.
    click(chip('ZA'))
    expect(pressed(chips()), 'South Africa pressed').toEqual(only(3, 2))
    click(chip('ZA'))
    expect(pressed(chips()), 'a second click keeps South Africa').toEqual(only(3, 2))
    click(chip('KE').lastElementChild as HTMLElement)
    expect(pressed(chips()), 'a click on the tag span selects Kenya').toEqual(only(3, 1))
    click(step(3))
    expect(pressed(steps()), 'step 04 pressed').toEqual(only(4, 3))
    click(step(3))
    expect(pressed(steps()), 'a second click keeps step 04').toEqual(only(4, 3))
    expect(panels().map((x) => x.getAttribute('aria-hidden')), 'step 04 alone is exposed').toEqual(['true', 'true', 'true', null])
    click(step(1).querySelector('.ds-icontile') as HTMLElement)
    expect(pressed(steps()), 'a click on the tile selects step 02').toEqual(only(4, 1))
  })

  it('IN-10 Coverage’s selection does not reach Intelligence', async () => {
    await show(view, createElement(Fragment, null, createElement(Coverage, { onBookDemo: () => undefined }), createElement(Intelligence)))
    const tabs = () => Array.from(view.container.querySelectorAll<HTMLButtonElement>('#coverage button[aria-pressed]'))
    expect(tabs(), 'three Coverage tabs').toHaveLength(3)
    expect(chips(), 'three Intelligence chips').toHaveLength(3)
    expect([pressed(tabs()), pressed(chips())], 'both start on Nigeria').toEqual([only(3, 0), only(3, 0)])

    click(tabs()[1])
    expect(pressed(tabs()), 'the Kenya tab is pressed').toEqual(only(3, 1))
    expect(pressed(chips()), 'the Nigeria chip stays pressed').toEqual(only(3, 0))
    expectAside('NG')

    click(chip('ZA'))
    expect(pressed(chips()), 'the South Africa chip is pressed').toEqual(only(3, 2))
    expect(pressed(tabs()), 'the Kenya tab stays pressed').toEqual(only(3, 1))
  })
})

/** Rows below 4.5:1 and every ambiguous row. */
function failures(rows: ContrastRow[]): string[] {
  return rows.flatMap((r) => (r.ambiguous ? [`${r.text}: ambiguous colour`] : r.ratio < 4.5 ? [`${r.text}: ${r.fg} on ${r.bg} = ${r.ratio.toFixed(2)}`] : []))
}

describe('Intelligence panel contrast', () => {
  it('IN-09 the panel text is readable on the cream workspace', () => {
    const r = root()
    expect(r, '#intelligence is mounted').not.toBeNull()
    const all = resolveTextContrast(r!, CSS)
    const rows = all.filter((x) => x.el.closest('[data-intel-panel]'))
    expect(rows.length, 'text elements measured inside the panels').toBeGreaterThanOrEqual(20)
    expect(rows.filter((x) => !/^#[0-9a-f]{6}$/.test(x.fg) || !/^#[0-9a-f]{6}$/.test(x.bg)).map((x) => x.text), 'rows with an unresolved colour').toEqual([])

    const bodies = rows.filter((x) => x.el.matches('p.t-body'))
    expect(bodies, 'four panel bodies are measured').toHaveLength(4)
    for (const b of bodies) {
      expect([b.fg, b.bg], `panel body "${b.text.slice(0, 24)}"`).toEqual([hex('--text-copy'), hex('--cream-card')])
    }
    // V329 and V326 colours: no other test reads them.
    for (const [key, token] of [['status', '--status-success-fg'], ['card', '--step-label']] as const) {
      const hits = rows.filter((x) => V_STEPS.some((v) => v[key] === x.text))
      expect(hits, `four ${key} lines are measured`).toHaveLength(4)
      for (const h of hits) expect([h.fg, h.ambiguous], `${key} "${h.text}"`).toEqual([hex(token), false])
    }
    expect(failures(rows)).toEqual([])
  })

  it('IN-11 the band text and the roadmap link resolve to their colours on the dark band', () => {
    // jsdom's stylesheet cascade ignores !important and :hover, so only the colour (resolver) and the underline (order) are read.
    const underline = (el: Element) => {
      const st = document.createElement('style')
      st.textContent = landingCss('ds.css') + landingCss('landing.css')
      document.head.appendChild(st)
      try {
        return getComputedStyle(el).borderBottomColor
      } finally {
        st.remove()
      }
    }
    const r = root()!
    const band = resolveTextContrast(r, CSS).filter((x) => !x.el.closest('[data-intel-workspace]') && !x.el.closest('button[aria-pressed="false"]'))
    expect(band.length, 'band text elements measured outside the workspace').toBeGreaterThanOrEqual(8)
    const row = (text: string) => {
      const hits = band.filter((x) => x.text === text)
      expect(hits, `"${text}" is measured once`).toHaveLength(1)
      return hits[0]
    }
    const cta = row('Explore the roadmap →')
    expect([cta.fg, cta.ambiguous, cta.bg], 'the CTA is --accent on the band, not --link').toEqual([hex('--accent'), false, hex('--surface-2')])
    expect(underline(cta.el), 'the CTA underline is --accent').toBe('var(--accent)')
    expect(row(V_HEADER_PARAGRAPH).fg, 'the header paragraph').toBe(hex('--surface-body'))
    expect(row(V_CLOSING).fg, 'the closing line').toBe(hex('--surface-foreground'))
    expect(row('Keep your next step clear.').fg, 'the highlighted H2 line').toBe(hex('--highlight-on-dark-2'))
    expect(band.every((x) => x.bg === hex('--surface-2') || x.el.closest('button[aria-pressed="true"]')), 'band rows sit on --surface-2 or the pressed chip').toBe(true)
    expect(failures(band)).toEqual([])
  })
})
