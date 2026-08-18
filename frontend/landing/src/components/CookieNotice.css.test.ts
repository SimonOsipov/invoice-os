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
 *  (0,1,0) form that loses to `.asc-app a` (0,1,1) and ships the link un-underlined. */
function bareCnLinkDecorationRules(css: string): CssRule[] {
  return parseRules(css).filter(
    (r) =>
      selectorParts(r).some((p) => /\.cn-link\b/.test(p) && !/\.lnk\.cn-link\b/.test(p)) &&
      propertiesOf(r.body).includes('text-decoration'),
  )
}

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

  it('Core AC 5 (amended): the desktop spacer reserves more than the card\'s own bottom inset', () => {
    // Right-anchored, the card's x band holds the footer's link column, so the spacer is
    // the only thing keeping those links clickable and is no longer zero. Asserted as a
    // relationship between the two declarations rather than a second copy of the literal:
    // a hardcoded number passes on the bug it exists to catch. landing-consent.spec.ts C3
    // and C4 re-derive the rendered band on the deployed build.
    const spacer = baseRulesFor(CSS_SRC, '.cn-spacer')
    expect(spacer.length, 'expected exactly one base .cn-spacer rule').toBe(1)
    const card = baseRulesFor(CSS_SRC, '.cookie-note')
    expect(card.length, 'expected exactly one base .cookie-note rule').toBe(1)

    // Control: the extractor reads a planted value and refuses a unitless one.
    expect(pxOf('height: 292px;', 'height'), 'control: the px extractor found nothing').toBe(292)
    expect(pxOf('height: 0;', 'height'), 'control: the px extractor accepted a unitless value').toBeNull()

    const reserved = pxOf(spacer[0].body, 'height')
    const inset = pxOf(card[0].body, 'bottom')
    expect(reserved, 'the desktop spacer declares no px height').not.toBeNull()
    expect(inset, 'the card declares no px bottom inset').not.toBeNull()
    expect(
      reserved!,
      `the desktop spacer reserves ${reserved}px against a ${inset}px inset — it cannot clear the card`,
    ).toBeGreaterThan(inset!)
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

  it('landing.css does not override the shared .eyebrow', () => {
    // `.cookie-note .eyebrow` is (0,2,0) and merely TIES `.asc-app .eyebrow`
    // (app-layer.css:163) — it would win on source order alone. Correction 6 says reuse
    // the class; an override here also risks defeating the 24px ::before rule.
    const probe = (css: string) =>
      parseRules(css).filter((r) => selectorParts(r).some((p) => /\.eyebrow\b/.test(p)))
    expect(probe('.cookie-note .eyebrow { font-size: 11.5px; }').length, 'control').toBe(1)
    expect(probe(CSS_SRC).map((r) => r.selector)).toEqual([])
  })
})
