/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { BrandMark } from './icons'

const HERE = dirname(fileURLToPath(import.meta.url))
const TOKENS = join(HERE, '../../../packages/design-tokens')
const read = (p: string) => readFileSync(p, 'utf8')

const MAIN = read(join(HERE, 'main.tsx'))
const ICONS = read(join(HERE, 'icons.tsx'))
const INDEX_HTML = read(join(HERE, '../index.html'))

const stripJsComments = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
const DS = '@invoice-os/design-tokens'

describe('v2 entry', () => {
  it('VE-01 main.tsx loads the v2 entry and layer, in order', () => {
    const specifiers = [...stripJsComments(MAIN).matchAll(/^\s*import\s+['"]([^'"]+)['"]/gm)].map((m) => m[1])
    expect(specifiers.length).toBeGreaterThan(0)
    const at = (s: string) => specifiers.indexOf(s)

    expect(at(`${DS}/v2/styles.css`), 'v2/styles.css imported').toBeGreaterThanOrEqual(0)
    expect(at(`${DS}/v2/app-layer.css`), 'v2/app-layer.css imported').toBeGreaterThan(at(`${DS}/v2/styles.css`))
    expect(at('./styles/platform.css'), 'platform.css after the layer').toBeGreaterThan(at(`${DS}/v2/app-layer.css`))
    expect(specifiers).not.toContain(`${DS}/styles.css`)
    const designTokens = specifiers.filter((s) => s.startsWith(`${DS}/`))
    expect(designTokens.length).toBeGreaterThan(0)
    for (const s of designTokens) expect(s, 'only v2 entries').toContain('/v2/')
  })

  it('VE-02 every design-tokens specifier is exported (error row)', () => {
    const exported = Object.keys(JSON.parse(read(join(TOKENS, 'package.json'))).exports)
    const specifiers = [MAIN, ICONS].flatMap((src) =>
      [...stripJsComments(src).matchAll(/['"]@invoice-os\/design-tokens\/([^'"]+)['"]/g)].map((m) => `./${m[1]}`),
    )
    expect(specifiers.length).toBeGreaterThan(0)
    for (const s of specifiers) expect(exported, `${s} is a key of package.json exports`).toContain(s)
  })

  it('VE-03 index.html links Manrope and Plex only', () => {
    const html = INDEX_HTML.replace(/<!--[\s\S]*?-->/g, '').replace(/&amp;/g, '&')
    const hrefs = [...html.matchAll(/<link\b[^>]*>/gi)]
      .map((m) => m[0])
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
  })

  it('VE-06 BrandMark renders the v2 mark with its corner', () => {
    const html = renderToStaticMarkup(createElement(BrandMark, { size: 20 }))
    const img = html.match(/<img\b[^>]*>/)?.[0]
    expect(img, `no <img> in ${html}`).toBeDefined()
    const attr = (name: string) => img!.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1]

    expect.soft(attr('src')).toMatch(/v2\/assets\/mark\.png(\?.*)?$/)
    expect.soft(attr('width')).toBe('20')
    expect.soft(attr('height')).toBe('20')
    expect.soft(attr('alt')).toBe('')
    expect.soft(attr('aria-hidden')).toBe('true')
    expect.soft(attr('style')).toContain('display:block')
    expect.soft(attr('style')).toContain('border-radius:var(--radius-sm)')
  })
})
