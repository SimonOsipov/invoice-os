// @vitest-environment jsdom
// SSR contract of the v2 footer (RESKIN-02-05): brand block, Platform and Connect columns, handlers.
// jsdom serves FT-04/05; renderToStaticMarkup runs unchanged under it.
import { act, createElement } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { Footer, PLATFORM_LINKS } from './Footer'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function noop() {}

// ASCII sub-needle: sidesteps the © and · glyphs and occurs exactly once.
const COPYRIGHT_ROW = '2026 ASComply Africa Limited'

const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop }))

type Link = { label: string; href: string }

// The v2 file's footer Platform list (V470), retyped.
const V2_PLATFORM: Link[] = [
  { label: 'Invoice workflows', href: '#platform' },
  { label: 'Country roadmap', href: '#coverage' },
  { label: 'AI-supported intelligence', href: '#intelligence' },
  { label: 'Solutions for partners', href: '#solutions' },
]

function isOrderedSubsequence(list: readonly Link[], of: readonly Link[]): boolean {
  let at = 0
  for (const item of list) {
    while (at < of.length && !(of[at].label === item.label && of[at].href === item.href)) at++
    if (at === of.length) return false
    at++
  }
  return true
}

const decode = (s: string) => s.replace(/&#x27;/g, "'").replace(/&amp;/g, '&')

function attrsOf(openTag: string): Record<string, string> {
  return Object.fromEntries([...openTag.matchAll(/\s([^\s=>/]+)(?:="([^"]*)")?/g)].map((m) => [m[1], m[2] ?? '']))
}

// Both bounds are load-bearing: the bottom row follows Connect and holds controls of its own.
function connectSlice(markup: string): string {
  const start = markup.indexOf('>Connect<')
  expect(start, 'expected to find the Connect column heading').toBeGreaterThan(-1)
  const end = markup.indexOf(COPYRIGHT_ROW)
  expect(end, 'expected the bottom row to follow the Connect column').toBeGreaterThan(start)
  return markup.slice(start, end)
}

function platformSlice(markup: string): string {
  const start = markup.indexOf('>Platform<')
  expect(start, 'expected to find the Platform column heading').toBeGreaterThan(-1)
  const end = markup.indexOf('>Connect<')
  expect(end, 'expected the Connect column to follow Platform').toBeGreaterThan(start)
  return markup.slice(start, end)
}

const controlsOf = (slice: string) =>
  [...slice.matchAll(/<(a|button)\b([^>]*)>([\s\S]*?)<\/\1>/g)].map((m) => ({
    tag: m[1],
    attrs: attrsOf(m[2]),
    label: decode(m[3].replace(/<[^>]*>/g, '')),
  }))

describe('FT-01 brand block: Logo and the two-line tagline', () => {
  it('holds one ds-logo with a 28px img and exactly one <p class="t-body-sm"> reading the tagline', () => {
    expect(html.match(/class="ds-logo"/g)?.length, 'expected exactly one DS Logo').toBe(1)
    const img = /<img\b[^>]*>/.exec(html)?.[0]
    expect(img, 'expected the Logo mark <img>').toBeDefined()
    expect(attrsOf(img!).width, 'Logo size').toBe('28')
    expect(attrsOf(img!).height, 'Logo size').toBe('28')

    const paras = [...html.matchAll(/<p\b([^>]*)>([\s\S]*?)<\/p>/g)]
    expect(paras.length, 'the footer must hold exactly one <p>').toBe(1)
    expect(attrsOf(paras[0][1]).class).toBe('t-body-sm')
    expect(paras[0][2].replace(/<[^>]*>/g, '')).toBe('Clarity for every invoice. Confidence for your business.')
  })
})

describe('FT-02 the Platform column is an in-order subsequence of V470', () => {
  it('PLATFORM_LINKS is a subsequence of V2_PLATFORM and the column renders exactly it', () => {
    expect(isOrderedSubsequence(V2_PLATFORM, V2_PLATFORM), 'control: V2_PLATFORM is its own subsequence').toBe(true)
    expect(
      isOrderedSubsequence(
        [
          { label: 'Country roadmap', href: '#coverage' },
          { label: 'Invoice workflows', href: '#platform' },
        ],
        V2_PLATFORM,
      ),
      'control: [Country roadmap, Invoice workflows] is out of order',
    ).toBe(false)

    expect(
      isOrderedSubsequence(PLATFORM_LINKS, V2_PLATFORM),
      `PLATFORM_LINKS is not an ordered subsequence of V470: ${JSON.stringify(PLATFORM_LINKS)}`,
    ).toBe(true)

    const rendered = controlsOf(platformSlice(html)).map((c) => ({ label: c.label, href: c.attrs.href }))
    expect(rendered).toEqual(PLATFORM_LINKS.map((l) => ({ label: l.label, href: l.href })))
  })
})

describe('FT-03 Connect holds three a-link buttons in order', () => {
  it('lists Book a demo, Open the cockpit, Contact ASComply, each <button type="button" class="a-link">, no <a>', () => {
    const controls = controlsOf(connectSlice(html))
    expect(controls.map((c) => c.label)).toEqual(['Book a demo', 'Open the cockpit', 'Contact ASComply'])
    for (const c of controls) {
      expect(c.tag, `"${c.label}" is not a <button>`).toBe('button')
      expect(c.attrs.type, `"${c.label}" type`).toBe('button')
      expect(c.attrs.class, `"${c.label}" class`).toBe('a-link')
    }
  })
})

describe('FT-04 / FT-05 the Connect controls call their handlers', () => {
  let container: HTMLDivElement
  let root: ReturnType<typeof createRoot>
  let consoleError: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
  })
  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.restoreAllMocks()
  })

  const mount = (props: { onBookDemo: () => void; onSignIn?: () => void }) =>
    act(async () => {
      root.render(createElement(Footer, props))
    })

  const buttonLabelled = (label: string) => {
    const found = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.trim() === label)
    expect(found, `expected a button labelled "${label}"`).toBeDefined()
    return found!
  }

  it('FT-04: Book a demo and Contact ASComply call onBookDemo, Open the cockpit calls onSignIn', async () => {
    const onBookDemo = vi.fn()
    const onSignIn = vi.fn()
    await mount({ onBookDemo, onSignIn })
    expect(onBookDemo, 'control: nothing fired on mount').not.toHaveBeenCalled()

    for (const label of ['Book a demo', 'Open the cockpit', 'Contact ASComply']) {
      const button = buttonLabelled(label)
      await act(async () => {
        button.click()
      })
    }
    expect(onBookDemo).toHaveBeenCalledTimes(2)
    expect(onSignIn).toHaveBeenCalledTimes(1)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('FT-05: without onSignIn, Open the cockpit clicks without throwing, logging or calling onBookDemo', async () => {
    const onBookDemo = vi.fn()
    await mount({ onBookDemo })
    const button = buttonLabelled('Open the cockpit')
    await act(async () => {
      expect(() => button.click()).not.toThrow()
    })
    expect(onBookDemo).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
  })
})
