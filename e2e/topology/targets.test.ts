import { basename } from 'node:path'
import { beforeAll, describe, expect, it } from 'vitest'

type Shard = { id: string; name: string; kind: string; subject: string }
let shardTenants: (specFile: string) => { a: Shard; b: Shard }
let shardPersonas: (specFile: string) => { SHARD: { a: Shard; b: Shard }; PERSONAS: { A: Shard & { tenantId: string }; B: Shard & { tenantId: string } } }

beforeAll(async () => {
  process.env.GATEWAY_URL = 'https://gateway.test'
  process.env.APP_URL = 'https://app.test'
  ;({ shardTenants } = (await import('./targets')) as unknown as { shardTenants: typeof shardTenants })
  ;({ shardPersonas } = (await import('./importWizardShared')) as unknown as { shardPersonas: typeof shardPersonas })
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

describe('shardPersonas', () => {
  it("pairs each persona with its own shard tenant, resolved from a spec file's absolute path", () => {
    const { SHARD, PERSONAS } = shardPersonas(basename('/runner/e2e/topology/import-wizard.spec.ts'))

    expect(PERSONAS.A.tenantId).toBe('11111111-1111-1111-1111-00000000e2e1')
    expect(PERSONAS.B.tenantId).toBe('22222222-2222-2222-2222-00000000e2e1')
    expect(PERSONAS.A.tenantId).toBe(SHARD.a.id)
    expect(PERSONAS.B.tenantId).toBe(SHARD.b.id)
    expect([PERSONAS.A.kind, PERSONAS.B.kind]).toEqual(['firm', 'in_house'])
  })

  it('throws for a spec outside every dedicated shard instead of defaulting to a tenant', () => {
    expect(() => shardPersonas('nope.spec.ts')).toThrow('not in a dedicated shard')
    expect(() => shardPersonas('roles.spec.ts')).toThrow('not in a dedicated shard')
  })
})
