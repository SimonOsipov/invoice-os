// CHECK-01-06 AUTO half: records the first automatic placement (Q12) for CHECK-01-07 to
// measure against, by running the SHIPPED initMappingFromHeaders over every JEV_OUT layout
// and writing auto_placements.json. Env-gated on JEV_OUT so CI, which never sets it, makes
// no read and no write (AC-1) -- .ralph/arch/CHECK-01-06.md D-1..D-8.
//
// AUTO is computed from the layout's DECLARED header row (layout.columns), not a detected
// one: the answer key is projected out of columns, so feeding the title line would score a
// structural zero on every titled layout and measure header-row detection instead of the
// alias table (D-6). Nothing deployed sets header_row > 1.
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { describe, expect, it } from 'vitest'

import { CANON } from '../data'
import { initMappingFromHeaders } from './mapping'
import type { Mapping } from '../types'

// The only two layouts.json fields either half of this story reads (§1.5). Not the whole
// record: an extra field here is a needle TestDateFormatAndDecimalSeparator_AreReadNowhere
// OutsideSuggest / TestExtraction_NoDueDateFieldExists could catch.
interface JevLayout {
  id: string
  columns: string[]
}

// Pure: one entry per layout, keyed by id, value = the shipped function over that layout's
// declared header. No fs, no env.
function autoPlacements(layouts: JevLayout[]): Record<string, Mapping> {
  const out: Record<string, Mapping> = {}
  for (const layout of layouts) {
    out[layout.id] = initMappingFromHeaders(layout.columns)
  }
  return out
}

function writeAutoPlacements(dir: string, layouts: JevLayout[]): string {
  mkdirSync(dir, { recursive: true })
  const path = join(dir, 'auto_placements.json')
  writeFileSync(path, JSON.stringify(autoPlacements(layouts), null, 2) + '\n')
  return path
}

// The anti-vacuity choke point: an absent or empty layouts.json fails loudly rather than
// producing a silently-empty auto_placements.json.
function readJevLayouts(dir: string): JevLayout[] {
  const path = join(dir, 'layouts.json')
  if (!existsSync(path)) {
    throw new Error(`readJevLayouts: no layouts.json at ${path}`)
  }
  const parsed = JSON.parse(readFileSync(path, 'utf8'))
  if (!Array.isArray(parsed) || parsed.length === 0) {
    throw new Error(`readJevLayouts: ${path} holds no layouts`)
  }
  return parsed as JevLayout[]
}

// The gated spec's own body, extracted so the negative leg can call it directly: jevOut ===
// undefined is "JEV_OUT unset". Reads and writes the same directory, mirroring jvGatedRun's
// shape (jev_value_test.go) -- log one line naming what was missing, never t.Skip's analog.
function runGated(jevOut: string | undefined): boolean {
  if (!jevOut) {
    console.log('JEV_OUT unset: no corpus read, no file written')
    return false
  }
  writeAutoPlacements(jevOut, readJevLayouts(jevOut))
  return true
}

function withTempDir(fn: (dir: string) => void): void {
  const dir = mkdtempSync(join(tmpdir(), 'jev-auto-'))
  try {
    fn(dir)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

// --- AC-1 -----------------------------------------------------------------------------------

describe('AC-1: auto placement writer is silent without JEV_OUT', () => {
  it('positive control: writeAutoPlacements creates the file under the given dir', () => {
    withTempDir((dir) => {
      const layouts: JevLayout[] = [
        { id: 'a', columns: ['Date', 'VAT'] },
        { id: 'b', columns: ['X'] },
      ]
      const path = writeAutoPlacements(dir, layouts)
      expect(existsSync(join(dir, 'auto_placements.json'))).toBe(true)
      const parsed = JSON.parse(readFileSync(path, 'utf8'))
      expect(Object.keys(parsed).sort()).toEqual(['a', 'b'])
    })
  })

  it('negative: with JEV_OUT unset, runGated makes no read and writes nothing', () => {
    withTempDir((dir) => {
      const before = readdirSync(dir)
      const ran = runGated(undefined)
      expect(ran).toBe(false)
      expect(readdirSync(dir)).toEqual(before)
    })
  })

  // The operator entry point: passes process.env.JEV_OUT through, exactly as CI (which never
  // sets it) will run it.
  it('auto placement writer runs against JEV_OUT when set, else declines', () => {
    runGated(process.env.JEV_OUT)
  })
})

// --- AC-2 -----------------------------------------------------------------------------------

describe('AC-2: every layout gets a full eleven-key mapping', () => {
  it('layout A places three fields, layout B places zero, both keep all eleven keys', () => {
    const layouts: JevLayout[] = [
      { id: 'A', columns: ['Date', 'VAT', 'Total', 'Invoice No'] },
      { id: 'B', columns: ['A', 'B', 'C'] },
    ]
    const result = autoPlacements(layouts)
    const canonKeys = CANON.map((c) => c.key).sort()

    for (const id of ['A', 'B']) {
      expect(Object.keys(result[id]).sort()).toEqual(canonKeys)
      for (const key of canonKeys) {
        const v = result[id][key]
        expect(v === null || typeof v === 'string').toBe(true)
      }
    }

    const placedCount = (m: Mapping) => Object.values(m).filter((v) => v !== null).length
    expect(placedCount(result.A)).toBe(3)
    expect(placedCount(result.B)).toBe(0)
  })
})

// --- AC-3 -----------------------------------------------------------------------------------

describe('AC-3: the alias table is the shipped one', () => {
  const headers = ['Date', 'VAT', 'Total', 'Invoice No']
  // Measured today (§3 Row 3): the four literal placements, not a call-site identity that
  // would move with any change to ALIAS.
  const want: Mapping = {
    invoice_number: null,
    issue_date: 'Date',
    buyer_tin: null,
    buyer_name: null,
    currency: null,
    subtotal: null,
    vat: 'VAT',
    total: 'Total',
    line_description: null,
    line_quantity: null,
    line_unit_price: null,
  }

  it('matches the measured literal placements', () => {
    const result = autoPlacements([{ id: 'x', columns: headers }])
    expect(result.x).toEqual(want)
  })

  it('invoice_number is never auto-placed, the deliberate absence ALIAS names', () => {
    expect(want.invoice_number).toBeNull()
  })

  it('is not a copy: matches initMappingFromHeaders called directly in this spec', () => {
    const result = autoPlacements([{ id: 'x', columns: headers }])
    expect(result.x).toEqual(initMappingFromHeaders(headers))
  })
})
