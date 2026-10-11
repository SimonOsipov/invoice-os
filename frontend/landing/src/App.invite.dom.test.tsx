// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The landing after the app refuses an invite: `?invite=<outcome>` (frontend/app/src/lib/sessionHandoff.ts InviteOutcome) shows a notice.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DIALOG = '[role="dialog"]'
const STATUS = '[role="status"]'
// D16 copy, pinned here and not imported: a wording change is a deliberate edit.
const OUTCOMES = [
  ['already-member', 'You already belong to a workspace. An account can belong to only one.'],
  ['invalid', 'This invite is no longer valid. Ask your workspace admin for a new one.'],
  ['other-address', 'This invite was sent to a different email address. Open the invite link from that email and sign in with the invited address.'],
] as const
const VERIFIED = 'Your email address is verified. Please sign in.'
const VERIFY_FAILED = 'This link is already used or expired. If you confirmed your email, sign in.'
const RESET_DONE = 'Your password is changed. Sign in with your new password.'
const RESET_FAILED = 'That reset link did not work. It may have expired or already been used.'
const REQUEST_NEW = 'Request a new link'

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
const buttonByText = (scope: ParentNode, text: string) => Array.from(scope.querySelectorAll('button')).find((b) => b.textContent?.trim() === text)

function onlyNotice(text: string): HTMLElement {
  const s = statuses()
  expect(s.length, `expected exactly one notice for "${text}"`).toBe(1)
  expect(s[0].textContent).toContain(text)
  return s[0]
}

describe('invite notice: the landing after the app refuses an invite', () => {
  it('app_inviteOutcomeShowsItsNoticeAndIsStripped', async () => {
    for (const [value, copy] of OUTCOMES) {
      await rebootAt(`/?invite=${value}&keep=1#top`)
      const notice = onlyNotice(copy)
      // Red, as the failed-verify notice: VerifyNotice.tsx TONE.
      expect(notice.style.background, value).toBe('var(--status-red-bg)')
      expect(notice.style.color, value).toBe('var(--status-red-text)')
      expect(buttonByText(notice, 'Dismiss'), value).toBeDefined()
      expect(buttonByText(notice, REQUEST_NEW), `${value} offers no reset control`).toBeUndefined()
      expect(window.location.search, value).toBe('?keep=1')
      expect(window.location.hash, value).toBe('#top')
      expect(document.querySelectorAll(DIALOG).length, value).toBe(0)
    }
    // The three notices are three different texts.
    expect(new Set(OUTCOMES.map(([, c]) => c)).size).toBe(3)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('app_inviteOutcomeYieldsToTheVerifyAndResetNotices', async () => {
    const invalid = OUTCOMES[1][1]
    await bootAt('/?invite=invalid')
    onlyNotice(invalid)

    for (const [search, copy] of [
      ['?verified=1&invite=invalid', VERIFIED],
      ['?verify=failed&invite=invalid', VERIFY_FAILED],
      ['?reset=1&invite=invalid', RESET_DONE],
      ['?reset=failed&invite=invalid', RESET_FAILED],
      ['?invite=invalid&verified=1', VERIFIED],
    ] as const) {
      await rebootAt(`/${search}`)
      const notice = onlyNotice(copy)
      expect(notice.textContent, search).not.toContain(invalid)
      expect(window.location.search, `${search} strips invite too`).toBe('')
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('an unrecognised or repeated invite value shows nothing and is still stripped', async () => {
    await bootAt('/?invite=invalid')
    onlyNotice(OUTCOMES[1][1])

    for (const search of ['?invite=x&keep=1', '?invite=&keep=1', '?invite=invalid&invite=invalid&keep=1', '?invite=invalid&invite=other-address&keep=1']) {
      await rebootAt(`/${search}`)
      expect(statuses().length, search).toBe(0)
      expect(window.location.search, search).toBe('?keep=1')
    }
  })

  it('Dismiss removes the invite notice', async () => {
    await bootAt('/?invite=other-address')
    const notice = onlyNotice(OUTCOMES[2][1])
    await act(async () => (buttonByText(notice, 'Dismiss') as HTMLButtonElement).click())
    expect(statuses().length).toBe(0)
    expect(document.body.textContent).not.toContain(OUTCOMES[2][1])
  })
})
