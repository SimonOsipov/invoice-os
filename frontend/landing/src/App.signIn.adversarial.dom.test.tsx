// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Adversarial coverage for landing's boot read and strip of `state` and `signin`.
/// <reference types="node" />
import { StrictMode, act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DIALOG = '[role="dialog"]'
const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
const OTHER = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn_-9'
const NO_WORKSPACE = 'This account has no workspace yet.'
const FAILED = "We couldn't open your workspace. Sign in again."

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>
let writes: unknown[]

function spyStore() {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => {
      writes.push([k, v])
      map.set(k, String(v))
    },
    removeItem: (k: string) => void map.delete(k),
    clear: () => map.clear(),
    key: () => null,
    get length() {
      return map.size
    },
  }
}

beforeEach(() => {
  writes = []
  vi.stubGlobal('localStorage', spyStore())
  vi.stubGlobal('sessionStorage', spyStore())
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
  vi.stubEnv('VITE_APP_URL', 'https://app.x')
  vi.resetModules()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  window.history.replaceState(null, '', '/')
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function bootAt(path: string, strict = false): Promise<void> {
  window.history.replaceState(null, '', path)
  expect(window.location.pathname + window.location.search + window.location.hash).toBe(path)
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(strict ? createElement(StrictMode, null, createElement(mod.default)) : createElement(mod.default))
  })
}

function dialogs(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>(DIALOG))
}

function onlyDialog(): HTMLElement {
  const d = dialogs()
  expect(d.length).toBe(1)
  return d[0]
}

function pageMounted(): void {
  expect(document.querySelectorAll('header button').length).toBeGreaterThan(0)
}

async function closeDialog(): Promise<void> {
  await act(async () => {
    onlyDialog().querySelector<HTMLButtonElement>('button[aria-label="Close"]')!.click()
  })
  expect(dialogs().length).toBe(0)
}

async function openFromNav(): Promise<void> {
  const b = Array.from(document.querySelectorAll('header button')).find((x) => x.textContent?.trim() === 'Explore the platform')
  expect(b).toBeDefined()
  await act(async () => (b as HTMLButtonElement).click())
}

describe('AUTH-05-07 adversarial: boot params', () => {
  it('a repeated state is ignored and every copy is stripped', async () => {
    await bootAt(`/?state=${STATE}&signin=ready&state=${OTHER}`)
    const d = onlyDialog()
    expect(d.querySelectorAll('input').length).toBe(0)
    expect(d.textContent).toContain('Continue with email')
    expect(window.location.search).toBe('')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('an empty signin is stripped and opens nothing', async () => {
    await bootAt('/?signin=&keep=1')
    pageMounted()
    expect(dialogs().length).toBe(0)
    expect(window.location.search).toBe('?keep=1')
  })

  it('a repeated signin is stripped whole, and the first value decides', async () => {
    await bootAt('/?a=1&signin=failed&b=2&signin=no-workspace')
    const alerts = Array.from(onlyDialog().querySelectorAll('[role="alert"]'))
    expect(alerts.length).toBe(1)
    expect(alerts[0].textContent).toContain(FAILED)
    expect(window.location.search).toBe('?a=1&b=2')
  })

  it('the strip keeps the path and the hash', async () => {
    await bootAt(`/?utm_source=x&state=${STATE}&signin=ready#pricing`)
    onlyDialog()
    expect(window.location.pathname).toBe('/')
    expect(window.location.search).toBe('?utm_source=x')
    expect(window.location.hash).toBe('#pricing')
  })

  it('a boot with neither param does not touch history', async () => {
    const replace = vi.spyOn(window.history, 'replaceState')
    await bootAt('/?verify=failed')
    replace.mockClear()
    const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
    await act(async () => root.unmount())
    root = createRoot(container)
    await act(async () => root.render(createElement(mod.default)))
    pageMounted()
    expect(replace).not.toHaveBeenCalled()
    expect(window.location.search).toBe('?verify=failed')
  })

  it('a malformed state alone opens nothing and is stripped', async () => {
    await bootAt('/?state=short')
    pageMounted()
    expect(dialogs().length).toBe(0)
    expect(window.location.search).toBe('')
  })

  it('configured: a front-door state alone opens nothing but is held', async () => {
    // Only a `signin` outcome opens the modal; the front-door bounce carries none.
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt(`/?state=${STATE}`)
    pageMounted()
    expect(dialogs().length).toBe(0)
    expect(window.location.search).toBe('')

    await openFromNav()
    const d = onlyDialog()
    expect(d.querySelectorAll('[role="alert"]').length).toBe(0)
    expect(d.textContent).not.toContain('Continue with email')
    const email = d.querySelectorAll<HTMLInputElement>('input[type="email"]')
    const password = d.querySelectorAll<HTMLInputElement>('input[type="password"]')
    expect(email.length).toBe(1)
    expect(password.length).toBe(1)
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    await act(async () => {
      setValue.call(email[0], 'ada@okafor.ng')
      email[0].dispatchEvent(new Event('input', { bubbles: true }))
      setValue.call(password[0], 'pw')
      password[0].dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect((JSON.parse(init.body as string) as { state: unknown }).state).toBe(STATE)
  })

  it('StrictMode: the boot read survives the double effect', async () => {
    await bootAt(`/?state=${STATE}&signin=no-workspace`, true)
    const d = onlyDialog()
    expect(d.querySelectorAll('input[type="password"]').length).toBe(1)
    expect(d.querySelector('[role="alert"]')?.textContent).toContain(NO_WORKSPACE)
    expect(window.location.search).toBe('')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('AUTH-05-07 adversarial: after boot', () => {
  it('the boot error clears on close, and the held state survives a reopen', async () => {
    await bootAt(`/?state=${STATE}&signin=failed`)
    expect(onlyDialog().querySelector('[role="alert"]')?.textContent).toContain(FAILED)
    await closeDialog()
    await openFromNav()
    const d = onlyDialog()
    expect(d.querySelectorAll('[role="alert"]').length).toBe(0)
    expect(d.querySelectorAll('input[type="password"]').length).toBe(1)
    expect(d.textContent).not.toContain('Continue with email')
  })

  it('no state is written to storage on any path', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(async () => new Response(JSON.stringify({ error: 'x' }), { status: 401, headers: { 'Content-Type': 'application/json' } }))
      .mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt(`/?state=${STATE}&signin=failed`)
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    for (let i = 0; i < 2; i++) {
      const d = onlyDialog()
      await act(async () => {
        for (const [sel, v] of [['input[type="email"]', 'ada@okafor.ng'], ['input[type="password"]', 'pw']]) {
          const el = d.querySelector<HTMLInputElement>(sel)!
          setValue.call(el, v)
          el.dispatchEvent(new Event('input', { bubbles: true }))
        }
      })
      await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
      await act(async () => {
        await new Promise((r) => setTimeout(r, 0))
      })
    }
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await closeDialog()
    await openFromNav()
    for (const w of writes) expect(JSON.stringify(w)).not.toContain(STATE)
    expect(document.cookie).not.toContain(STATE)
    expect(window.location.href).not.toContain(STATE)
  })
})

describe('AUTH-05-07 adversarial: unconfigured landing (D7: production unchanged until U2)', () => {
  it('unconfigured: a front-door state alone opens nothing', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    await bootAt(`/?state=${STATE}`)
    pageMounted()
    expect(dialogs().length).toBe(0)
    expect(window.location.search).toBe('')
  })
})
