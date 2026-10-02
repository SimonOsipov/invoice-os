// The DS primitives' stylesheet values. Source scans: jsdom has no cascade and no page mounts a primitive yet.
// Every expected token is also checked against the vendored v2 tokens, so the spec cannot name a token v2 lacks.
// Literals (brightness steps, 0.45, 2px ring) are the DS Button.jsx / README values, not implementation text.
/// <reference types="node" />
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import {
  LANDING_SRC,
  customPropNames,
  declarations,
  parseRules,
  readV2Css,
  selectorParts,
  stripSource,
} from '../cssScan.test.util'

const DS_PATH = join(LANDING_SRC, 'styles', 'ds.css')

function readDs(): string {
  expect(existsSync(DS_PATH), `expected ${DS_PATH} to exist (RESKIN-01-05 adds it)`).toBe(true)
  return stripSource('ds.css', readFileSync(DS_PATH, 'utf8'))
}

const norm = (v: string) => v.replace(/\s+/g, ' ').trim()

/** Last declared value of `props` across the rules whose selector list holds `selector` exactly. */
function valueOf(css: string, selector: string, ...props: string[]): string | undefined {
  const values = parseRules(css)
    .filter((r) => selectorParts(r).includes(selector))
    .flatMap((r) => declarations(r.body))
    .filter((d) => props.includes(d.prop))
    .map((d) => norm(d.value))
  return values.at(-1)
}

function expectDecl(css: string, selector: string, props: string[], want: string) {
  expect(valueOf(css, selector, ...props), `${selector} { ${props[0]} }`).toBe(want)
}

const v2Tokens = () => new Set(Object.values(readV2Css()).flatMap(customPropNames))

describe('ds.css values', () => {
  it('DSC-01 buttons use the 7px radius and the DS hovers', () => {
    const css = readDs()
    expectDecl(css, '.ds-btn', ['border-radius'], 'var(--radius-btn)')
    const hovers: [string, string[], string][] = [
      ['primary', ['filter'], 'brightness(1.18)'],
      ['accent', ['filter'], 'brightness(1.06)'],
      ['ghostDark', ['filter'], 'brightness(1.12)'],
      ['outline', ['background', 'background-color'], 'var(--muted)'],
      ['text', ['color'], 'var(--teal)'],
    ]
    expect(hovers).toHaveLength(5)
    for (const [variant, props, want] of hovers) {
      expectDecl(css, `.ds-btn--${variant}:hover:not(:disabled)`, props, want)
    }
    expectDecl(css, '.ds-btn:disabled', ['opacity'], '0.45')
    const tokens = v2Tokens()
    for (const name of ['--radius-btn', '--muted', '--teal']) expect(tokens.has(name), `v2 declares ${name}`).toBe(true)
  })

  it('DSC-02 badges and tags use the 4px radius', () => {
    const css = readDs()
    expectDecl(css, '.ds-badge', ['border-radius'], 'var(--radius-sm)')
    expectDecl(css, '.ds-tag', ['border-radius'], 'var(--radius-sm)')
    expect(v2Tokens().has('--radius-sm')).toBe(true)
  })

  it('DSC-03 only the badge dot and the play circle are pills', () => {
    const PILL = /999|--radius-pill|50%/
    const OWNER = /\.(?:ds-badge-dot|ds-btn-play)$/
    const pillSelectors = (css: string) =>
      parseRules(css).flatMap((r) =>
        declarations(r.body).some((d) => /^border(?:-[a-z]+){0,2}-radius$/.test(d.prop) && PILL.test(d.value))
          ? selectorParts(r)
          : [],
      )

    const stray = (css: string) => pillSelectors(css).filter((s) => !OWNER.test(s))
    expect(stray('.ds-btn { border-radius: 9999px; }'), 'planted pill on .ds-btn').toEqual(['.ds-btn'])

    const owners = pillSelectors(readDs())
    expect(stray(readDs())).toEqual([])
    expect(owners.some((s) => s.endsWith('.ds-badge-dot')), 'badge dot is a pill').toBe(true)
    expect(owners.some((s) => s.endsWith('.ds-btn-play')), 'play circle is a pill').toBe(true)
  })

  it('DSC-05 every colour in ds.css is a v2 token', () => {
    const NAMED = /\b(?:white|black|red|green|blue|gray|grey|orange|yellow|purple|pink|brown|silver|navy|maroon|olive|lime|aqua|cyan|magenta|gold|ivory|beige|tan|salmon|coral|crimson)\b/gi
    const LITERAL = /#[0-9a-f]{3,8}\b|\b(?:rgba?|hsla?|oklch|oklab|lch|lab|hwb)\(/gi
    const values = (css: string) => parseRules(css).flatMap((r) => declarations(r.body).map((d) => d.value))
    const literals = (css: string) =>
      values(css).flatMap((v) => [...v.matchAll(LITERAL)].concat([...v.replace(/var\([^)]*\)/g, '').matchAll(NAMED)]))

    expect(literals('.x { color: #FFF; background: RGBA(0,0,0,.1) }')).toHaveLength(2)
    expect(literals('.x { border: 1px solid white; color: var(--teal) }')).toHaveLength(1)

    const css = readDs()
    const tokenised = values(css).filter((v) => v.includes('var(--'))
    expect(tokenised.length, 'ds.css declares at least 10 var(--…) values').toBeGreaterThanOrEqual(10)
    expect(literals(css).map((m) => m[0])).toEqual([])
  })

  it('DSC-06 focus-visible draws the sibling ring', () => {
    const css = readDs()
    expectDecl(css, '.ds-btn:focus-visible', ['outline'], '2px solid var(--ring)')
    expectDecl(css, '.ds-btn:focus-visible', ['outline-offset'], '2px')
    expect(v2Tokens().has('--ring')).toBe(true)
  })

  it('DSC-10 the Logo mark has the 6px corner', () => {
    expectDecl(readDs(), '.ds-logo-mark', ['border-radius'], 'var(--radius-md)')
    expect(v2Tokens().has('--radius-md')).toBe(true)
  })
})

type Specificity = [number, number, number]

function dropWhere(selector: string): string {
  for (let at = selector.indexOf(':where('); at !== -1; at = selector.indexOf(':where(')) {
    let depth = 0
    let end = at + ':where'.length
    for (; end < selector.length; end++) {
      if (selector[end] === '(') depth++
      else if (selector[end] === ')' && --depth === 0) break
    }
    selector = selector.slice(0, at) + selector.slice(end + 1)
  }
  return selector
}

function specificity(selector: string): Specificity {
  let [a, b, c] = [0, 0, 0]
  let rest = dropWhere(selector.trim())
  rest = rest.replace(/:(?:not|is|has)\(([^()]*)\)/g, (_m, arg: string) => {
    const [x, y, z] = specificity(arg)
    a += x
    b += y
    c += z
    return ''
  })
  rest = rest.replace(/\[[^\]]*\]/g, () => (b++, ''))
  rest = rest.replace(/#[\w-]+/g, () => (a++, ''))
  rest = rest.replace(/::[\w-]+/g, () => (c++, ''))
  rest = rest.replace(/:[\w-]+/g, () => (b++, ''))
  rest = rest.replace(/\.[\w-]+/g, () => (b++, ''))
  c += (rest.match(/(?:^|[\s>+~])[a-zA-Z][\w-]*/g) ?? []).length
  return [a, b, c]
}

const beats = (x: Specificity, y: Specificity) => (x[0] - y[0] || x[1] - y[1] || x[2] - y[2]) > 0

const HOVER_COLOUR: Record<string, string> = {
  primary: 'var(--primary-foreground)',
  accent: 'var(--accent-foreground)',
  outline: 'var(--ink)',
  ghostDark: 'var(--surface-foreground)',
  text: 'var(--teal)',
}

/** One entry per variant hover rule that misses its colour or fails to outrank `floor`; one per variant with no hover rule. */
function hoverDefects(css: string, floor: Specificity): string[] {
  const defects: string[] = []
  for (const [variant, colour] of Object.entries(HOVER_COLOUR)) {
    const hits = parseRules(css).flatMap((r) =>
      selectorParts(r)
        .filter((s) => s.includes(`.ds-btn--${variant}`) && s.includes(':hover'))
        .map((s) => ({ s, colour: declarations(r.body).filter((d) => d.prop === 'color').map((d) => norm(d.value)).at(-1) })),
    )
    if (hits.length === 0) defects.push(`${variant}: no hover rule`)
    for (const h of hits) {
      if (h.colour !== colour) defects.push(`${variant}: ${h.s} declares color ${h.colour ?? 'nothing'}, want ${colour}`)
      if (!beats(specificity(h.s), floor)) defects.push(`${variant}: ${h.s} does not outrank the v2 a:hover`)
    }
  }
  return defects
}

describe('ds.css hover cascade', () => {
  it('DSC-11 every button hover pins its colour above the v2 a:hover rule', () => {
    const utilities = readV2Css()['utilities.css']
    expect(utilities, 'v2 utilities.css is vendored').toBeDefined()
    const rule = parseRules(utilities).find((r) => selectorParts(r).includes('a:hover'))
    expect(rule, 'v2 utilities.css has an a:hover rule').toBeDefined()
    const v2Hover = specificity('a:hover')
    expect(v2Hover).toEqual([0, 1, 1])
    expect(declarations((rule as { body: string }).body).find((d) => d.prop === 'color')?.value).toBe('var(--teal)')

    const planted = [
      '.ds-btn--primary:hover { filter: brightness(1.18); }',
      ...Object.entries(HOVER_COLOUR)
        .filter(([v]) => v !== 'primary')
        .map(([v, c]) => `.ds-btn--${v}:hover:not(:disabled) { color: ${c}; }`),
    ].join('\n')
    expect(hoverDefects(planted, v2Hover), 'planted colourless primary hover').toEqual([
      'primary: .ds-btn--primary:hover declares color nothing, want var(--primary-foreground)',
    ])
    expect(
      hoverDefects(':where(.ds-btn--primary):hover { color: var(--primary-foreground); }', v2Hover),
      'planted rule that does not outrank a:hover',
    ).toContain('primary: :where(.ds-btn--primary):hover does not outrank the v2 a:hover')
    expect(
      hoverDefects(':where(.ds-btn--primary:hover:not(:disabled)) { color: var(--primary-foreground); }', v2Hover),
      'planted :where around a nested :not does not outrank a:hover',
    ).toContain('primary: :where(.ds-btn--primary:hover:not(:disabled)) does not outrank the v2 a:hover')
    expect(specificity('.ds-btn--primary:hover:not(:disabled)')).toEqual([0, 3, 0])

    expect(hoverDefects(readDs(), v2Hover)).toEqual([])
  })
})
