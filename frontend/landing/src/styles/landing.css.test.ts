// Source rules of landing.css for the v2 header and hero (RESKIN-02-01, -02); the rendered result is the topology job's.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

import { LANDING_SRC, V2_DIR, customPropValues, declarations, parseRules, selectorParts, type CssRule } from '../cssScan.test.util'

const LANDING_CSS = readFileSync(join(LANDING_SRC, 'styles', 'landing.css'), 'utf8')

const declared = (rules: CssRule[], selector: string, prop: string, at: (a: string[]) => boolean): string | undefined =>
  rules
    .filter((r) => at(r.at) && selectorParts(r).includes(selector))
    .flatMap((r) => declarations(r.body))
    .filter((d) => d.prop === prop)
    .at(-1)
    ?.value.replace(/\s+/g, ' ')
    .toLowerCase()

const display = (rules: CssRule[], selector: string, at: (a: string[]) => boolean): string | undefined =>
  declared(rules, selector, 'display', at)?.replace(/\s*!important$/, '')

const maxWidth1120 = (at: string[]) => at.length === 1 && /^@media \(\s*max-width\s*:\s*1120px\s*\)$/i.test(at[0])
const minWidthAbove1120 = (at: string[]) => {
  const m = at.length === 1 ? /^@media \(\s*min-width\s*:\s*(\d+(?:\.\d+)?)px\s*\)$/i.exec(at[0]) : null
  return m !== null && Number(m[1]) > 1120
}

/** The breakpoint contract: the failures, empty when it holds. */
function breakpointFailures(css: string): string[] {
  const rules = parseRules(css)
  const checks: [string, string | undefined, string][] = [
    ['.a-burger outside any media', display(rules, '.a-burger', (a) => a.length === 0), 'none'],
    ['.a-nav at max-width 1120px', display(rules, '.a-nav', maxWidth1120), 'none'],
    ['.a-login at max-width 1120px', display(rules, '.a-login', maxWidth1120), 'none'],
    ['.a-burger at max-width 1120px', display(rules, '.a-burger', maxWidth1120), 'inline-flex'],
    ['.a-menu at min-width above 1120px', display(rules, '.a-menu', minWidthAbove1120), 'none'],
  ]
  return checks.filter(([, got, want]) => got !== want).map(([what, got, want]) => `${what}: ${got} != ${want}`)
}

const FIXTURE = (max: string) => `
.a-burger { display: none; }
@media (max-width: ${max}) { .a-nav, .a-login { display: none; } .a-burger { display: inline-flex; } }
@media (min-width: 1120.02px) { .a-menu { display: none; } }
`

describe('HD-11 the 1120px breakpoint swaps the nav for the burger', () => {
  it('controls: the lookup accepts a 1120px fixture and rejects a 1119px one', () => {
    expect(breakpointFailures(FIXTURE('1120px'))).toEqual([])
    expect(breakpointFailures(FIXTURE('1119px')).length, 'a max-width 1119px copy must fail the lookup').toBeGreaterThan(0)
    const commented = FIXTURE('1120px').replace('.a-burger { display: inline-flex; }', '/* .a-burger { display: inline-flex; } */')
    expect(commented).not.toBe(FIXTURE('1120px'))
    expect(breakpointFailures(commented).length, 'a commented-out rule must fail the lookup').toBeGreaterThan(0)
  })

  it('landing.css hides .a-nav and .a-login and shows .a-burger at max-width 1120px, hides .a-menu above it', () => {
    expect(breakpointFailures(LANDING_CSS)).toEqual([])
  })
})

describe('HD-12 the shed rules are gone and the nav link rule stays', () => {
  it('no shed or hide-mobile selector; .ios-nav-link:hover is teal', () => {
    const rules = parseRules(LANDING_CSS)
    expect(rules.length, 'control: the file parsed').toBeGreaterThanOrEqual(20)
    const selectors = rules.flatMap(selectorParts)
    expect(selectors.some((s) => s.includes('.ios-nav-link')), 'control: .ios-nav-link remains').toBe(true)

    expect(selectors.filter((s) => s.includes('.ios-nav-shed-') || s.includes('.ios-hide-mobile'))).toEqual([])
    const hover = rules.filter((r) => selectorParts(r).some((s) => s.includes('.ios-nav-link:hover')))
    expect(hover.length, 'expected one .ios-nav-link:hover rule').toBe(1)
    expect(declarations(hover[0].body).find((d) => d.prop === 'color')?.value.toLowerCase().replace(/\s+/g, ' ')).toBe(
      'var(--teal) !important',
    )
  })
})

const maxWidth900 = (at: string[]) => at.length === 1 && /^@media \(\s*max-width\s*:\s*900px\s*\)$/i.test(at[0])

describe('HB-11 the split collapses at 900px and the old h1 rule is gone', () => {
  it('.split is a grid outside any media, one column at max-width 900px, and .ios-hero-h1 is gone', () => {
    const rules = parseRules(LANDING_CSS)
    expect(rules.length, 'control: the file parsed').toBeGreaterThanOrEqual(20)

    expect(display(rules, '.split', (a) => a.length === 0)).toBe('grid')
    expect(declared(rules, '.split', 'grid-template-columns', maxWidth900)).toBe('minmax(0, 1fr) !important')
    expect(rules.flatMap(selectorParts).filter((s) => s.includes('.ios-hero-h1'))).toEqual([])
  })
})

describe('HB-14 the h1 highlight line takes --highlight-on-dark', () => {
  it('.t-hl colours with --highlight-on-dark, which colors.css defines; jsdom applies no CSS to see it', () => {
    const rules = parseRules(readFileSync(join(V2_DIR, 'utilities.css'), 'utf8'))
    expect(rules.length, 'control: the file parsed').toBeGreaterThanOrEqual(20)
    const color = (selector: string) => declared(rules, selector, 'color', (a) => a.length === 0)
    expect(color('.t-hl-peach'), 'control: a sibling resolves to its own token').toBe('var(--highlight-on-peach)')

    expect(color('.t-hl')).toBe('var(--highlight-on-dark)')
    const tokens = customPropValues(readFileSync(join(V2_DIR, 'tokens', 'colors.css'), 'utf8'))
    expect(tokens.has('--highlight-on-dark'), '--highlight-on-dark is defined').toBe(true)
  })
})
