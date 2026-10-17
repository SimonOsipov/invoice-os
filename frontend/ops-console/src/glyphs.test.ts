import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import { GLYPHS } from './glyphs'

const NON_CATEGORY = [
  'external-link', 'plus', 'search', 'pin', 'chevron-left', 'trash-2', 'arrow-up',
  'arrow-down', 'image', 'circle-check', 'calendar-clock', 'play', 'arrow-right', 'file-text',
]

const glyphs = GLYPHS as Record<string, readonly string[]>

function serverIcons(): string[] {
  const go = readFileSync(resolve(__dirname, '../../../internal/blog/blog.go'), 'utf8')
  const body = /var Icons = \[\]string\{([^}]*)\}/.exec(go)?.[1]
  expect(body, 'var Icons found in blog.go').toBeDefined()
  return [...body!.matchAll(/"([^"]+)"/g)].map((m) => m[1])
}

describe('GLYPHS', () => {
  it('every server category icon has a glyph', () => {
    const icons = serverIcons()
    expect(icons, 'control: 16 server icons').toHaveLength(16)
    for (const name of icons) expect(glyphs[name]?.length, name).toBeGreaterThan(0)
  })

  it('the blog glyphs exist', () => {
    for (const name of NON_CATEGORY) expect(glyphs[name]?.length, name).toBeGreaterThan(0)
  })

  it('holds exactly the 30 planned glyphs, each a list of non-empty path strings', () => {
    expect(Object.keys(glyphs)).toHaveLength(serverIcons().length + NON_CATEGORY.length)
    for (const [name, paths] of Object.entries(glyphs)) {
      for (const d of paths) expect(d.trim().length, `${name} path`).toBeGreaterThan(0)
    }
  })
})
