// @vitest-environment jsdom
// Solutions band (jsdom): header, tabs, stacked panels, card and copy per panel, ids, keys.
import { createElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { GLYPHS, type GlyphName } from '../icons'
import { click, fire, focusedIndex, mountView, press, selectedIndex, show, spyConsoleError, tabsIn, unmountView, type View } from './ds/dsDom.test.util'
import { Solutions } from './Solutions'

type Id = 'fin' | 'firm' | 'dev'
const ORDER: readonly Id[] = ['fin', 'firm', 'dev']

// solOptions labels, in order.
const V_TABS: Record<Id, string> = { fin: 'Finance teams', firm: 'Accounting & tax firms', dev: 'Developers & partners' }

type Row = [name: string, status: string]
type VSol = { cardIcon: GlyphName; cardTitle: string; overview: string; cardHead: string; rows: Row[]; label: string; h3: string; body: string; points: string[]; cta: string }

// SOL, retyped. dev.label takes the repo's wording for the retired "layer" phrase.
const V_SOL: Record<Id, VSol> = {
  fin: { cardIcon: 'layout-dashboard', cardTitle: 'Invoice workspace', overview: 'WORKSPACE OVERVIEW', cardHead: 'A clearer working day.', rows: [['Invoice INV-2026-0481', 'Validated'], ['Invoice INV-2026-0482', 'In review'], ['Invoice INV-2026-0483', 'Validated']], label: 'MORE CONTROL. LESS CHASING.', h3: 'Keep your team focused on the business.', body: 'Bring invoices, approvals and compliance checks into a single workflow. Spot what needs attention and keep everyone working from the same record.', points: ['Review exceptions before submission', 'Give every approval a clear owner', 'Keep invoice records ready for review'], cta: 'Find your workflow' },
  firm: { cardIcon: 'building-2', cardTitle: 'Client portfolio', overview: 'WORKSPACE OVERVIEW', cardHead: 'Your clients, connected.', rows: [['Client company A', 'Validated'], ['Client company B', 'In review'], ['Client company C', 'Validated']], label: 'ONE WORKSPACE. EVERY CLIENT.', h3: 'Bring every client company into clearer view.', body: 'Work across client companies from one portal. Organise invoice reviews and identify the businesses that need attention without juggling separate workspaces.', points: ['Switch between client companies', 'Track approvals by client', "Keep each client's records organised"], cta: 'Find your workflow' },
  dev: { cardIcon: 'plug', cardTitle: 'Integration workspace', overview: 'WORKSPACE OVERVIEW', cardHead: 'Your data, connected.', rows: [['ERP invoice import', 'Connected'], ['Field mapping', 'Mapped'], ['Validation API request', 'Validated']], label: 'YOUR STACK. OUR COMPLIANCE SOLUTION.', h3: 'Build compliance into the way you work.', body: 'Plan an invoice workflow around your existing ERP, accounting or fintech platform. Talk to our team about API access, data mapping and your integration requirements.', points: ['Explore invoice validation via API', 'Map your invoice data and workflow', 'Discuss connector and partner opportunities'], cta: 'Discuss a partnership' },
}

// tn, retyped.
const V_TONE: Record<string, string> = { Validated: 'success', Connected: 'success', Mapped: 'success', 'In review': 'progress' }

const V_EYEBROW = 'YOUR TEAM. YOUR WORKFLOW.'
const V_H2 = 'One platform. Different perspectives.'
const V_INTRO = 'For the people who own invoices, clients or integrations, each view shows what they need to move forward.'
const V_FOOT = 'One workspace. A shared view.'

let view: View
let consoleError: ReturnType<typeof spyConsoleError>

beforeEach(async () => {
  view = mountView()
  consoleError = spyConsoleError()
  await show(view, createElement(Solutions, { onBookDemo: () => undefined }))
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

const root = () => view.container.querySelector<HTMLElement>('#solutions')
const panels = () => Array.from(view.container.querySelectorAll<HTMLElement>('[data-sol-panel]'))

function tabs(): HTMLButtonElement[] {
  const found = tabsIn(view.container)
  expect(found, 'three tabs').toHaveLength(3)
  return found
}
function allPanels(): HTMLElement[] {
  const found = panels()
  expect(found.map((p) => p.dataset.solPanel), 'three panels, in order').toEqual([...ORDER])
  return found
}
const visiblePanels = () => allPanels().filter((p) => p.style.visibility === 'visible')

function expectOnly(id: Id, where: string) {
  const at = ORDER.indexOf(id)
  expect(tabs().map((t) => t.getAttribute('aria-selected')), `${where}: selected tab`).toEqual(ORDER.map((_, i) => String(i === at)))
  expect(allPanels().map((p) => [p.style.visibility, p.getAttribute('aria-hidden')]), `${where}: panels`).toEqual(
    ORDER.map((_, i) => (i === at ? ['visible', null] : ['hidden', 'true'])),
  )
  const vis = visiblePanels()
  expect(vis, `${where}: one visible panel`).toHaveLength(1)
  expect(vis[0].dataset.solPanel, `${where}: the visible panel is ${id}`).toBe(id)
}

describe('Solutions band', () => {
  it('SL-01 the band is #solutions on the off-white band', () => {
    expect(view.container.children, 'Solutions renders one root').toHaveLength(1)
    const r = view.container.firstElementChild!
    expect([r.localName, r.id, r.className]).toEqual(['section', 'solutions', 'ds-section band-cream'])
    expect(r.querySelectorAll('h2'), 'exactly one h2').toHaveLength(1)
    expect(r.querySelectorAll('[role=tablist]'), 'exactly one tablist').toHaveLength(1)
  })

  it('SL-02 the header copy is V’s', () => {
    expect(root(), 'control: the band rendered').not.toBeNull()
    const r = root()!
    const eyebrows = r.querySelectorAll('.t-eyebrow')
    expect(eyebrows, 'one eyebrow').toHaveLength(1)
    expect(norm(eyebrows[0].textContent)).toBe(V_EYEBROW)

    const h2s = r.querySelectorAll('h2')
    expect(h2s, 'exactly one h2').toHaveLength(1)
    expect(norm(h2s[0].textContent)).toBe(V_H2)
    expect(h2s[0].querySelectorAll('br'), 'one line break').toHaveLength(1)
    const spans = h2s[0].querySelectorAll('span')
    expect(spans, 'one span').toHaveLength(1)
    expect([norm(spans[0].textContent), spans[0].style.color]).toEqual(['Different perspectives.', 'var(--teal)'])

    const intro = Array.from(r.querySelectorAll('p.t-body')).filter((p) => !p.closest('[data-sol-panels]'))
    expect(intro.map((p) => norm(p.textContent)), 'the header paragraph outside the panels').toEqual([V_INTRO])
  })

  it('SL-03 three segments, Finance teams first', () => {
    const lists = view.container.querySelectorAll('[role=tablist]')
    expect(lists, 'one tablist').toHaveLength(1)
    expect(lists[0].getAttribute('aria-label')).toBe("Who it's for")
    expect(tabs().map((t) => norm(t.textContent))).toEqual(ORDER.map((id) => V_TABS[id]))
    expectOnly('fin', 'at rest')
  })

  it('SL-04 a click moves the selection and the visible panel together', () => {
    for (const id of ['dev', 'firm', 'fin'] as const) {
      click(tabs()[ORDER.indexOf(id)])
      expectOnly(id, `after ${V_TABS[id]}`)
      const [panel] = visiblePanels()
      expect(norm(panel.querySelector('h3')?.textContent), `${id} h3`).toBe(V_SOL[id].h3)
      expect(leafTexts(panel.querySelector('[data-sol-card]')!)[3], `${id} card head`).toBe(V_SOL[id].cardHead)
    }
  })

  it('SL-05 re-clicking the selected tab keeps it', () => {
    click(tabs()[0])
    click(tabs()[0])
    expectOnly('fin', 'after two clicks on Finance teams')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('SL-06 the keyboard follows the DS tab keys', () => {
    tabs()[0].focus()
    expect(focusedIndex(tabs()), 'focus starts on Finance teams').toBe(0)
    const steps: [string, Id][] = [['ArrowRight', 'firm'], ['End', 'dev'], ['Home', 'fin'], ['ArrowLeft', 'dev'], ['ArrowRight', 'fin']]
    steps.forEach(([key, id], i) => {
      press(document.activeElement as Element, key)
      expectOnly(id, `${key} (step ${i + 1})`)
      expect(selectedIndex(tabs()), `${key} selects`).toBe(ORDER.indexOf(id))
      expect(focusedIndex(tabs()), `${key} focuses the selected tab`).toBe(ORDER.indexOf(id))
    })
    const withCtrl = fire(document.activeElement as Element, 'ArrowRight', { ctrlKey: true })
    expect(withCtrl.defaultPrevented, 'Ctrl+ArrowRight is left to the browser').toBe(false)
    expectOnly('fin', 'after Ctrl+ArrowRight')
  })

  it('SL-07 each panel carries V’s copy', () => {
    for (const [i, p] of allPanels().entries()) {
      const id = ORDER[i]
      const v = V_SOL[id]
      const card = p.querySelector<HTMLElement>('[data-sol-card]')
      expect(card, `${id} card`).not.toBeNull()
      expect(card!.classList.contains('card-sage'), `${id} card is .card-sage`).toBe(true)
      expect(leafTexts(card!), `${id} card, in order`).toEqual([v.cardTitle, 'Sample data', v.overview, v.cardHead, ...v.rows.flat(), V_FOOT])
      expect(norm(card!.querySelector('.t-card-title')?.textContent), `${id} card title`).toBe(v.cardTitle)
      expect(norm(card!.querySelector('.t-meta')?.textContent), `${id} card tag`).toBe('Sample data')
      expect(norm(card!.querySelector('.t-step')?.textContent), `${id} card overview`).toBe(v.overview)
      const tile = card!.querySelector<HTMLElement>('.ds-icontile--primary')
      expect(tile, `${id} icon tile`).not.toBeNull()
      expect([tile!.style.width, paths(tile)], `${id} icon tile size and glyph`).toEqual(['36px', GLYPHS[v.cardIcon]])

      const outside = (sel: string) => Array.from(p.querySelectorAll(sel)).filter((el) => !card!.contains(el))
      expect(outside('.t-step').map((el) => norm(el.textContent)), `${id} label`).toEqual([v.label])
      expect(outside('h3.t-h3').map((el) => norm(el.textContent)), `${id} h3`).toEqual([v.h3])
      expect(outside('p.t-body').map((el) => norm(el.textContent)), `${id} body`).toEqual([v.body])
      expect(Array.from(p.querySelectorAll('.ds-checklist-item'), (el) => norm(el.textContent)), `${id} points`).toEqual(v.points)
      expect(Array.from(p.querySelectorAll('button'), (b) => norm(b.textContent)), `${id} CTA is the one button`).toEqual([v.cta])
    }
  })

  it('SL-08 row badges follow V’s tones', () => {
    const badges = Array.from(view.container.querySelectorAll('[data-sol-card] .ds-badge'))
    expect(badges, 'three rows in each of three cards').toHaveLength(9)
    const want = ORDER.flatMap((id) => V_SOL[id].rows.map(([, status]) => [status, `ds-badge ds-badge--${V_TONE[status]}`]))
    expect(badges.map((b) => [norm(b.textContent), b.className])).toEqual(want)
    expect(view.container.querySelectorAll('.ds-badge-dot'), 'no badge holds a dot').toHaveLength(0)
  })

  it('SL-10 panels are tabpanels linked to their tabs', () => {
    const t = tabs()
    expect(new Set(t.map((x) => x.id)).size, 'three distinct, non-empty tab ids').toBe(3)
    expect(t.every((x) => x.id !== '')).toBe(true)
    t.forEach((tab, i) => {
      const id = ORDER[i]
      const target = tab.getAttribute('aria-controls')
      expect(target, `${id} tab names its panel`).toBeTruthy()
      const panel = document.getElementById(target!)
      expect(panel, `${id} aria-controls resolves`).not.toBeNull()
      expect([panel!.getAttribute('role'), panel!.dataset.solPanel], `${id} panel role and id`).toEqual(['tabpanel', id])
      expect(document.getElementById(panel!.getAttribute('aria-labelledby') ?? ''), `${id} panel is labelled by its tab`).toBe(tab)
    })
  })

  it('SL-10b the selected tab controls the panel that is visible', () => {
    for (const id of ['dev', 'firm', 'fin'] as const) {
      click(tabs()[ORDER.indexOf(id)])
      const selected = tabs()[selectedIndex(tabs())]
      const [panel] = visiblePanels()
      expect(panel.id, `${id}: the visible panel has an id`).not.toBe('')
      expect(selected.getAttribute('aria-controls'), `${id}: the selected tab controls the visible panel`).toBe(panel.id)
      expect(panel.getAttribute('aria-labelledby'), `${id}: the visible panel is labelled by the selected tab`).toBe(selected.id)
    }
  })

  it('SL-11 two bands in one document share no id', async () => {
    const second = mountView()
    try {
      await show(second, createElement(Solutions, { onBookDemo: () => undefined }))
      const ids = [view, second].flatMap((v) => Array.from(v.container.querySelectorAll('[role=tab], [role=tabpanel]'), (el) => el.id))
      expect(ids, 'both bands carry a non-empty id on 3 tabs and 3 panels each').toHaveLength(12)
      expect(ids.every((id) => id !== ''), 'no id is empty').toBe(true)
      expect(new Set(ids).size, 'no id repeats across the two bands').toBe(ids.length)
      for (const v of [view, second]) {
        for (const tab of tabsIn(v.container)) {
          expect(v.container.querySelector(`[id="${tab.getAttribute('aria-controls')}"]`), 'aria-controls resolves inside its own band').not.toBeNull()
        }
      }
    } finally {
      unmountView(second)
    }
  })
})
