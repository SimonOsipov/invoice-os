// The cookie notice's CSS-source claims: the button box contract, the cascade win on
// the policy link, and the reduced-motion, geometry and focus arms. Source-read idiom
// from analytics.test.ts:22-27.
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

/** The value declared for `prop` in a rule body, whitespace-collapsed, or null. */
function valueOf(body: string, prop: string): string | null {
  for (const d of body.split(';')) {
    const idx = d.indexOf(':')
    if (idx > 0 && d.slice(0, idx).trim().toLowerCase() === prop) return d.slice(idx + 1).trim().replace(/\s+/g, ' ')
  }
  return null
}

const BOX_PROPS = new Set([
  'height', 'width', 'min-height', 'max-height', 'min-width', 'max-width',
  'flex', 'flex-grow', 'flex-shrink', 'flex-basis',
  'padding', 'margin', 'border', 'border-radius',
  'font-size', 'font-weight', 'line-height', 'box-sizing', 'display',
])

// border-color paints the outline and cannot move the box; border-width still can.
function isBoxProp(prop: string): boolean {
  if (prop === 'border-color') return false
  return (
    BOX_PROPS.has(prop) ||
    prop.startsWith('padding-') ||
    prop.startsWith('margin-') ||
    prop.startsWith('border-')
  )
}

// Every box-affecting property is declared once here, so the two buttons cannot
// diverge. font-family / font-size / cursor are in the list because a native
// <button> inherits none of them (the page base sets only font-family).
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

// Matches the hook under any attribute operator (=, ^=, *=, |=, ~=) and any interior
// whitespace. A literal '[data-consent=' misses `[data-consent = "accept"]` and
// `[data-consent^="acc"]`, both of which style a real button.
const CONSENT_ATTR = /\[\s*data-consent\b/

/** Every block keyed off a [data-consent…] hook, base and pseudo-class alike. */
function consentBlocks(css: string): ConsentBlock[] {
  const out: ConsentBlock[] = []
  for (const rule of parseRules(css)) {
    for (const part of selectorParts(rule)) {
      if (!CONSENT_ATTR.test(part)) continue
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
 *  (0,1,0) form that loses to a two-class rule and ships the link un-underlined. */
function bareCnLinkDecorationRules(css: string): CssRule[] {
  return parseRules(css).filter(
    (r) =>
      selectorParts(r).some((p) => /\.cn-link\b/.test(p) && !/\.lnk\.cn-link\b/.test(p)) &&
      propertiesOf(r.body).includes('text-decoration'),
  )
}

type Spec = [number, number, number]

function specificity(selector: string): Spec {
  let [a, b, c] = [0, 0, 0]
  let s = selector.replace(/:where\([^)]*\)/g, '')
  s = s.replace(/:(?:not|is|has)\(([^)]*)\)/g, (_m, arg: string) => {
    const [x, y, z] = specificity(arg)
    a += x
    b += y
    c += z
    return ''
  })
  s = s.replace(/\[[^\]]*\]/g, () => (b++, ''))
  s = s.replace(/#[\w-]+/g, () => (a++, ''))
  s = s.replace(/::[\w-]+/g, () => (c++, ''))
  s = s.replace(/\.[\w-]+/g, () => (b++, ''))
  s = s.replace(/:[\w-]+/g, () => (b++, ''))
  s = s.replace(/(^|[\s>+~])[a-zA-Z][\w-]*/g, (_m, lead: string) => (c++, lead))
  return [a, b, c]
}

// Last compound is `a`, `a.lnk` or `.lnk`, optionally `:hover` — whatever ancestors precede it.
const LINK_TARGET = /^(?:a(?:\.lnk)?|\.lnk)(?::hover)?$/

/** Rules that can set the policy link's text-decoration, with the specificity of each matching selector. */
function linkDecorationRules(css: string): { selector: string; spec: Spec }[] {
  return parseRules(css)
    .filter((r) => propertiesOf(r.body).some((p) => p.startsWith('text-decoration')))
    .flatMap((r) =>
      selectorParts(r)
        .filter((p) => LINK_TARGET.test(p.split(/[\s>+~]+/).pop() ?? ''))
        .map((selector) => ({ selector, spec: specificity(selector) })),
    )
}

/** True when `spec` is below (0,2,0), the specificity of .lnk.cn-link. */
const weakerThanLnkCnLink = ([a, b]: Spec) => a === 0 && b < 2

// Any selector that can reach a <button> inside the card, whether or not it names the
// [data-consent] hook. `.cn-actions button:first-child` and `.cn-actions button + button`
// diverge the two boxes without mentioning the hook at all, so the hook-keyed arms alone
// do not encode "the two buttons cannot diverge".
const CARD_SCOPE = /\.cn-actions|\.cookie-note/
const SHARED_BUTTON_SELECTOR = '.cn-actions button'

function cardButtonBlocks(css: string): { selector: string; props: string[] }[] {
  const out: { selector: string; props: string[] }[] = []
  for (const rule of parseRules(css)) {
    for (const part of selectorParts(rule)) {
      const reachesButton = /\bbutton\b/.test(part) && CARD_SCOPE.test(part)
      if (!reachesButton && !CONSENT_ATTR.test(part)) continue
      out.push({ selector: part, props: propertiesOf(rule.body) })
    }
  }
  return out
}

/** Box declarations made anywhere BUT the one shared selector. */
function boxOffenders(css: string): string[] {
  return cardButtonBlocks(css)
    .filter((b) => b.selector !== SHARED_BUTTON_SELECTOR)
    .flatMap((b) => b.props.filter(isBoxProp).map((prop) => `${b.selector} { ${prop} }`))
}

function baseRulesFor(css: string, selector: string): CssRule[] {
  return parseRules(css).filter(
    (r) => r.at.length === 0 && selectorParts(r).some((p) => p === selector),
  )
}

/** The px value declared for `prop` in a rule body, or null when it is absent or unitless. */
function pxOf(body: string, prop: string): number | null {
  const m = new RegExp(`(?:^|;)\\s*${prop}\\s*:\\s*(-?[\\d.]+)px\\s*(?:;|$)`).exec(body)
  return m ? Number(m[1]) : null
}

const SKIN_PROPS = /^(?:background(?:-.+)?|border(?:-.+)?|box-shadow)$/

/** Declarations that repaint the card from a rule whose subject is `.cookie-note`, any at-rule. */
function skinOffenders(css: string): string[] {
  return parseRules(css).flatMap((r) =>
    selectorParts(r)
      .filter((p) => (p.split(/[\s>+~]+/).pop() ?? '').includes('.cookie-note'))
      .flatMap((p) => propertiesOf(r.body).filter((prop) => SKIN_PROPS.test(prop)).map((prop) => `${p} { ${prop} }`)),
  )
}

/** Selectors of every rule that declares --cn-band, at any at-rule depth. */
function cnBandDeclarers(css: string): string[] {
  return parseRules(css)
    .filter((r) => propertiesOf(r.body).includes('--cn-band'))
    .flatMap(selectorParts)
}

function outranks(a: Spec, b: Spec): boolean {
  return a[0] !== b[0] ? a[0] > b[0] : a[1] !== b[1] ? a[1] > b[1] : a[2] > b[2]
}

/** The rule declaring --cn-band under `:root`, base (no at-rule) or inside the 640px query. */
function cnBandRoot(css: string, phone: boolean): CssRule[] {
  return parseRules(css).filter(
    (r) =>
      selectorParts(r).includes(':root') &&
      propertiesOf(r.body).includes('--cn-band') &&
      (phone ? r.at.length === 1 && /max-width:\s*640px/.test(r.at[0]) : r.at.length === 0),
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

// Five selector shapes that each diverge the two button boxes on a live page while the
// hook-keyed arms (T2-10 a/b) stay green. Every one was verified to slip through before
// cardButtonBlocks existed.
const EVASION_CSS = `
[data-consent = "accept"] { height: 72px; }
[data-consent^="acc"] { padding: 0 48px; }
.cn-actions button:first-child { flex: 3; }
.cn-actions button + button { font-weight: 800; }
.cookie-note .cn-actions button:nth-child(2) { min-width: 300px; }
`

const PLANTED_SKIN_CSS = `
.cookie-note { box-shadow: none; }
@media (max-width: 640px) { .cookie-note { border-radius: 0; } }
.cookie-note .cn-body { background: red; }
`

const PLANTED_BAND_CSS = `
:root { --cn-band: 320px; }
html:has(.cookie-note) { --cn-band: 1px; }
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
    // Pins AC-4's border, which nothing else asserts. Transparent: Reject's outline colour
    // comes from its own per-button rule (CN-2).
    expect(base[0].body).toMatch(/border:\s*1px\s+solid\s+transparent/)
  })

  it('T2-10(b) arm 1: base [data-consent] blocks set colours only — Accept {background, color}, Reject adds border-color', () => {
    const blocks = consentBlocks(CSS_SRC).filter((b) => !b.pseudo)
    expect(blocks.length, 'expected the two per-button base blocks').toBe(2)
    const accept = blocks.find((b) => b.selector.includes('"accept"'))
    const reject = blocks.find((b) => b.selector.includes('"reject"'))
    expect(accept, 'expected the Accept base block').toBeDefined()
    expect(reject, 'expected the Reject base block').toBeDefined()
    expect(new Set(accept!.props), accept!.selector).toEqual(new Set(['background', 'color']))
    expect(new Set(reject!.props), reject!.selector).toEqual(new Set(['background', 'color', 'border-color']))
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
    // ...and so must the box-geometry scan's isBoxProp.
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
    // An outline has a real offset; box-shadow has no offset semantics.
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
    expect(rules.length, 'expected a .lnk.cn-link rule — (0,2,0) beats the v2 a {} rule at (0,0,1)').toBeGreaterThan(0)
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

  it('T2-13(d) / AC-3: every rule that can set the policy link decoration is weaker than .lnk.cn-link', () => {
    const planted = linkDecorationRules('.asc-app a.lnk { text-decoration: none; }')
    expect(planted, 'control: the detector finds the planted two-class-plus-type rule').toEqual([
      { selector: '.asc-app a.lnk', spec: [0, 2, 1] },
    ])
    expect(planted.filter((r) => !weakerThanLnkCnLink(r.spec)), 'control: it is flagged').toHaveLength(1)
    expect(
      linkDecorationRules('a { text-decoration: none; } a:hover { text-decoration: none; }').filter((r) => !weakerThanLnkCnLink(r.spec)),
      'control: one-type and one-type-plus-pseudo rules are not flagged',
    ).toEqual([])

    const utilitiesPath = join(HERE, '..', '..', '..', '..', 'packages', 'design-tokens', 'v2', 'utilities.css')
    const bridgePath = join(HERE, '..', 'styles', 'bridge.css')
    expect(existsSync(bridgePath), `expected ${bridgePath} to exist ([RESKIN-01-02] adds the bridge)`).toBe(true)
    const rules = [
      ...[utilitiesPath, bridgePath].flatMap((path) => linkDecorationRules(readFileSync(path, 'utf8'))),
      ...linkDecorationRules(CSS_SRC),
    ]
    expect(rules.some((r) => r.selector === 'a'), 'the v2 a {} rule is among them').toBe(true)
    expect(rules.filter((r) => !weakerThanLnkCnLink(r.spec))).toEqual([])
  })

  it('AC-9: only the shared .cn-actions button selector declares box geometry on the card buttons', () => {
    const blocks = cardButtonBlocks(CSS_SRC)
    expect(blocks.length, 'expected the card button rules to exist').toBeGreaterThan(0)
    expect(
      blocks.some((b) => b.selector === SHARED_BUTTON_SELECTOR),
      'control needle: the shared base selector must be among them',
    ).toBe(true)
    expect(boxOffenders(CSS_SRC)).toEqual([])
  })

  it('control: the box-geometry scan catches the five shapes that evaded the hook-keyed arms', () => {
    // Without this the claim above passes on an extractor that simply sees nothing.
    expect(boxOffenders(EVASION_CSS).sort()).toEqual(
      [
        '.cn-actions button + button { font-weight }',
        '.cn-actions button:first-child { flex }',
        '.cookie-note .cn-actions button:nth-child(2) { min-width }',
        '[data-consent = "accept"] { height }',
        '[data-consent^="acc"] { padding }',
      ].sort(),
    )
    // The two attribute shapes must also reach the hook-keyed arms, not only this one.
    const consent = consentBlocks(EVASION_CSS).map((b) => b.selector)
    expect(consent).toContain('[data-consent = "accept"]')
    expect(consent).toContain('[data-consent^="acc"]')
  })

  it('Core AC 5 (amended): the desktop band exceeds the card\'s own bottom inset', () => {
    // A relationship between two declarations, not a second copy of the literal: a hardcoded
    // number passes on the bug it exists to catch. landing-consent.spec.ts C3 and C4
    // re-derive the rendered band on the deployed build.
    const band = cnBandRoot(CSS_SRC, false)
    expect(band.length, 'expected exactly one base :root rule declaring --cn-band').toBe(1)
    const card = baseRulesFor(CSS_SRC, '.cookie-note')
    expect(card.length, 'expected exactly one base .cookie-note rule').toBe(1)

    // Control: the extractor reads a planted value and refuses a unitless one.
    expect(pxOf('--cn-band: 137px;', '--cn-band'), 'control: the px extractor found nothing').toBe(137)
    expect(pxOf('--cn-band: 0;', '--cn-band'), 'control: the px extractor accepted a unitless value').toBeNull()

    const reserved = pxOf(band[0].body, '--cn-band')
    const inset = pxOf(card[0].body, 'bottom')
    expect(reserved, 'the base :root declares no px --cn-band').not.toBeNull()
    expect(inset, 'the card declares no px bottom inset').not.toBeNull()
    expect(
      reserved!,
      `the desktop band reserves ${reserved}px against a ${inset}px inset — it cannot clear the card`,
    ).toBeGreaterThan(inset!)
  })

  it('CN-1: no rule naming .cookie-note repaints the card (the skin is card-floating\'s)', () => {
    // Control: the collector flags planted rules in the base and in an at-rule, not a descendant rule.
    expect(skinOffenders(PLANTED_SKIN_CSS).sort()).toEqual([
      '.cookie-note { box-shadow }',
      '.cookie-note { border-radius }',
    ].sort())
    // Population floor: the base card rule exists, so an empty offender list means something.
    expect(baseRulesFor(CSS_SRC, '.cookie-note').length).toBe(1)
    expect(skinOffenders(CSS_SRC)).toEqual([])
  })

  it('CN-2: the buttons are the v2 primary and outline', () => {
    const base = buttonBaseRules(CSS_SRC)
    expect(base.length, 'expected exactly one base .cn-actions button rule').toBe(1)
    expect(valueOf(base[0].body, 'border')).toBe('1px solid transparent')
    expect(valueOf(base[0].body, 'border-radius')).toBe('var(--radius-btn)')
    expect(valueOf(base[0].body, 'font-size')).toBe('var(--fs-btn)')
    expect(valueOf(base[0].body, 'font-weight')).toBe('var(--fw-bold)')
    const transition = valueOf(base[0].body, 'transition') ?? ''
    expect(transition, 'transition tokens').toContain('var(--dur-fast)')
    expect(transition, 'transition tokens').toContain('var(--ease-out)')

    const rules = (selector: string) => baseRulesFor(CSS_SRC, selector)
    const accept = rules('.cn-actions [data-consent="accept"]')
    const reject = rules('.cn-actions [data-consent="reject"]')
    expect(accept.length, 'expected one .cn-actions [data-consent="accept"] rule').toBe(1)
    expect(reject.length, 'expected one .cn-actions [data-consent="reject"] rule').toBe(1)
    expect(valueOf(reject[0].body, 'background')).toBe('transparent')
    expect(valueOf(reject[0].body, 'color')).toBe('var(--ink)')
    expect(valueOf(reject[0].body, 'border-color')).toBe('var(--button-outline-border)')

    const acceptHover = rules('.cn-actions [data-consent="accept"]:hover')
    const rejectHover = rules('.cn-actions [data-consent="reject"]:hover')
    expect(acceptHover.length, 'expected the Accept hover rule').toBe(1)
    expect(rejectHover.length, 'expected the Reject hover rule').toBe(1)
    expect(valueOf(acceptHover[0].body, 'filter')).toBe('brightness(1.18)')
    expect(valueOf(rejectHover[0].body, 'background')).toBe('var(--muted)')
  })

  it('CN-2 (P-22): Reject\'s border-color outranks the shared border shorthand, so the outline renders', () => {
    // Control: the comparison refuses the bare attribute selector and accepts the prefixed one.
    expect(outranks(specificity('[data-consent="reject"]'), specificity(SHARED_BUTTON_SELECTOR))).toBe(false)
    expect(outranks(specificity('.cn-actions [data-consent="reject"]'), specificity(SHARED_BUTTON_SELECTOR))).toBe(true)

    const withColour = consentBlocks(CSS_SRC).filter((b) => !b.pseudo && b.props.includes('border-color'))
    expect(withColour.length, 'expected a per-button border-color rule').toBeGreaterThan(0)
    for (const block of withColour) {
      expect(
        outranks(specificity(block.selector), specificity(SHARED_BUTTON_SELECTOR)),
        `${block.selector} loses to ${SHARED_BUTTON_SELECTOR} on border-color`,
      ).toBe(true)
    }
  })

  it('control: border-width on one button is still a box offender; border-color is not', () => {
    expect(boxOffenders('[data-consent="reject"] { border-width: 2px; }')).toEqual([
      '[data-consent="reject"] { border-width }',
    ])
    expect(boxOffenders('[data-consent="reject"] { border-color: red; }')).toEqual([])
  })

  it('CN-3: one --cn-band reserves the band and the focus clearance', () => {
    const base = cnBandRoot(CSS_SRC, false)
    const phone = cnBandRoot(CSS_SRC, true)
    expect(base.length, 'expected a base :root --cn-band').toBe(1)
    expect(phone.length, 'expected a --cn-band under the 640px query').toBe(1)
    const baseBand = pxOf(base[0].body, '--cn-band')
    const phoneBand = pxOf(phone[0].body, '--cn-band')
    expect(baseBand, 'base --cn-band is not px').not.toBeNull()
    expect(phoneBand, 'phone --cn-band is not px').not.toBeNull()
    expect(baseBand!, 'the desktop band must exceed the phone band').toBeGreaterThan(phoneBand!)

    const spacers = parseRules(CSS_SRC).filter((r) => selectorParts(r).includes('.cn-spacer'))
    expect(spacers.length, 'expected a .cn-spacer rule').toBeGreaterThan(0)
    expect(valueOf(baseRulesFor(CSS_SRC, '.cn-spacer')[0]?.body ?? '', 'height')).toBe('var(--cn-band)')
    // A leftover phone-query height would shadow the band and drift from the scroll padding.
    for (const r of spacers) {
      const height = valueOf(r.body, 'height')
      expect(height === null || height === 'var(--cn-band)', `a .cn-spacer rule declares height: ${height}`).toBe(true)
    }

    const has = parseRules(CSS_SRC).filter((r) => selectorParts(r).includes('html:has(.cookie-note)'))
    expect(has.length, 'expected one html:has(.cookie-note) rule').toBe(1)
    expect(valueOf(has[0].body, 'scroll-padding-bottom')).toBe('var(--cn-band)')
  })

  it('CN-5: --cn-band is declared on :root only, never under :has()', () => {
    // Control: the collector reports the planted :has declaration and the :root one.
    expect(cnBandDeclarers(PLANTED_BAND_CSS)).toEqual([':root', 'html:has(.cookie-note)'])

    const declarers = cnBandDeclarers(CSS_SRC)
    expect(declarers.length, 'expected --cn-band declarations on :root').toBeGreaterThan(0)
    for (const selector of declarers) {
      expect(selector, '--cn-band must sit on :root so the spacer needs no :has()').toBe(':root')
      expect(selector).not.toContain(':has(')
    }
  })

  it('CN-4: the notice text keeps the UI line height', () => {
    const card = baseRulesFor(CSS_SRC, '.cookie-note')
    expect(card.length, 'expected exactly one base .cookie-note rule').toBe(1)
    expect(valueOf(card[0].body, 'line-height')).toBe('var(--lh-ui)')
    const text = parseRules(CSS_SRC).filter((r) => selectorParts(r).some((p) => /\.cn-body|\.cn-link/.test(p)))
    expect(text.length, 'expected .cn-body and .cn-link rules').toBeGreaterThan(0)
    for (const r of text) expect(propertiesOf(r.body), r.selector).not.toContain('line-height')
  })

  it('the card is anchored to the right edge on desktop', () => {
    // The footer's link column lives in the card's x band only on this side; C4 is what
    // proves those links stay clickable.
    const card = baseRulesFor(CSS_SRC, '.cookie-note')
    expect(card.length, 'expected exactly one base .cookie-note rule').toBe(1)
    expect(propertiesOf(card[0].body), 'the base card rule must not declare left').not.toContain('left')
    expect(pxOf(card[0].body, 'right'), 'the base card rule declares no px right inset').toBe(24)
  })

  it('the mobile form is one max-width: 640px query and keeps the 44px touch target', () => {
    // Correction 3 fixed the breakpoint at 640; the file's own ladder is 600/920/1079.98/
    // 1239.98 and "harmonising" to 600 is explicitly rejected. Decision 2 fences the 44px.
    const queries = new Set(
      parseRules(CSS_SRC)
        .filter((r) => selectorParts(r).some((p) => /\.cookie-note|\.cn-/.test(p)))
        .flatMap((r) => r.at)
        .filter((a) => /max-width/.test(a)),
    )
    expect([...queries], 'the card must use exactly one width query, at 640px').toEqual([
      '@media (max-width: 640px)',
    ])

    const mobileButton = parseRules(CSS_SRC).filter(
      (r) =>
        r.at.some((a) => /max-width:\s*640px/.test(a)) &&
        selectorParts(r).some((p) => p === SHARED_BUTTON_SELECTOR),
    )
    expect(mobileButton.length, 'expected the shared mobile button rule').toBe(1)
    expect(mobileButton[0].body).toMatch(/height:\s*44px/)
  })

  it('landing.css does not restyle the shared .t-step label', () => {
    const probe = (css: string) =>
      parseRules(css).filter((r) => selectorParts(r).some((p) => /\.t-step\b/.test(p)))
    expect(probe('.cookie-note .t-step { font-size: 11.5px; }').length, 'control').toBe(1)
    expect(baseRulesFor(CSS_SRC, '.cookie-note').length, 'population floor: the card rule exists').toBe(1)
    expect(probe(CSS_SRC).map((r) => r.selector)).toEqual([])
  })

  it('CN-6: Accept is the primary — background and colour from the tokens', () => {
    const accept = baseRulesFor(CSS_SRC, '.cn-actions [data-consent="accept"]')
    expect(accept.length, 'expected one Accept base rule').toBe(1)
    expect(valueOf(accept[0].body, 'background')).toBe('var(--primary)')
    expect(valueOf(accept[0].body, 'color')).toBe('var(--primary-foreground)')
  })

  it('CN-7: each hover rule outranks its own base rule, so the hover shows', () => {
    // Control: a bare-attribute hover ties the prefixed base and does not outrank it.
    expect(outranks(specificity('[data-consent="reject"]:hover'), specificity('.cn-actions [data-consent="reject"]'))).toBe(false)
    for (const choice of ['accept', 'reject']) {
      const base = `.cn-actions [data-consent="${choice}"]`
      expect(baseRulesFor(CSS_SRC, base).length, `expected the ${choice} base rule`).toBe(1)
      expect(baseRulesFor(CSS_SRC, `${base}:hover`).length, `expected the ${choice} hover rule`).toBe(1)
      expect(outranks(specificity(`${base}:hover`), specificity(base)), `${choice} hover loses to its base`).toBe(true)
    }
  })

  it('CN-8: the bridge hover colour outranks .lnk.cn-link, so the policy link still turns teal', () => {
    const bridge = readFileSync(join(HERE, '..', 'styles', 'bridge.css'), 'utf8')
    const hover = parseRules(bridge).filter(
      (r) => selectorParts(r).includes('a.lnk:hover') && propertiesOf(r.body).includes('color'),
    )
    expect(hover.length, 'expected the bridge a.lnk:hover colour rule').toBe(1)
    const link = parseRules(CSS_SRC).filter((r) => selectorParts(r).includes('.lnk.cn-link'))
    expect(link.length, 'expected one .lnk.cn-link rule').toBe(1)
    expect(valueOf(link[0].body, 'color')).toBe('var(--link)')
    expect(outranks(specificity('a.lnk:hover'), specificity('.lnk.cn-link')), 'the link hover colour loses').toBe(true)
    // Control: one more class on the notice link and the hover loses.
    expect(outranks(specificity('a.lnk:hover'), specificity('.lnk.cn-link.cn-link'))).toBe(false)
  })

  it('CN-9: the entry animation names a real keyframes block, and reduced motion resets it to none after the base rule', () => {
    const rules = parseRules(CSS_SRC)
    const base = rules.findIndex((r) => r.at.length === 0 && selectorParts(r).includes('.cookie-note'))
    expect(base, 'expected the base .cookie-note rule').toBeGreaterThanOrEqual(0)
    const name = (valueOf(rules[base].body, 'animation') ?? '').split(' ')[0]
    expect(name).toBe('cn-in')
    const frames = rules.filter((r) => r.at.includes(`@keyframes ${name}`)).map((r) => r.selector)
    expect(frames.sort(), `@keyframes ${name} needs a from and a to`).toEqual(['from', 'to'])

    const reduced = rules.filter(
      (r) =>
        r.at.some((a) => /prefers-reduced-motion\s*:\s*reduce/.test(a)) &&
        selectorParts(r).includes('.cookie-note'),
    )
    expect(reduced.length, 'expected a reduced-motion .cookie-note rule').toBeGreaterThan(0)
    for (const r of reduced) {
      expect(valueOf(r.body, 'animation')).toBe('none')
      expect(rules.indexOf(r), 'the reduced-motion rule must follow the base rule to win at equal specificity').toBeGreaterThan(base)
    }
  })

  it('CN-10: the 640px query is the whole phone card, and the base rule is the 460px desktop card', () => {
    const rules = parseRules(CSS_SRC)
    const phone = rules.filter((r) => r.at.length === 1 && r.at[0] === '@media (max-width: 640px)')
    const card = phone.filter((r) => selectorParts(r).includes('.cookie-note'))
    expect(card.length, 'expected one phone .cookie-note rule').toBe(1)
    for (const [prop, px] of [['left', 12], ['right', 12], ['bottom', 12], ['padding', 16], ['gap', 12]] as const) {
      expect(pxOf(card[0].body, prop), `phone ${prop}`).toBe(px)
    }
    expect(valueOf(card[0].body, 'width')).toBe('auto')
    // BUG-25: the phone buttons share one row; no card-scoped rule at any depth may stack them.
    const stackers = (css: string) =>
      parseRules(css).filter(
        (r) => selectorParts(r).some((p) => CARD_SCOPE.test(p)) && valueOf(r.body, 'flex-direction') === 'column',
      )
    expect(
      stackers('@media (max-width: 640px) { .cookie-note .cn-actions { flex-direction: column; } }').length,
      'control',
    ).toBe(1)
    expect(stackers(CSS_SRC).map((r) => r.selector)).toEqual([])

    const desktop = baseRulesFor(CSS_SRC, '.cookie-note')
    expect(desktop.length).toBe(1)
    expect(pxOf(desktop[0].body, 'width')).toBe(460)
    expect(valueOf(desktop[0].body, 'display')).toBe('grid')
    expect(pxOf(desktop[0].body, 'gap')).toBe(16)
    expect(pxOf(desktop[0].body, 'padding')).toBe(24)
    expect(pxOf(desktop[0].body, 'bottom')).toBe(24)
  })

  it('CN-11: the policy link sits at the start of its grid cell', () => {
    const link = parseRules(CSS_SRC).filter((r) => selectorParts(r).includes('.lnk.cn-link'))
    expect(link.length, 'expected one .lnk.cn-link rule').toBe(1)
    expect(valueOf(link[0].body, 'justify-self')).toBe('start')
  })

  it('CN-12: the focus clearance rule is unconditional and the band is declared outside every at-rule but the 640px query', () => {
    const rules = parseRules(CSS_SRC)
    const has = rules.filter((r) => selectorParts(r).includes('html:has(.cookie-note)'))
    expect(has.length, 'expected one html:has(.cookie-note) rule').toBe(1)
    expect(has[0].at, 'the scroll padding must not depend on a media query').toEqual([])
    const declarers = rules.filter((r) => propertiesOf(r.body).includes('--cn-band'))
    expect(declarers.length, 'expected the base and phone --cn-band declarations').toBe(2)
    for (const r of declarers) expect([[], ['@media (max-width: 640px)']], r.selector).toContainEqual(r.at)
  })
})
