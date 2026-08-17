// SSR via renderToStaticMarkup — no jsdom, no testing-library (vitest.config.ts: 'node').
//
// The component is loaded through a runtime specifier rather than a static import so a
// missing or renamed module fails as an ASSERTION (loadNotice's existsSync guard) rather
// than a collection error that reports nothing about which claim broke.
//
// React 19 SSR facts relied on here (react-dom 19.2.7, also stated in
// Privacy.render.test.tsx / Footer.render.test.tsx): `&` in JSX text emits `&amp;`;
// `inert={true}` emits `inert=""` and `inert={false}` emits nothing; attribute
// output order follows JSX source order.
/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { CONSENT_VERSION } from '../consent'
import type { ConsentRecord } from '../consent'

const HERE = dirname(fileURLToPath(import.meta.url))
const COMPONENT_PATH = join(HERE, 'CookieNotice.tsx')
// Non-literal on purpose — see the header note.
const COMPONENT_SPECIFIER = './CookieNotice'

type ConsentChoice = 'accept' | 'reject'
type NoticeProps = {
  current: ConsentRecord | null
  suppressed: boolean
  onChoose: (choice: ConsentChoice) => void
}
type CookieNoticeModule = { CookieNotice: (props: NoticeProps) => ReactElement }

function noop() {}

const GRANTED: ConsentRecord = { analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }
const DENIED: ConsentRecord = { analytics: false, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }

// Verbatim from the story's Final copy table. Not paraphrased, punctuation untouched.
const BODY_COPY =
  'We use Google Analytics to see how people find and use this page. That is the only non-essential cookie we set: no advertising, no remarketing, no data sold to anyone.'
const LINK_TEXT = 'Read the privacy &amp; cookie policy'
const SETTING_ON = 'Analytics cookies are on.'
const SETTING_OFF = 'Analytics cookies are off.'

async function loadNotice(): Promise<CookieNoticeModule> {
  expect(existsSync(COMPONENT_PATH), `expected ${COMPONENT_PATH} to exist`).toBe(true)
  const mod = (await import(COMPONENT_SPECIFIER)) as CookieNoticeModule
  expect(typeof mod.CookieNotice, 'expected a CookieNotice named export').toBe('function')
  return mod
}

async function render(overrides: Partial<NoticeProps> = {}): Promise<string> {
  const mod = await loadNotice()
  const props: NoticeProps = { current: null, suppressed: false, onChoose: noop, ...overrides }
  const html = renderToStaticMarkup(createElement(mod.CookieNotice, props))
  // Population floor: a component that rendered nothing would satisfy every
  // absence claim below vacuously.
  expect(html.length, 'expected non-empty SSR markup').toBeGreaterThan(0)
  expect(html, 'expected the card root class as a control needle').toContain('cookie-note')
  return html
}

// Uses expect rather than a throw so a miss is a failing assertion, not an error.
function extractTag(html: string, re: RegExp): string {
  const match = html.match(re)
  expect(match, `expected to find a tag matching ${re}`).not.toBeNull()
  return match![0]
}

describe('CookieNotice SSR render (LAND-05-02)', () => {
  it('T2-1 / AC-1: the card is a polite live region, and is not a modal', async () => {
    const html = await render()
    const root = extractTag(html, /<div[^>]*\bclass="cookie-note"[^>]*>/)

    expect(root).toContain('role="region"')
    expect(root).toContain('aria-label="Cookie notice"')
    expect(root).toContain('aria-live="polite"')
    expect(html).not.toContain('aria-modal')
  })

  it('T2-2: the body copy is verbatim', async () => {
    const html = await render()
    expect(html).toContain(BODY_COPY)
  })

  it('T2-3 / AC-4: exactly two buttons, both type="button", labelled Accept and Reject', async () => {
    const html = await render()

    expect(html).toMatch(/<button[^>]*type="button"[^>]*data-consent="accept"[^>]*>Accept</)
    expect(html).toMatch(/<button[^>]*type="button"[^>]*data-consent="reject"[^>]*>Reject</)

    const buttons = html.match(/<button/g) ?? []
    expect(buttons.length).toBe(2)
  })

  it('T2-4 / AC-3: the policy link carries BOTH classes and href="/privacy"', async () => {
    const html = await render()

    // Ordered form. Requires the JSX to write className before href — the order the
    // architect's markup block specifies, and the order React reproduces verbatim.
    expect(html).toMatch(/<a [^>]*class="lnk cn-link"[^>]*href="\/privacy"/)

    // Order-agnostic siblings, so an attribute reorder fails loudly on its own line
    // rather than silently taking the whole claim with it.
    expect(html).toContain('class="lnk cn-link"')
    expect(html).toContain('href="/privacy"')
    expect(html).toContain(LINK_TEXT)
  })

  it('T2-5 / AC-5: no dismissal affordance of any kind exists', async () => {
    const html = await render()

    const buttons = html.match(/<button/g) ?? []
    expect(buttons.length).toBe(2)
    expect(html).not.toMatch(/close/i)
    expect(html).not.toMatch(/dismiss/i)
    for (const glyph of ['×', '✕', '✖', '&times;']) {
      expect(html, `expected no ${glyph}`).not.toContain(glyph)
    }
  })

  it('T2-6 / AC-2: the eyebrow reuses the shared class and renders the copy-table text', async () => {
    const html = await render()

    // Rendered text, not the ALL-CAPS literal every other landing eyebrow passes:
    // text-transform: uppercase makes the two render identically, so a literal check
    // would let `COOKIES` through against a copy table that says `Cookies`.
    expect(html).toMatch(/class="eyebrow"[^>]*>Cookies</)
    expect(html).not.toContain('>COOKIES<')

    // No hand-rolled 24px rule standing in for .eyebrow::before.
    expect(html).not.toMatch(/<span[^>]*style="[^"]*24px/)
  })

  it('T2-7 / AC-6: current === null renders no .cn-setting line', async () => {
    const html = await render({ current: null })
    expect(html).not.toContain('cn-setting')
    expect(html).not.toContain(SETTING_ON)
    expect(html).not.toContain(SETTING_OFF)
  })

  it('T2-7 / AC-6: a granted record renders the "on" sentence', async () => {
    const html = await render({ current: GRANTED })
    expect(html).toMatch(/class="cn-setting"[^>]*>Analytics cookies are on\.</)
    expect(html).not.toContain(SETTING_OFF)
  })

  it('T2-7 / AC-6: a denied record renders the "off" sentence', async () => {
    const html = await render({ current: DENIED })
    expect(html).toMatch(/class="cn-setting"[^>]*>Analytics cookies are off\.</)
    expect(html).not.toContain(SETTING_ON)
  })

  it('T2-8 / AC-7: inert appears on the card root only when suppressed', async () => {
    const suppressed = await render({ suppressed: true })
    const root = extractTag(suppressed, /<div[^>]*\bclass="cookie-note"[^>]*>/)
    expect(root).toContain('inert=""')

    const live = await render({ suppressed: false })
    expect(live).not.toContain('inert')
  })

  it('T2-9: the spacer is present and hidden from assistive tech', async () => {
    const html = await render()
    expect(html).toContain('<div aria-hidden="true" class="cn-spacer">')
  })
})

describe('CookieNotice adversarial (LAND-05-02)', () => {
  // SSR drops event handlers, so the only way to see the wiring is the element tree.
  function buttonsOf(element: ReactElement): ReactElement[] {
    const found: ReactElement[] = []
    const walk = (node: unknown): void => {
      if (Array.isArray(node)) return void node.forEach(walk)
      if (!node || typeof node !== 'object') return
      const el = node as ReactElement<{ children?: unknown }>
      if (el.type === 'button') found.push(el)
      walk(el.props?.children)
    }
    walk(element)
    return found
  }

  async function tree(overrides: Partial<NoticeProps> = {}): Promise<ReactElement> {
    const mod = await loadNotice()
    return mod.CookieNotice({ current: null, suppressed: false, onChoose: noop, ...overrides })
  }

  it('each button calls onChoose with its OWN verdict — a swapped pair is not a cosmetic bug', () => {
    return (async () => {
      const seen: string[] = []
      const el = await tree({ onChoose: (choice) => seen.push(choice) })
      const buttons = buttonsOf(el)
      expect(buttons.length, 'expected the walker to reach both buttons').toBe(2)

      for (const button of buttons) {
        const props = button.props as { onClick?: () => void; 'data-consent'?: string }
        expect(typeof props.onClick, `${props['data-consent']} must carry a handler`).toBe('function')
        props.onClick!()
      }

      const hooks = buttons.map((b) => (b.props as { 'data-consent'?: string })['data-consent'])
      expect(hooks).toEqual(['accept', 'reject'])
      expect(seen, 'the handler must match the hook on its own element').toEqual(hooks)
    })()
  })

  it('a record with an unexpected shape fails CLOSED, and never renders both sentences', async () => {
    // parseConsent guarantees a boolean, but the prop is a plain object at runtime and
    // App.tsx wires a caller. Anything not truthy must read as declined.
    for (const shape of [{}, { analytics: undefined }, { analytics: null }, { analytics: 0 }]) {
      const html = await render({ current: shape as unknown as ConsentRecord })
      expect(html, `${JSON.stringify(shape)} must read as declined`).toContain(SETTING_OFF)
      expect(html).not.toContain(SETTING_ON)
      expect((html.match(/cn-setting/g) ?? []).length).toBe(1)
    }
  })

  it('the copy survives HTML escaping intact — decoded once, never twice', async () => {
    const html = await render()
    expect(html, 'double-escaped ampersand').not.toContain('&amp;amp;')

    const decode = (t: string) =>
      t.replace(/&amp;/g, '&').replace(/&quot;/g, '"').replace(/&#x27;/g, "'").replace(/&lt;/g, '<')

    const body = html.match(/<p class="cn-body">([\s\S]*?)<\/p>/)
    expect(body, 'expected the body paragraph').not.toBeNull()
    expect(decode(body![1])).toBe(BODY_COPY)

    const link = html.match(/<a class="lnk cn-link" href="\/privacy">([\s\S]*?)<\/a>/)
    expect(link, 'expected the policy link').not.toBeNull()
    expect(decode(link![1])).toBe('Read the privacy & cookie policy')
  })

  it('suppressing the card changes the inert attribute and NOTHING else', async () => {
    const suppressed = await render({ suppressed: true })
    const live = await render({ suppressed: false })
    expect(suppressed.replace(' inert=""', '')).toBe(live)
  })

  it('a not-suppressed card emits no inert attribute in any form', async () => {
    // React 19 drops `inert={false}`; a downgrade that emitted the truthy string
    // `inert="false"` would suppress the live card. Control first, then the claim.
    const probe = (html: string) => /\binert(=|\b)/.test(html)
    expect(probe('<div class="cookie-note" inert="false">'), 'control').toBe(true)
    expect(probe('<div class="cookie-note" inert="">'), 'control').toBe(true)
    expect(probe(await render({ suppressed: false }))).toBe(false)
  })

  it('renders silently on every path', async () => {
    const calls: string[] = []
    const methods = ['error', 'warn', 'log'] as const
    const originals = methods.map((m) => console[m])
    methods.forEach((m) => {
      console[m] = (...args: unknown[]) => void calls.push(`${m}: ${String(args[0])}`)
    })
    try {
      await render({ current: null })
      await render({ current: GRANTED, suppressed: true })
      await render({ current: DENIED, suppressed: false })
    } finally {
      methods.forEach((m, i) => {
        console[m] = originals[i]
      })
    }
    expect(calls).toEqual([])
  })
})
