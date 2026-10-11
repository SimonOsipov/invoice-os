// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
/// <reference types="node" />
// ?demo and ?register open their modals at mount. Setup mirrors App.register.dom.test.tsx.
import { StrictMode, act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ConsentStore } from './consent'

vi.mock('./analytics', async (orig) => ({ ...(await orig<typeof import('./analytics')>()), trackDemoOpen: vi.fn() }))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DIALOG = '[role="dialog"]'
const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'

function memoryStorage(): ConsentStore {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => {
      map.set(k, String(v))
    },
  }
}

let container: HTMLDivElement
let root: Root
let originalStorage: PropertyDescriptor | undefined
let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  Object.defineProperty(globalThis, 'localStorage', { value: memoryStorage(), configurable: true, writable: true })
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
  vi.stubEnv('VITE_APP_URL', 'https://app.x')
  vi.stubEnv('VITE_REGISTRATION_OPEN', 'true')
  fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
  vi.stubGlobal('fetch', fetchMock)
  vi.resetModules()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage)
  else delete (globalThis as { localStorage?: unknown }).localStorage
  window.history.replaceState(null, '', '/')
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

async function bootAt(path: string, strict = false): Promise<void> {
  window.history.replaceState(null, '', path)
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(strict ? createElement(StrictMode, null, createElement(mod.default)) : createElement(mod.default))
  })
}

const dialogs = () => Array.from(document.querySelectorAll<HTMLElement>(DIALOG)).map((d) => d.getAttribute('aria-label'))

describe('deep links', () => {
  it('deepLink_demoOpensTheDemoModal', async () => {
    await bootAt('/?demo')
    expect(dialogs()).toEqual(['Book a demo'])
    expect(window.location.search).toBe('')
  })

  it('deepLink_registerOpensRegistrationWhenOpen', async () => {
    await bootAt(`/?state=${STATE}&register`)
    expect(dialogs()).toEqual(['Create an account'])
    expect(window.location.search).toBe('')
  })

  it('deepLink_registerKeepsTheHeldState', async () => {
    await bootAt(`/?state=${STATE}&register`)
    await act(async () => document.querySelector<HTMLElement>(`${DIALOG} [aria-label="Close"], ${DIALOG} button.close`)?.click())
    if (dialogs().length) await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(dialogs(), 'the register dialog closed').toEqual([])
    const login = Array.from(document.querySelectorAll<HTMLButtonElement>('header button')).find((b) => b.textContent?.trim() === 'Sign in')!
    await act(async () => login.click())
    const d = document.querySelector<HTMLElement>(DIALOG)!
    const cont = Array.from(d.querySelectorAll<HTMLButtonElement>('button')).find((b) => b.textContent?.includes('Continue with email'))
    if (cont) await act(async () => cont.click())
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    const email = d.querySelector<HTMLInputElement>('input[type="email"]')!
    const password = d.querySelector<HTMLInputElement>('input[type="password"]')!
    await act(async () => {
      setValue.call(email, 'ada@okafor.ng')
      email.dispatchEvent(new Event('input', { bubbles: true }))
      setValue.call(password, 'pw')
      password.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string).state).toBe(STATE)
  })

  it('deepLink_registerFallsBackToDemoWhenClosed', async () => {
    vi.stubEnv('VITE_REGISTRATION_OPEN', '')
    await bootAt('/?register')
    expect(dialogs()).toEqual(['Book a demo'])
  })

  it('deepLink_registerFallsBackToDemoWhenNoGateway', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    await bootAt('/?register')
    expect(dialogs()).toEqual(['Book a demo'])
  })

  it('deepLink_aSignInOutcomeWins', async () => {
    await bootAt(`/?state=${STATE}&signin=ready&register`)
    expect(dialogs()).toEqual(['Sign in'])
  })

  it('deepLink_registerWinsOverDemo', async () => {
    await bootAt('/?register&demo')
    expect(dialogs()).toEqual(['Create an account'])
  })

  it('deepLink_demoWinsNothingWhenRegistrationClosed', async () => {
    vi.stubEnv('VITE_REGISTRATION_OPEN', '')
    await bootAt('/?register&demo')
    expect(dialogs()).toEqual(['Book a demo'])
  })

  it.each(['/?demo=0', '/?demo=false'])('deepLink_anyValueOpens %s', async (path) => {
    await bootAt(path)
    expect(dialogs()).toEqual(['Book a demo'])
  })

  it('deepLink_stripKeepsOtherParams', async () => {
    await bootAt('/?demo&utm_source=lib#faq')
    expect(window.location.search).toBe('?utm_source=lib')
    expect(window.location.hash).toBe('#faq')
  })

  it('deepLink_sendsNoDemoOpenEvent', async () => {
    await bootAt('/?demo')
    const { trackDemoOpen } = await import('./analytics')
    expect(trackDemoOpen).not.toHaveBeenCalled()
    await act(async () => document.querySelector<HTMLElement>(DIALOG)!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    const book = Array.from(document.querySelectorAll<HTMLButtonElement>('header button')).find((b) => b.textContent?.trim() === 'Book a demo')!
    await act(async () => book.click())
    expect(trackDemoOpen).toHaveBeenCalledWith('nav')
  })

  it.each([
    ['/?demo', 'Book a demo'],
    ['/?register', 'Create an account'],
  ])('deepLink_strictModeDoubleInitSeesOneUrl %s', async (path, title) => {
    await bootAt(path, true)
    expect(dialogs()).toEqual([title])
    expect(window.location.search).toBe('')
  })

  it('deepLink_noParamOpensNothing', async () => {
    await bootAt('/')
    expect(dialogs()).toEqual([])
  })
})
