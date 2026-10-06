import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const DS = join(dirname(fileURLToPath(import.meta.url)), '../../../../packages/design-tokens')
const LAYER = join(DS, 'v2/app-layer.css')

const uncomment = (css: string) => css.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ''))

function rawLayer(): string {
  expect(existsSync(LAYER), 'packages/design-tokens/v2/app-layer.css exists').toBe(true)
  const raw = readFileSync(LAYER, 'utf8')
  expect(raw.length, 'v2/app-layer.css is not empty').toBeGreaterThan(0)
  return raw
}

type Block = { selector: string; tokens: string[]; decls: Map<string, string> }

// Flat scan: at-rule bodies (@keyframes) are walked, their inner blocks count as blocks.
function parseBlocks(css: string): Block[] {
  const blocks: Block[] = []
  let prelude = ''
  for (let i = 0; i < css.length; i++) {
    const ch = css[i]
    if (ch === "'" || ch === '"') {
      const end = css.indexOf(ch, i + 1)
      const stop = end === -1 ? css.length : end + 1
      prelude += css.slice(i, stop)
      i = stop - 1
    } else if (ch === ';') {
      prelude = ''
    } else if (ch === '}') {
      prelude = ''
    } else if (ch === '{') {
      const head = prelude.trim()
      prelude = ''
      if (head.startsWith('@')) continue
      const close = css.indexOf('}', i)
      const end = close === -1 ? css.length : close
      const decls = new Map<string, string>()
      for (const d of css.slice(i + 1, end).split(';')) {
        const idx = d.indexOf(':')
        if (idx > 0) decls.set(d.slice(0, idx).trim().toLowerCase(), d.slice(idx + 1).trim())
      }
      const tokens = head.split(',').map((s) => s.trim().replace(/\s+/g, ' '))
      blocks.push({ selector: head.replace(/\s+/g, ' '), tokens, decls })
      i = end
    } else {
      prelude += ch
    }
  }
  return blocks
}

const layerBlocks = () => parseBlocks(uncomment(rawLayer()))

// Exact selector-list match, so the prototype's wider `.pf-select` / `.pf-chip` lists never stand in.
const sameList = (b: Block, list: string[]) =>
  b.tokens.length === list.length && list.every((t) => b.tokens.includes(t))

const declared = (css: string) => new Set([...css.matchAll(/(--\w[\w-]*)\s*:/g)].map((m) => m[1]))

describe('v2 app layer', () => {
  it('AL-01 the package exports the v2 app layer', () => {
    const pkg = JSON.parse(readFileSync(join(DS, 'package.json'), 'utf8')) as { exports: Record<string, string> }
    expect(Object.keys(pkg.exports).sort()).toEqual(
      ['./v2/styles.css', './v2/assets/mark.png', './v2/app-layer.css'].sort(),
    )
    expect(pkg.exports['./v2/app-layer.css']).toBe('./v2/app-layer.css')
  })

  it('AL-02 the file opens with the prototype layer', () => {
    const raw = rawLayer()
    expect(raw.startsWith('/* ASComply — product/app layer, v2 brand.')).toBe(true)
    expect(uncomment(raw).trimStart()).toMatch(/^@import url\(\s*['"]https:\/\/fonts\.googleapis\.com\/css2\?family=IBM\+Plex\+Mono/)
    const blocks = parseBlocks(uncomment(raw))
    expect(blocks.some((b) => sameList(b, ['.asc-app', '.asc-light']) && b.decls.has('--font-mono')), 'prototype token block').toBe(true)
    expect(blocks.some((b) => sameList(b, ['.asc-dark']))).toBe(true)
  })

  it('AL-03 the two app corners are bound to DS radii', () => {
    const block = layerBlocks().find((b) => sameList(b, ['.asc-app', '.asc-light']) && b.decls.has('--radius-input'))
    expect(block, '.asc-app, .asc-light block holding --radius-input').toBeDefined()
    expect(block!.decls.get('--radius-input')).toBe('var(--radius-btn)')
    expect(block!.decls.get('--radius-xs')).toBe('var(--radius-sm)')
    const css = uncomment(rawLayer())
    for (const name of ['--radius-input', '--radius-xs']) {
      expect(css.match(new RegExp(`${name}\\s*:`, 'g')), `${name} is declared once`).toHaveLength(1)
    }
    const spacing = uncomment(readFileSync(join(DS, 'v2/tokens/spacing.css'), 'utf8'))
    expect(spacing).toMatch(/--radius-btn:\s*7px\s*;/)
    expect(spacing).toMatch(/--radius-sm:\s*4px\s*;/)
  })

  it('AL-04 .card-title uses v2 type tokens', () => {
    const block = layerBlocks().find((b) => sameList(b, ['.asc-app .card-title']))
    expect(block, '.asc-app .card-title block').toBeDefined()
    expect(Object.fromEntries(block!.decls)).toEqual({
      'font-family': 'var(--font-sans)',
      'font-size': 'var(--fs-ui)',
      'font-weight': 'var(--fw-bold)',
      'line-height': 'var(--lh-snug)',
      'letter-spacing': 'var(--tracking-card)',
      color: 'var(--fg-1)',
    })
  })

  it('AL-05 .pf-file is visually hidden and rings on keyboard focus', () => {
    const blocks = layerBlocks()
    const file = blocks.find((b) => sameList(b, ['.asc-app .pf-file']))
    expect(file, '.asc-app .pf-file block').toBeDefined()
    expect(Object.fromEntries(file!.decls)).toEqual({
      position: 'absolute',
      width: '1px',
      height: '1px',
      padding: '0',
      margin: '-1px',
      overflow: 'hidden',
      clip: 'rect(0 0 0 0)',
      'white-space': 'nowrap',
      border: '0',
    })
    const focus = blocks.find((b) => sameList(b, ['.asc-app .pf-file:focus-visible + label']))
    expect(focus, '.asc-app .pf-file:focus-visible + label block').toBeDefined()
    expect(Object.fromEntries(focus!.decls)).toEqual({ 'border-color': 'var(--ring)', 'box-shadow': '0 0 0 2px var(--ring)' })
  })

  it('AL-06 native select chrome is stripped', () => {
    const block = layerBlocks().find((b) => sameList(b, ['.asc-app select', '.asc-app .pf-select']))
    expect(block, '.asc-app select, .asc-app .pf-select block').toBeDefined()
    expect(Object.fromEntries(block!.decls)).toEqual({ appearance: 'none', '-webkit-appearance': 'none', 'padding-right': '32px' })
  })

  it('AL-07 no DC syntax', () => {
    const needles = ['{{', '}}', '<sc-', 'hint-placeholder', 'data-dc']
    const hits = (css: string) => needles.filter((n) => css.includes(n))
    expect(hits('a {{ b }} <sc-x hint-placeholder data-dc'), 'control: every needle is found').toEqual(needles)
    expect(hits(rawLayer())).toEqual([])
  })

  it('AL-08 every var() resolves', () => {
    const css = uncomment(rawLayer())
    const refs = new Set([...css.matchAll(/var\(\s*(--\w[\w-]*)/gi)].map((m) => m[1]))
    expect(refs.size, 'distinct var() names referenced').toBeGreaterThanOrEqual(40)
    const tokenDir = join(DS, 'v2/tokens')
    const sources = [
      css,
      uncomment(readFileSync(join(DS, 'v2/utilities.css'), 'utf8')),
      ...readdirSync(tokenDir)
        .filter((f) => f.endsWith('.css'))
        .map((f) => uncomment(readFileSync(join(tokenDir, f), 'utf8'))),
    ]
    const known = new Set(sources.flatMap((s) => [...declared(s)]))
    expect([...refs].filter((n) => !known.has(n)).sort()).toEqual([])
  })

  it('AL-09 no v1 vocabulary', () => {
    const css = uncomment(rawLayer())
    expect(parseBlocks(css).length, 'rule blocks').toBeGreaterThanOrEqual(40)
    const needles = [/oklch\(/i, /fraunces/i, /\binter\b/i, /--gradient-/i, /opsz/i]
    const planted = 'a { x: OKLCH(1 0 0); y: Fraunces; z: INTER; --Gradient-a: 1; w: "OPSZ" 9 }'
    expect(needles.filter((re) => !re.test(planted)), 'control: every needle is found in any letter case').toEqual([])
    for (const re of needles) {
      expect(css.match(re), String(re)).toBeNull()
    }
  })

  it('AL-10 shape rules; toggles untouched', () => {
    const blocks = layerBlocks()
    const btn = blocks.find((b) => b.tokens.includes('.asc-app .pf-btn'))
    expect(btn, 'block whose selectors include .asc-app .pf-btn').toBeDefined()
    expect(btn!.decls.get('border-radius')).toBe('var(--radius-btn) !important')
    const chip = blocks.find((b) => b.tokens.includes('.asc-app .pf-chip'))
    expect(chip, 'block whose selectors include .asc-app .pf-chip').toBeDefined()
    expect(chip!.decls.get('border-radius')).toBe('var(--radius-sm) !important')
    expect(blocks.length, 'rule blocks').toBeGreaterThanOrEqual(40)
    expect(blocks.some((b) => b.tokens.includes('.asc-app .pf-nav')), 'control: a sibling shape rule is scanned').toBe(true)
    const selectors = blocks.map((b) => b.selector).join('\n').toLowerCase()
    expect(selectors).not.toContain('.pf-knob')
    expect(selectors).not.toContain('.pf-toggle')
  })

  it('AL-11 the appended block adds no !important (boundary)', () => {
    const raw = rawLayer()
    const at = raw.indexOf('@keyframes pulse')
    expect(at, 'prefix ends with @keyframes pulse').toBeGreaterThanOrEqual(0)
    const appended = uncomment(raw.slice(raw.indexOf('\n', at)))
    expect(appended).toContain('--radius-input')
    expect(appended).not.toContain('!important')
  })

  it('AL-12 the dark scope keeps its scrollbar colours: its rules outrank the .asc-app ones', () => {
    const blocks = layerBlocks()
    const classes = (sel: string) => (sel.match(/\.[\w-]+/g) ?? []).length
    const appRule = (part: string) => blocks.find((b) => b.selector === `.asc-app ::-webkit-scrollbar-${part}`)
    const darkRule = (part: string) => blocks.find((b) => b.selector === `.asc-app .asc-dark ::-webkit-scrollbar-${part}`)
    expect(appRule('track'), 'control: the light track rule is scanned').toBeDefined()
    expect(appRule('thumb'), 'control: the light thumb rule is scanned').toBeDefined()
    expect(darkRule('track')?.decls.get('background'), 'dark track rule').toBe('transparent')
    expect(darkRule('thumb')?.decls.get('background'), 'dark thumb rule').toBe('var(--surface-panel-border)')
    for (const part of ['track', 'thumb']) {
      expect(classes(darkRule(part)!.selector), `dark ${part} selector class count`).toBeGreaterThan(classes(appRule(part)!.selector))
    }
  })
})
