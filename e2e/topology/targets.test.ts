import { beforeAll, describe, expect, it } from 'vitest'

type Shard = { id: string; name: string; kind: string; subject: string }
let shardTenants: (specFile: string) => { a: Shard; b: Shard }

beforeAll(async () => {
  process.env.GATEWAY_URL = 'https://gateway.test'
  process.env.APP_URL = 'https://app.test'
  ;({ shardTenants } = (await import('./targets')) as unknown as { shardTenants: typeof shardTenants })
})

describe('shardTenants', () => {
  it("returns the owning shard's tenant ids, with the source tenants' names, kinds and subjects", () => {
    const wizard = shardTenants('import-wizard.spec.ts')
    const surfaces = shardTenants('invoice-surfaces.spec.ts')

    expect(wizard.a.id).toBe('11111111-1111-1111-1111-00000000e2e1')
    expect(wizard.b.id).toBe('22222222-2222-2222-2222-00000000e2e1')
    expect(surfaces.a.id).toBe('11111111-1111-1111-1111-00000000e2e2')
    expect(surfaces.b.id).toBe('22222222-2222-2222-2222-00000000e2e2')
    expect([wizard.a.name, wizard.a.kind, wizard.a.subject]).toEqual(['Okafor & Partners', 'firm', 'c0000000-0000-0000-0000-000000000001'])
    expect([wizard.b.name, wizard.b.kind, wizard.b.subject]).toEqual(['Honeywell Group', 'in_house', 'c0000000-0000-0000-0000-000000000002'])
  })

  it('throws for a serial-lane spec and for an unknown one', () => {
    expect(() => shardTenants('roles.spec.ts')).toThrow('not in a dedicated shard')
    expect(() => shardTenants('nope.spec.ts')).toThrow('not in a dedicated shard')
  })
})
