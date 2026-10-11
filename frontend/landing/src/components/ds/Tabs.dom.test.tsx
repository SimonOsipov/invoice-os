// @vitest-environment jsdom
// Interactive contract of Tabs (jsdom). Source of truth: the DS Tabs.jsx and the
// story's § Design Tabs row (WAI-ARIA tabs, automatic activation, wrap, ids from useId). Controlled only: value + onChange.
import { createElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  click,
  expectProbesSeeBrokenKeys,
  fire,
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

const TABS = [
  { id: 'validate', label: 'Validate', step: '01' },
  { id: 'approve', label: 'Approve', step: '02' },
  { id: 'submit', label: 'Submit', step: '03' },
]
const IDS = TABS.map((t) => t.id)
const body = () => createElement('p', null, 'Panel body')

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
  const Tabs = await load('Tabs', 'Tabs')
  await show(view, createElement(Tabs, { tabs: TABS, onChange: () => undefined, ...props }, body()))
  return view.container
}

describe('Tabs', () => {
  it('TB-01 Tabs renders a tablist with one tab per entry and a mono step', async () => {
    const c = await mount({ value: 'validate' })
    const lists = c.querySelectorAll('[role=tablist]')
    expect(lists, 'one tablist').toHaveLength(1)
    const tabs = Array.from(lists[0].querySelectorAll('button[role=tab][type=button]'))
    expect(tabs, 'three type=button tabs inside the tablist').toHaveLength(3)
    expect(lists[0].querySelectorAll('[role=tab]'), 'no tab outside a button').toHaveLength(3)
    tabs.forEach((tab, i) => {
      expect(tab.classList.contains('ds-tab'), `tab ${i} has .ds-tab`).toBe(true)
      const steps = tab.querySelectorAll('.ds-tab-step')
      expect(steps, `tab ${i} holds one .ds-tab-step`).toHaveLength(1)
      expect(steps[0].textContent, `tab ${i} step`).toBe(TABS[i].step)
      expect(tab.textContent, `tab ${i} text`).toBe(TABS[i].step + TABS[i].label)
    })
    expect(lists[0].classList.contains('ds-tabs-list'), 'tablist has .ds-tabs-list').toBe(true)
  })

  it('TB-02 only the selected tab is aria-selected and tabbable', async () => {
    const tabs = tabsIn(await mount({ value: 'approve' }))
    expect(tabs).toHaveLength(3)
    expect(tabs.map((t) => t.getAttribute('aria-selected'))).toEqual(['false', 'true', 'false'])
    expect(tabs.map((t) => t.tabIndex)).toEqual([-1, 0, -1])
  })

  it('TB-03 the tabs and the panel name each other', async () => {
    const c = await mount({ value: 'approve' })
    const tabs = tabsIn(c)
    const panels = c.querySelectorAll('[role=tabpanel]')
    expect(tabs).toHaveLength(3)
    expect(panels, 'one tabpanel').toHaveLength(1)
    const panel = panels[0] as HTMLElement
    expect(panel.id, 'panel has an id').not.toBe('')
    expect(panel.classList.contains('ds-tabs-panel'), 'panel has .ds-tabs-panel').toBe(true)
    expect(panel.tabIndex, 'panel is a tab stop').toBe(0)
    expect(tabs.map((t) => t.getAttribute('aria-controls'))).toEqual([panel.id, panel.id, panel.id])
    expect(new Set(tabs.map((t) => t.id)).size, 'three distinct non-empty tab ids').toBe(3)
    expect(tabs.every((t) => t.id !== '')).toBe(true)
    expect(panel.getAttribute('aria-labelledby')).toBe(tabs[1].id)
    expect(document.getElementById(panel.getAttribute('aria-labelledby') as string)).toBe(tabs[1])

    const Tabs = await load('Tabs', 'Tabs')
    await show(view, createElement(Tabs, { tabs: TABS, value: 'submit', onChange: () => undefined }, body()))
    const after = tabsIn(view.container)
    expect(view.container.querySelector('[role=tabpanel]')?.getAttribute('aria-labelledby'), 'follows the selection').toBe(after[2].id)
  })

  it('TB-04 a click selects a tab', async () => {
    const onChange = vi.fn()
    const tabs = tabsIn(await mount({ value: 'validate', onChange }))
    expect(tabs).toHaveLength(3)
    expect(onChange).not.toHaveBeenCalled()
    click(tabs[2])
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledWith('submit')
    expect(selectedIndex(tabsIn(view.container)), 'controlled: value alone moves selection').toBe(0)
    click(tabs[0])
    expect(onChange, 'the selected tab reports itself too').toHaveBeenCalledTimes(2)
    expect(onChange).toHaveBeenLastCalledWith('validate')
    expect(selectedIndex(tabsIn(view.container))).toBe(0)
  })

  it('TB-05 arrow keys move selection and focus, wrapping at both ends', async () => {
    await expectProbesSeeBrokenKeys(view)
    const spy = vi.fn()
    const Tabs = await load('Tabs', 'Tabs')
    await show(view, stateful(Tabs, { tabs: TABS }, 'submit', spy, body()))
    let tabs = tabsIn(view.container)
    expect(tabs).toHaveLength(3)
    tabs[2].focus()
    expect(focusedIndex(tabs), 'focus starts on the selected tab').toBe(2)
    const steps: [string, number][] = [
      ['ArrowRight', 0],
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
      expect(spy).toHaveBeenLastCalledWith(IDS[to])
    })
  })

  it('TB-06 Home and End jump to the ends', async () => {
    const spy = vi.fn()
    const Tabs = await load('Tabs', 'Tabs')
    await show(view, stateful(Tabs, { tabs: TABS }, 'approve', spy, body()))
    let tabs = tabsIn(view.container)
    expect(tabs).toHaveLength(3)
    tabs[1].focus()
    for (const [key, to] of [['Home', 0], ['End', 2], ['Home', 0]] as const) {
      press(document.activeElement as Element, key)
      tabs = tabsIn(view.container)
      expect(selectedIndex(tabs), `${key} selects`).toBe(to)
      expect(focusedIndex(tabs), `${key} focuses`).toBe(to)
    }
    expect(spy.mock.calls.map((a) => a[0])).toEqual(['validate', 'submit', 'validate'])
  })

  it('TB-07 an unknown value keeps the first tab reachable', async () => {
    const c = await mount({ value: 'missing' })
    const tabs = tabsIn(c)
    expect(tabs).toHaveLength(3)
    expect(tabs.map((t) => t.tabIndex)).toEqual([0, -1, -1])
    expect(tabs.map((t) => t.getAttribute('aria-selected'))).toEqual(['false', 'false', 'false'])
    expect(c.querySelector('[role=tabpanel]')?.getAttribute('tabindex'), 'the panel stays a tab stop').toBe('0')
  })

  it('TB-08 panelStyle and children land in the panel', async () => {
    const c = await mount({ value: 'approve', panelStyle: { padding: '48px' } })
    const panel = c.querySelector('[role=tabpanel]') as HTMLElement
    expect(panel).not.toBeNull()
    expect(panel.style.padding).toBe('48px')
    expect(panel.textContent).toContain('Panel body')
    await mount({ value: 'approve' })
    expect((view.container.querySelector('[role=tabpanel]') as HTMLElement).style.padding, 'no inline padding by default').toBe('')
  })

  it('TB-09 the step defaults to the padded index and className is passed through', async () => {
    const bare = TABS.map(({ id, label }) => ({ id, label }))
    const c = await mount({ tabs: bare, value: 'validate', className: 'a-tabs' })
    const steps = Array.from(c.querySelectorAll('.ds-tab-step')).map((s) => s.textContent)
    expect(steps).toEqual(['01', '02', '03'])
    await mount({ tabs: [{ id: 'a', label: 'A', step: 'X' }, { id: 'b', label: 'B' }], value: 'a' })
    expect(Array.from(view.container.querySelectorAll('.ds-tab-step')).map((s) => s.textContent), 'an explicit step wins; a bare tab still pads its index').toEqual(['X', '02'])
    await mount({ tabs: bare, value: 'validate', className: 'a-tabs' })
    const root = c.firstElementChild as HTMLElement
    expect(root.tagName).toBe('DIV')
    expect(Array.from(root.classList)).toEqual(['ds-tabs', 'a-tabs'])
    await mount({ tabs: bare, value: 'validate' })
    expect(Array.from((view.container.firstElementChild as HTMLElement).classList), 'no className adds no token').toEqual(['ds-tabs'])
  })

  it('TB-10 keys that are not navigation leave selection and focus alone', async () => {
    const spy = vi.fn()
    const Tabs = await load('Tabs', 'Tabs')
    await show(view, stateful(Tabs, { tabs: TABS }, 'approve', spy, body()))
    const tabs = tabsIn(view.container)
    expect(tabs).toHaveLength(3)
    tabs[1].focus()
    press(tabs[1], 'ArrowRight')
    expect(spy, 'a navigation key does call onChange').toHaveBeenCalledTimes(1)
    spy.mockClear()
    const now = tabsIn(view.container)
    for (const key of ['a', 'Tab', 'Escape', 'ArrowUp', 'ArrowDown', 'Enter', ' ', 'PageDown']) {
      expect(fire(now[2], key).defaultPrevented, `${JSON.stringify(key)} is left to the browser`).toBe(false)
    }
    expect(spy).not.toHaveBeenCalled()
    expect(selectedIndex(tabsIn(view.container))).toBe(2)
  })

  it('TB-11 a navigation key is consumed so the page does not scroll', async () => {
    const Tabs = await load('Tabs', 'Tabs')
    await show(view, stateful(Tabs, { tabs: TABS }, 'approve', vi.fn(), body()))
    const tabs = tabsIn(view.container)
    expect(tabs).toHaveLength(3)
    tabs[1].focus()
    for (const key of ['ArrowRight', 'ArrowLeft', 'Home', 'End']) {
      expect(fire(tabs[1], key).defaultPrevented, `${key} is consumed`).toBe(true)
    }
  })

  it('TB-12 a modified arrow, Home or End is left to the browser', async () => {
    const spy = vi.fn()
    const Tabs = await load('Tabs', 'Tabs')
    await show(view, stateful(Tabs, { tabs: TABS }, 'approve', spy, body()))
    const tabs = tabsIn(view.container)
    expect(tabs).toHaveLength(3)
    tabs[1].focus()
    for (const mod of ['ctrlKey', 'altKey', 'metaKey']) {
      for (const key of ['ArrowRight', 'ArrowLeft', 'Home', 'End']) {
        expect(fire(tabs[1], key, { [mod]: true }).defaultPrevented, `${mod}+${key} is not consumed`).toBe(false)
      }
    }
    expect(spy, 'no modified key changes the selection').not.toHaveBeenCalled()
    expect(selectedIndex(tabsIn(view.container))).toBe(1)
    expect(focusedIndex(tabsIn(view.container))).toBe(1)
    fire(tabs[1], 'ArrowRight')
    expect(spy, 'the same key unmodified does navigate').toHaveBeenCalledTimes(1)
  })

  it('TB-13 a single tab keeps selection and focus on every navigation key', async () => {
    const spy = vi.fn()
    const Tabs = await load('Tabs', 'Tabs')
    await show(view, stateful(Tabs, { tabs: [{ id: 'only', label: 'Only' }] }, 'only', spy, body()))
    const tabs = tabsIn(view.container)
    expect(tabs).toHaveLength(1)
    tabs[0].focus()
    for (const key of ['ArrowRight', 'ArrowLeft', 'Home', 'End']) {
      press(document.activeElement as Element, key)
      expect(selectedIndex(tabsIn(view.container)), `${key} keeps the selection`).toBe(0)
      expect(focusedIndex(tabsIn(view.container)), `${key} keeps the focus`).toBe(0)
    }
    expect(spy.mock.calls.length, 'each key reports').toBe(4)
    expect(spy.mock.calls.every((a) => a[0] === 'only'), 'and only ever the one id').toBe(true)
  })

  it('TB-14 a re-render that changes the tab list keeps ids, selection and key targets in step', async () => {
    const onChange = vi.fn()
    const Tabs = await load('Tabs', 'Tabs')
    const render = (tabs: { id: string; label: string }[], value: string) =>
      show(view, createElement(Tabs, { tabs, value, onChange }, body()))
    const [validate, approve, submit] = TABS
    const panel = () => view.container.querySelector('[role=tabpanel]') as HTMLElement

    await render([validate, approve, submit], 'approve')
    const before = tabsIn(view.container)[1].id

    await render([submit, validate, approve], 'approve')
    let tabs = tabsIn(view.container)
    expect(tabs.map((t) => t.getAttribute('aria-selected')), 'selection follows the id, not the index').toEqual(['false', 'false', 'true'])
    expect(tabs.map((t) => t.tabIndex)).toEqual([-1, -1, 0])
    expect(tabs[2].id, 'a tab keeps its id when it moves').toBe(before)
    expect(panel().getAttribute('aria-labelledby')).toBe(before)

    await render([validate, submit], 'approve')
    tabs = tabsIn(view.container)
    expect(tabs).toHaveLength(2)
    expect(tabs.map((t) => t.getAttribute('aria-selected')), 'a removed selection leaves none selected').toEqual(['false', 'false'])
    expect(tabs.map((t) => t.tabIndex), 'and the first tab stays reachable').toEqual([0, -1])
    expect(panel().hasAttribute('aria-labelledby'), 'and the panel names no tab').toBe(false)
    tabs[0].focus()
    press(tabs[0], 'ArrowRight')
    expect(onChange).toHaveBeenLastCalledWith('submit')
    expect(focusedIndex(tabsIn(view.container)), 'a key moves focus within the shrunk list').toBe(1)

    await render([validate, approve, submit, { id: 'extra', label: 'Extra' }], 'validate')
    tabs = tabsIn(view.container)
    expect(tabs).toHaveLength(4)
    tabs[0].focus()
    press(tabs[0], 'End')
    expect(onChange).toHaveBeenLastCalledWith('extra')
    expect(focusedIndex(tabsIn(view.container)), 'End reaches a tab added by a re-render').toBe(3)
  })

  it('TB-15 two Tabs on one page never share an id', async () => {
    const Tabs = await load('Tabs', 'Tabs')
    const one = () => createElement(Tabs, { tabs: TABS, value: 'approve', onChange: () => undefined }, body())
    await show(view, createElement('div', null, createElement(one), createElement(one)))
    const roots = Array.from(view.container.querySelectorAll('.ds-tabs'))
    expect(roots, 'two Tabs mounted').toHaveLength(2)
    const ids = Array.from(view.container.querySelectorAll('[id]')).map((e) => e.id)
    expect(ids, 'two panels and six tabs carry ids').toHaveLength(8)
    expect(new Set(ids).size, 'every id is unique on the page').toBe(8)
    for (const root of roots) {
      const panel = root.querySelector('[role=tabpanel]') as HTMLElement
      for (const tab of tabsIn(root)) expect(document.getElementById(tab.getAttribute('aria-controls') as string), 'aria-controls resolves inside its own Tabs').toBe(panel)
      expect(root.contains(document.getElementById(panel.getAttribute('aria-labelledby') as string))).toBe(true)
    }
  })

  it('TB-16 Tabs names its tablist only when asked', async () => {
    const named = await mount({ value: 'approve', 'aria-label': 'Steps' })
    const list = named.querySelector('[role=tablist]')
    expect(list?.getAttribute('aria-label')).toBe('Steps')
    expect(named.querySelectorAll('[aria-label]'), 'only the tablist carries the name').toHaveLength(1)

    const bare = await mount({ value: 'approve' })
    const unnamed = bare.querySelector('[role=tablist]')
    expect(unnamed, 'control: the tablist rendered').not.toBeNull()
    expect(unnamed?.hasAttribute('aria-label')).toBe(false)
  })

  it('TB-17 the panel is a tab stop and a labelled tabpanel', async () => {
    const c = await mount({ value: 'approve' })
    const panel = c.querySelector('[role=tabpanel]')
    expect(panel, 'control: the panel rendered').not.toBeNull()
    expect(panel?.getAttribute('tabindex')).toBe('0')
    expect(panel?.getAttribute('aria-labelledby')).toBe(tabsIn(c)[1].id)
    expect(tabsIn(c)[1].getAttribute('aria-controls')).toBe(panel?.id)
  })
})
