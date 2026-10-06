// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The reset landing: `?reset=1` and `?reset=failed` (set by internal/gateway/reset_password.go) show a notice and a way back to the forgot view.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DIALOG = '[role="dialog"]'
const STATUS = '[role="status"]'
// Pinned here, not imported: a wording change is a deliberate edit (story D15).
const RESET_DONE = 'Your password is changed. Sign in with your new password.'
const RESET_FAILED = 'That reset link did not work. It may have expired or already been used.'
const VERIFIED = 'Your email address is verified. Please sign in.'
const VERIFY_FAILED = 'That link did not work. It may have expired or already been used.'
const REQUEST_NEW = 'Request a new link'
const SIGN_IN_HEADING = 'Sign in to your workspace'
const RESET_HEADING = 'Reset your password'

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>

function memoryStore() {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => void map.set(k, String(v)),
    removeItem: (k: string) => void map.delete(k),
    clear: () => map.clear(),
    key: () => null,
    get length() {
      return map.size
    },
  }
}

beforeEach(() => {
  vi.stubGlobal('localStorage', memoryStore())
  vi.stubGlobal('sessionStorage', memoryStore())
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

async function bootAt(path: string): Promise<void> {
  window.history.replaceState(null, '', path)
  expect(window.location.pathname + window.location.search + window.location.hash).toBe(path)
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(createElement(mod.default))
  })
}

async function rebootAt(path: string): Promise<void> {
  await act(async () => root.unmount())
  root = createRoot(container)
  vi.resetModules()
  await bootAt(path)
}

const statuses = () => Array.from(document.querySelectorAll<HTMLElement>(STATUS))
const dialogs = () => document.querySelectorAll(DIALOG)
const buttonByText = (scope: ParentNode, text: string) => Array.from(scope.querySelectorAll('button')).find((b) => b.textContent?.trim() === text)
const headings = () => Array.from(document.querySelectorAll(`${DIALOG} h3`), (h) => h.textContent)

async function press(scope: ParentNode, text: string): Promise<void> {
  const btn = buttonByText(scope, text)
  expect(btn, `expected a "${text}" button`).toBeDefined()
  await act(async () => (btn as HTMLButtonElement).click())
}

function onlyNotice(text: string): HTMLElement {
  const s = statuses()
  expect(s.length).toBe(1)
  expect(s[0].textContent).toContain(text)
  return s[0]
}

async function escape(): Promise<void> {
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
  })
}

describe('reset notice: the landing after the emailed reset link', () => {
  it('the reset outcome shows its notice and the boot strip removes it', async () => {
    await bootAt('/?reset=1&x=2')
    const done = onlyNotice(RESET_DONE)
    expect(buttonByText(done, REQUEST_NEW), 'success offers no new request').toBeUndefined()
    expect(buttonByText(done, 'Dismiss')).toBeDefined()
    expect(window.location.search).toBe('?x=2')
    expect(dialogs().length).toBe(0)

    await rebootAt('/?reset=failed#top')
    const failed = onlyNotice(RESET_FAILED)
    expect(failed.textContent).not.toContain(RESET_DONE)
    expect(buttonByText(failed, REQUEST_NEW)).toBeDefined()
    expect(window.location.search).toBe('')
    expect(window.location.hash).toBe('#top')
    expect(dialogs().length).toBe(0)

    // The verify outcome wins; reset is still stripped.
    await rebootAt('/?verified=1&reset=1')
    const verified = onlyNotice(VERIFIED)
    expect(verified.textContent).not.toContain(RESET_DONE)
    expect(window.location.search).toBe('')

    await rebootAt('/?verify=failed&reset=failed&keep=1')
    const verifyFailed = onlyNotice(VERIFY_FAILED)
    expect(buttonByText(verifyFailed, REQUEST_NEW), 'the verify notice has no reset control').toBeUndefined()
    expect(window.location.search).toBe('?keep=1')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('an unrecognised reset value shows nothing and is still stripped', async () => {
    await bootAt('/?reset=1')
    onlyNotice(RESET_DONE)

    for (const search of ['?reset=0&keep=1', '?reset=1&reset=1&keep=1']) {
      await rebootAt(`/${search}`)
      expect(statuses().length, search).toBe(0)
      expect(window.location.search, search).toBe('?keep=1')
    }
  })

  it('Dismiss removes the reset notice', async () => {
    await bootAt('/?reset=failed')
    const n = onlyNotice(RESET_FAILED)
    await press(n, 'Dismiss')
    expect(statuses().length).toBe(0)
    expect(document.body.textContent).not.toContain(RESET_FAILED)
    expect(dialogs().length).toBe(0)
  })

  it('Request a new link opens the forgot view', async () => {
    await bootAt('/?reset=failed')
    await press(onlyNotice(RESET_FAILED), REQUEST_NEW)

    expect(dialogs().length).toBe(1)
    expect(document.querySelector(DIALOG)!.getAttribute('aria-label')).toBe('Platform login')
    expect(headings()).toEqual([RESET_HEADING])

    // D34: closing and opening from the Nav shows the sign-in view.
    await escape()
    expect(dialogs().length).toBe(0)
    const navLogin = Array.from(document.querySelectorAll('header button')).find((b) => b.textContent?.trim() === 'Platform login')
    expect(navLogin, 'the Nav control').toBeDefined()
    await act(async () => (navLogin as HTMLButtonElement).click())
    expect(headings()).toEqual([SIGN_IN_HEADING])

    // D34: the Footer control too, after a fresh forgot-view open.
    await escape()
    await rebootAt('/?reset=failed')
    await press(onlyNotice(RESET_FAILED), REQUEST_NEW)
    expect(headings()).toEqual([RESET_HEADING])
    await escape()
    await press(document.querySelector('footer')!, 'Open the cockpit')
    expect(headings()).toEqual([SIGN_IN_HEADING])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('Create an account from the forgot view still reopens on the sign-in view (D34)', async () => {
    vi.stubEnv('VITE_REGISTRATION_OPEN', 'true')
    await bootAt('/?reset=failed')
    await press(onlyNotice(RESET_FAILED), REQUEST_NEW)
    expect(headings(), 'control: the forgot view is open').toEqual([RESET_HEADING])

    await press(document.querySelector(DIALOG)!, 'Create an account')
    expect(document.querySelector(DIALOG)!.getAttribute('aria-label'), 'control: the register window replaced it').not.toBe('Platform login')
    await escape()
    expect(dialogs().length).toBe(0)

    const navLogin = Array.from(document.querySelectorAll('header button')).find((b) => b.textContent?.trim() === 'Platform login')
    await act(async () => (navLogin as HTMLButtonElement).click())
    expect(headings()).toEqual([SIGN_IN_HEADING])
  })

  it('the Close button also resets the view', async () => {
    await bootAt('/?reset=failed')
    await press(onlyNotice(RESET_FAILED), REQUEST_NEW)
    expect(headings()).toEqual([RESET_HEADING])
    const close = document.querySelector<HTMLButtonElement>(`${DIALOG} button[aria-label="Close"]`)
    expect(close, 'the modal Close control').not.toBeNull()
    await act(async () => close!.click())
    expect(dialogs().length).toBe(0)

    const navLogin = Array.from(document.querySelectorAll('header button')).find((b) => b.textContent?.trim() === 'Platform login')
    await act(async () => (navLogin as HTMLButtonElement).click())
    expect(headings()).toEqual([SIGN_IN_HEADING])
  })
})
