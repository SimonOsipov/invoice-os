// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'
import { App } from './App'
import { TEST_DATA } from './testData'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

describe('App at /subscribe', () => {
  it('resolve_subscribeRendersTheForm', async () => {
    window.history.replaceState(null, '', '/subscribe')
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    await act(async () => root.render(<App load={() => Promise.resolve(TEST_DATA)} />))
    const main = container.querySelector('#blog-main')!
    expect(main.querySelector('h1')?.textContent).toBe('New posts,on your schedule.')
    const chips = [...main.querySelectorAll('button[aria-pressed]')].filter((b) =>
      TEST_DATA.categories.some((c) => c.name === b.textContent),
    )
    expect(chips).toHaveLength(TEST_DATA.categories.length)
    const sidebarSubscribe = [...container.querySelectorAll('.asc-app > aside button')].filter((b) => b.textContent?.includes('Subscribe'))
    expect(sidebarSubscribe.length).toBeGreaterThan(0)
    expect([...main.querySelectorAll('button')].some((b) => b.textContent === 'Subscribe')).toBe(true)
  })
})
