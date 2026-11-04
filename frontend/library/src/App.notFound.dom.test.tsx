// @vitest-environment jsdom
import { act, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'
import { App } from './App'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const store = new Map<string, string>()
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    clear: () => store.clear(),
  },
})

let container: HTMLDivElement
let root: Root

function mount(path: string) {
  window.history.replaceState(null, '', path)
  localStorage.setItem('asc_consent', '{"analytics":false,"ts":"","v":1}')
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root.render(<StrictMode><App /></StrictMode>))
}

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  localStorage.clear()
})

const main = () => container.querySelector<HTMLElement>('main#lib-main')!
const byText = (sel: string, text: string) =>
  [...container.querySelectorAll<HTMLElement>(sel)].find((e) => e.textContent?.trim() === text)!
const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const popTo = (path: string) =>
  act(() => {
    window.history.pushState(null, '', path)
    window.dispatchEvent(new PopStateEvent('popstate'))
  })

describe('App not-found view', () => {
  it('inApp_popstateToUnknownShowsNotFound', () => {
    mount('/library/invoices')
    popTo('/library/nope')
    expect(main().querySelector('h1')!.textContent).toBe('Page not found')
    expect(byText('main a', 'Back to home').getAttribute('href')!.endsWith('/')).toBe(true)
    expect(byText('main button', 'Browse the library')).toBeTruthy()
  })

  it('inApp_browseGoesHome', () => {
    mount('/library/invoices')
    popTo('/library/nope')
    click(byText('main button', 'Browse the library'))
    expect(window.location.pathname).toBe('/library/')
    expect(container.textContent).toContain('Feature library')
    expect(container.textContent).not.toContain('Page not found')
  })

  it('inApp_notFoundActivatesNoGroup', () => {
    mount('/library/nope')
    expect(container.querySelectorAll('aside [id^="nav-"][style*="font-weight: 700"]')).toHaveLength(0)
    for (const s of container.querySelectorAll<HTMLElement>('.lib-stage')) expect(s.style.background).toBe('transparent')
    expect(container.querySelector('aside [aria-current="true"]')).toBeNull()
  })

  it('inApp_realPathsStillRender', () => {
    mount('/library/invoices')
    expect(main().querySelector('h2')!.textContent).toBeTruthy()
    expect(container.textContent).not.toContain('Page not found')
    act(() => root.unmount())
    container.remove()
    mount('/library/invoices/import-files')
    expect(main().querySelector('h2')).not.toBeNull()
    expect(container.textContent).not.toContain('Page not found')
  })
})
