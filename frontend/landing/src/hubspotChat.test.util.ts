// Shared HubSpot chat test fakes: the SDK stand-in and cookie helpers.
import { vi } from 'vitest'

export type Sdk = { widget: { load: () => void; remove: () => void }; clear: (opts?: { resetWidget?: boolean }) => void }
export type TestWindow = Window & {
  _hsq?: unknown[]
  HubSpotConversations?: Sdk
  hsConversationsSettings?: Record<string, unknown>
  hsConversationsOnReady?: Array<() => void>
}
export const COOKIES = ['__hstc', 'hubspotutk', '__hssrc', '__hssc', 'messagesUtk', 'hs-messages-is-open', 'hs-messages-hide-welcome-message']
export const CONTAINER_ID = 'hubspot-messages-iframe-container'

export const w = window as TestWindow

export function jarNames(): string[] {
  return document.cookie
    .split(';')
    .map((pair) => pair.split('=')[0].trim())
    .filter(Boolean)
}

export function resetJar() {
  for (const name of jarNames()) {
    document.cookie = `${name}=; Max-Age=0; path=/`
    document.cookie = `${name}=; Max-Age=0; path=/; domain=.ascomply.com`
  }
}

export function seed(...names: string[]) {
  for (const name of names) document.cookie = `${name}=v; path=/; domain=.ascomply.com`
}

export function container() {
  return document.getElementById(CONTAINER_ID)
}

/** Mirrors the vendor document: remove() deletes the container, load() re-creates it. */
export function installSdk(): Sdk {
  const make = () => {
    if (container()) return
    const el = document.createElement('div')
    el.id = CONTAINER_ID
    document.body.appendChild(el)
  }
  make()
  const sdk: Sdk = {
    widget: { load: vi.fn(make), remove: vi.fn(() => container()?.remove()) },
    clear: vi.fn(),
  }
  w.HubSpotConversations = sdk
  return sdk
}
