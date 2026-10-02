// Shared DOM helpers for the interactive DS primitive tests (jsdom). Modules load through
// import.meta.glob so a missing file or export fails the test that names it.
import { act, createElement, useState, type ComponentType, type ReactElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { expect, vi } from 'vitest'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

export type Props = Record<string, unknown>

const modules = import.meta.glob('./*.tsx')

export async function load(file: string, name: string): Promise<ComponentType<Props>> {
  const loader = modules[`./${file}.tsx`]
  expect(loader, `components/ds/${file}.tsx must exist`).toBeDefined()
  const mod = (await loader()) as Record<string, unknown>
  expect(typeof mod[name], `${file}.tsx must export ${name}`).toBe('function')
  return mod[name] as ComponentType<Props>
}

export type View = { container: HTMLDivElement; root: Root }

export function mountView(): View {
  const container = document.createElement('div')
  document.body.appendChild(container)
  return { container, root: createRoot(container) }
}

export function unmountView(view: View): void {
  act(() => view.root.unmount())
  view.container.remove()
}

export async function show(view: View, el: ReactElement): Promise<void> {
  await act(async () => {
    view.root.render(el)
  })
}

export function press(el: Element, key: string): void {
  act(() => {
    el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
  })
}

export function click(el: HTMLElement): void {
  act(() => el.click())
}

export const tabsIn = (scope: ParentNode): HTMLButtonElement[] =>
  Array.from(scope.querySelectorAll<HTMLButtonElement>('[role=tab]'))

export const selectedIndex = (tabs: HTMLElement[]): number => tabs.findIndex((t) => t.getAttribute('aria-selected') === 'true')
export const focusedIndex = (tabs: HTMLElement[]): number => tabs.findIndex((t) => t === document.activeElement)

/** A component that holds `value` and feeds every `onChange(id)` back into it, reporting each call to `spy`. */
export function stateful(Comp: ComponentType<Props>, base: Props, initial: string, spy: (id: string) => void, ...children: ReactElement[]): ReactElement {
  function Harness() {
    const [value, setValue] = useState(initial)
    return createElement(
      Comp,
      {
        ...base,
        value,
        onChange: (id: string) => {
          spy(id)
          setValue(id)
        },
      },
      ...children,
    )
  }
  return createElement(Harness)
}

function naiveTablist(moveSelection: boolean): ReactElement {
  function Naive() {
    const [sel, setSel] = useState(0)
    return createElement(
      'div',
      { role: 'tablist' },
      [0, 1, 2].map((i) =>
        createElement(
          'button',
          {
            key: i,
            role: 'tab',
            type: 'button',
            'aria-selected': i === sel,
            tabIndex: i === sel ? 0 : -1,
            onKeyDown: (e: { key: string }) => {
              if (moveSelection && e.key === 'ArrowRight') setSel((sel + 1) % 3)
            },
          },
          `t${i}`,
        ),
      ),
    )
  }
  return createElement(Naive)
}

/** Planted controls: the selected/focused probes must see a tablist that ignores keys, and one that moves selection but not focus. */
export async function expectProbesSeeBrokenKeys(view: View): Promise<void> {
  for (const [moves, want] of [
    [false, { selected: 0, focused: 0 }],
    [true, { selected: 1, focused: 0 }],
  ] as const) {
    await show(view, naiveTablist(moves))
    const tabs = tabsIn(view.container)
    expect(tabs, 'planted tablist renders 3 tabs').toHaveLength(3)
    tabs[0].focus()
    press(tabs[0], 'ArrowRight')
    const now = tabsIn(view.container)
    expect({ selected: selectedIndex(now), focused: focusedIndex(now) }, `planted tablist, moveSelection ${moves}`).toEqual(want)
  }
}

export function spyConsoleError() {
  return vi.spyOn(console, 'error').mockImplementation(() => undefined)
}
