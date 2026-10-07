// @vitest-environment jsdom
import { act, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root
let errorSpy: ReturnType<typeof vi.spyOn>

function mount(path: string) {
  window.history.replaceState(null, '', path)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root.render(<StrictMode><App /></StrictMode>))
}

beforeEach(() => {
  errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllEnvs()
  const errors = errorSpy.mock.calls.length
  errorSpy.mockRestore()
  expect(errors).toBe(0)
})

const byText = (sel: string, text: string) =>
  [...container.querySelectorAll<HTMLElement>(sel)].find((e) => e.textContent?.trim() === text)!
const nav = (gid: string) => container.querySelector<HTMLElement>(`#nav-${gid}`)!
const main = () => container.querySelector<HTMLElement>('main#lib-main')!
const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const card = (name: string) => [...container.querySelectorAll<HTMLElement>('.lib-card')].find((e) => e.textContent!.includes(name))!
const stage = (label: string) => [...container.querySelectorAll<HTMLElement>('.lib-stage')].find((e) => e.textContent!.includes(label))!
const h2s = () => [...main().querySelectorAll('h2')].map((h) => h.textContent)
const links = (text: string) => [...container.querySelectorAll('a')].filter((a) => a.textContent!.includes(text))

async function move(go: () => void) {
  const popped = new Promise<void>((r) => window.addEventListener('popstate', () => r(), { once: true }))
  await act(async () => {
    go()
    await popped
  })
}

describe('App', () => {
  it('root_isTheAscAppFrameWithTheSidebarAndTheScrollingMain', () => {
    mount('/')
    const app = container.firstElementChild as HTMLElement
    expect(app.className).toBe('asc-app')
    expect([app.style.height, app.style.display, app.style.overflow]).toEqual(['100vh', 'flex', 'hidden'])
    const aside = app.firstElementChild as HTMLElement
    expect(aside.tagName).toBe('ASIDE')
    expect(aside.style.width).toBe('288px')
    const m = main().style
    expect([m.flexGrow, m.minWidth, m.overflowY]).toEqual(['1', '0', 'auto'])
    expect(main().firstElementChild!.textContent).toContain('INVOICE JOURNEY')
  })

  it('boot_aPathOpensItsView', () => {
    mount('/rules')
    expect(nav('rules').style.fontWeight).toBe('700')
    expect(h2s()).toContain('Rules & validation')
    act(() => root.unmount())
    container.remove()

    mount('/clearance/submit-clear')
    expect(byText('aside button', 'Submit for FIRS clearance').style.color).toBe('var(--accent)')
    expect(stage('Clear').style.background).toBe('var(--sage-panel)')
    expect(stage('Validate').style.background).toBe('transparent')
    expect(main().querySelector('h2')).toBeNull()
    act(() => root.unmount())
    container.remove()

    mount('/')
    expect(container.textContent).toContain('Feature library')
  })

  it('boot_anUnknownPathShowsTheHomeAndKeepsTheUrl', () => {
    for (const path of ['/nope', '/invoices/learns']) {
      window.history.replaceState(null, '', path)
      const len = window.history.length
      mount(path)
      expect(container.textContent).toContain('Feature library')
      expect(window.location.pathname).toBe(path)
      expect(window.history.length).toBe(len)
      act(() => root.unmount())
      container.remove()
    }
    mount('/')
  })

  it('click_pushesOnePathPerView', () => {
    mount('/')
    const len = window.history.length
    click(card('Rules & validation'))
    expect(window.location.pathname).toBe('/rules')
    click(container.querySelector('#fc-validate')!)
    expect(window.location.pathname).toBe('/rules/validate')
    click(byText('aside button', 'Overview'))
    expect(window.location.pathname).toBe('/')
    expect(window.history.length).toBe(len + 3)
    click(stage('Validate'))
    expect(window.location.pathname).toBe('/rules')
    expect(window.history.length).toBe(len + 4)
    click(nav('rules'))
    expect(window.history.length).toBe(len + 4)
    act(() => root.unmount())
    container.remove()

    mount('/invoices/')
    const l2 = window.history.length
    click(nav('invoices'))
    expect(window.history.length).toBe(l2)
    expect(window.location.pathname).toBe('/invoices')
    act(() => root.unmount())
    container.remove()

    mount('/nope')
    const l3 = window.history.length
    click(byText('aside button', 'Overview'))
    expect(window.history.length).toBe(l3)
    expect(window.location.pathname).toBe('/')
    act(() => root.unmount())
    container.remove()

    mount('/rules')
    const l4 = window.history.length
    click(byText('aside button', 'Plain validation messages'))
    expect(window.location.pathname).toBe('/rules/validate')
    expect(window.history.length).toBe(l4 + 1)
  })

  it('backAndForward_restoreEachView', async () => {
    mount('/')
    click(card('Rules & validation'))
    click(container.querySelector('#fc-validate')!)
    click(byText('aside button', 'Overview'))
    const len = window.history.length
    const validate = () => byText('aside button', 'Plain validation messages')

    await move(() => window.history.back())
    expect(validate().style.color).toBe('var(--accent)')
    await move(() => window.history.back())
    expect(h2s()).toContain('Rules & validation')
    await move(() => window.history.forward())
    expect(validate().style.color).toBe('var(--accent)')
    expect(window.history.length).toBe(len)
  })

  it('navigation_scrollsMainToTheTop', async () => {
    mount('/')
    main().scrollTop = 500
    click(nav('audit'))
    expect(main().scrollTop).toBe(0)
    main().scrollTop = 500
    await move(() => window.history.back())
    expect(main().scrollTop).toBe(0)
    const len = window.history.length
    main().scrollTop = 500
    click(byText('aside button', 'Overview'))
    expect(main().scrollTop).toBe(0)
    expect(window.history.length).toBe(len)
  })

  it('links_followTheEnvironment', () => {
    vi.stubEnv('VITE_APP_URL', 'https://a.example')
    vi.stubEnv('VITE_LANDING_URL', 'https://l.example')
    mount('/')
    const demo = links('Book the Demo')
    expect(demo.map((a) => a.getAttribute('href'))).toEqual(['https://l.example/?demo', 'https://l.example/?demo'])
    click(nav('rules'))
    expect(links('Open in Platform').map((a) => a.getAttribute('href'))).toEqual(['https://a.example/rules?via=library'])
    for (const gid of ['notifications', 'settings']) {
      click(nav(gid))
      expect(links('Open in Platform')).toHaveLength(0)
    }
    act(() => root.unmount())
    container.remove()

    vi.stubEnv('VITE_APP_URL', '')
    vi.stubEnv('VITE_LANDING_URL', '')
    mount('/')
    expect(links('Book the Demo')).toHaveLength(0)
    click(nav('rules'))
    expect(links('Book the Demo')).toHaveLength(0)
    expect(links('Open in Platform')).toHaveLength(0)
  })

  it('featurePath_rendersNoOpenInPlatform', () => {
    vi.stubEnv('VITE_APP_URL', 'https://a.example')
    mount('/notifications/alerts')
    expect(links('Open in Platform')).toHaveLength(0)
    expect(byText('aside button', 'Alerts where you workComing soon').textContent).toContain('Coming soon')
    act(() => root.unmount())
    container.remove()

    mount('/rules/validate')
    expect(links('Open in Platform')).toHaveLength(0)
  })
})
