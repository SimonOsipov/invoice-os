// Source rules of landing.css for the v2 header, hero and live check card (RESKIN-02-01 to -03); the rendered result is the topology job's.
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

const reducedMotion = (at: string[]) => at.length === 1 && /^@media \(\s*prefers-reduced-motion\s*:\s*reduce\s*\)$/i.test(at[0])
const noAt = (a: string[]) => a.length === 0
const noBang = (v: string | undefined) => v?.replace(/\s*!important$/, '')

/** The row and scanline contract: the failures, empty when it holds. */
function heroMotionFailures(css: string): string[] {
  const rules = parseRules(css)
  const anim = (sel: string, at: (a: string[]) => boolean) => noBang(declared(rules, sel, 'animation', at))
  const checks: [string, string | undefined, string][] = [
    ['.hero-row animation', anim('.hero-row', noAt), 'rowin 320ms var(--ease-out) both'],
    ['.hero-scan animation', anim('.hero-scan', noAt), 'scanline 2.8s var(--ease-out) infinite'],
    ['.hero-row animation under reduced motion', anim('.hero-row', reducedMotion), 'none'],
    ['.hero-scan display under reduced motion', noBang(display(rules, '.hero-scan', reducedMotion)), 'none'],
  ]
  return checks.filter(([, got, want]) => got !== want).map(([what, got, want]) => `${what}: ${got} != ${want}`)
}

const MOTION_FIXTURE = `
.hero-row { animation: rowIn 320ms var(--ease-out) both; }
.hero-scan { animation: scanline 2.8s var(--ease-out) infinite; }
@media (prefers-reduced-motion: reduce) { .hero-row { animation: none; } .hero-scan { display: none; } }
`

describe('HC-08 row and scanline animations, and the reduced-motion end state', () => {
  it('controls: the lookup accepts the fixture and rejects a copy without the reduced-motion block, or with it under another query', () => {
    expect(heroMotionFailures(MOTION_FIXTURE)).toEqual([])
    const block = MOTION_FIXTURE.slice(MOTION_FIXTURE.indexOf('@media'))
    expect(heroMotionFailures(MOTION_FIXTURE.replace(block, '')).length, 'no reduced-motion block must fail').toBeGreaterThan(0)
    expect(heroMotionFailures(MOTION_FIXTURE.replace('reduce)', 'no-preference)')).length, 'wrong query must fail').toBeGreaterThan(0)
    expect(heroMotionFailures(MOTION_FIXTURE.replace('320ms', '300ms')).length, 'wrong duration must fail').toBeGreaterThan(0)
  })

  it('landing.css animates .hero-row and .hero-scan, and ends them quiet under prefers-reduced-motion', () => {
    expect(parseRules(LANDING_CSS).length, 'control: the file parsed').toBeGreaterThanOrEqual(20)
    expect(heroMotionFailures(LANDING_CSS)).toEqual([])
    const keyframes = parseRules(LANDING_CSS).flatMap((r) => r.at).filter((a) => a.startsWith('@keyframes'))
    expect(keyframes, 'the animation names resolve to keyframes').toEqual(expect.arrayContaining(['@keyframes rowIn', '@keyframes scanline']))
  })
})

describe('HC-09 the scanline keyframes follow V29', () => {
  it('@keyframes scanline runs -30px to 240px, fading in at 10% and out at 90%', () => {
    const frames = parseRules(LANDING_CSS).filter((r) => r.at.length === 1 && r.at[0] === '@keyframes scanline')
    expect(frames.length, 'control: the keyframes parsed').toBeGreaterThanOrEqual(4)
    const frame = (stop: string, prop: string) =>
      frames
        .filter((r) => selectorParts(r).includes(stop))
        .flatMap((r) => declarations(r.body))
        .find((d) => d.prop === prop)
        ?.value.replace(/\s+/g, '')
    expect(frame('0%', 'transform')).toBe('translateY(-30px)')
    expect(frame('0%', 'opacity')).toBe('0')
    expect(frame('10%', 'opacity')).toBe('1')
    expect(frame('90%', 'opacity')).toBe('1')
    expect(frame('100%', 'transform')).toBe('translateY(240px)')
    expect(frame('100%', 'opacity')).toBe('0')
  })
})

/** Reduced-motion leaks: a rule after the reduced-motion rule, or an !important before it, that re-enables the motion. */
function motionLeaks(css: string): string[] {
  const rules = parseRules(css)
  const targets: [string, string[]][] = [
    ['.hero-row', ['animation', 'animation-name']],
    ['.hero-scan', ['display']],
  ]
  const leaks: string[] = []
  for (const [sel, props] of targets) {
    const mine = rules.map((r, i) => ({ r, i })).filter(({ r }) => selectorParts(r).includes(sel))
    const quiet = mine.filter(({ r }) => reducedMotion(r.at) && declarations(r.body).some((d) => props.includes(d.prop)))
    if (quiet.length === 0) {
      leaks.push(`${sel}: no reduced-motion rule`)
      continue
    }
    const last = quiet[quiet.length - 1].i
    for (const { r, i } of mine.filter(({ r }) => !reducedMotion(r.at))) {
      const hit = declarations(r.body).filter((d) => props.includes(d.prop))
      if (hit.some((d) => i > last || /!important/i.test(d.value))) leaks.push(`${sel}: ${hit.map((d) => d.prop).join(',')} re-enabled outside reduced motion`)
    }
  }
  return leaks
}

describe('HC-10 nothing re-enables the hero motion after the reduced-motion rules', () => {
  it('controls: a later display rule and an !important animation are both flagged', () => {
    expect(motionLeaks(MOTION_FIXTURE)).toEqual([])
    expect(motionLeaks(`${MOTION_FIXTURE}\n.hero-scan { display: block; }`).length, 'a later rule must be flagged').toBeGreaterThan(0)
    expect(motionLeaks(MOTION_FIXTURE.replace('both;', 'both !important;')).length, 'an !important row animation must be flagged').toBeGreaterThan(0)
  })

  it('landing.css has no later or !important rule that wakes .hero-row or .hero-scan under reduced motion', () => {
    const rules = parseRules(LANDING_CSS)
    expect(rules.filter((r) => selectorParts(r).includes('.hero-row')).length, 'control: .hero-row has its base and reduced-motion rules').toBeGreaterThanOrEqual(2)
    expect(rules.filter((r) => selectorParts(r).includes('.hero-scan')).length, 'control: .hero-scan has its base and reduced-motion rules').toBeGreaterThanOrEqual(2)
    expect(motionLeaks(LANDING_CSS)).toEqual([])
  })
})

describe('HC-11 the row and scanline boxes and the keyframe end states, which jsdom cannot render', () => {
  const rules = parseRules(LANDING_CSS)
  const base = (sel: string, prop: string) => declared(rules, sel, prop, noAt)

  it('.hero-row is a centred 11px-gap flex row with 7px 14px padding (V129)', () => {
    expect(rules.length, 'control: the file parsed').toBeGreaterThanOrEqual(20)
    expect(base('.hero-row', 'display')).toBe('flex')
    expect(base('.hero-row', 'align-items')).toBe('center')
    expect(base('.hero-row', 'gap')).toBe('11px')
    expect(base('.hero-row', 'padding')).toBe('7px 14px')
  })

  it('.hero-scan is a 30px click-through overlay pinned to the top, with the accent-20 gradient (V128)', () => {
    expect(base('.hero-scan', 'position')).toBe('absolute')
    expect(base('.hero-scan', 'left')).toBe('0')
    expect(base('.hero-scan', 'right')).toBe('0')
    expect(base('.hero-scan', 'top')).toBe('0')
    expect(base('.hero-scan', 'height')).toBe('30px')
    expect(base('.hero-scan', 'pointer-events')).toBe('none')
    expect(base('.hero-scan', 'background')).toBe('linear-gradient(180deg, var(--accent-20), transparent)')
  })

  it('scanline is declared once, and rowIn ends at opacity 1 so the "both" fill leaves each row visible', () => {
    const at = (name: string) => rules.filter((r) => r.at.length === 1 && r.at[0] === `@keyframes ${name}`)
    expect(at('scanline').filter((r) => selectorParts(r).includes('0%')).length, 'one scanline 0% frame').toBe(1)
    expect(at('rowIn').length, 'control: rowIn has from and to').toBeGreaterThanOrEqual(2)
    const stop = (s: string, prop: string) =>
      at('rowIn')
        .filter((r) => selectorParts(r).includes(s))
        .flatMap((r) => declarations(r.body))
        .find((d) => d.prop === prop)?.value
    expect(stop('from', 'opacity')).toBe('0')
    expect(stop('to', 'opacity')).toBe('1')
  })
})
