import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { GLYPHS } from './icons'

const glyphs = GLYPHS as Record<string, readonly string[]>

function serverIcons(): string[] {
  const go = readFileSync(resolve(__dirname, '../../../internal/blog/blog.go'), 'utf8')
  const body = /var Icons = \[\]string\{([^}]*)\}/.exec(go)?.[1]
  expect(body, 'var Icons found in blog.go').toBeDefined()
  return [...body!.matchAll(/"([^"]+)"/g)].map((m) => m[1])
}

describe('GLYPHS', () => {
  it('icons_everyServerCategoryIconHasAGlyph', () => {
    const icons = serverIcons()
    expect(icons.length, 'control: icons parsed').toBeGreaterThanOrEqual(1)
    for (const name of icons) {
      expect(glyphs[name]?.length, name).toBeGreaterThan(0)
      for (const d of glyphs[name]) expect(d.trim().length, `${name} path`).toBeGreaterThan(0)
    }
  })
})
