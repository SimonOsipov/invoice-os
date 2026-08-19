// Structural invariants DEMO-06-03's Object.values(copy) scan depends on: every export
// is a non-empty string, never a function -- a template function would escape the scan.
import { describe, expect, it } from 'vitest'

import * as copy from './copy'

describe('demo copy shape', () => {
  const entries = Object.entries(copy)

  it('exports at least one string', () => {
    expect(entries.length).toBeGreaterThan(0)
  })

  it.each(entries)('%s is a non-empty string', (_name, value) => {
    expect(typeof value).toBe('string')
    expect((value as string).length).toBeGreaterThan(0)
  })
})
