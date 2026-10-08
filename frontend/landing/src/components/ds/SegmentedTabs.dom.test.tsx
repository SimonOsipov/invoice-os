// @vitest-environment jsdom
// Interactive contract of SegmentedTabs (jsdom). Source of truth: the DS SegmentedTabs.jsx
// and the story's decisions on it (tablist of tab buttons, no panel; id and aria-controls only with idBase; keys shared with Tabs). Controlled only.
import { createElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  click,
  expectProbesSeeBrokenKeys,
  focusedIndex,
  load,
  mountView,
  press,
  selectedIndex,
  show,
  spyConsoleError,
  stateful,
  tabsIn,
  unmountView,
  type View,
} from './dsDom.test.util'

const OPTIONS = [
  { id: 'firm', label: 'Firms' },
  { id: 'fin', label: 'Fintech' },
  { id: 'dev', label: 'Developers' },
]

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

async function mount(props: Record<string, unknown>) {
  const SegmentedTabs = await load('SegmentedTabs', 'SegmentedTabs')
  await show(view, createElement(SegmentedTabs, { options: OPTIONS, 'aria-label': 'Audience', onChange: () => undefined, ...props }))
  return view.container
}

describe('SegmentedTabs', () => {
  it('SG-01 SegmentedTabs is a labelled tablist of tab buttons; ids and aria-controls only with idBase', async () => {
    const c = await mount({ value: 'firm' })
    const lists = c.querySelectorAll('[role=tablist][aria-label=Audience]')
    expect(lists, 'one labelled tablist').toHaveLength(1)
    expect(c.querySelectorAll('[role=tablist]'), 'and no other tablist').toHaveLength(1)
    const tabs = Array.from(lists[0].querySelectorAll('button[type=button][role=tab]'))
    expect(tabs, 'three type=button tabs').toHaveLength(3)
    expect(tabs.map((t) => t.textContent)).toEqual(['Firms', 'Fintech', 'Developers'])
    expect(c.querySelectorAll('[role=tabpanel]'), 'renders no panel').toHaveLength(0)
    expect(tabs.map((t) => t.hasAttribute('aria-controls')), 'knows no panel id without idBase').toEqual([false, false, false])
    expect(tabs.map((t) => t.hasAttribute('id')), 'has no tab id without idBase').toEqual([false, false, false])
    expect(lists[0].classList.contains('ds-seg'), 'group has .ds-seg').toBe(true)
    expect(tabs.every((t) => t.classList.contains('ds-seg-btn')), 'tabs have .ds-seg-btn').toBe(true)

    const based = await mount({ value: 'a', idBase: 'x', options: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }] })
    const basedTabs = tabsIn(based)
    expect(basedTabs, 'two tabs with idBase').toHaveLength(2)
    expect(basedTabs.map((t) => t.id), 'tab ids').toEqual(['x-tab-a', 'x-tab-b'])
    expect(basedTabs.map((t) => t.getAttribute('aria-controls')), 'aria-controls').toEqual(['x-panel-a', 'x-panel-b'])
  })

  it('SG-02 only the selected option is aria-selected and tabbable', async () => {
    const tabs = tabsIn(await mount({ value: 'firm' }))
    expect(tabs).toHaveLength(3)
    expect(tabs.map((t) => t.getAttribute('aria-selected'))).toEqual(['true', 'false', 'false'])
    expect(tabs.map((t) => t.tabIndex)).toEqual([0, -1, -1])
    const moved = tabsIn(await mount({ value: 'dev' }))
    expect(moved.map((t) => t.getAttribute('aria-selected'))).toEqual(['false', 'false', 'true'])
    expect(moved.map((t) => t.tabIndex)).toEqual([-1, -1, 0])
    const unknown = tabsIn(await mount({ value: 'missing' }))
    expect(unknown.map((t) => t.getAttribute('aria-selected'))).toEqual(['false', 'false', 'false'])
    expect(unknown.map((t) => t.tabIndex), 'an unknown value keeps the first option reachable').toEqual([0, -1, -1])
  })

  it('SG-03 a click reports the option', async () => {
    const onChange = vi.fn()
    const tabs = tabsIn(await mount({ value: 'fin', onChange }))
    expect(tabs).toHaveLength(3)
    expect(onChange).not.toHaveBeenCalled()
    click(tabs[2])
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledWith('dev')
    expect(selectedIndex(tabsIn(view.container)), 'controlled: value alone moves selection').toBe(1)
  })

  it('SG-04 an option click inside a form submits nothing', async () => {
    const SegmentedTabs = await load('SegmentedTabs', 'SegmentedTabs')
    const submit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault())
    const form = (inner: ReturnType<typeof createElement>) => createElement('form', { onSubmit: submit }, inner)

    await show(view, form(createElement('button', { role: 'tab' }, 'planted: no type')))
    click(view.container.querySelector('button') as HTMLElement)
    expect(submit, 'planted untyped button does submit').toHaveBeenCalledTimes(1)
    submit.mockClear()

    await show(view, form(createElement(SegmentedTabs, { options: OPTIONS, 'aria-label': 'Audience', value: 'fin', onChange: () => undefined })))
    const tabs = tabsIn(view.container)
    expect(tabs).toHaveLength(3)
    for (const tab of tabs) click(tab)
    expect(submit).not.toHaveBeenCalled()
  })

  it('SG-05 arrow keys move selection and focus with wrap', async () => {
    await expectProbesSeeBrokenKeys(view)
    const spy = vi.fn()
    const SegmentedTabs = await load('SegmentedTabs', 'SegmentedTabs')
    await show(view, stateful(SegmentedTabs, { options: OPTIONS, 'aria-label': 'Audience' }, 'dev', spy))
    let tabs = tabsIn(view.container)
    expect(tabs).toHaveLength(3)
    tabs[2].focus()
    expect(focusedIndex(tabs), 'focus starts on the selected option').toBe(2)
    const steps: [string, number][] = [
      ['ArrowRight', 0],
      ['End', 2],
      ['Home', 0],
      ['ArrowLeft', 2],
      ['ArrowLeft', 1],
      ['ArrowRight', 2],
    ]
    steps.forEach(([key, to], n) => {
      press(document.activeElement as Element, key)
      tabs = tabsIn(view.container)
      expect(selectedIndex(tabs), `${key} #${n} selects`).toBe(to)
      expect(focusedIndex(tabs), `${key} #${n} focuses`).toBe(to)
      expect(tabs.map((t) => t.tabIndex), `${key} #${n} roving tabIndex`).toEqual(tabs.map((_, i) => (i === to ? 0 : -1)))
      expect(spy, `${key} #${n} calls onChange once`).toHaveBeenCalledTimes(n + 1)
      expect(spy).toHaveBeenLastCalledWith(OPTIONS[to].id)
    })
  })
})
