// Plain node test (no jsdom) for healthPillStyle's label casing -- separate from the
// jsdom ClientsView.test.tsx filter suite. Precedent: WorkflowParts.tsx/.test.ts.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import type { EntityHealth } from '../lib/dashboard'
import * as ClientsViewModule from './ClientsView'

// healthPillStyle isn't exported yet (BUG-01-11) -- index-signature cast keeps this a
// runtime lookup rather than a "has no exported member" compile error.
const healthPillStyle = (ClientsViewModule as unknown as Record<string, (h: EntityHealth) => { label: string }>)
  .healthPillStyle

const ALL_HEALTH: EntityHealth[] = [
  { kind: 'no-invoices' },
  { kind: 'clear' },
  { kind: 'needs-attention', count: 1 },
  { kind: 'needs-attention', count: 4 },
]

describe('healthPillStyle', () => {
  it('every health pill label is upper-case', () => {
    for (const h of ALL_HEALTH) {
      const label = healthPillStyle(h).label
      expect(label).toBe(label.toUpperCase())
    }
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
