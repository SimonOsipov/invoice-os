// Adversarial coverage for LAND-04-02, added at QA after a mutation sweep of
// Privacy.render.test.tsx: deleting whole ledger claims from the page (C2, C6,
// C9, C13/E1, C16, C17), dropping a withdrawal qualification, retyping the
// hostname, or letting the rendered measure diverge from data-prose-max all
// left that file green.
//
// Needles are apostrophe-, ampersand- and quote-free: SSR escapes ASCII ' to
// &#x27; (Privacy.render.test.tsx header).
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { PROSE_MAX_WIDTH, Privacy } from './Privacy'

const SRC_DIR = fileURLToPath(new URL('.', import.meta.url))
const PRIVACY_TSX = join(SRC_DIR, 'Privacy.tsx')
const DOCS = join(SRC_DIR, '..', '..', '..', '..', 'docs')

const html = renderToStaticMarkup(createElement(Privacy))

// One row per ledger claim that had no render assertion. docs/privacy-policy-claims.md
// is the authority for what each id means.
const LEDGER_NEEDLES: readonly (readonly [string, string])[] = [
  ['C1 present tense', 'It is running right now, on this page'],
  ['C2 not in the signed-in product', 'There is no analytics code anywhere inside the signed-in ASComply product'],
  ['C5 no identifying detail', 'We never send Google your name, your email address, your company'],
  ['C6 two cookies', 'Google Analytics sets two cookies on your device'],
  ['C6 the _ga names', 'One is named _ga; the other starts _ga_'],
  ['C7 transfer out of Nigeria and the EEA', 'outside Nigeria and outside the EEA'],
  ['C9 Google Signals off', 'We have Google Signals turned off'],
  ['C11 HubSpot receives the answers', 'Booking a demo sends your answers to HubSpot'],
  ['C12 the name is split', 'split from the single full name you typed'],
  ['C13 empties are dropped', 'Anything left empty is not sent at all'],
  ['C16 no browsing history to HubSpot', 'We do not send HubSpot your browsing history, the pages you visited'],
  ['C17 no advertising network', 'We run no advertising network on this site'],
  ['C18 no privacy control yet', 'This site has no privacy control of its own yet'],
  ['E1 optional answers are pre-selected', 'come with an answer already selected when the form opens'],
  ['E2 not a marketing list', 'does not add you to a marketing list'],
  ['E3 our code sets no cookies', 'Our own code sets no cookies at all'],
  ['lede: no third company', 'Your browser loads nothing on this site from any other company'],
]

// AC7: each mechanism must state what it does NOT stop. Deleting either
// qualification left Privacy.render.test.tsx green.
const WITHDRAWAL_NEEDLES: readonly (readonly [string, string])[] = [
  ['W3 lead-in', 'Block or clear cookies for this site'],
  ['W3 does not stop the measurement', 'It does not stop the measurement itself'],
  ['W4 does not stop fonts or the form', 'It does not affect the fonts and it does not affect the demo form'],
  ['W5 lead-in', 'Block googletagmanager.com with a content blocker'],
  ['W5 does not stop the fonts', 'It does not stop the fonts, which come from a different Google host'],
  ['W7 none of the four stops the fonts', 'None of the four stops Google Fonts'],
]

describe('AC1: every ledger claim survives on the page', () => {
  it.each(LEDGER_NEEDLES)('%s', (_id, needle) => {
    expect(html).toContain(needle)
  })
})

describe('AC7: each withdrawal mechanism states what it does not stop', () => {
  it.each(WITHDRAWAL_NEEDLES)('%s', (_id, needle) => {
    expect(html).toContain(needle)
  })
})

describe('AC9: the declared measure is the rendered measure', () => {
  it('the prose element carries max-width equal to PROSE_MAX_WIDTH, not just the data attribute', () => {
    // A mutation setting maxWidth to 900 while data-prose-max stayed 720 kept
    // every existing row green — LAND-04-05 compares the two in a browser.
    const tag = html.match(/<[^>]*data-testid="privacy-prose"[^>]*>/)?.[0]
    expect(tag, 'privacy-prose tag not found').toBeDefined()
    expect(tag).toContain(`max-width:${PROSE_MAX_WIDTH}px`)
    expect(tag).toContain(`data-prose-max="${PROSE_MAX_WIDTH}"`)
  })

  it('the prose block is nested inside the container, not a sibling', () => {
    // LAND-04-05 measures the prose column inside the container; swapping the
    // two testids keeps the presence assertions green.
    const container = html.indexOf('data-testid="privacy-container"')
    const prose = html.indexOf('data-testid="privacy-prose"')
    expect(container).toBeGreaterThan(-1)
    expect(prose).toBeGreaterThan(container)
    expect(html.slice(container, prose)).not.toContain('</div>')
  })
})

describe('AC1: the hostname is imported, never retyped', () => {
  it('Privacy.tsx imports PRODUCTION_HOSTNAMES and carries no hostname literal', () => {
    const src = readFileSync(PRIVACY_TSX, 'utf8')
    expect(src).toMatch(/import\s*\{[^}]*PRODUCTION_HOSTNAMES[^}]*\}\s*from\s*['"]\.\.\/hubspot['"]/)
    expect(src, 'the hostname is retyped instead of read from the allowlist').not.toContain('www.ascomply.com')
  })
})

describe('mobile: nothing can force horizontal scroll', () => {
  it('the page declares only max-widths, never a fixed width', () => {
    // 390px viewport - 32px container padding either side = 326px of content.
    // A `width:` would ignore that; a `max-width:` cannot.
    expect(html.match(/[^-]width:/g) ?? []).toHaveLength(0)
  })
})

describe('outline', () => {
  it('one h1 and no h3 — LAND-04-03 swaps out the sites only other h1', () => {
    expect(html.match(/<h1/g) ?? []).toHaveLength(1)
    expect(html.match(/<h3/g) ?? []).toHaveLength(0)
    expect((html.match(/<h2/g) ?? []).length).toBeGreaterThan(0)
  })
})

describe('AC11 + AC12: the docs this page is defended by', () => {
  it('the claim ledger exists and covers C1 to C19', () => {
    const ledger = readFileSync(join(DOCS, 'privacy-policy-claims.md'), 'utf8')
    for (let n = 1; n <= 19; n += 1) {
      expect(ledger, `ledger has no row C${n}`).toContain(`| C${n} |`)
    }
    expect(ledger).toContain('Privacy.tsx')
  })

  it('docs/analytics.md no longer says the measurement id is absent', () => {
    const analytics = readFileSync(join(DOCS, 'analytics.md'), 'utf8')
    expect(analytics).not.toContain('Measured absent')
    expect(analytics.match(/operator-confirmed 2026-08-16/g) ?? []).toHaveLength(2)
  })
})
