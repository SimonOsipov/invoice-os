import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const HERE = dirname(fileURLToPath(import.meta.url))
const V2 = join(HERE, '../../../../packages/design-tokens/v2')

const uncomment = (css: string) => css.replace(/\/\*[\s\S]*?\*\//g, '')

function platformCss(): string {
  const css = uncomment(readFileSync(join(HERE, 'platform.css'), 'utf8'))
  expect(css.length, 'platform.css is not empty').toBeGreaterThan(0)
  return css
}

// Top-level rules only: the selector starts a line and its body has no nested braces.
function decls(css: string, selector: string): Map<string, string> {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const body = new RegExp(`^${escaped}\\s*\\{([^}]*)\\}`, 'm').exec(css)?.[1]
  expect(body, `${selector} block exists`).toBeDefined()
  return new Map(body!.split(';').filter((d) => d.includes(':')).map((d) => [d.slice(0, d.indexOf(':')).trim(), d.slice(d.indexOf(':') + 1).trim()]))
}

describe('platform.css focus rings', () => {
  it('PC-02 the chip box rings with --ring, over its inline border', () => {
    const d = decls(platformCss(), '.asc-app .pf-chipbox:focus-within')

    expect(d.get('box-shadow'), 'control: the block declarations are read').toBe('0 0 0 2px var(--ring)')
    // !important: the box's inline `border` shorthand would otherwise win.
    expect(d.get('border-color')).toBe('var(--ring) !important')
  })

  it('PC-02b the input and number focus rules ring with --ring', () => {
    const css = platformCss()

    for (const selector of ['.pf-input:focus', '.pf-num:focus']) {
      const d = decls(css, selector)
      expect(d.get('box-shadow'), `control: ${selector} declarations are read`).toBe('0 0 0 2px var(--ring)')
      expect(d.get('border-color'), selector).toBe('var(--ring)')
    }
  })
})

describe('platform.css custom properties', () => {
  it('PC-04 every var() resolves under v2 (pin, green at write)', () => {
    const declared = new Set<string>()
    const files = [
      ...readdirSync(join(V2, 'tokens')).filter((f) => f.endsWith('.css')).map((f) => join(V2, 'tokens', f)),
      join(V2, 'utilities.css'),
      join(V2, 'app-layer.css'),
    ]
    for (const f of files) for (const m of uncomment(readFileSync(f, 'utf8')).matchAll(/(--[\w-]+)\s*:/g)) declared.add(m[1])
    const used = [...new Set([...platformCss().matchAll(/var\(\s*(--[\w-]+)/g)].map((m) => m[1]))]

    expect(declared.has('--ring'), 'control: the v2 files declare their tokens').toBe(true)
    expect(used.length, 'control: platform.css names tokens').toBeGreaterThan(5)
    expect(used.filter((n) => !declared.has(n))).toEqual([])
  })
})

describe('platform.css toggle', () => {
  it('the knob and track rules set only a transition, so the inline pill radius stays', () => {
    const css = platformCss()

    for (const selector of ['.pf-toggle', '.pf-knob']) {
      const d = decls(css, selector)
      expect(d.get('transition'), `control: ${selector} declarations are read`).toContain('ease-out')
      expect([...d.keys()], selector).toEqual(['transition'])
    }
  })
})

describe('platform.css code block', () => {
  it('pre.pf-json is a light code block', () => {
    const d = decls(platformCss(), 'pre.pf-json')

    expect(d.get('font-family'), 'control: the block declarations are read').toBe('var(--font-mono)')
    expect.soft(d.get('background')).toBe('var(--bg-2)')
    expect.soft(d.get('color')).toBe('var(--fg-2)')
    expect.soft(d.get('border')).toBe('1px solid var(--line-1)')
    expect.soft(d.get('border-radius')).toBe('var(--radius-md)')
    expect.soft(d.get('padding')).toBe('14px')
    expect.soft(d.get('font-size')).toBe('11.5px')
    expect.soft(d.get('line-height')).toBe('1.6')
    expect.soft(d.get('overflow-x')).toBe('auto')
    expect.soft(d.get('white-space')).toBe('pre')
  })
})
