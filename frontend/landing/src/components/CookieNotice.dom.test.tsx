// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The spacer lives and dies with the notice: a spacer left behind is dead scroll room under the footer.
// Setup mirrors consentActions.dom.test.tsx: a memory store, because Node 25's own localStorage shadows jsdom's.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CONSENT_STORAGE_KEY, CONSENT_VERSION, type ConsentStore } from '../consent'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const NOTICE = '[aria-label="Cookie notice"]'
const GRANTED = JSON.stringify({ analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION })

let container: HTMLDivElement
let root: Root
let originalStorage: PropertyDescriptor | undefined

beforeEach(() => {
  document.head.innerHTML = ''
  originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  const map = new Map<string, string>()
  const store: ConsentStore = {
    getItem: (k) => (map.has(k) ? map.get(k)! : null),
    setItem: (k, v) => void map.set(k, String(v)),
  }
  Object.defineProperty(globalThis, 'localStorage', { value: store, configurable: true, writable: true, enumerable: true })
  vi.resetModules()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage)
  else delete (globalThis as { localStorage?: unknown }).localStorage
  vi.restoreAllMocks()
})

async function mountApp(stored?: string) {
  if (stored) globalThis.localStorage.setItem(CONSENT_STORAGE_KEY, stored)
  const mod = (await import('../App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => root.render(createElement(mod.default)))
}

async function click(find: () => Element | undefined | null, what: string) {
  const el = find()
  expect(el, `expected ${what}`).toBeTruthy()
  await act(async () => (el as HTMLElement).click())
}

const byText = (text: string) => () => Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.trim() === text)
const consent = (choice: string) => () => document.querySelector(`[data-consent="${choice}"]`)

function expectUp(up: boolean, when: string) {
  const want = up ? 1 : 0
  expect(document.querySelectorAll(NOTICE).length, `the notice ${when}`).toBe(want)
  expect(document.querySelectorAll('.cn-spacer').length, `the spacer ${when}`).toBe(want)
}

describe('the cookie notice and its spacer', () => {
  it('come and go together: first visit, a choice, a reopen, a second choice', async () => {
    await mountApp()
    expectUp(true, 'on a first visit')
    await click(consent('accept'), 'Accept')
    expectUp(false, 'after Accept')

    await click(byText('Cookie choices'), 'the footer control')
    expectUp(true, 'on a reopen')
    expect(document.querySelector(`${NOTICE} .cn-setting`)?.textContent).toBe('Analytics cookies are on.')
    await click(consent('reject'), 'Reject')
    expectUp(false, 'after the reopened choice')
  })

  it('are both absent when a choice is already stored', async () => {
    await mountApp(GRANTED)
    expect(document.querySelector('footer'), 'control: the page rendered').toBeTruthy()
    expectUp(false, 'with a stored choice')
  })

  it('keep the spacer while a modal makes the notice inert', async () => {
    await mountApp()
    await click(byText('Sign in'), 'the sign-in trigger')
    expect(document.querySelector(NOTICE)!.hasAttribute('inert'), 'control: the notice is inert under the modal').toBe(true)
    expect(document.querySelectorAll('.cn-spacer').length, 'the modal dropped the band').toBe(1)
  })
})
