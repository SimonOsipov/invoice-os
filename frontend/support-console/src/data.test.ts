// Specs for the audit filter and the health-card derivation, run over the mock dataset.
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

import * as data from './data'
import { AUDIT_ENTRIES, healthCards } from './data'
import { filterAudit } from './components/Audit'

describe('audit', () => {
  it('DAT-14: an unfiltered view returns everything', () => {
    expect(filterAudit(AUDIT_ENTRIES, '', 'all')).toHaveLength(AUDIT_ENTRIES.length)
  })

  it('DAT-15: the chip filter matches the object type exactly', () => {
    const rules = filterAudit(AUDIT_ENTRIES, '', 'rule')
    expect(rules.length).toBeGreaterThan(0)
    for (const a of rules) {
      expect(a.objectType).toBe('rule')
    }
  })

  // 'state' must not also match 'submission' rows — an earlier substring implementation
  // would have, because objectType strings share letters.
  it('DAT-16: filtering by state excludes submissions', () => {
    for (const a of filterAudit(AUDIT_ENTRIES, '', 'state')) {
      expect(a.objectType).toBe('state')
    }
  })

  it('DAT-17: the free-text query spans action, object, tenant and actor', () => {
    expect(filterAudit(AUDIT_ENTRIES, 'kill-switch', 'all').length).toBeGreaterThan(0)
    expect(filterAudit(AUDIT_ENTRIES, 'Kano', 'all').length).toBeGreaterThan(0)
    expect(filterAudit(AUDIT_ENTRIES, 'Emeka', 'all').length).toBeGreaterThan(0)
    expect(filterAudit(AUDIT_ENTRIES, 'INV-2026-04417', 'all').length).toBeGreaterThan(0)
  })

  it('DAT-18: the query is case- and whitespace-insensitive, and a miss returns nothing', () => {
    expect(filterAudit(AUDIT_ENTRIES, '  EMEKA  ', 'all').length).toBeGreaterThan(0)
    expect(filterAudit(AUDIT_ENTRIES, 'no-such-thing', 'all')).toHaveLength(0)
  })

  // Query and chip are ANDed, not ORed.
  it('DAT-19: query and filter combine', () => {
    expect(filterAudit(AUDIT_ENTRIES, 'Emeka', 'submission')).toHaveLength(0)
  })
})

describe('healthCards', () => {
  // The dead-letter tile is the one live figure on the screen — it must follow the queue
  // down to zero and flip out of its ATTENTION state when it gets there.
  it('DAT-23: the dead-letter card tracks the live count', () => {
    const busy = healthCards(2).find((c) => c.label === 'Dead-letter')
    expect(busy?.value).toBe('2')
    expect(busy?.status).toBe('ATTENTION')
    expect(busy?.tone).toBe('red')

    const clear = healthCards(0).find((c) => c.label === 'Dead-letter')
    expect(clear?.value).toBe('0')
    expect(clear?.status).toBe('CLEAR')
    expect(clear?.tone).toBe('green')
  })
})

describe('Rules admin mock data', () => {
  it('no Rules admin mock data remains', () => {
    for (const name of ['RULE_SET_VERSIONS', 'LEARNED_RULES', 'RULE_PARAMS', 'SEED_RULES']) expect(Object.keys(data), name).not.toContain(name)
  })

  it('rulesFixture is imported only by tests', () => {
    const files = (dir: string): string[] =>
      readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? files(join(dir, e.name)) : /\.tsx?$/.test(e.name) ? [join(dir, e.name)] : []))
    const all = files(__dirname).filter((f) => !f.endsWith('data.test.ts'))
    const isTest = (f: string) => /\.test\.tsx?$/.test(f)
    const naming = all.filter((f) => !f.endsWith('rulesFixture.ts') && readFileSync(f, 'utf8').includes('rulesFixture'))
    expect(naming.filter((f) => !isTest(f))).toEqual([])
    expect(naming.filter(isTest).length).toBeGreaterThan(0)
  })
})
