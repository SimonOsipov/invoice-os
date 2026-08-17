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
  ['C1 consent-gated', 'It runs only if you have allowed analytics'],
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
  ['C18 the notice is the control', 'The cookie notice on this site is where you choose'],
  ['E1 optional answers are pre-selected', 'come with an answer already selected when the form opens'],
  ['E2 not a marketing list', 'does not add you to a marketing list'],
  ['E3 our code sets no cookies', 'Our own code sets no cookies at all'],
  ['lede: no third company', 'Your browser loads nothing on this site from any other company'],
]

// AC7: each mechanism must state what it does NOT stop. Deleting either
// qualification left Privacy.render.test.tsx green.
const WITHDRAWAL_NEEDLES: readonly (readonly [string, string])[] = [
  ['W3 lead-in', 'Block or clear cookies for this site'],
  // The condition is load-bearing: unqualified, this claim is false under the
  // denied default (consent.ts CONSENT_DEFAULT_ANALYTICS), because no tag loads.
  [
    'W3 does not stop the measurement, once analytics is allowed',
    'If you have allowed analytics, it does not stop the measurement itself',
  ],
  // Without this the denied branch could be deleted and every other row stays green.
  [
    'W3 there is nothing to stop, while analytics is not allowed',
    'If you have not allowed analytics, you are not being measured at all',
  ],
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

// RED specs (task-562, LAND-05-03, Test-first) — T3-16, T3-19 and the docs sweep the
// mount falsifies. Oracles only: none of them pins wording this file invented.

describe('T3-16 (AC-9): the C18 needle is retargeted, not dropped', () => {
  it('a C18 row still exists and no longer pins a denial', () => {
    expect(LEDGER_NEEDLES.length, 'the ledger needle table shrank').toBeGreaterThanOrEqual(17)
    const rows = LEDGER_NEEDLES.filter(([id]) => id.startsWith('C18'))
    expect(rows.length, 'expected exactly one C18 row').toBe(1)

    const needle = rows[0][1]
    expect(needle.length, 'the C18 needle is empty').toBeGreaterThan(0)
    for (const denial of ['no privacy control', 'no notice, no toggle', 'is being built', 'does not exist yet']) {
      expect(needle.toLowerCase(), `C18 still pins a denial: "${needle}"`).not.toContain(denial)
    }
    // The it.each row above already asserts the needle is on the page, so deleting
    // the replacement sentence from Privacy.tsx turns C18 red.
    expect(html, `C18 needle is not on the page: "${needle}"`).toContain(needle)
  })
})

describe('T3-19 (AC-12): both branches of the W3 passage are pinned', () => {
  it('the denied branch has a needle of its own beside the allowed one', () => {
    const rows = WITHDRAWAL_NEEDLES.filter(([id]) => id.startsWith('W3'))
    expect(rows.length, 'expected a W3 lead-in plus both branches').toBeGreaterThanOrEqual(3)

    const allowed = rows.filter(([, needle]) => /if you have allowed analytics/i.test(needle))
    const denied = rows.filter(([, needle]) => /have not allowed analytics/i.test(needle))
    expect(allowed.length, 'the allowed branch lost its needle').toBe(1)
    expect(denied.length, 'the denied branch has no needle — deleting that sentence stays green').toBe(1)

    for (const [id, needle] of rows) {
      expect(html, `${id} is not on the page`).toContain(needle)
    }
  })
})

describe('AC-12: the claim ledger asserts nothing the mount makes false', () => {
  const ledger = readFileSync(join(DOCS, 'privacy-policy-claims.md'), 'utf8')
  // The ledger wraps its prose, so every needle is matched against a whitespace-
  // flattened copy.
  const flat = ledger.replace(/\s+/g, ' ')

  // Ten line-sites, not the five the plan named. gaCookies.ts becomes the repo's
  // first document.cookie writer and consentActions.ts writeConsent's first
  // production caller, which is what falsifies E3's evidence and C18/W2.
  const FALSIFIED: readonly (readonly [string, string])[] = [
    ['C18 and W2 — zero call sites', 'zero production call sites'],
    ['Table 2 lead — no control', 'There is no consent control on this site yet'],
    ['Table 2 lead — never called', 'is never called by anything that ships'],
    ['Table 2 lead — no way to allow', 'there is no way for a visitor to allow it either'],
    ['the rule behind the D7 guard', 'may not mention a notice, a banner, a Reject button or a preference centre'],
    ['forward instruction to this subtask', 'LAND-05-03 still rewrites this section'],
    ['W2 — no control of its own', 'The site has no privacy control of its own yet'],
    ['W5 — the quoted send() guard', 'if (!loaded) return'],
    ['E3 — the document.cookie grep', 'occurrences in `frontend/landing/src`'],
    ['E3 — nothing writes the key', 'and nothing writes it'],
    ['deliberate omissions — no description', 'No description of the consent notice'],
  ]

  it('control: the ledger read resolved and its stable markers survive', () => {
    expect(ledger.length).toBeGreaterThan(0)
    expect(ledger, 'the C18 row marker must never be renamed, only its cell text').toContain('| C18 |')
    expect(ledger).toContain('Privacy.tsx')
    expect(FALSIFIED.length).toBe(11)
  })

  it.each(FALSIFIED)('%s is corrected in the same commit as the mount', (_label, needle) => {
    expect(flat, `the ledger still asserts: "${needle}"`).not.toContain(needle)
  })

  it('the section this subtask discharges is recorded as closed, per the ledger own rule', () => {
    expect(flat).toContain('Closed at LAND-05-03')
  })
})

describe('AC-12: docs/analytics.md carries the OWED enhanced-measurement item', () => {
  const analytics = readFileSync(join(DOCS, 'analytics.md'), 'utf8')
  const items = Array.from(analytics.matchAll(/^(\d+)\. \*\*/gm))

  it('control: the doc read resolved and its checklist is numbered', () => {
    expect(analytics.length).toBeGreaterThan(0)
    expect(analytics).toContain('## Operator checklist')
    expect(items.length, 'no numbered checklist items found').toBeGreaterThan(0)
  })

  it('the stated count matches the list', () => {
    expect(analytics, 'the checklist header still says six').not.toContain('Six items.')
    expect(analytics).toContain('Seven items.')
    expect(items.length).toBe(7)
  })

  it('the new item is OPEN, never reported as done', () => {
    const idx = analytics.indexOf('\n7. **')
    expect(idx, 'no seventh checklist item').toBeGreaterThan(-1)
    const seventh = analytics.slice(idx)
    expect(seventh.toLowerCase()).toContain('enhanced measurement')
    // The tripwire: the row above pins exactly two `operator-confirmed 2026-08-16`
    // over this file, so wording item 7 as confirmed turns it red. That is the
    // mechanism that stops the owed item being reported as discharged on merge.
    expect(seventh, 'the owed item is worded as already confirmed').not.toContain('operator-confirmed 2026-08-16')
  })
})
