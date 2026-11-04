// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// Pinned here, not imported: a wording change is a deliberate edit.
const HEADING = 'Page not found'
const BODY = 'The page you are looking for does not exist or has moved.'
const LIBRARY = 'https://lib.example'

let container: HTMLDivElement
let root: Root

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
  vi.stubEnv('VITE_LIBRARY_URL', LIBRARY)
  vi.resetModules()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  window.history.replaceState(null, '', '/')
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

async function mountAt(path: string): Promise<void> {
  window.history.replaceState(null, '', path)
  const mod = (await import('./App')) as { default: (p: { notFound?: boolean }) => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(createElement(mod.default, { notFound: true }))
  })
}

const link = (text: string) => Array.from(document.querySelectorAll('a')).find((a) => a.textContent?.trim() === text)

describe('App notFound', () => {
  it('notFound_rendersChromeHeadingBodyAndHomeLink', async () => {
    await mountAt('/nope')
    expect(document.querySelector('nav[aria-label="Primary"]')).not.toBeNull()
    expect(document.querySelector('footer')).not.toBeNull()
    expect(document.querySelector('h1')?.textContent).toBe(HEADING)
    expect(document.body.textContent).toContain(BODY)
    expect(link('Back to home')?.getAttribute('href')).toBe('/')
    expect(document.querySelector('#top')).toBeNull()
  })

  it('notFound_winsOverPrivacy', async () => {
    await mountAt('/Privacy')
    expect(document.querySelector('h1')?.textContent).toBe(HEADING)
    expect(document.querySelector('[data-testid="privacy-container"]')).toBeNull()
  })

  it('notFound_libraryPathAddsBrowseLink', async () => {
    await mountAt('/library/nope')
    const browse = link('Browse the library')
    expect(browse?.getAttribute('href')).toBe(LIBRARY)
    const home = link('Back to home')!
    expect(home.compareDocumentPosition(browse!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('notFound_otherPathHasNoBrowseLink', async () => {
    for (const path of ['/libraryx', '/nope']) {
      await mountAt(path)
      expect(link('Browse the library'), path).toBeUndefined()
      await act(async () => root.unmount())
      root = createRoot(container)
      vi.resetModules()
    }
  })

  it('notFound_sectionLinksArePrefixed', async () => {
    await mountAt('/nope')
    const scoped = Array.from(document.querySelectorAll<HTMLAnchorElement>('nav[aria-label="Primary"] a, footer a')).filter((a) =>
      (a.getAttribute('href') ?? '').includes('#'),
    )
    expect(scoped.length).toBeGreaterThan(0)
    for (const a of scoped) expect(a.getAttribute('href'), a.textContent ?? '').toMatch(/^\/#/)
  })
})
