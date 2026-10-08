/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { FEATURES, GROUPS } from './content'
import { FeaturePage } from './components/FeaturePage'
import { GroupPage } from './components/GroupPage'
import { Home } from './components/Home'
import { TourOverlay } from './components/TourOverlay'
import { JourneyStepper } from './components/JourneyStepper'
import { Player } from './components/Player'
import { Sidebar } from './components/Sidebar'
import { GLYPHS } from './icons'
import { PHONE_MAX_WIDTH } from './phone'
import { START } from './player'
import { parseLibraryPath } from './route'

const HERE = dirname(fileURLToPath(import.meta.url))
const read = (p: string) => readFileSync(p, 'utf8')
const stripComments = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, '')

type Rule = { selector: string; body: Record<string, string> }
const rules = (css: string): Rule[] =>
  [...stripComments(css).matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({
    selector: m[1].trim().replace(/\s+/g, ' '),
    body: Object.fromEntries(
      m[2]
        .split(';')
        .map((d) => d.trim())
        .filter(Boolean)
        .map((d) => [d.slice(0, d.indexOf(':')).trim(), d.slice(d.indexOf(':') + 1).trim()]),
    ),
  }))
const ruleFor = (rs: Rule[], selector: string) => {
  const hit = rs.filter((r) => r.selector === selector)
  expect(hit, `exactly one rule "${selector}"`).toHaveLength(1)
  return hit[0].body
}
// [ids, classes + pseudo-classes + attributes, elements] of one simple selector.
const specificity = (sel: string): [number, number, number] => [
  (sel.match(/#[\w-]+/g) ?? []).length,
  (sel.match(/\.[\w-]+|\[[^\]]*\]|:(?!:)[\w-]+/g) ?? []).length,
  (sel.replace(/#[\w-]+|\.[\w-]+|\[[^\]]*\]|::?[\w-]+/g, ' ').match(/(?:^|[\s>+~])[a-z][\w-]*/gi) ?? []).length,
]
const outranks = (a: [number, number, number], b: [number, number, number]) =>
  a[0] !== b[0] ? a[0] > b[0] : a[1] !== b[1] ? a[1] > b[1] : a[2] > b[2]

// rules() cannot read nested blocks; this maps each @keyframes name to its from/to declarations.
const decls = (body: string) =>
  Object.fromEntries(
    body
      .split(';')
      .map((d) => d.trim())
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(':')).trim(), d.slice(d.indexOf(':') + 1).trim()]),
  )
const keyframes = (css: string) =>
  Object.fromEntries(
    [...stripComments(css).matchAll(/@keyframes\s+([\w-]+)\s*\{\s*from\s*\{([^{}]*)\}\s*to\s*\{([^{}]*)\}\s*\}/g)].map((m) => [
      m[1],
      { from: decls(m[2]), to: decls(m[3]) },
    ]),
  )

describe('library entry', () => {
  it('VE-01 the glyph set is the 21 glyphs the prototype draws', () => {
    expect(Object.keys(GLYPHS).sort()).toEqual(
      ('archive arrow-right bell building-2 chart-column chevron-left chevron-right circle-check file-search ' +
        'file-text layout-dashboard pen-tool play plug rotate-cw send shield-check triangle-alert users workflow x').split(' '),
    )
  })

  it('VE-02 every group icon is a glyph', () => {
    const icons = GROUPS.map((g) => g.icon)
    expect(icons).toHaveLength(11)
    expect(icons).toContain('bell')
    expect(icons).toContain('building-2')
    for (const i of icons) expect(Object.keys(GLYPHS), i).toContain(i)
  })

  it('VE-03 library.css gives each Button variant and size the DS values', () => {
    const rs = rules(read(join(HERE, 'styles/library.css')))
    const box = (sel: string, h: string, p: string) => expect(ruleFor(rs, sel)).toMatchObject({ height: h, padding: p })
    box('.ds-btn--sm', '40px', '0 16px')
    box('.ds-btn--md', '46px', '12px 21px')
    box('.ds-btn--lg', '57px', '17px 27px')
    expect(ruleFor(rs, '.ds-btn--outline, .ds-btn--outlineDark')['font-size']).toBe('var(--fs-btn-sm)')
    expect(ruleFor(rs, '.asc-app .ds-btn--primary')).toMatchObject({
      background: 'var(--primary)',
      color: 'var(--primary-foreground)',
      border: '1px solid transparent',
    })
    expect(ruleFor(rs, '.asc-app .ds-btn--outline')).toMatchObject({
      background: 'transparent',
      color: 'var(--ink)',
      border: '1px solid var(--button-outline-border)',
    })
    expect(ruleFor(rs, '.asc-app .ds-btn--outlineDark')).toMatchObject({
      background: 'transparent',
      color: 'var(--surface-foreground)',
      border: '1px solid var(--on-dark-20)',
    })
    expect(ruleFor(rs, '.asc-app .ds-btn--primary:hover:not(:disabled)').filter).toBe('brightness(1.18)')
    expect(ruleFor(rs, '.asc-app .ds-btn--outline:hover:not(:disabled)').background).toBe('var(--muted)')
    expect(ruleFor(rs, '.asc-app .ds-btn--outlineDark:hover:not(:disabled)').background).toBe('var(--on-dark-10)')
  })

  it('VE-04 a link Button keeps its colour inside .asc-app', () => {
    const layer = rules(read(join(HERE, '../../../packages/design-tokens/v2/app-layer.css')))
    expect(ruleFor(layer, '.asc-app a').color).toBe('inherit')
    const base = specificity('.asc-app a')
    expect(base).toEqual([0, 1, 1])
    const colored = rules(read(join(HERE, 'styles/library.css'))).filter(
      (r) => 'color' in r.body && r.selector.includes('.ds-btn--'),
    )
    expect(colored.length).toBeGreaterThanOrEqual(6)
    for (const r of colored) for (const sel of r.selector.split(',')) expect(outranks(specificity(sel.trim()), base), sel).toBe(true)
  })

  it('VE-05 the prototype hover states are rules in library.css', () => {
    const rs = rules(read(join(HERE, 'styles/library.css')))
    expect(ruleFor(rs, '.lib-nav:hover').background).toBe('var(--surface-panel) !important')
    expect(ruleFor(rs, '.lib-tour:hover').filter).toBe('brightness(1.06)')
    expect(ruleFor(rs, '.lib-stage:hover').background).toBe('var(--sage-panel) !important')

    const noop = () => {}
    const classes = (html: string, tag: string) =>
      [...html.matchAll(new RegExp(`<${tag}\\b([^>]*)>`, 'g'))].map((m) => m[1].match(/class="([^"]*)"/)?.[1] ?? '')
    const side = (path: string) =>
      renderToStaticMarkup(
        createElement(Sidebar, { route: parseLibraryPath(path), demoHref: null, onHome: noop, onGroup: noop, onFeature: noop, onTour: noop }),
      )
    const buttons = (html: string) => classes(html, 'button')
    const homeHtml = side('/')
    expect(buttons(homeHtml).filter((c) => c === 'lib-tour')).toHaveLength(1)
    expect(buttons(homeHtml).filter((c) => c === 'lib-nav')).toHaveLength(12)
    const groupHtml = side('/invoices')
    expect(buttons(groupHtml).filter((c) => c === 'lib-nav')).toHaveLength(12 + 3)
    expect(buttons(side('/invoices/import-files')).filter((c) => c === 'lib-nav')).toHaveLength(12 + 3)
    const stages = buttons(renderToStaticMarkup(createElement(JourneyStepper, { route: parseLibraryPath('/'), onGroup: noop })))
    expect(stages).toEqual(Array(6).fill('lib-stage'))
  })

  it("VE-06 a card's hover border is --primary", () => {
    const rs = rules(read(join(HERE, 'styles/library.css')))
    expect(ruleFor(rs, '.lib-card:hover')['border-color']).toBe('var(--primary) !important')
    const html = renderToStaticMarkup(createElement(Home, { demoHref: null, onGroup: () => {}, onTour: () => {} }))
    const cards = [...html.matchAll(/<button\b([^>]*)>/g)].map((m) => m[1].match(/class="([^"]*)"/)?.[1] ?? '').filter((c) => c !== 'ds-btn ds-btn--primary ds-btn--md')
    expect(cards).toEqual(Array(11).fill('lib-card'))
  })

  it('VE-07 main.tsx mounts App in the crash boundary after instrument, the tokens, the layer and library.css', () => {
    const src = stripComments(read(join(HERE, 'main.tsx'))).replace(/^\s*\/\/.*$/gm, '')
    const side = [...src.matchAll(/^import\s+'([^']+)'/gm)].map((m) => m[1])
    expect(side).toEqual([
      './instrument',
      '@invoice-os/design-tokens/v2/styles.css',
      '@invoice-os/design-tokens/v2/app-layer.css',
      './styles/library.css',
    ])
    expect(src).toMatch(/\.render\(\s*<StrictMode>\s*<CrashBoundary\b[^>]*>[\s\S]*?<\/CrashBoundary>\s*<\/StrictMode>/)
    expect(src).toMatch(/<CrashBoundary\b[\s\S]*?>\s*<App\s*\/>\s*<\/CrashBoundary>/)
  })

  it('VE-08 library.css holds the scene keyframes', () => {
    const css = read(join(HERE, 'styles/library.css'))
    expect(stripComments(css).match(/@keyframes/g)).toHaveLength(2)
    const kf = keyframes(css)
    expect(Object.keys(kf).sort()).toEqual(['libFade', 'libPop'])
    expect(kf.libPop).toEqual({ from: { opacity: '0', transform: 'translateY(6px)' }, to: { opacity: '1', transform: 'none' } })
    expect(kf.libFade).toEqual({ from: { opacity: '0' }, to: { opacity: '1' } })
  })

  it('VE-09 the play button hover is a rule in library.css', () => {
    const rs = rules(read(join(HERE, 'styles/library.css')))
    expect(ruleFor(rs, '.lib-play:hover').filter).toBe('brightness(1.06)')
    const feature = FEATURES.find((f) => f.id === 'import-files')!
    const html = renderToStaticMarkup(createElement(Player, { feature, clock: START, onToggle: () => {}, onSeek: () => {} }))
    const buttons = [...html.matchAll(/<button\b([^>]*)>/g)].map((m) => m[1].match(/class="([^"]*)"/)?.[1])
    expect(buttons).toEqual(['lib-play'])
  })

  it('VE-10 the back button hover is a rule in library.css', () => {
    expect(ruleFor(rules(read(join(HERE, 'styles/library.css'))), '.lib-back:hover').color).toBe('var(--teal) !important')
  })

  it("VE-11 the library's disabled rule has the landing's declarations", () => {
    const landing = ruleFor(rules(read(join(HERE, '../../landing/src/styles/ds.css'))), '.ds-btn:disabled')
    expect(landing).toMatchObject({ cursor: 'not-allowed', opacity: '0.45' })
    const lib = ruleFor(rules(read(join(HERE, 'styles/library.css'))), '.ds-btn:disabled')
    expect(lib).toEqual(landing)
  })

  it('VE-12 the tour close hover is a rule in library.css', () => {
    const rs = rules(read(join(HERE, 'styles/library.css')))
    expect(ruleFor(rs, '.lib-tour-x:hover').color).toBe('var(--ink) !important')
    const html = renderToStaticMarkup(
      createElement(TourOverlay, { tour: { i: 0, phase: 'card' }, rect: null, win: { w: 100, h: 100 }, onBack() {}, onNext() {}, onWatch() {}, onClose() {} }),
    )
    expect(html.match(/<button\b[^>]*class="lib-tour-x"/g)).toHaveLength(1)
  })

  const TOKENS = join(HERE, '../../../packages/design-tokens/v2')
  const queries = (css: string) => [...stripComments(css).matchAll(/@media\s*([^{]+)\{/g)].map((m) => m[1].trim())
  // The phone block's rules, read out of the one @media body by brace matching.
  const phoneRules = () => {
    const css = stripComments(read(join(HERE, 'styles/library.css')))
    const start = css.indexOf('{', css.indexOf('@media')) + 1
    let depth = 1
    let end = start
    while (depth > 0) depth += css[end++] === '{' ? 1 : css[end - 1] === '}' ? -1 : 0
    return rules(css.slice(start, end - 1))
  }

  it('PH-01 the one media query is the phone breakpoint', () => {
    expect(PHONE_MAX_WIDTH).toBe(767)
    expect(queries(read(join(HERE, 'styles/library.css')))).toEqual([`(max-width: ${PHONE_MAX_WIDTH}px)`])
  })

  it('PH-02 the design tokens flip at PHONE_MAX_WIDTH', () => {
    for (const f of ['spacing.css', 'typography.css']) {
      const qs = queries(read(join(TOKENS, 'tokens', f)))
      expect(qs.length, f).toBeGreaterThan(0)
      expect(qs.map((q) => Number(q.match(/^\(max-width:\s*(\d+)px\)$/)?.[1])), f).toContain(PHONE_MAX_WIDTH)
    }
    expect(read(join(HERE, 'main.tsx'))).toContain("import '@invoice-os/design-tokens/v2/styles.css'")
  })

  it('PH-03 the phone block stacks the shell', () => {
    const rs = phoneRules()
    expect(ruleFor(rs, '.asc-app')).toMatchObject({ 'flex-direction': 'column !important', height: 'auto !important', overflow: 'visible !important' })
    expect(ruleFor(rs, '.asc-app > aside')).toMatchObject({ width: '100% !important', 'border-right': '0 !important' })
    expect(ruleFor(rs, '.asc-app > aside nav')).toMatchObject({ 'flex-direction': 'row !important', 'overflow-x': 'auto !important' })
    expect(ruleFor(rs, '.asc-app > aside nav button').width).toBe('auto !important')
    expect(ruleFor(rs, '.lib-subnav, .lib-navlabel').display).toBe('none !important')
    expect(ruleFor(rs, '#lib-main')['overflow-y']).toBe('visible !important')
    expect(ruleFor(rs, '.lib-px')).toMatchObject({ 'padding-left': '20px !important', 'padding-right': '20px !important' })
    expect(ruleFor(rs, '.lib-cols')['grid-template-columns']).toBe('minmax(0, 1fr) !important')
  })

  const noop = () => {}
  const feature = FEATURES.find((f) => f.id === 'import-files')!
  const markup = {
    sidebar: renderToStaticMarkup(createElement(Sidebar, { route: parseLibraryPath('/invoices'), demoHref: null, onHome: noop, onGroup: noop, onFeature: noop, onTour: noop })),
    stepper: renderToStaticMarkup(createElement(JourneyStepper, { route: parseLibraryPath('/'), onGroup: noop })),
    home: renderToStaticMarkup(createElement(Home, { demoHref: null, onGroup: noop, onTour: noop })),
    group: renderToStaticMarkup(createElement(GroupPage, { group: GROUPS[0], openHref: null, onFeature: noop })),
    feature: renderToStaticMarkup(
      createElement(FeaturePage, { group: GROUPS.find((g) => g.id === feature.gid)!, feature, openHref: null, onGroup: noop, onFeature: noop }),
    ),
  }
  const withClass = (html: string, cls: string) =>
    [...html.matchAll(/<(\w+)\b([^>]*)>/g)].filter((m) => (m[2].match(/class="([^"]*)"/)?.[1] ?? '').split(' ').includes(cls)).map((m) => m[2])

  it('PH-04 every hook the phone block names is rendered, and its override is live', () => {
    expect(withClass(markup.sidebar, 'lib-subnav')).toHaveLength(1)
    expect(withClass(markup.sidebar, 'lib-navlabel')).toHaveLength(1)
    expect([markup.stepper, markup.home, markup.group, markup.feature].map((h) => withClass(h, 'lib-px').length)).toEqual([1, 3, 1, 1])
    const cols = [markup.home, markup.group, markup.feature].flatMap((h) => withClass(h, 'lib-cols'))
    expect(cols).toHaveLength(3)
    for (const a of cols) expect(a).toMatch(/style="[^"]*grid-template-columns:/)
    for (const a of Object.values(markup).flatMap((h) => withClass(h, 'lib-px'))) expect(a).toMatch(/style="[^"]*padding:/)
  })

  it('PH-08 the player is a stretched child of a one-column section', () => {
    const section = markup.feature.match(/<section\b[^>]*class="lib-px"[^>]*style="([^"]*)"/)![1]
    expect(section).toContain('display:flex')
    expect(section).toContain('flex-direction:column')
    expect(section).not.toContain('align-items')
    const player = [...markup.feature.matchAll(/<div\b[^>]*style="([^"]*)"/g)].map((m) => m[1]).find((st) => st.includes('border-radius:10px'))!
    for (const p of ['width', 'max-width', 'margin', 'align-self', 'position:absolute']) expect(player, p).not.toContain(p)
    const header = [...markup.feature.matchAll(/<div\b[^>]*style="([^"]*)"/g)].map((m) => m[1]).find((st) => st.includes('max-width:720px'))
    expect(header).toBeDefined()
  })
})
