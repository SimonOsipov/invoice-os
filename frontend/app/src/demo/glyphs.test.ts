// The demo module builds its own icon sizes rather than adding demo-only exports to the
// shared glyph module -- see glyphs.tsx's header comment for why. This guards that split.
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const SHARED_GLYPHS = join(dirname(fileURLToPath(import.meta.url)), '../glyphs.tsx')

describe('shared glyphs.tsx', () => {
  it('gains no demo-prefixed export from this subtask', () => {
    const src = readFileSync(SHARED_GLYPHS, 'utf8')
    expect(src).not.toMatch(/export const demo\w+/)
  })
})
