// RED specs (task-396, BUG-03-07 item 6, Mode A) — pin the '19-check MBS rule pack' copy
// and the untouched readinessNote fence before the executor edits data.tsx/connectors.ts.
// vitest environment is 'node' (vitest.config.ts) — no jsdom, no component rendered.
import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { CFG, CONNECTOR_DEFS, ONBOARD_STEPS } from './data'
import { connectorDetail } from './lib/connectors'

// Recursively walks `rootDir` for .ts/.tsx files containing `needle`. Duplicated from
// importFlow.test.ts's own scanForIdentifier — test-only helper in an independently
// owned spec file, not shared (that file's own stated convention).
function scanForIdentifier(rootDir: string, needle: string): string[] {
  const hits: string[] = []
  function walk(dir: string): void {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name)
      if (entry.isDirectory()) {
        walk(full)
      } else if (/\.(ts|tsx)$/.test(entry.name) && readFileSync(full, 'utf8').includes(needle)) {
        hits.push(path.relative(rootDir, full))
      }
    }
  }
  walk(rootDir)
  return hits
}

describe('rule-pack copy (BUG-03-07 item 6)', () => {
  const srcRoot = fileURLToPath(new URL('.', import.meta.url))
  const selfRelPath = 'data.test.ts'

  it('the retired 16-check literal is gone from frontend/app/src', () => {
    // Concatenated so this file's own source text never spells the retired literal —
    // a literal spelling here would self-match and could never fail. Self-excluded
    // too, same belt-and-braces idiom as importFlow.test.ts's QA-MOCK-2.
    const needle = '16-ch' + 'eck'
    const hits = scanForIdentifier(srcRoot, needle).filter((relPath) => relPath !== selfRelPath)
    expect(hits.sort()).toEqual([])
  })

  it('ONBOARD_STEPS and the validated activity-feed copy both read 19-check', () => {
    expect(ONBOARD_STEPS[2].body).toContain('19-check MBS rule pack')

    // connectorDetail's activity feed always includes 'validated' entries (fixed literal,
    // independent of the seeded PRNG) — positive companion guards this against a filter
    // that vacuously passes over zero rows.
    const validated = connectorDetail(CONNECTOR_DEFS[0]).activity.filter((e) => e.kind === 'validated')
    expect(validated.length).toBeGreaterThan(0)
    validated.forEach((e) => expect(e.desc).toBe('Passed 19-check MBS rule pack'))
  })

  // Fence pin (AC-4): these four are dead fields no component reads, and item 6 does not
  // touch them — this pins the current literal values, not the '16-check' copy above.
  // CFG[1..4] (Sahara Foods/Nigerian Delta/Adeyemi & Sons/Honeywell) are "the four";
  // CFG[0] (Lagos Freight) and CFG[5] (Kano Textile, onboarding) are not part of the set
  // item 6's AC-4 names. Verified against data.tsx directly, not re-derived: index 3
  // (Adeyemi & Sons) reads "of 16 groups", not "of 16 rule groups" like the other three —
  // an existing wording inconsistency this pin preserves rather than corrects, since
  // fixing it is outside item 6's scope.
  it('CFG.readinessNote keeps its dead entries byte-identical, untouched by item 6', () => {
    expect(CFG[1].readinessNote).toBe('11 of 16 rule groups clear — resolve the open errors to reach transmit-ready.')
    expect(CFG[2].readinessNote).toBe('8 of 16 rule groups clear. TIN and address gaps are the main blockers.')
    expect(CFG[3].readinessNote).toBe('Only 6 of 16 groups clear. Bulk-fix TINs and totals to lift the score fast.')
    expect(CFG[4].readinessNote).toBe('14 of 16 rule groups clear. Nearly transmit-ready across the board.')
  })
})
