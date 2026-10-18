import { readdirSync, readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { partitionErrors, shardTenantError, UNITS } from './shards'

const onDisk = readdirSync(new URL('.', import.meta.url), { recursive: true })
  .map(String)
  .filter((f) => f.endsWith('.spec.ts'))

describe('topology shard map', () => {
  it('owns every spec file on disk exactly once', () => {
    expect(onDisk.length, 'the disk scan found no spec files').toBeGreaterThan(5)
    expect(partitionErrors(onDisk)).toEqual([])
  })

  it('reports a spec file in no unit and an entry whose file is gone', () => {
    const owned = UNITS.flatMap((u) => u.specs)
    expect(partitionErrors(onDisk.filter((f) => f !== owned[0]))).toEqual([`${owned[0]} is named by unit ${UNITS[0].name} but does not exist`])
    expect(partitionErrors([...onDisk, 'ghost.spec.ts'])).toEqual(['ghost.spec.ts is in no unit'])
  })

  it('serial-lane has a reason for each spec it keeps and none for a spec it lost', () => {
    const lane = UNITS.find((u) => u.name === 'serial-lane')!
    expect(Object.keys(lane.reasons ?? {}).sort()).toEqual([...lane.specs].sort())
  })

  it('partitionErrors names a spec that two units own', () => {
    const first = UNITS[0].specs[0]
    const extra = { name: 'extra', specs: [first] }
    UNITS.push(extra)
    try {
      expect(partitionErrors(onDisk)).toEqual([`${first} is in two units: ${UNITS[0].name}, extra`])
    } finally {
      UNITS.splice(UNITS.indexOf(extra), 1)
    }
  })
})

const withTenants = UNITS.flatMap((u) => (u.tenants ? [{ name: u.name, ...u.tenants }] : []))

describe('shard tenants', () => {
  it('no tenant id belongs to two units', () => {
    const ids = withTenants.flatMap((u) => [u.firm, u.inHouse])
    expect(ids.length).toBeGreaterThan(0)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('firm ids lead with 1 and in-house ids with 2', () => {
    expect(withTenants.length).toBeGreaterThan(0)
    for (const u of withTenants) {
      expect(u.firm[0], `${u.name} firm`).toBe('1')
      expect(u.inHouse[0], `${u.name} inHouse`).toBe('2')
    }
  })

  it('every unit tenant is seeded by db/seed.e2e-shards.sql', () => {
    const sql = readFileSync(new URL('../../db/seed.e2e-shards.sql', import.meta.url), 'utf8')
    const rows = new Map<string, { source: string; firm: string }>()
    for (const m of sql.matchAll(/\('([0-9a-f-]{36})',\s*'([0-9a-f-]{36})',\s*(true|false)\)/g)) {
      rows.set(m[1], { source: m[2], firm: m[3] })
    }
    expect(withTenants.length).toBeGreaterThan(0)
    for (const u of withTenants) {
      expect(rows.get(u.firm), `${u.name} firm`).toEqual({ source: '11111111-1111-1111-1111-111111111111', firm: 'true' })
      expect(rows.get(u.inHouse), `${u.name} inHouse`).toEqual({ source: '22222222-2222-2222-2222-222222222222', firm: 'false' })
    }
  })
})

describe('shardTenantError', () => {
  const ui = UNITS.find((u) => u.name === 'ui-surfaces')!.tenants!
  const wizard = UNITS.find((u) => u.name === 'import-wizard')!.tenants!

  it("allows only a dedicated shard's own tenants", () => {
    expect(shardTenantError('ui-surfaces', ui.firm)).toBeUndefined()
    expect(shardTenantError('ui-surfaces', ui.inHouse)).toBeUndefined()
    for (const id of [undefined, '11111111-1111-1111-1111-111111111111', wizard.firm]) {
      expect(shardTenantError('ui-surfaces', id)).toContain('ui-surfaces')
    }
  })

  it('ignores a project with no dedicated tenants', () => {
    for (const p of ['serial-lane', 'library', 'chromium']) {
      expect(shardTenantError(p, undefined)).toBeUndefined()
      expect(shardTenantError(p, '11111111-1111-1111-1111-111111111111')).toBeUndefined()
    }
  })
})
