/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { FEATURES, GROUPS } from './content'
import { Home } from './components/Home'
import { JourneyStepper } from './components/JourneyStepper'
import { Player } from './components/Player'
import { Sidebar } from './components/Sidebar'
import { GLYPHS } from './icons'
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
    expect(ruleFor(rs, '.asc-app .ds-btn--primary:hover').filter).toBe('brightness(1.18)')
    expect(ruleFor(rs, '.asc-app .ds-btn--outline:hover').background).toBe('var(--muted)')
    expect(ruleFor(rs, '.asc-app .ds-btn--outlineDark:hover').background).toBe('var(--on-dark-10)')
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

  it('VE-07 main.tsx mounts App after the tokens, the layer and library.css', () => {
    const src = stripComments(read(join(HERE, 'main.tsx'))).replace(/^\s*\/\/.*$/gm, '')
    const side = [...src.matchAll(/^import\s+'([^']+)'/gm)].map((m) => m[1])
    expect(side).toEqual([
      '@invoice-os/design-tokens/v2/styles.css',
      '@invoice-os/design-tokens/v2/app-layer.css',
      './styles/library.css',
    ])
    expect(src).toMatch(/\.render\(\s*<StrictMode>\s*<App\s*\/>\s*<\/StrictMode>/)
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
})
