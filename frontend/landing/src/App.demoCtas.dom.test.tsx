// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// F-3: every rendered demo CTA opens the same "Book a demo" modal.
// LIMIT: jsdom has no visibility engine, so the CTAs in the hidden Solutions panels click through here;
// that does not prove a visitor can reach them.
// Setup: production URL, memory localStorage, console.error spy asserted empty.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ConsentStore } from './consent'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DIALOG = '[role="dialog"]'
const DEMO_DIALOG_LABEL = 'Book a demo'
const SCOPES = ['header', '#top', '#platform', '#coverage', '#solutions', '#integrations', '#api', '#faq', '[data-closing]', 'footer']

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

// Local, unexported copy per Decisions -> [click-by-text-duplicated]: neither existing
// copy (consentActions.mount.dom.test.tsx / App.signIn.dom.test.tsx) is imported. The
// `root` parameter is what makes "exactly one within this scope" expressible.
async function clickByText(root: ParentNode, text: string): Promise<void> {
  const button = Array.from(root.querySelectorAll('button')).find((b) => b.textContent?.trim() === text)
  expect(button, `expected a button labelled "${text}" within the given scope`).toBeDefined()
  await act(async () => {
    button!.click()
  })
}

// Scope + label roster, in page order: the literal each component renders (V). Labels repeat across scopes,
// never within one: the per-entry check asserts that.
const ROSTER: { scope: string; label: string }[] = [
  { scope: 'header', label: 'Book a demo' }, // Nav.tsx
  { scope: '#top', label: 'Book a demo' }, // Hero.tsx
  { scope: '#platform', label: 'See validation in action →' }, // Platform.tsx, Validate tab at rest
  { scope: '#coverage', label: 'Discuss your country →' }, // Coverage.tsx
  { scope: '[data-sol-panel="fin"]', label: 'Find your workflow' }, // Solutions.tsx, SOL.fin.cta
  { scope: '[data-sol-panel="firm"]', label: 'Find your workflow' }, // SOL.firm.cta
  { scope: '[data-sol-panel="dev"]', label: 'Discuss a partnership' }, // SOL.dev.cta
  { scope: '#integrations', label: 'Discuss your integration →' }, // Integrations.tsx
  { scope: '#api', label: 'Request API access' }, // Api.tsx
  { scope: '#faq', label: 'Talk to our team →' }, // Faq.tsx
  { scope: '[data-closing]', label: 'Book a demo' }, // ClosingCta.tsx
  { scope: 'footer', label: 'Book a demo' }, // Footer.tsx
  { scope: 'footer', label: 'Contact ASComply' }, // Footer.tsx, same onBookDemo (D-24)
]

// The eighteen controls in these ten scopes that are NOT demo CTAs, named so the
// 31-button completeness guard below is not a magic number:
//   header        -- "Sign in" (sign-in), the burger
//   #platform     -- the Validate / Approve / Submit tabs (3)
//   #coverage     -- the Nigeria / Kenya / South Africa country tabs (3)
//   #solutions    -- the three Who-it's-for tabs (3)
//   #faq          -- the five question headers (5)
//   footer        -- "Open the cockpit" (sign-in), "Cookie choices"
const NON_CTA_COUNT = 18

describe('F-3: every rendered demo CTA opens the same modal', () => {
  it('F3-a: control needle -- zero dialogs at rest, and every scope resolves to >= 1 button', async () => {
    await mountApp()
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    for (const scope of SCOPES) {
      const scopeEl = document.querySelector(scope)
      expect(scopeEl, `expected scope "${scope}" to resolve`).not.toBeNull()
      expect(scopeEl!.querySelectorAll('button').length, `expected >=1 button in "${scope}"`).toBeGreaterThan(0)
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  // Measured 3 (header) + 1 (#top) + 4 (#platform) + 4 (#coverage) + 6 (#solutions) + 1 (#integrations) +
  // 1 (#api) + 6 (#faq) + 1 (closing) + 4 (footer) = 31 = the 13-entry roster + the 18 named non-CTA controls above. Asserted
  // with the demo modal closed -- App.tsx mounts SignInModal/DemoModal as
  // siblings of Footer, outside every one of these scopes, but an OPEN modal still
  // adds buttons to the page (its own Close, and form controls) that this total ignores
  // by construction.
  it('F3-f: the ten scopes hold exactly 31 buttons in total, modal closed', async () => {
    await mountApp()
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    const total = SCOPES.reduce((sum, scope) => {
      const el = document.querySelector(scope)
      expect(el, `expected scope "${scope}" to resolve`).not.toBeNull()
      return sum + el!.querySelectorAll('button').length
    }, 0)
    expect(total).toBe(ROSTER.length + NON_CTA_COUNT)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it.each(ROSTER)('F3-c/d/e: "$label" in "$scope" resolves once, opens the demo dialog, and closes cleanly', async ({ scope, label }) => {
    await mountApp()
    const scopeEl = document.querySelector(scope)!
    expect(scopeEl, `expected scope "${scope}" to resolve`).not.toBeNull()

    const matches = Array.from(scopeEl.querySelectorAll('button')).filter((b) => b.textContent?.trim() === label)
    expect(matches.length, `expected exactly one "${label}" button within "${scope}"`).toBe(1)

    await clickByText(scopeEl, label)

    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    const dialog = document.querySelector(DIALOG)!
    expect(dialog.getAttribute('aria-label')).toBe(DEMO_DIALOG_LABEL)

    const closeButton = dialog.querySelector<HTMLButtonElement>('button[aria-label="Close"]')
    expect(closeButton, 'expected the modal Close control').not.toBeNull()
    await act(async () => {
      closeButton!.click()
    })
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('F3-g: the header Book a demo keeps its attribution and closes the burger menu', () => {
  it("opens the demo dialog with trackDemoOpen('nav') once and leaves no menu behind", async () => {
    const trackDemoOpen = vi.fn()
    vi.doMock('./analytics', async () => ({ ...(await vi.importActual<object>('./analytics')), trackDemoOpen }))
    try {
      await mountApp()
      const burger = document.querySelector<HTMLButtonElement>('header button.a-burger')
      expect(burger, 'expected the header burger').not.toBeNull()
      await act(async () => {
        burger!.click()
      })
      expect(document.querySelector('.a-menu'), 'control: the menu is open').not.toBeNull()
      expect(trackDemoOpen, 'control: nothing tracked yet').not.toHaveBeenCalled()

      await clickByText(document.querySelector('header')!, 'Book a demo')

      expect(trackDemoOpen.mock.calls).toEqual([['nav']])
      expect(document.querySelectorAll(DIALOG).length).toBe(1)
      expect(document.querySelector('.a-menu')).toBeNull()
      expect(consoleError).not.toHaveBeenCalled()
    } finally {
      vi.doUnmock('./analytics')
    }
  })
})

describe('F3-h: the hero Book a demo keeps its attribution', () => {
  it("opens the demo dialog with trackDemoOpen('hero') once", async () => {
    const trackDemoOpen = vi.fn()
    vi.doMock('./analytics', async () => ({ ...(await vi.importActual<object>('./analytics')), trackDemoOpen }))
    try {
      await mountApp()
      expect(trackDemoOpen, 'control: nothing tracked yet').not.toHaveBeenCalled()

      await clickByText(document.querySelector('#top')!, 'Book a demo')

      expect(trackDemoOpen.mock.calls).toEqual([['hero']])
      expect(document.querySelectorAll(DIALOG).length).toBe(1)
      expect(consoleError).not.toHaveBeenCalled()
    } finally {
      vi.doUnmock('./analytics')
    }
  })
})

describe('F3-j: the Platform panel link reports platform on every tab', () => {
  it("each tab's link opens one demo dialog and reports trackDemoOpen('platform'); selecting a tab alone reports nothing", async () => {
    const trackDemoOpen = vi.fn()
    vi.doMock('./analytics', async () => ({ ...(await vi.importActual<object>('./analytics')), trackDemoOpen }))
    try {
      await mountApp()
      const platform = document.querySelector('#platform')
      expect(platform, 'expected #platform to resolve').not.toBeNull()
      const tabs = Array.from(platform!.querySelectorAll<HTMLButtonElement>('[role="tab"]'))
      expect(tabs.length, 'control: three Platform tabs').toBe(3)
      expect(trackDemoOpen, 'control: nothing tracked yet').not.toHaveBeenCalled()

      const links = ['See validation in action →', 'See approvals in action →', 'See submission in action →']
      for (const [i, link] of links.entries()) {
        await act(async () => {
          tabs[i].click()
        })
        expect(trackDemoOpen, `tab ${i}: selecting it reports nothing`).toHaveBeenCalledTimes(i)

        await clickByText(platform!, link)

        expect(trackDemoOpen, `tab ${i}: the link reported once`).toHaveBeenCalledTimes(i + 1)
        expect(document.querySelectorAll(DIALOG).length, `tab ${i}: one dialog`).toBe(1)
        await act(async () => {
          document.querySelector<HTMLButtonElement>(`${DIALOG} button[aria-label="Close"]`)!.click()
        })
        expect(document.querySelectorAll(DIALOG).length, `tab ${i}: closed`).toBe(0)
      }

      expect(trackDemoOpen.mock.calls).toEqual([['platform'], ['platform'], ['platform']])
      expect(consoleError).not.toHaveBeenCalled()
    } finally {
      vi.doUnmock('./analytics')
    }
  })
})

describe('R4-F3-1: "Discuss your country →" reports coverage', () => {
  it("opens the demo dialog with trackDemoOpen('coverage') once", async () => {
    const trackDemoOpen = vi.fn()
    vi.doMock('./analytics', async () => ({ ...(await vi.importActual<object>('./analytics')), trackDemoOpen }))
    try {
      await mountApp()
      const band = document.querySelector('#coverage')!
      expect(band, 'expected #coverage to resolve').not.toBeNull()
      expect(trackDemoOpen, 'control: nothing tracked yet').not.toHaveBeenCalled()

      await clickByText(band, 'Kenya')
      expect(trackDemoOpen, 'control: a country tab books nothing').not.toHaveBeenCalled()
      expect(document.querySelectorAll(DIALOG).length, 'control: a country tab opens no dialog').toBe(0)

      await clickByText(band, 'Discuss your country →')

      expect(trackDemoOpen.mock.calls).toEqual([['coverage']])
      expect(document.querySelectorAll(DIALOG).length).toBe(1)
      expect(document.querySelector(DIALOG)!.getAttribute('aria-label')).toBe(DEMO_DIALOG_LABEL)
      expect(consoleError).not.toHaveBeenCalled()
    } finally {
      vi.doUnmock('./analytics')
    }
  })
})

describe('R5-F3-audience: every Solutions panel CTA reports audience', () => {
  it("the CTAs of the fin, firm and dev panels, hidden ones included, each report trackDemoOpen('audience'); selecting a tab reports nothing", async () => {
    const trackDemoOpen = vi.fn()
    vi.doMock('./analytics', async () => ({ ...(await vi.importActual<object>('./analytics')), trackDemoOpen }))
    try {
      await mountApp()
      const band = document.querySelector('#solutions')
      expect(band, 'expected #solutions to resolve').not.toBeNull()
      const tabs = Array.from(band!.querySelectorAll<HTMLButtonElement>('[role="tab"]'))
      expect(tabs.length, 'control: three Solutions tabs').toBe(3)
      expect(trackDemoOpen, 'control: nothing tracked yet').not.toHaveBeenCalled()
      for (const tab of tabs) {
        await act(async () => {
          tab.click()
        })
      }
      expect(trackDemoOpen, 'selecting each tab reports nothing').not.toHaveBeenCalled()
      expect(document.querySelectorAll(DIALOG).length, 'selecting a tab opens no dialog').toBe(0)

      const ctas = [
        ['fin', 'Find your workflow'],
        ['firm', 'Find your workflow'],
        ['dev', 'Discuss a partnership'],
      ] as const
      for (const [i, [id, label]] of ctas.entries()) {
        const panel = document.querySelector(`[data-sol-panel="${id}"]`)
        expect(panel, `expected the ${id} panel to resolve`).not.toBeNull()
        await clickByText(panel!, label)
        expect(trackDemoOpen, `${id}: the CTA reported once`).toHaveBeenCalledTimes(i + 1)
        expect(document.querySelectorAll(DIALOG).length, `${id}: one dialog`).toBe(1)
        await act(async () => {
          document.querySelector<HTMLButtonElement>(`${DIALOG} button[aria-label="Close"]`)!.click()
        })
        expect(document.querySelectorAll(DIALOG).length, `${id}: closed`).toBe(0)
      }

      expect(trackDemoOpen.mock.calls).toEqual([['audience'], ['audience'], ['audience']])
      expect(consoleError).not.toHaveBeenCalled()
    } finally {
      vi.doUnmock('./analytics')
    }
  })
})

describe('R5-F3-integrations-api: each CTA reports its own source', () => {
  it("Discuss your integration → reports trackDemoOpen('integrations') and Request API access reports 'api'", async () => {
    const trackDemoOpen = vi.fn()
    vi.doMock('./analytics', async () => ({ ...(await vi.importActual<object>('./analytics')), trackDemoOpen }))
    try {
      await mountApp()
      expect(trackDemoOpen, 'control: nothing tracked yet').not.toHaveBeenCalled()
      for (const [i, [scope, label]] of (
        [
          ['#integrations', 'Discuss your integration →'],
          ['#api', 'Request API access'],
        ] as const
      ).entries()) {
        const band = document.querySelector(scope)
        expect(band, `expected ${scope} to resolve`).not.toBeNull()
        await clickByText(band!, label)
        expect(trackDemoOpen, `${scope}: the CTA reported once`).toHaveBeenCalledTimes(i + 1)
        expect(document.querySelectorAll(DIALOG).length, `${scope}: one dialog`).toBe(1)
        await act(async () => {
          document.querySelector<HTMLButtonElement>(`${DIALOG} button[aria-label="Close"]`)!.click()
        })
        expect(document.querySelectorAll(DIALOG).length, `${scope}: closed`).toBe(0)
      }
      expect(trackDemoOpen.mock.calls).toEqual([['integrations'], ['api']])
      expect(consoleError).not.toHaveBeenCalled()
    } finally {
      vi.doUnmock('./analytics')
    }
  })
})

describe('R5-F3-faq-closing: each CTA reports its own source', () => {
  it("Talk to our team → reports trackDemoOpen('faq') and the closing Book a demo reports 'closing'; opening an FAQ item reports nothing", async () => {
    const trackDemoOpen = vi.fn()
    vi.doMock('./analytics', async () => ({ ...(await vi.importActual<object>('./analytics')), trackDemoOpen }))
    try {
      await mountApp()
      const faq = document.querySelector('#faq')
      expect(faq, 'expected #faq to resolve').not.toBeNull()
      expect(trackDemoOpen, 'control: nothing tracked yet').not.toHaveBeenCalled()
      const heads = Array.from(faq!.querySelectorAll<HTMLButtonElement>('.ds-faq-btn'))
      expect(heads.length, 'control: five FAQ headers').toBe(5)
      for (const head of [heads[2], heads[2]]) {
        await act(async () => {
          head.click()
        })
      }
      expect(trackDemoOpen, 'opening and closing an FAQ item reports nothing').not.toHaveBeenCalled()
      expect(document.querySelectorAll(DIALOG).length, 'an FAQ item opens no dialog').toBe(0)

      for (const [i, [scope, label]] of (
        [
          ['#faq', 'Talk to our team →'],
          ['[data-closing]', 'Book a demo'],
        ] as const
      ).entries()) {
        const band = document.querySelector(scope)
        expect(band, `expected ${scope} to resolve`).not.toBeNull()
        await clickByText(band!, label)
        expect(trackDemoOpen, `${scope}: the CTA reported once`).toHaveBeenCalledTimes(i + 1)
        expect(document.querySelectorAll(DIALOG).length, `${scope}: one dialog`).toBe(1)
        await act(async () => {
          document.querySelector<HTMLButtonElement>(`${DIALOG} button[aria-label="Close"]`)!.click()
        })
        expect(document.querySelectorAll(DIALOG).length, `${scope}: closed`).toBe(0)
      }
      expect(trackDemoOpen.mock.calls).toEqual([['faq'], ['closing']])
      expect(consoleError).not.toHaveBeenCalled()
    } finally {
      vi.doUnmock('./analytics')
    }
  })
})

describe('F3-i: the footer demo buttons keep the footer attribution; Open the cockpit tracks nothing', () => {
  it.each([['Book a demo'], ['Contact ASComply']])("%s reports trackDemoOpen('footer') once", async (label) => {
    const trackDemoOpen = vi.fn()
    vi.doMock('./analytics', async () => ({ ...(await vi.importActual<object>('./analytics')), trackDemoOpen }))
    try {
      await mountApp()
      expect(trackDemoOpen, 'control: nothing tracked yet').not.toHaveBeenCalled()

      await clickByText(document.querySelector('footer')!, label)

      expect(trackDemoOpen.mock.calls).toEqual([['footer']])
      expect(document.querySelectorAll(DIALOG).length).toBe(1)
      expect(consoleError).not.toHaveBeenCalled()
    } finally {
      vi.doUnmock('./analytics')
    }
  })

  it('Open the cockpit opens no demo dialog and reports no demo open', async () => {
    const trackDemoOpen = vi.fn()
    vi.doMock('./analytics', async () => ({ ...(await vi.importActual<object>('./analytics')), trackDemoOpen }))
    try {
      await mountApp()
      await clickByText(document.querySelector('footer')!, 'Open the cockpit')

      expect(trackDemoOpen).not.toHaveBeenCalled()
      expect(Array.from(document.querySelectorAll(DIALOG), (d) => d.getAttribute('aria-label'))).toEqual(['Sign in'])
    } finally {
      vi.doUnmock('./analytics')
    }
  })
})
