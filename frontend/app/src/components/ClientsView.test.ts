// Plain node test (no jsdom) for healthPillStyle's label casing -- separate from the
// jsdom ClientsView.test.tsx filter suite. Precedent: WorkflowParts.tsx/.test.ts.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import type { EntityHealth } from '../lib/dashboard'
import { healthPillStyle } from './ClientsView'

// Keyed by EntityHealth['kind'] via a mapped type, not a hand-typed array: widening
// EntityHealth with a new variant makes this object literal fail to typecheck until a
// fixture is added for it -- so a 4th variant can't go untested by omission.
type Fixtures = { [K in EntityHealth['kind']]: Extract<EntityHealth, { kind: K }> }
const FIXTURES: Fixtures = {
  'no-invoices': { kind: 'no-invoices' },
  'needs-attention': { kind: 'needs-attention', count: 4 },
  clear: { kind: 'clear' },
}

describe('healthPillStyle', () => {
  it.each(Object.entries(FIXTURES))('%s label is upper-case', (_kind, h) => {
    const label = healthPillStyle(h).label
    expect(label).toBe(label.toUpperCase())
  })

  it('the singular 1 NEEDS ATTENTION count also stays upper-case', () => {
    expect(healthPillStyle({ kind: 'needs-attention', count: 1 }).label).toBe('1 NEEDS ATTENTION')
  })

  it('the no-invoices pill reads NO INVOICES YET', () => {
    expect(healthPillStyle({ kind: 'no-invoices' }).label).toBe('NO INVOICES YET')
  })
})

describe('Sidebar.tsx scope fence', () => {
  it('keeps its own lower-case health labels untouched', () => {
    const src = readFileSync(fileURLToPath(new URL('./Sidebar.tsx', import.meta.url)), 'utf8')
    expect(src).toContain("'no invoices yet'")
    expect(src).toContain("'all clear'")
    expect(src).toContain('needing attention')
  })
})
