/// <reference types="node" />
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { stripComments } from '@invoice-os/api-client/strip-comments'

import { BrandMark } from './icons'

const HERE = dirname(fileURLToPath(import.meta.url))
const TOKENS = join(HERE, '../../../packages/design-tokens')
const read = (p: string) => readFileSync(p, 'utf8')

const MAIN = read(join(HERE, 'main.tsx'))
const ICONS = read(join(HERE, 'icons.tsx'))
const INDEX_HTML = read(join(HERE, '../index.html'))
const OPS_CSS = read(join(HERE, 'styles/ops.css'))
const OVERVIEW = read(join(HERE, 'components/Overview.tsx'))

const stripCssComments = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, '')
const DS = '@invoice-os/design-tokens'

// Body of the first `selector { ... }` block; fails loudly when the block is missing.
function cssBlock(css: string, selector: string, nth = 0): string {
  const re = new RegExp(`(?:^|[\\s}])${selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*\\{([^}]*)\\}`, 'g')
  const body = [...stripCssComments(css).matchAll(re)][nth]?.[1]
  expect(body, `${selector} block #${nth} exists`).toBeDefined()
  return body!.replace(/\s+/g, ' ').trim()
}

describe('v2 entry', () => {
  it('VE-01 main.tsx loads the v2 entry, then the layer, then ops.css', () => {
    const specifiers = [...stripComments(MAIN).matchAll(/^\s*import\s+['"]([^'"]+)['"]/gm)].map((m) => m[1])
    expect(specifiers.length).toBeGreaterThan(0)
    const at = (s: string) => specifiers.indexOf(s)

    expect(at(`${DS}/v2/styles.css`), 'v2/styles.css imported').toBeGreaterThanOrEqual(0)
    expect(at(`${DS}/v2/app-layer.css`), 'v2/app-layer.css after v2/styles.css').toBeGreaterThan(at(`${DS}/v2/styles.css`))
    expect(at('./styles/ops.css'), 'ops.css after the layer').toBeGreaterThan(at(`${DS}/v2/app-layer.css`))
    expect(specifiers).not.toContain(`${DS}/styles.css`)
    const designTokens = specifiers.filter((s) => s.startsWith(`${DS}/`))
    expect(designTokens.length).toBeGreaterThan(0)
    for (const s of designTokens) expect(s, 'only v2 entries').toContain('/v2/')
    expect(designTokens, 'no other design-tokens stylesheet').toEqual([`${DS}/v2/styles.css`, `${DS}/v2/app-layer.css`])
  })

  it('VE-02 every design-tokens specifier is exported (error row)', () => {
    const exported = Object.keys(JSON.parse(read(join(TOKENS, 'package.json'))).exports)
    const specifiers = [MAIN, ICONS].flatMap((src) =>
      [...stripComments(src).matchAll(/['"]@invoice-os\/design-tokens\/([^'"]+)['"]/g)].map((m) => `./${m[1]}`),
    )
    expect(specifiers.length).toBeGreaterThan(0)
    for (const s of specifiers) expect(exported, `${s} is a key of package.json exports`).toContain(s)
  })

  it('VE-03 index.html links Manrope and Plex only', () => {
    const html = INDEX_HTML.replace(/<!--[\s\S]*?-->/g, '').replace(/&amp;/g, '&')
    const tags = [...html.matchAll(/<link\b[^>]*>/gi)].map((m) => m[0])
    const hrefs = tags
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
    expect([...hrefs].sort()).toEqual([manrope, plex].sort())
    for (const h of hrefs) {
      expect(h).not.toMatch(/inter\b/i)
      expect(h).not.toMatch(/fraunces/i)
    }
    const preconnects = tags
      .filter((t) => /\brel\s*=\s*["']?preconnect/i.test(t))
      .map((t) => t.match(/\bhref\s*=\s*["']([^"']*)["']/i)?.[1])
    expect(preconnects.sort()).toEqual(['https://fonts.googleapis.com', 'https://fonts.gstatic.com'])
  })

  it('VE-04 BrandMark renders the v2 mark at 22 with no radius', () => {
    const imgOf = (props: { size?: number } | null) => {
      const html = renderToStaticMarkup(createElement(BrandMark, props))
      const img = html.match(/<img\b[^>]*>/)?.[0]
      expect(img, `no <img> in ${html}`).toBeDefined()
      return (name: string) => img!.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1]
    }

    const attr = imgOf(null)
    expect.soft(attr('src')).toMatch(/\/v2\/assets\/mark\.png(\?.*)?$/)
    expect.soft(attr('width')).toBe('22')
    expect.soft(attr('height')).toBe('22')
    expect.soft(attr('style')).toContain('display:block')
    expect.soft(attr('style')).not.toContain('border-radius')

    const sized = imgOf({ size: 20 })
    expect(sized('width'), 'the size prop still wins').toBe('20')
    expect(sized('height'), 'the size prop sets the height too').toBe('20')
  })

  it('VE-05 ops.css and Overview.tsx drop the bar-grow animation', () => {
    const css = stripCssComments(OPS_CSS)
    const overview = stripComments(OVERVIEW)
    expect(css, 'control: ops.css is read').toContain('.ops-row')
    expect(overview, 'control: Overview.tsx is read').toContain('ops-kpi-strip')
    expect(css, 'ops.css holds opsGrow').not.toMatch(/opsgrow/i)
    expect(css, 'ops.css holds .ops-bar').not.toMatch(/ops-bar/i)
    expect(overview, 'Overview.tsx holds ops-bar').not.toMatch(/ops-bar/i)
  })

  it('VE-05 (source pin) .ops-input:focus sets the ring border; app-layer.css forces it with !important', () => {
    const focus = cssBlock(OPS_CSS, '.ops-input:focus')
    expect(focus).toMatch(/(?:^|;)\s*border-color:\s*var\(--ring\)\s*(?:;|$)/)
    expect(focus).not.toContain('var(--accent)')
  })

  it('VE-05b ops.css pad and JSON corner follow the prototype', () => {
    expect(cssBlock(OPS_CSS, '.ops-screen-pad', 0)).toMatch(/(?:^|;)\s*padding:\s*26px 28px 56px\s*(?:;|$)/)
    expect(cssBlock(OPS_CSS, 'pre.ops-json')).toMatch(/(?:^|;)\s*border-radius:\s*var\(--radius-md\)\s*(?:;|$)/)
    // boundary: the 480px override stays
    expect(cssBlock(OPS_CSS, '.ops-screen-pad', 1)).toContain('padding: 18px 14px 40px')
  })

  it('VE-06 every owned var() resolves under v2', () => {
    const cssFiles = [
      ...readdirSync(join(TOKENS, 'v2/tokens'))
        .filter((f) => f.endsWith('.css'))
        .map((f) => join(TOKENS, 'v2/tokens', f)),
      join(TOKENS, 'v2/utilities.css'),
      join(TOKENS, 'v2/app-layer.css'),
    ]
    const declared = new Set(
      cssFiles.flatMap((f) => [...stripCssComments(read(f)).matchAll(/(?:^|[\s;{])(--[\w-]+)\s*:/g)].map((m) => m[1])),
    )
    expect(declared.size).toBeGreaterThan(30)

    const walk = (dir: string): string[] =>
      readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
        e.isDirectory() ? walk(join(dir, e.name)) : [join(dir, e.name)],
      )
    const owned = [...walk(HERE).filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f)), join(HERE, 'styles/ops.css')]
    expect(owned.length, 'control: owned files collected').toBeGreaterThan(10)

    const used = new Set<string>()
    const undeclared: string[] = []
    const dynamic: string[] = []
    for (const f of owned) {
      const src = f.endsWith('.css') ? stripCssComments(read(f)) : stripComments(read(f))
      for (const m of src.matchAll(/var\(\s*--(?![\w-]+\s*[,)])/g)) dynamic.push(`${m[0]} (${relative(HERE, f)})`)
      for (const m of src.matchAll(/var\(\s*(--[\w-]+)/g)) {
        used.add(m[1])
        if (!declared.has(m[1])) undeclared.push(`${m[1]} (${relative(HERE, f)})`)
      }
    }
    expect(used.size, 'control: var() names collected').toBeGreaterThan(30)
    expect(dynamic, 'var() names built at runtime cannot be resolved here').toEqual([])
    expect([...new Set(undeclared)].sort(), 'var() names not declared by the v2 entry or layer').toEqual([])
  })
})
