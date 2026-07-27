// Specs for the mock dataset and the one piece of filtering logic that lives beside it.
// These are consistency guards, not fixtures-of-fixtures: the screens derive counts and
// badges from this data, so an internally inconsistent seed shows up as a UI bug that no
// component test would catch.
import { describe, expect, it } from 'vitest'

import { AUDIT_ENTRIES, CRUMB_BY_SCREEN, JOB_FILTERS, NAV_ITEMS, RECON_ROWS, RULE_PARAMS, SEED_JOBS, SEED_RULES, TENANTS, healthCards } from './data'
import { filterAudit } from './components/Audit'
import { statusStyle } from './helpers'
import type { Screen } from './types'

describe('navigation', () => {
  it('DAT-1: every nav item has a crumb and vice versa', () => {
    const navKeys = NAV_ITEMS.map((n) => n.key).sort()
    const crumbKeys = (Object.keys(CRUMB_BY_SCREEN) as Screen[]).sort()
    expect(navKeys).toEqual(crumbKeys)
  })

  it('DAT-2: nav keys are unique', () => {
    expect(new Set(NAV_ITEMS.map((n) => n.key)).size).toBe(NAV_ITEMS.length)
  })
})

describe('jobs', () => {
  it('DAT-3: job ids are unique', () => {
    expect(new Set(SEED_JOBS.map((j) => j.id)).size).toBe(SEED_JOBS.length)
  })

  // The filter chips enumerate states; a seeded state with no chip would be unreachable.
  it('DAT-4: every seeded job state has a filter chip', () => {
    for (const j of SEED_JOBS) {
      expect(JOB_FILTERS, `job ${j.id}`).toContain(j.state)
    }
  })

  it('DAT-5: every job state renders a badge', () => {
    for (const j of SEED_JOBS) {
      expect(statusStyle(j.state), `job ${j.id}`).toBeDefined()
    }
  })

  // The dead-letter callout is the screen's headline; it must have something to describe.
  it('DAT-6: the seed contains dead-letter jobs, and they are the ones at max attempts', () => {
    const dl = SEED_JOBS.filter((j) => j.state === 'dead-letter')
    expect(dl.length).toBeGreaterThan(0)
    for (const j of dl) {
      expect(j.attempts, `job ${j.id}`).toBe(5)
      expect(j.lastError, `job ${j.id}`).not.toBe('—')
    }
  })

  // A job that has never been retried cannot carry an error, and vice versa.
  it('DAT-7: a clean job has zero-or-one attempts and no error', () => {
    for (const j of SEED_JOBS.filter((j) => j.lastError === '—')) {
      expect(j.attempts, `job ${j.id}`).toBeLessThanOrEqual(1)
    }
  })
})

describe('reconciliation', () => {
  // The whole point of a mismatch row is that the two states DISAGREE.
  it('DAT-8: every recon row has genuinely divergent states', () => {
    expect(RECON_ROWS.length).toBeGreaterThan(0)
    for (const r of RECON_ROWS) {
      expect(r.internal, `row ${r.id}`).not.toBe(r.app)
    }
  })
})

describe('rules', () => {
  it('DAT-9: rule keys are unique', () => {
    expect(new Set(SEED_RULES.map((r) => r.key)).size).toBe(SEED_RULES.length)
  })

  // The drawer falls back to a placeholder param when a type is unmapped; that fallback
  // should never be what a seeded rule actually hits.
  it('DAT-10: every seeded rule type has parameters defined', () => {
    for (const r of SEED_RULES) {
      expect(RULE_PARAMS[r.type], `rule ${r.key} (type ${r.type})`).toBeDefined()
    }
  })

  // The kill-switch flow only has meaning if something is live to kill.
  it('DAT-11: the seed has both enabled and disabled rules', () => {
    expect(SEED_RULES.some((r) => r.enabled)).toBe(true)
    expect(SEED_RULES.some((r) => !r.enabled)).toBe(true)
  })
})

describe('audit', () => {
  it('DAT-12: entry ids are unique', () => {
    expect(new Set(AUDIT_ENTRIES.map((a) => a.id)).size).toBe(AUDIT_ENTRIES.length)
  })

  // Human-vs-system attribution is the screen's argument, so both must be represented.
  it('DAT-13: the log mixes system and human actors', () => {
    expect(AUDIT_ENTRIES.some((a) => a.actor === 'system')).toBe(true)
    expect(AUDIT_ENTRIES.some((a) => a.actor !== 'system')).toBe(true)
  })

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

describe('tenants', () => {
  it('DAT-20: tenant ids are unique and every tenant has members and KPIs', () => {
    expect(new Set(TENANTS.map((t) => t.id)).size).toBe(TENANTS.length)
    for (const t of TENANTS) {
      expect(t.members.length, `tenant ${t.id}`).toBeGreaterThan(0)
      expect(t.kpis.length, `tenant ${t.id}`).toBe(4)
      expect(t.recent.length, `tenant ${t.id}`).toBeGreaterThan(0)
    }
  })

  // Exactly one admin per tenant keeps the membership list legible; more importantly, a
  // tenant with none would misrepresent how memberships actually work.
  it('DAT-21: every tenant has an admin', () => {
    for (const t of TENANTS) {
      expect(t.members.some((m) => m.role === 'admin'), `tenant ${t.id}`).toBe(true)
    }
  })

  it('DAT-22: the status dot has all three states represented', () => {
    expect(new Set(TENANTS.map((t) => t.status))).toEqual(new Set(['ok', 'warn', 'red']))
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

  it('DAT-24: every card has at least two points so its sparkline can be drawn', () => {
    for (const c of healthCards(2)) {
      expect(c.points.length, `card ${c.label}`).toBeGreaterThanOrEqual(2)
    }
  })
})
