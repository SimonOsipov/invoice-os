// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The sign-in preflight settles late: a window opened meanwhile wins, and a bounce fires once per load.
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { captureNavigation } from './navigation.test.util'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DIALOG = '[role="dialog"]'

let container: HTMLDivElement
let root: Root
let restoreLocation: (() => void) | undefined
let assigned: string[]
let fetchMock: ReturnType<typeof vi.fn>
let settle: { resolve: () => void; reject: () => void }

function storage() {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => void map.set(k, String(v)),
    removeItem: (k: string) => void map.delete(k),
    clear: () => map.clear(),
    key: () => null,
    length: 0,
  }
}

beforeEach(async () => {
  vi.stubGlobal('localStorage', storage())
  vi.stubGlobal('sessionStorage', storage())
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
  vi.stubEnv('VITE_APP_URL', 'https://app.x')
  vi.resetModules()
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
  const nav = captureNavigation()
  restoreLocation = nav.restore
  assigned = nav.assigned
  fetchMock = vi.fn(
    () =>
      new Promise((resolve, reject) => {
        settle = { resolve: () => resolve(new Response(null)), reject: () => reject(new TypeError('Failed to fetch')) }
      }),
  )
  vi.stubGlobal('fetch', fetchMock)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(createElement(mod.default))
  })
})

afterEach(() => {
  restoreLocation?.()
  act(() => root.unmount())
  container.remove()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function click(root: ParentNode, text: string): Promise<void> {
  const b = Array.from(root.querySelectorAll('button')).find((x) => x.textContent?.trim() === text)
  expect(b, `button "${text}"`).toBeDefined()
  await act(async () => b!.click())
}

const nav = () => document.querySelector('header')!
const dialogs = () => Array.from(document.querySelectorAll<HTMLElement>(DIALOG))

async function flush(): Promise<void> {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
}

describe('a window opened during the preflight wins', () => {
  it('a failed preflight opens no sign-in window over the demo window', async () => {
    await click(nav(), 'Sign in')
    await click(nav(), 'Book a demo')
    expect(dialogs().length).toBe(1)
    await act(async () => settle.reject())
    await flush()
    expect(dialogs().length).toBe(1)
    expect(dialogs()[0].getAttribute('aria-label')).not.toBe('Sign in')
  })

  it('a good preflight does not navigate once the demo window is open', async () => {
    await click(nav(), 'Sign in')
    await click(nav(), 'Book a demo')
    await act(async () => settle.resolve())
    await flush()
    expect(assigned).toEqual([])
  })

  it('a dropped preflight frees the control for the next click', async () => {
    await click(nav(), 'Sign in')
    await click(nav(), 'Book a demo')
    await act(async () => settle.resolve())
    await flush()
    await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label="Close"]')!.click())
    await click(nav(), 'Sign in')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
})

describe('one bounce per load', () => {
  it('a second click after a good bounce starts no second preflight or navigation', async () => {
    await click(nav(), 'Sign in')
    await act(async () => settle.resolve())
    await flush()
    expect(assigned).toEqual(['https://app.x?auth=start'])
    await click(nav(), 'Sign in')
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(assigned.length).toBe(1)
  })

  it('a bfcache restore allows the next click to bounce again', async () => {
    await click(nav(), 'Sign in')
    await act(async () => settle.resolve())
    await flush()
    await act(async () => {
      const e = new Event('pageshow') as Event & { persisted: boolean }
      e.persisted = true
      window.dispatchEvent(e)
    })
    await click(nav(), 'Sign in')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('a failed bounce frees the control and shows the unavailable window', async () => {
    await click(nav(), 'Sign in')
    await act(async () => settle.reject())
    await flush()
    expect(dialogs().length).toBe(1)
  })
})
