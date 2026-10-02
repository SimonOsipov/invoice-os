// The landing-local bridge (bridge.css) maps the app-layer names the sections still use onto v2.
// Source scans: no runtime test sees an unresolved var() (it computes to the property's initial value).
/// <reference types="node" />
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import {
  LANDING_SRC,
  classNameTokens,
  classSelectors,
  classSelectorsInStrings,
  customPropNames,
  customPropValues,
  declarations,
  landingBuildInput,
  parseRules,
  readV2Css,
  selectorParts,
  stripSource,
  varRefs,
  type CssRule,
} from '../cssScan.test.util'

const BRIDGE_PATH = join(LANDING_SRC, 'styles', 'bridge.css')
const LANDING_CSS_PATH = join(LANDING_SRC, 'styles', 'landing.css')

function readBridge(): string {
  expect(existsSync(BRIDGE_PATH), `expected ${BRIDGE_PATH} to exist ([RESKIN-01-02] adds the bridge)`).toBe(true)
  return stripSource('bridge.css', readFileSync(BRIDGE_PATH, 'utf8'))
}

const rootDeclarations = (css: string) =>
  parseRules(css)
    .filter((r) => selectorParts(r).includes(':root'))
    .flatMap((r) => declarations(r.body))
    .filter((d) => d.prop.startsWith('--'))

const v2Names = (files: Record<string, string>) => [...new Set(Object.values(files).flatMap(customPropNames))].sort()

function unresolvedVars(inputs: Record<string, string>, declaring: readonly string[]) {
  const referenced = new Set<string>()
  const declared = new Set(declaring.flatMap(customPropNames))
  for (const [file, raw] of Object.entries(inputs)) {
    const src = stripSource(file, raw)
    varRefs(src).forEach((n) => referenced.add(n))
    customPropNames(src).forEach((n) => declared.add(n))
  }
  const names = [...referenced].sort()
  return { referenced: names, unresolved: names.filter((n) => !declared.has(n)) }
}

function undefinedClasses(inputs: Record<string, string>, definingCss: readonly string[]) {
  const defined = new Set(definingCss.flatMap(classSelectors))
  const used = new Map<string, string[]>()
  for (const [file, raw] of Object.entries(inputs)) {
    const src = stripSource(file, raw)
    if (file.endsWith('.css')) {
      classSelectors(src).forEach((c) => defined.add(c))
    } else {
      classSelectorsInStrings(src).forEach((c) => defined.add(c))
      for (const tok of classNameTokens(src)) {
        // D-38 rule (2): the monitoring package keeps `asc-app` for the v1 apps; it is inert on the landing.
        if (tok === 'asc-app' && file.startsWith('packages/monitoring/')) continue
        used.set(tok, [...(used.get(tok) ?? []), file])
      }
    }
  }
  const undef: Record<string, string[]> = {}
  for (const [tok, files] of used) if (!defined.has(tok)) undef[tok] = files
  return { checked: [...used.keys()], undef }
}

function hexOf(name: string, values: Map<string, string>): string | null {
  let v = values.get(name)
  for (let hops = 0; v && hops < 10; hops++) {
    if (/^#[0-9a-f]{6}$/i.test(v)) return v.toLowerCase()
    const m = /^var\(\s*(--[\w-]+)\s*\)$/.exec(v)
    v = m ? values.get(m[1]) : undefined
  }
  return null
}

function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(fg: string, bg: string): number {
  const [hi, lo] = [luminance(fg), luminance(bg)].sort((a, b) => b - a)
  return (hi + 0.05) / (lo + 0.05)
}

describe('bridge.css maps the app-layer names onto v2', () => {
  it('BR-01 every var(--x) in the build inputs resolves', () => {
    const planted = unresolvedVars({ 'planted.tsx': 'var(--fg-1) var(--fg-2)' }, [':root { --fg-2: var(--foreground); }'])
    expect(planted.referenced, 'control: both planted names are referenced').toEqual(['--fg-1', '--fg-2'])
    expect(planted.unresolved, 'control: only the undeclared planted name is unresolved').toEqual(['--fg-1'])

    expect(existsSync(BRIDGE_PATH), `expected ${BRIDGE_PATH} to exist ([RESKIN-01-02] adds the bridge)`).toBe(true)
    const { referenced, unresolved } = unresolvedVars(landingBuildInput({ withMonitoring: true }), Object.values(readV2Css()))
    expect(referenced.length).toBeGreaterThanOrEqual(20)
    // [RESKIN-01-03] removes --gradient-hero and this exception with it.
    expect(unresolved).toEqual(['--gradient-hero'])
  })

  it('BR-03 every static className token has a class rule', () => {
    const v2 = readV2Css()
    const defining = [v2['utilities.css'], v2['tokens/hero-grid.css']]
    expect(defining.every((c) => typeof c === 'string' && c.length > 0), 'v2 utilities.css and hero-grid.css exist').toBe(true)

    const planted = undefinedClasses(
      {
        'planted.tsx': "const a = <i className=\"a-cls no-such-cls\" /><i className={'p-one'} /><i className={`p-two ${v} p-three ds--${v}`} /><i className={ok ? 'a-cls' : 'p-four'} />",
        'planted.css': '.a-cls { color: red }',
        'packages/monitoring/src/R.tsx': '<i className="asc-app" />',
        'App.tsx': '<i className="asc-app" />',
      },
      [],
    )
    expect(Object.keys(planted.undef).sort(), 'control: planted undefined tokens are reported, dynamic fragments and the monitoring asc-app are not').toEqual(
      ['asc-app', 'no-such-cls', 'p-four', 'p-one', 'p-three', 'p-two'],
    )
    expect(planted.undef['asc-app'], 'control: asc-app is reported for the landing file only').toEqual(['App.tsx'])

    expect(existsSync(BRIDGE_PATH), `expected ${BRIDGE_PATH} to exist ([RESKIN-01-02] adds the bridge)`).toBe(true)
    const { checked, undef } = undefinedClasses(landingBuildInput({ withMonitoring: true }), defining)
    expect(checked.length).toBeGreaterThanOrEqual(20)
    // [RESKIN-01-03] removes band-gradient and this exception with it.
    expect(Object.keys(undef).sort(), JSON.stringify(undef)).toEqual(['band-gradient'])
  })

  it('BR-04 every bridge :root value is a v2 token or a hex', () => {
    const v2Tokens = v2Names(Object.fromEntries(Object.entries(readV2Css()).filter(([k]) => k.startsWith('tokens/'))))
    expect(v2Tokens.length).toBeGreaterThanOrEqual(150)
    const classify = (value: string) => {
      const m = /^var\(\s*(--[\w-]+)\s*\)$/.exec(value)
      return /^#[0-9a-f]{6}$/i.test(value) || (m !== null && v2Tokens.includes(m[1]))
    }

    expect(classify('var(--card)'), 'control: a declared v2 token passes').toBe(true)
    expect(classify('#fbeaea'), 'control: a 6-digit hex passes').toBe(true)
    for (const bad of ['oklch(50% .1 200)', 'rgba(0,0,0,.1)', 'var(--gradient-hero)']) {
      expect(classify(bad), `control: ${bad} is rejected`).toBe(false)
    }

    const decls = rootDeclarations(readBridge())
    expect(decls.length).toBeGreaterThanOrEqual(1)
    expect(decls.filter((d) => !classify(d.value)).map((d) => `${d.prop}: ${d.value}`)).toEqual([])
  })

  it('BR-06 the bridge holds no oklch(', () => {
    const search = (css: string) => css.match(/oklch\(/gi) ?? []
    expect(search('a { color: OKLCH(50% .1 200) }'), 'control: a planted upper-case oklch( matches once').toHaveLength(1)

    const css = readBridge()
    expect(rootDeclarations(css).length).toBeGreaterThanOrEqual(1)
    expect(search(css)).toEqual([])
  })

  it('BR-07 status text contrast is at least 4.5:1', () => {
    expect(contrast('#777777', '#ffffff'), 'control: #777777 on white is 4.48').toBeLessThan(4.5)
    expect(contrast('#767676', '#ffffff'), 'control: #767676 on white is 4.54').toBeGreaterThanOrEqual(4.5)

    const bridge = readBridge()
    const values = new Map([...customPropValues(readV2Css()['tokens/colors.css']), ...customPropValues(bridge)])
    const card = hexOf('--card', values)
    const page = hexOf('--background', values)
    expect(card && page, 'v2 --card and --background resolve to hex').toBeTruthy()
    const failures: string[] = []
    let checked = 0
    for (const tone of ['green', 'amber', 'red', 'muted']) {
      const text = hexOf(`--status-${tone}-text`, values)
      const bg = hexOf(`--status-${tone}-bg`, values)
      if (!text || !bg) {
        failures.push(`${tone}: --status-${tone}-text / -bg do not resolve to hex through the bridge and v2 colors.css`)
        continue
      }
      checked++
      for (const [on, hex] of [['its own bg', bg], ['--card', card!], ['--background', page!]] as const) {
        const ratio = contrast(text, hex)
        if (ratio < 4.5) failures.push(`${tone}: ${text} on ${on} ${hex} = ${ratio.toFixed(2)}`)
      }
    }
    expect(checked).toBeGreaterThanOrEqual(3)
    expect(failures).toEqual([])
  })

  it('BR-09 no !important and no heading selector', () => {
    const violations = (css: string) =>
      parseRules(css).flatMap((r) => [
        ...selectorParts(r).filter((p) => /\bh[1-3]\b/i.test(p)).map((p) => `selector ${p}`),
        ...declarations(r.body).filter((d) => /!\s*important/i.test(d.value)).map((d) => `declaration ${d.prop}`),
      ])
    expect(violations('h2 { color: red !important }'), 'control: selector and declaration are both flagged').toEqual([
      'selector h2',
      'declaration color',
    ])
    expect(violations('.a { color: red }'), 'control: a clean rule is not flagged').toEqual([])

    const css = readBridge()
    expect(parseRules(css).length).toBeGreaterThanOrEqual(1)
    expect(violations(css)).toEqual([])
  })

  it('BR-10 .mono keeps the v2 mono stack', () => {
    // v2 packages/design-tokens/v2/tokens/typography.css `--font-mono`
    const V2_FONT_MONO = 'ui-monospace, SFMono-Regular, Menlo, monospace'
    expect(customPropValues(readV2Css()['tokens/typography.css']).get('--font-mono')).toBe(V2_FONT_MONO)

    const css = readBridge()
    const mono = parseRules(css).filter((r) => selectorParts(r).includes('.mono'))
    expect(mono.length, 'expected a .mono rule in the bridge').toBeGreaterThanOrEqual(1)
    const families = mono.flatMap((r) => declarations(r.body)).filter((d) => d.prop === 'font-family')
    expect(families.map((d) => d.value)).toEqual(['var(--font-mono)'])
    expect(css).not.toMatch(/--font-mono\s*:/i)
  })

  it('BR-11 bridge names do not shadow v2 names', () => {
    const bridgeNames = rootDeclarations(readBridge()).map((d) => d.prop)
    expect(bridgeNames.length).toBeGreaterThanOrEqual(1)
    const v2 = v2Names(readV2Css())
    expect(v2.length).toBeGreaterThanOrEqual(150)
    expect(bridgeNames.filter((n) => v2.includes(n))).toEqual([])
  })

  it('BR-12 the bridge is unscoped', () => {
    const offenders = (css: string) =>
      parseRules(css).flatMap((r) => [
        ...selectorParts(r).filter((p) => /\.asc-app/i.test(p)).map((p) => `scoped ${p}`),
        ...(selectorParts(r).every((p) => p === ':root')
          ? []
          : declarations(r.body).filter((d) => d.prop.startsWith('--')).map((d) => `${d.prop} outside :root`)),
      ])
    expect(offenders('.asc-app { --x: 1 }'), 'control: a scoped custom property is flagged twice').toEqual(['scoped .asc-app', '--x outside :root'])
    expect(offenders(':root { --x: 1 }'), 'control: a :root property is clean').toEqual([])

    const css = readBridge()
    expect(rootDeclarations(css).length).toBeGreaterThanOrEqual(1)
    expect(offenders(css)).toEqual([])
  })

  it('BR-13 the em and ::placeholder rules follow v2', () => {
    const css = readBridge()
    const props = (selector: string) =>
      new Map(
        parseRules(css)
          .filter((r: CssRule) => selectorParts(r).includes(selector))
          .flatMap((r) => declarations(r.body))
          .map((d): [string, string] => [d.prop, d.value]),
      )
    const em = props('em')
    expect(em.size, 'expected an em rule in the bridge').toBeGreaterThanOrEqual(1)
    expect(em.get('font-style')).toBe('normal')
    expect(em.get('color')).toBe('var(--primary)')
    const placeholder = props('::placeholder')
    expect(placeholder.size, 'expected a ::placeholder rule in the bridge').toBeGreaterThanOrEqual(1)
    expect(placeholder.get('color')).toBe('var(--muted-foreground)')
  })

  it('BR-14 .ios-link hover uses teal', () => {
    const offenders = (css: string) => {
      const rules = parseRules(css).filter((r) => selectorParts(r).includes('.ios-link:hover'))
      return {
        count: rules.length,
        bad: rules.flatMap((r) => [
          ...declarations(r.body)
            .filter((d) => d.prop === 'color' && d.value.replace(/\s*!important\s*$/i, '') !== 'var(--teal)')
            .map((d) => `color: ${d.value}`),
          ...(/--accent\b/.test(r.body) ? ['references --accent'] : []),
        ]),
      }
    }
    expect(offenders('.ios-link:hover { color: var(--accent) !important }').bad, 'control: a planted accent hover is flagged').not.toEqual([])
    expect(offenders('.ios-link:hover { color: var(--teal) !important }'), 'control: a teal hover is clean').toEqual({ count: 1, bad: [] })

    const real = offenders(readFileSync(LANDING_CSS_PATH, 'utf8'))
    expect(real.count).toBeGreaterThanOrEqual(1)
    expect(real.bad).toEqual([])
  })
})
