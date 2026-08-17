// RED specs (task-561, LAND-05-02, Test-first) — T2-10..T2-13 plus the AC-8 and
// focus/duration source arms. FIRST CSS-reading test in this repo; the source-read
// idiom is analytics.test.ts:22-27.
//
// Why a parser and not toContain: landing.css:29-33 ALREADY carries a
// `@media (prefers-reduced-motion: reduce)` block (for `html { scroll-behavior: auto }`),
// so a substring check for the at-rule passes without the card being covered at all.
// Every claim below therefore runs through parseRules and carries a planted-hit
// control proving the same instrument can find what it says is absent.
/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const CSS_PATH = join(HERE, '..', 'styles', 'landing.css')
const CSS_SRC = readFileSync(CSS_PATH, 'utf8')
const COMPONENT_PATH = join(HERE, 'CookieNotice.tsx')

type CssRule = { selector: string; body: string; at: string[] }

// Flat scanner: comments and quoted strings are skipped, `@`-preludes push onto an
// at-rule stack, everything else is a style rule whose body runs to the next `}`.
// Sufficient for landing.css (no nested style rules, no braces inside declarations).
function parseRules(css: string): CssRule[] {
  const rules: CssRule[] = []
  const at: string[] = []
  let prelude = ''
  let i = 0
  while (i < css.length) {
    const ch = css[i]
    if (ch === '/' && css[i + 1] === '*') {
      const end = css.indexOf('*/', i + 2)
      i = end === -1 ? css.length : end + 2
      continue
    }
    if (ch === '"' || ch === "'") {
      const end = css.indexOf(ch, i + 1)
      const stop = end === -1 ? css.length : end + 1
      prelude += css.slice(i, stop)
      i = stop
      continue
    }
    if (ch === '{') {
      const head = prelude.trim()
      prelude = ''
      if (head.startsWith('@')) {
        at.push(head.replace(/\s+/g, ' '))
        i += 1
        continue
      }
      const close = css.indexOf('}', i)
      const end = close === -1 ? css.length : close
      rules.push({ selector: head, body: css.slice(i + 1, end), at: [...at] })
      i = end + 1
      continue
    }
    if (ch === '}') {
      at.pop()
      prelude = ''
      i += 1
      continue
    }
    prelude += ch
    i += 1
  }
  return rules
}

function selectorParts(rule: CssRule): string[] {
  return rule.selector
    .split(',')
    .map((s) => s.trim().replace(/\s+/g, ' '))
    .filter(Boolean)
}

function propertiesOf(body: string): string[] {
  return body
    .split(';')
    .map((d) => d.trim())
    .filter(Boolean)
    .map((d) => {
      const idx = d.indexOf(':')
      return idx > 0 ? d.slice(0, idx).trim().toLowerCase() : ''
    })
    .filter(Boolean)
}

const BOX_PROPS = new Set([
  'height', 'width', 'min-height', 'max-height', 'min-width', 'max-width',
  'flex', 'flex-grow', 'flex-shrink', 'flex-basis',
  'padding', 'margin', 'border', 'border-radius',
  'font-size', 'font-weight', 'line-height', 'box-sizing', 'display',
])

function isBoxProp(prop: string): boolean {
  return (
    BOX_PROPS.has(prop) ||
    prop.startsWith('padding-') ||
    prop.startsWith('margin-') ||
    prop.startsWith('border-')
  )
}

// Every box-affecting property is declared once here, so the two buttons cannot
// diverge. font-family / font-size / cursor are in the list because a native
// <button> inherits none of them and no `.asc-app button` rule exists.
const REQUIRED_BUTTON_PROPS = [
  'height', 'flex', 'padding', 'border', 'border-radius',
  'font-weight', 'font-family', 'font-size', 'cursor',
]

const ALLOWED_PSEUDO_PROPS = new Set(['background', 'filter'])

/** The shared base block: exact selector, no pseudo-class, outside any at-rule. */
function buttonBaseRules(css: string): CssRule[] {
  return parseRules(css).filter(
    (r) => r.at.length === 0 && selectorParts(r).some((p) => p === '.cn-actions button'),
  )
}

type ConsentBlock = { selector: string; props: string[]; pseudo: boolean }

/** Every block keyed off a [data-consent=…] hook, base and pseudo-class alike. */
function consentBlocks(css: string): ConsentBlock[] {
  const out: ConsentBlock[] = []
  for (const rule of parseRules(css)) {
    for (const part of selectorParts(rule)) {
      if (!part.includes('[data-consent=')) continue
      // Strip the attribute selector before looking for a pseudo-class, so a colon
      // inside an attribute value can never be mistaken for one.
      const bare = part.replace(/\[[^\]]*\]/g, '')
      out.push({ selector: part, props: propertiesOf(rule.body), pseudo: bare.includes(':') })
    }
  }
  return out
}

/** Reduced-motion blocks whose SELECTOR names `needle` — not merely the at-rule. */
function reducedMotionRulesNaming(css: string, needle: string): CssRule[] {
  return parseRules(css).filter(
    (r) =>
      r.at.some((a) => a.startsWith('@media') && /prefers-reduced-motion\s*:\s*reduce/.test(a)) &&
      selectorParts(r).some((p) => p.includes(needle)),
  )
}

/** Rules that style .cn-link at one class only AND declare text-decoration — the
 *  (0,1,0) form that loses to `.asc-app a` (0,1,1) and ships the link un-underlined. */
function bareCnLinkDecorationRules(css: string): CssRule[] {
  return parseRules(css).filter(
    (r) =>
      selectorParts(r).some((p) => /\.cn-link\b/.test(p) && !/\.lnk\.cn-link\b/.test(p)) &&
      propertiesOf(r.body).includes('text-decoration'),
  )
}

function baseRulesFor(css: string, selector: string): CssRule[] {
  return parseRules(css).filter(
    (r) => r.at.length === 0 && selectorParts(r).some((p) => p === selector),
  )
}

function readComponentSrc(): string {
  expect(existsSync(COMPONENT_PATH), `expected ${COMPONENT_PATH} to exist`).toBe(true)
  return readFileSync(COMPONENT_PATH, 'utf8')
}

// ---------------------------------------------------------------- fixtures

const PLANTED_BUTTON_CSS = `
.cn-actions button { height: 40px; flex: 1; padding: 0 16px; }
[data-consent="accept"] { background: var(--primary); color: var(--primary-foreground); }
[data-consent="reject"] { background: var(--card); color: var(--primary); height: 44px; }
[data-consent="accept"]:hover { filter: brightness(1.22); }
`

// A copy of the block landing.css already ships at :29-33. The decoy T2-11 must not
// be fooled by.
const DECOY_ONLY_CSS = `
@media (prefers-reduced-motion: reduce) {
  html { scroll-behavior: auto; }
}
`

const PLANTED_RM_CSS = `
@media (prefers-reduced-motion: reduce) {
  html { scroll-behavior: auto; }
  .cookie-note { animation: none; }
}
`

// ---------------------------------------------------------------- specs

describe('CookieNotice CSS source (LAND-05-02)', () => {
  it('T2-10(a) / AC-4, AC-9: the shared .cn-actions button block declares every box property once', () => {
    // Population floor + control needle: a misresolved read would pass everything below.
    expect(CSS_SRC.length, 'expected to read a non-empty landing.css').toBeGreaterThan(0)
    expect(CSS_SRC, 'control needle: pre-existing landing.css content').toContain('.ios-nav-link')
    expect(CSS_SRC, 'the card must be styled in landing.css').toContain('.cookie-note')

    const base = buttonBaseRules(CSS_SRC)
    expect(base.length, 'expected exactly one base .cn-actions button rule').toBe(1)

    const props = propertiesOf(base[0].body)
    expect(props.length, 'expected a non-empty declaration set').toBeGreaterThan(0)
    for (const required of REQUIRED_BUTTON_PROPS) {
      expect(props, `.cn-actions button must declare ${required}`).toContain(required)
    }
    // Pins AC-4's border, which nothing else asserts.
    expect(base[0].body).toMatch(/border:\s*1px\s+solid\s+var\(--primary\)/)
  })

  it('T2-10(b) arm 1: base [data-consent] blocks declare exactly {background, color}', () => {
    const blocks = consentBlocks(CSS_SRC).filter((b) => !b.pseudo)
    expect(blocks.length, 'expected the two per-button base blocks').toBe(2)
    for (const block of blocks) {
      expect(new Set(block.props), block.selector).toEqual(new Set(['background', 'color']))
    }
  })

  it('T2-10(b) arm 2: pseudo-class [data-consent] blocks stay within {background, filter}', () => {
    const blocks = consentBlocks(CSS_SRC).filter((b) => b.pseudo)
    expect(blocks.length, 'expected at least the accept hover').toBeGreaterThan(0)
    for (const block of blocks) {
      for (const prop of block.props) {
        expect(ALLOWED_PSEUDO_PROPS.has(prop), `${block.selector} may not declare ${prop}`).toBe(true)
      }
    }
  })

  it('T2-10(b) arm 3: no [data-consent] block of any kind touches box geometry', () => {
    const blocks = consentBlocks(CSS_SRC)
    expect(blocks.length, 'expected the per-button blocks to exist').toBeGreaterThan(0)
    const offenders = blocks.flatMap((b) => b.props.filter(isBoxProp).map((p) => `${b.selector} { ${p} }`))
    expect(offenders).toEqual([])
  })

  it('T2-10(c) / AC-9: no cn-accept / cn-reject class exists in the source', () => {
    // Population floor — the claim is meaningless until the card ships.
    expect(CSS_SRC).toContain('.cookie-note')
    const componentSrc = readComponentSrc()

    // Control: the same instrument finds a planted hit.
    const planted = '.cn-accept { background: red; } .cn-reject { background: blue; }'
    expect(planted).toContain('cn-accept')
    expect(planted).toContain('cn-reject')

    for (const [name, src] of [['landing.css', CSS_SRC], ['CookieNotice.tsx', componentSrc]] as const) {
      expect(src, `${name} must not name cn-accept`).not.toContain('cn-accept')
      expect(src, `${name} must not name cn-reject`).not.toContain('cn-reject')
    }
  })

  it('T2-12: the extractors find a planted box property under a [data-consent] hook (non-vacuity control)', () => {
    // Proves arm (a)'s extractor is not simply returning nothing.
    expect(buttonBaseRules(PLANTED_BUTTON_CSS).length).toBe(1)

    const blocks = consentBlocks(PLANTED_BUTTON_CSS)
    expect(blocks.length, 'expected three planted [data-consent] blocks').toBe(3)

    const reject = blocks.find((b) => b.selector === '[data-consent="reject"]')
    expect(reject, 'expected the planted reject block').toBeDefined()
    expect(new Set(reject!.props)).toEqual(new Set(['background', 'color', 'height']))

    // Arm 1 must go red on it...
    expect(new Set(reject!.props)).not.toEqual(new Set(['background', 'color']))
    // ...and so must arm 3.
    expect(reject!.props.filter(isBoxProp)).toEqual(['height'])

    // Arm 2's allowlist still admits the planted hover, so it is not simply rejecting everything.
    const hover = blocks.find((b) => b.pseudo)
    expect(hover, 'expected the planted hover block').toBeDefined()
    expect(hover!.props.every((p) => ALLOWED_PSEUDO_PROPS.has(p))).toBe(true)
    expect(hover!.props).toEqual(['filter'])
  })

  it('T2-11 control: the extractor finds a .cookie-note reduced-motion rule and ignores the pre-existing one', () => {
    // The decoy is real: landing.css:29-33 already carries a reduced-motion block.
    expect(CSS_SRC).toContain('prefers-reduced-motion')
    expect(
      reducedMotionRulesNaming(CSS_SRC, 'html').length,
      'expected the pre-existing html { scroll-behavior } decoy',
    ).toBeGreaterThan(0)

    // Discrimination: given ONLY the decoy, the .cookie-note query returns nothing.
    // This is what stops the claim below from passing off the pre-existing block.
    expect(reducedMotionRulesNaming(DECOY_ONLY_CSS, '.cookie-note')).toEqual([])

    // Planted hit: with the decoy AND a .cookie-note rule present it finds exactly the latter.
    const planted = reducedMotionRulesNaming(PLANTED_RM_CSS, '.cookie-note')
    expect(planted.length).toBe(1)
    expect(propertiesOf(planted[0].body)).toContain('animation')
  })

  it('T2-11 / AC-8: the entry animation is dropped under prefers-reduced-motion: reduce', () => {
    const hits = reducedMotionRulesNaming(CSS_SRC, '.cookie-note')
    expect(hits.length, 'expected a reduced-motion block naming .cookie-note').toBeGreaterThan(0)
    expect(hits.flatMap((r) => propertiesOf(r.body))).toContain('animation')
  })

  it('AC-8: the card is position: fixed at z-index 40', () => {
    const cards = baseRulesFor(CSS_SRC, '.cookie-note')
    expect(cards.length, 'expected exactly one base .cookie-note rule').toBe(1)
    expect(cards[0].body).toMatch(/position:\s*fixed/)
    expect(cards[0].body).toMatch(/z-index:\s*40\b/)
  })

  it('the entry animation is fully tokenised — var(--dur-base), never a literal 220ms', () => {
    const cards = baseRulesFor(CSS_SRC, '.cookie-note')
    expect(cards.length, 'expected exactly one base .cookie-note rule').toBe(1)
    expect(cards[0].body).toContain('var(--dur-base)')
    expect(cards[0].body).toContain('var(--ease-out)')
    expect(cards[0].body).not.toContain('220ms')
  })

  it('the focus ring is an outline with a real 2px offset, declared once for both buttons', () => {
    // Deliberate divergence from the design system's box-shadow focus idiom: that
    // idiom is scoped to input/textarea (app-layer.css:236-240) so there is no
    // cascade conflict, and box-shadow has no offset semantics.
    const rules = parseRules(CSS_SRC).filter((r) =>
      selectorParts(r).some((p) => p === '.cn-actions button:focus-visible'),
    )
    expect(rules.length, 'expected exactly one shared :focus-visible rule').toBe(1)
    expect(rules[0].body).toMatch(/outline:\s*2px\s+solid\s+var\(--ring\)/)
    expect(rules[0].body).toMatch(/outline-offset:\s*2px/)
    expect(propertiesOf(rules[0].body)).not.toContain('box-shadow')
  })

  it('T2-13(a,b) / AC-3: the underline is declared on the two-class .lnk.cn-link selector', () => {
    const rules = parseRules(CSS_SRC).filter((r) => selectorParts(r).some((p) => /\.lnk\.cn-link\b/.test(p)))
    expect(rules.length, 'expected a .lnk.cn-link rule — (0,2,0) beats .asc-app a at (0,1,1)').toBeGreaterThan(0)
    const body = rules.map((r) => r.body).join('\n')
    expect(body).toMatch(/text-decoration:\s*underline/)
    expect(body).toMatch(/text-underline-offset:\s*3px/)
  })

  it('T2-13(c) / AC-3: no single-class .cn-link rule declares text-decoration', () => {
    // Population floor — vacuous until the card ships.
    expect(CSS_SRC).toContain('.cookie-note')

    // Control: the detector finds a planted bare rule...
    expect(
      bareCnLinkDecorationRules('.cn-link { text-decoration: underline; text-underline-offset: 3px; }').length,
      'control: expected the detector to find a planted bare .cn-link rule',
    ).toBe(1)
    // ...and does not misfire on the compound form.
    expect(bareCnLinkDecorationRules('.lnk.cn-link { text-decoration: underline; }')).toEqual([])

    expect(bareCnLinkDecorationRules(CSS_SRC)).toEqual([])
  })
})
