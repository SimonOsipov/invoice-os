// @vitest-environment jsdom
// Platform composed with the DS Tabs in jsdom: panel content per tab, key navigation, the panel link.
import { createElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { GLYPHS } from '../icons'
import { click, focusedIndex, mountView, press, selectedIndex, show, spyConsoleError, tabsIn, unmountView, type View } from './ds/dsDom.test.util'
import { Platform } from './Platform'
import { PLATFORM_COPY } from './Platform.copy.test.util'

let view: View
let consoleError: ReturnType<typeof spyConsoleError>

beforeEach(() => {
  view = mountView()
  consoleError = spyConsoleError()
})

afterEach(() => {
  expect(consoleError).not.toHaveBeenCalled()
  unmountView(view)
  vi.restoreAllMocks()
})

async function mount(onBookDemo: () => void = () => undefined) {
  await show(view, createElement(Platform, { onBookDemo }))
  return view.container
}

const norm = (s: string | null | undefined) => (s ?? '').replace(/\s+/g, ' ').trim()
const texts = (nodes: Iterable<Element>) => [...nodes].map((n) => norm(n.textContent))
const paths = (el: Element | null | undefined) => [...(el?.querySelectorAll('path') ?? [])].map((p) => p.getAttribute('d'))
const panelOf = (c: HTMLElement) => {
  const panels = c.querySelectorAll<HTMLElement>('[role=tabpanel]')
  expect(panels, 'one tabpanel').toHaveLength(1)
  return panels[0]
}

describe('PL-03 each tab shows its own panel, verbatim', () => {
  it.each(PLATFORM_COPY.map((p, i) => [p.id, i] as const))('%s: the panel holds its step, heading, body, tags, link and result card', async (_id, i) => {
    const c = await mount()
    const tabs = tabsIn(c)
    expect(tabs, 'control: three tabs').toHaveLength(3)
    const p = PLATFORM_COPY[i]
    click(tabs[i])

    const panel = panelOf(c)
    expect(panel.getAttribute('aria-labelledby')).toBe(tabs[i].id)
    expect(tabs[i].getAttribute('aria-selected')).toBe('true')

    expect(texts(panel.querySelectorAll('.t-step')), 'step label, then the result kind').toEqual([p.stepLabel, p.kind])
    const h3s = panel.querySelectorAll('h3.t-h3')
    expect(h3s).toHaveLength(1)
    expect(norm(h3s[0].textContent)).toBe(`${p.h1} ${p.h2}`)
    expect(texts(panel.querySelectorAll('p.t-body'))).toEqual([p.body])
    expect(texts(panel.querySelectorAll('.ds-tag'))).toEqual(p.tags)
    const links = panel.querySelectorAll('button.ds-btn--text')
    expect(links).toHaveLength(1)
    expect(norm(links[0].textContent)).toBe(p.link)

    const tiles = panel.querySelectorAll('.ds-icontile')
    expect(tiles).toHaveLength(1)
    expect(paths(tiles[0])).toEqual([...GLYPHS[p.cardIcon]])
    expect(texts(panel.querySelectorAll('.t-card-title'))).toEqual([p.cardTitle])
    expect(texts(panel.querySelectorAll('.t-caption'))).toEqual([p.sub])
    const kindLine = panel.querySelectorAll('.t-step')[1]
    expect(texts(kindLine.parentElement!.children), 'kind, result, sub in order').toEqual([p.kind, p.result, p.sub])

    const badges = panel.querySelectorAll('.ds-badge')
    expect(badges, 'four badges, all success').toHaveLength(4)
    expect(panel.querySelectorAll('.ds-badge--success')).toHaveLength(4)
    expect([...badges].map((b) => [norm(b.parentElement?.firstElementChild?.textContent), norm(b.textContent)])).toEqual(p.rows)
    expect(texts(panel.querySelectorAll('.ds-badge')), 'statuses').toEqual(p.rows.map((r) => r[1]))
    badges.forEach((b) => expect(paths(b.parentElement?.firstElementChild), 'row tick').toEqual([...GLYPHS['circle-check']]))

    expect(panel.textContent?.split('Clarity at every checkpoint.').length, 'the footer line, once').toBe(2)
    PLATFORM_COPY.filter((_, j) => j !== i).forEach((other) => {
      expect(panel.textContent, `no ${other.id} heading in the ${p.id} panel`).not.toContain(other.h1)
    })
  })
})

describe('PL-04 the keys drive the composed tabs', () => {
  it('moves selection, focus and the panel on ArrowRight, ArrowLeft with wrap, Home and End', async () => {
    const c = await mount()
    const h3 = () => norm(panelOf(c).querySelector('h3')?.textContent)
    const state = () => {
      const tabs = tabsIn(c)
      return { selected: selectedIndex(tabs), focused: focusedIndex(tabs), h3: h3(), labelledby: panelOf(c).getAttribute('aria-labelledby') === tabs[selectedIndex(tabs)].id }
    }
    const heading = (i: number) => `${PLATFORM_COPY[i].h1} ${PLATFORM_COPY[i].h2}`
    // Roving tabindex and aria-controls follow the selection after every key.
    const wiring = () => {
      const tabs = tabsIn(c)
      const panel = panelOf(c)
      expect(panel.id, 'the panel has an id').not.toBe('')
      return { tabIndexes: tabs.map((t) => t.tabIndex), controls: tabs.map((t) => t.getAttribute('aria-controls') === panel.id) }
    }
    const at = (i: number) => ({ tabIndexes: [0, 1, 2].map((j) => (j === i ? 0 : -1)), controls: [true, true, true] })
    expect(tabsIn(c), 'control: three tabs').toHaveLength(3)
    tabsIn(c)[0].focus()
    expect(state(), 'control: Validate selected and focused at rest').toEqual({ selected: 0, focused: 0, h3: heading(0), labelledby: true })
    expect(wiring()).toEqual(at(0))

    press(document.activeElement!, 'ArrowRight')
    expect(state()).toEqual({ selected: 1, focused: 1, h3: 'Every decision. A clear owner.', labelledby: true })
    expect(wiring()).toEqual(at(1))
    press(document.activeElement!, 'ArrowLeft')
    expect(state()).toEqual({ selected: 0, focused: 0, h3: heading(0), labelledby: true })
    press(document.activeElement!, 'ArrowLeft')
    expect(state(), 'ArrowLeft wraps to Submit').toEqual({ selected: 2, focused: 2, h3: heading(2), labelledby: true })
    expect(wiring()).toEqual(at(2))
    press(document.activeElement!, 'ArrowRight')
    expect(state(), 'ArrowRight wraps to Validate').toEqual({ selected: 0, focused: 0, h3: heading(0), labelledby: true })
    press(document.activeElement!, 'End')
    expect(state()).toEqual({ selected: 2, focused: 2, h3: heading(2), labelledby: true })
    press(document.activeElement!, 'Home')
    expect(state()).toEqual({ selected: 0, focused: 0, h3: heading(0), labelledby: true })
    expect(wiring()).toEqual(at(0))
    press(document.activeElement!, 'End')
    expect(state()).toEqual({ selected: 2, focused: 2, h3: heading(2), labelledby: true })
    expect(wiring()).toEqual(at(2))
  })
})

describe('PL-05 the panel link books a demo on every tab', () => {
  it('calls onBookDemo once per link click on each tab, and never for the tabs alone', async () => {
    const onBookDemo = vi.fn()
    const c = await mount(onBookDemo)
    const tabs = tabsIn(c)
    expect(tabs, 'control: three tabs').toHaveLength(3)

    tabs.forEach((t) => click(t))
    expect(onBookDemo, 'selecting the three tabs books nothing').not.toHaveBeenCalled()

    for (const [i, p] of PLATFORM_COPY.entries()) {
      click(tabs[i])
      const links = [...panelOf(c).querySelectorAll<HTMLButtonElement>('button.ds-btn--text')]
      expect(links.map((l) => norm(l.textContent))).toEqual([p.link])
      click(links[0])
      expect(onBookDemo, `after the ${p.id} link`).toHaveBeenCalledTimes(i + 1)
    }
  })
})

describe('PL-10 the panel is a tab stop named by its tab', () => {
  it.each([0, 1, 2])('after tab %i is selected, the stop after it is the panel, then the panel link', async (i) => {
    const c = await mount()
    const tabs = tabsIn(c)
    click(tabs[i])
    const panel = panelOf(c)
    expect(panel.getAttribute('tabindex')).toBe('0')
    expect(panel.getAttribute('aria-labelledby')).toBe(tabs[i].id)

    const stops = [...c.querySelectorAll<HTMLElement>('button, [tabindex]')].filter((el) => el.tabIndex >= 0)
    const at = stops.indexOf(tabs[i])
    expect(at, 'control: the selected tab is a stop').toBeGreaterThanOrEqual(0)
    expect(stops[at + 1]).toBe(panel)
    expect(stops[at + 2]).toBe(panel.querySelector('button.ds-btn--text'))
  })
})
