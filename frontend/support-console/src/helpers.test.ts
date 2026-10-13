// Specs for the pure presentation helpers: token NAMES only, never literals.
import { describe, expect, it } from 'vitest'

import { severityStyle } from './helpers'
import type { Severity } from './types'

const ALL_SEVERITIES: Severity[] = ['error', 'warn', 'info']

describe('severityStyle', () => {
  it('HLP-5: covers every severity and emits only token references', () => {
    for (const s of ALL_SEVERITIES) {
      const st = severityStyle(s)
      expect(st.label).toBe(s.toUpperCase())
      for (const v of [st.bg, st.border, st.text]) {
        expect(v, `severity ${s}`).toMatch(/^var\(--[a-z0-9-]+\)$/)
      }
    }
  })
})
