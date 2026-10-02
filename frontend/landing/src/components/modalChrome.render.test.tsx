// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// SSR markup of both modals, parsed into a detached DOM; nothing is mounted.
// modalChrome loads through a runtime specifier so a missing module fails as an assertion.
/// <reference types="node" />
import { describe, expect, it, vi } from 'vitest'
import { createElement } from 'react'
import type { CSSProperties, ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { GLYPHS } from '../icons'
import { DemoModal } from './DemoModal'
import { SignInModal } from './SignInModal'

const HERE = dirname(fileURLToPath(import.meta.url))
const MODULE_PATH = join(HERE, 'modalChrome.tsx')
const MODULE_SPECIFIER = './modalChrome'

type ChromeModule = {
  MODAL_SCRIM_STYLE: CSSProperties
  modalCardStyle: (maxWidth: number) => CSSProperties
  MODAL_CHROME_CSS: string
  ModalHeader: (props: { onClose: () => void; padX: 18 | 20 }) => ReactElement
}

async function loadChrome(): Promise<ChromeModule> {
  expect(existsSync(MODULE_PATH), `expected ${MODULE_PATH} to exist`).toBe(true)
  const mod = (await import(MODULE_SPECIFIER)) as ChromeModule
  expect(typeof mod.MODAL_SCRIM_STYLE, 'expected a MODAL_SCRIM_STYLE export').toBe('object')
  expect(typeof mod.modalCardStyle, 'expected a modalCardStyle export').toBe('function')
  expect(typeof mod.MODAL_CHROME_CSS, 'expected a MODAL_CHROME_CSS export').toBe('string')
  expect(typeof mod.ModalHeader, 'expected a ModalHeader export').toBe('function')
  return mod
}

function noop() {}

type Rendered = { html: string; dialog: HTMLElement; card: HTMLElement; header: HTMLElement }

function parse(html: string): Rendered {
  const host = document.createElement('div')
  host.innerHTML = html
  const dialog = host.querySelector<HTMLElement>('[role="dialog"]')
  expect(dialog, 'expected a role="dialog" element').not.toBeNull()
  const card = Array.from(dialog!.children).find((c) => c.tagName === 'DIV') as HTMLElement | undefined
  expect(card, 'expected a card <div> inside the dialog').toBeDefined()
  const header = card!.firstElementChild as HTMLElement | null
  expect(header, 'expected a header as the card’s first child').not.toBeNull()
  return { html, dialog: dialog!, card: card!, header: header! }
}

const signIn = () => parse(renderToStaticMarkup(createElement(SignInModal, { onClose: noop })))
const demo = () => parse(renderToStaticMarkup(createElement(DemoModal, { onClose: noop })))

function configuredSignInHtml(): string {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
  vi.stubEnv('VITE_APP_URL', 'https://app.x/')
  try {
    return renderToStaticMarkup(createElement(SignInModal, { onClose: noop }))
  } finally {
    vi.unstubAllEnvs()
  }
}

// `prop: value` pairs of an inline style attribute, in source order.
function declarations(el: Element): Array<[string, string]> {
  const raw = el.getAttribute('style')
  expect(raw, 'expected a style attribute').toBeTruthy()
  return raw!
    .split(';')
    .filter((d) => d.trim() !== '')
    .map((d) => {
      const i = d.indexOf(':')
      return [d.slice(0, i).trim(), d.slice(i + 1).trim()] as [string, string]
    })
}

function cssRules(css: string): string[] {
  const flat = css.replace(/@media\s*\([^)]*\)\s*\{/g, '')
  return Array.from(flat.matchAll(/([^{}]+)\{([^{}]*)\}/g))
    .map((m) => `${m[1].split(/\s+/).filter(Boolean).join(' ')} { ${m[2].split(/\s+/).filter(Boolean).join(' ')} }`)
    .filter((r) => !r.startsWith(' '))
}

describe('the shared modal chrome (RESKIN-06-01)', () => {
  it('MC-00 the chrome module loads', async () => {
    const mod = await loadChrome()
    expect(mod.modalCardStyle(452).maxWidth).toBe(452)
    expect(mod.modalCardStyle(510).maxWidth).toBe(510)
  })

  it('MC-01 the two scrims are one style', () => {
    const a = signIn()
    const b = demo()
    const sa = a.dialog.getAttribute('style')
    expect(sa).toBeTruthy()
    expect(sa).toBe(b.dialog.getAttribute('style'))
    expect(sa).toContain('background:color-mix(in srgb, var(--surface) 55%, transparent)')
    expect(sa).toContain('z-index:200')
    expect(sa).toContain('backdrop-filter:blur(6px)')
    expect(sa).toContain('animation:ovIn 160ms ease-out')
  })

  it('MC-02 the two cards differ only in max-width', () => {
    const cards = [
      { r: signIn(), width: '452px' },
      { r: demo(), width: '510px' },
    ]
    const remainders = cards.map(({ r, width }) => {
      const decls = declarations(r.card)
      const mw = decls.filter(([k]) => k === 'max-width')
      expect(mw.map(([, v]) => v)).toEqual([width])
      return decls.filter(([k]) => k !== 'max-width')
    })
    expect(remainders[0].length).toBeGreaterThan(0)
    expect(remainders[0]).toEqual(remainders[1])
    const flat = Object.fromEntries(remainders[0])
    expect(flat['border-radius']).toBe('var(--radius-md)')
    expect(flat['box-shadow']).toBe('var(--shadow-elegant)')
    expect(flat.border).toBe('1px solid var(--border)')
    expect(flat.background).toBe('var(--card)')
    expect(flat['max-height']).toBe('calc(100dvh - 48px)')
    expect(flat['overflow-y']).toBe('auto')
    expect(flat.animation).toBe('cardIn 200ms var(--ease-out)')
  })

  it('MC-03 the header is the DS logo and a v2 close', () => {
    for (const [label, r] of [['sign-in', signIn()], ['demo', demo()]] as const) {
      const logos = r.header.querySelectorAll('img.ds-logo-mark')
      expect(logos.length, `${label}: ds-logo-mark count`).toBe(1)
      expect(logos[0].getAttribute('width'), label).toBe('28')
      const closes = r.header.querySelectorAll('button[aria-label="Close"]')
      expect(closes.length, `${label}: Close count`).toBe(1)
      const decls = Object.fromEntries(declarations(closes[0]))
      expect(decls.width, label).toBe('36px')
      expect(decls.height, label).toBe('36px')
      expect(decls['border-radius'], label).toBe('var(--radius-btn)')
      const d = Array.from(closes[0].querySelectorAll('path')).map((p) => p.getAttribute('d'))
      expect(d.length, `${label}: close glyph paths`).toBeGreaterThan(0)
      expect(d, label).toEqual([...GLYPHS.x])
    }
  })

  it('MC-04 no oklch and no pill in either modal', () => {
    const OKLCH = /oklch\(/gi
    const PILL = /border-radius:\s*(?:99|999|9999)px\b|--radius-pill/g
    const planted = '<div style="border-radius:99px;color:oklch(50% .1 200)">'
    expect(planted.match(OKLCH)?.length).toBe(1)
    expect(planted.match(PILL)?.length).toBe(1)

    const surfaces = [
      ['sign-in', signIn().html],
      ['sign-in (configured)', configuredSignInHtml()],
      ['demo', demo().html],
    ] as const
    for (const [label, html] of surfaces) {
      expect(html.length, label).toBeGreaterThan(0)
      expect(html.match(OKLCH), `${label}: oklch(`).toBeNull()
      expect(html.match(PILL), `${label}: pill radius`).toBeNull()
    }
  })

  it('MC-06 the chrome CSS is the v2 set', async () => {
    const { MODAL_CHROME_CSS } = await loadChrome()
    const rules = cssRules(MODAL_CHROME_CSS)
    expect(rules.length).toBeGreaterThan(0)
    expect([...rules].sort()).toEqual(
      [
        'from { opacity: 0; }',
        'to { opacity: 1; }',
        'from { opacity: 0; transform: translateY(8px); }',
        'to { opacity: 1; transform: none; }',
        '.si-close { transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out); }',
        '.si-close:hover { background: var(--muted); color: var(--ink); }',
        '.si-close:focus-visible { outline: 2px solid var(--ring); outline-offset: 2px; }',
      ].sort(),
    )
    // cssRules drops the keyframe names.
    expect(MODAL_CHROME_CSS).toMatch(/@keyframes\s+ovIn\s*\{\s*from\s*\{\s*opacity:\s*0;\s*\}\s*to\s*\{\s*opacity:\s*1;\s*\}\s*\}/)
    expect(MODAL_CHROME_CSS).toMatch(/@keyframes\s+cardIn\s*\{\s*from\s*\{\s*opacity:\s*0;\s*transform:\s*translateY\(8px\);/)
  })
})
