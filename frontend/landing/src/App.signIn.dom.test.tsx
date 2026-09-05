// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// F-2: the nav's "Explore the platform" control opens the sign-in modal specifically,
// without navigating away, and dismissing it restores the page. Same setup contract as
// consentActions.mount.dom.test.tsx: production URL, an installed memory localStorage,
// a console.error spy asserted empty. "Explore the platform" also renders in Hero
// (Hero.tsx:42), so the control is reached scoped to `header`, never by an unscoped find.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ConsentStore } from './consent'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const SIGN_IN_CTA = 'Explore the platform'
const DIALOG = '[role="dialog"]'

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
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  Object.defineProperty(globalThis, 'localStorage', { value: memoryStorage(), configurable: true, writable: true })

  vi.resetModules()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage)
  else delete (globalThis as { localStorage?: unknown }).localStorage
  vi.restoreAllMocks()
})

async function mountApp(): Promise<void> {
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(createElement(mod.default))
  })
}

// Local, unexported copy per Decisions -> [click-by-text-duplicated]: the two existing
// copies (consentActions.mount.dom.test.tsx, consentActions.dom.test.tsx) query
// `document` and cannot express "the nav one, not the hero one". The `root` parameter
// is the one difference.
async function clickByText(root: ParentNode, text: string): Promise<void> {
  const button = Array.from(root.querySelectorAll('button')).find((b) => b.textContent?.trim() === text)
  expect(button, `expected a button labelled "${text}" within the given scope`).toBeDefined()
  await act(async () => {
    button!.click()
  })
}

describe('F-2: the sign-in control opens the sign-in modal', () => {
  it('F2-a: control needle -- at rest zero dialogs, and header holds at least one button', async () => {
    await mountApp()
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    expect(document.querySelectorAll('header button').length).toBeGreaterThan(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F2-b: exactly one "Explore the platform" in header, exactly two on the page', async () => {
    await mountApp()
    const header = document.querySelector('header')!
    const inHeader = Array.from(header.querySelectorAll('button')).filter((b) => b.textContent?.trim() === SIGN_IN_CTA)
    const onPage = Array.from(document.querySelectorAll('button')).filter((b) => b.textContent?.trim() === SIGN_IN_CTA)
    expect(inHeader.length, 'nav CTA missing or duplicated').toBe(1)
    expect(onPage.length, 'expected nav + hero copies').toBe(2)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F2-c/d/e: clicking opens the Sign-in dialog, does not navigate, and Close restores the page', async () => {
    await mountApp()
    const header = document.querySelector('header')!
    const sectionsBefore = document.querySelectorAll('section[id]').length
    const buttonsBefore = document.querySelectorAll('button').length
    const pathBefore = window.location.pathname

    await clickByText(header, SIGN_IN_CTA)

    // F2-c: exactly one dialog, and it is the Sign-in one -- not Book a demo.
    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    const dialog = document.querySelector(DIALOG)!
    expect(dialog.getAttribute('aria-label')).toBe('Sign in')

    // F2-d: no navigation.
    expect(window.location.pathname).toBe(pathBefore)
    expect(document.querySelector('#pricing')).not.toBeNull()

    // F2-e: Close leaves zero dialogs and restores the pre-open snapshot.
    const closeButton = dialog.querySelector<HTMLButtonElement>('button[aria-label="Close"]')
    expect(closeButton, 'expected the modal Close control').not.toBeNull()
    await act(async () => {
      closeButton!.click()
    })
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    expect(document.querySelectorAll('section[id]').length).toBe(sectionsBefore)
    expect(document.querySelectorAll('button').length).toBe(buttonsBefore)
    expect(consoleError).not.toHaveBeenCalled()
  })
})
