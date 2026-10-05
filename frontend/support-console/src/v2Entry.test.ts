/// <reference types="node" />
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { stripComments } from '@invoice-os/api-client/strip-comments'

import { BrandMark } from './icons'

const HERE = dirname(fileURLToPath(import.meta.url))
const TOKENS = join(HERE, '../../../packages/design-tokens')
const read = (p: string) => readFileSync(p, 'utf8')
const stripCss = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, '')

const MAIN = read(join(HERE, 'main.tsx'))
const ICONS = read(join(HERE, 'icons.tsx'))
const INDEX_HTML = read(join(HERE, '../index.html'))
const SUPPORT_CSS = read(join(HERE, 'styles/support.css'))
const DS = '@invoice-os/design-tokens'

const SRC_FILES = readdirSync(HERE, { recursive: true, encoding: 'utf8' })
  .filter((f) => /\.(ts|tsx|css)$/.test(f) && !/\.test\.tsx?$/.test(f) && !/\.d\.ts$/.test(f))
  .sort()
const stripSrc = (f: string) => (f.endsWith('.css') ? stripCss(read(join(HERE, f))) : stripComments(read(join(HERE, f))))

const ruleBodies = (css: string, selector: string) =>
  [...stripCss(css).matchAll(/([^{}]+)\{([^{}]*)\}/g)]
    .filter((m) => m[1].split(',').some((sel) => sel.trim() === selector))
    .map((m) => m[2])
const declarations = (body: string) =>
  new Map(
    body
      .split(';')
      .map((d) => d.trim())
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(':')).trim(), d.slice(d.indexOf(':') + 1).trim()] as const),
  )

describe('v2 entry', () => {
  it('VE-01 main.tsx loads the v2 entry, then the layer, then support.css', () => {
    const specifiers = [...stripComments(MAIN).matchAll(/^\s*import\s+['"]([^'"]+)['"]/gm)].map((m) => m[1])
    expect(specifiers.length).toBeGreaterThan(0)
    const at = (s: string) => specifiers.indexOf(s)

    expect(at(`${DS}/v2/styles.css`), 'v2/styles.css imported').toBeGreaterThanOrEqual(0)
    expect(at(`${DS}/v2/app-layer.css`), 'v2/app-layer.css after v2/styles.css').toBeGreaterThan(at(`${DS}/v2/styles.css`))
    expect(at('./styles/support.css'), 'support.css after the layer').toBeGreaterThan(at(`${DS}/v2/app-layer.css`))
    expect(specifiers).not.toContain(`${DS}/styles.css`)
    const designTokens = specifiers.filter((s) => s.startsWith(`${DS}/`))
    expect(designTokens.length).toBeGreaterThan(0)
    for (const s of designTokens) expect(s, 'only v2 entries').toContain('/v2/')

    const everywhere = SRC_FILES.flatMap((f) =>
      [...stripSrc(f).matchAll(/['"]@invoice-os\/design-tokens\/([^'"]+)['"]/g)].map((m) => `${f}: ${m[1]}`),
    )
    expect(everywhere.length, 'design-tokens specifiers across src').toBeGreaterThanOrEqual(3)
    expect(everywhere.filter((e) => !e.split(': ')[1].startsWith('v2/')), 'no v1 design-tokens specifier in any src file').toEqual([])
  })

  it('VE-02 every design-tokens specifier is exported (pin, green at write)', () => {
    const exported = Object.keys(JSON.parse(read(join(TOKENS, 'package.json'))).exports)
    const specifiers = [MAIN, ICONS].flatMap((src) =>
      [...stripComments(src).matchAll(/['"]@invoice-os\/design-tokens\/([^'"]+)['"]/g)].map((m) => `./${m[1]}`),
    )
    expect(specifiers.length).toBeGreaterThan(0)
    for (const s of specifiers) expect(exported, `${s} is a key of package.json exports`).toContain(s)
  })

  it('VE-03 index.html links Manrope and IBM Plex Mono only', () => {
    const html = INDEX_HTML.replace(/<!--[\s\S]*?-->/g, '').replace(/&amp;/g, '&')
    const links = [...html.matchAll(/<link\b[^>]*>/gi)].map((m) => m[0])
    const hrefs = links
      .filter((tag) => /\brel\s*=\s*["']?stylesheet/i.test(tag))
      .map((tag) => tag.match(/\bhref\s*=\s*["']([^"']*)["']/i)?.[1] ?? '')
    expect(hrefs).toHaveLength(2)

    const importOf = (css: string) => {
      const urls = [...css.matchAll(/@import\s+url\('([^']+)'\)/g)].map((m) => m[1])
      expect(urls).toHaveLength(1)
      return urls[0]
    }
    const manrope = importOf(read(join(TOKENS, 'v2/tokens/typography.css')))
    const plex = importOf(read(join(TOKENS, 'v2/app-layer.css')))
    expect([...hrefs].sort(), 'stylesheet hrefs equal the two token @import URLs').toEqual([manrope, plex].sort())

    const retired = [/\binter\b/i, /fraunces/i]
    expect(
      retired.map((re) => re.test('family=INTER:wght@400&family=FRAUNCES')),
      'control: each needle matches in any case',
    ).toEqual([true, true])
    expect(retired.some((re) => re.test('interval internal')), 'control: inter inside a word is no hit').toBe(false)
    for (const re of retired) expect(html, `index.html names ${re}`).not.toMatch(re)

    expect(links.filter((tag) => /\brel\s*=\s*["']?preconnect/i.test(tag))).toHaveLength(2)
  })

  it('VE-04 BrandMark renders the v2 mark at 26 with a 6px corner', () => {
    const imgOf = (size?: number) => {
      const html = renderToStaticMarkup(createElement(BrandMark, size === undefined ? {} : { size }))
      const img = html.match(/<img\b[^>]*>/)?.[0]
      expect(img, `no <img> in ${html}`).toBeDefined()
      return (name: string) => img!.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1]
    }

    const attr = imgOf()
    expect.soft(attr('src')).toMatch(/v2\/assets\/mark\.png(\?.*)?$/)
    expect.soft(attr('width')).toBe('26')
    expect.soft(attr('height')).toBe('26')
    expect.soft(attr('alt')).toBe('')
    expect.soft(attr('aria-hidden')).toBe('true')
    expect.soft(attr('style')).toContain('display:block')
    expect.soft(attr('style')).toContain('border-radius:var(--radius-md)')

    const small = imgOf(20)
    expect.soft(small('width'), 'an explicit size still wins').toBe('20')
    expect.soft(small('height'), 'an explicit size wins on height too').toBe('20')
    expect.soft(small('style'), 'an explicit size keeps the corner').toContain('border-radius:var(--radius-md)')
  })

  it('VE-05 the dead .ops-input:focus rule names --ring (source pin)', () => {
    // The rule matches no element under the layer, so no runtime read can see it.
    const blocks = ruleBodies(SUPPORT_CSS, '.ops-input:focus')
    expect(blocks, 'support.css has a .ops-input:focus block').toHaveLength(1)

    const body = blocks[0]
    expect(body, '.ops-input:focus declares border-color: var(--ring)').toMatch(/(^|[;\s])border-color\s*:\s*var\(--ring\)\s*(;|$)/i)
    expect(body, '.ops-input:focus no longer reads --accent').not.toMatch(/var\(--accent\)/i)
  })

  it('VE-06 every owned var() resolves under v2 (pin, green at write)', () => {
    const cssDeclared = ['tokens/colors.css', 'tokens/hero-grid.css', 'tokens/spacing.css', 'tokens/typography.css', 'utilities.css', 'app-layer.css']
    const declared = new Set(
      cssDeclared.flatMap((f) => [...stripCss(read(join(TOKENS, 'v2', f))).matchAll(/(?:^|[\s;{])(--[\w-]+)\s*:/g)].map((m) => m[1])),
    )
    expect(declared.size, 'declared custom properties').toBeGreaterThan(30)
    expect(declared.has('--ring')).toBe(true)

    expect(SRC_FILES).toContain('styles/support.css')
    expect(SRC_FILES).toContain('icons.tsx')

    const used = new Map<string, string[]>()
    for (const f of SRC_FILES) {
      for (const m of stripSrc(f).matchAll(/var\(\s*(--[\w-]+)/g)) used.set(m[1], [...(used.get(m[1]) ?? []), f])
    }
    expect(used.size, 'var() names collected').toBeGreaterThan(30)

    const undeclared = [...used].filter(([name]) => !declared.has(name)).map(([name, fs]) => `${name} in ${[...new Set(fs)].join(', ')}`)
    expect(undeclared, 'var() names no v2 file declares').toEqual([])
  })

  it('VE-10 support.css field rules, input border and corners (source pin: jsdom applies no CSS; resolved reads are SUP-03 and SUP-04)', () => {
    const decl = (selector: string, prop: string) => {
      const bodies = ruleBodies(SUPPORT_CSS, selector)
      expect(bodies, `support.css has one ${selector} block`).toHaveLength(1)
      return declarations(bodies[0]).get(prop)
    }

    expect(decl('.ops-input', 'border')).toBe('1px solid var(--input)')
    expect(decl('.ops-input', 'border-radius')).toBe('var(--radius-btn)')
    expect(decl('pre.ops-json', 'border-radius')).toBe('var(--radius-md)')
    expect(decl('.ops-field:focus-within', 'border-color')).toBe('var(--ring) !important')
    expect(decl('.ops-field:focus-within', 'box-shadow')).toBe('0 0 0 2px var(--ring) !important')
    expect(decl('.asc-app .ops-field input:focus', 'box-shadow')).toBe('none !important')
  })
})
