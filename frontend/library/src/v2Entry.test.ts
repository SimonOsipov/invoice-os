/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { GROUPS } from './content'
import { GLYPHS } from './icons'

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
})
