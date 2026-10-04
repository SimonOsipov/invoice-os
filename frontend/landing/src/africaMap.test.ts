import { createHash } from 'node:crypto'
import { describe, expect, it, vi } from 'vitest'
import { AFRICA_MAP } from './africaMap'

// Digest of the prototype's africa-map.js data object, re-serialised by JSON.stringify.
const V_DIGEST = '01b7099ddb37e807c31c07fc36a740226ea572b926ffcde0edba5aeda635878d'

describe('africaMap (node)', () => {
  it("MP-01 the vendored data is the prototype's", () => {
    expect(AFRICA_MAP.w).toBe(600)
    expect(AFRICA_MAP.h).toBe(673)
    expect(AFRICA_MAP.markers).toEqual({ NG: [228.9, 264.5], KE: [476.6, 343.8], ZA: [364.3, 610.1] })
    const { countries } = AFRICA_MAP
    expect(countries, '51 countries').toHaveLength(51)
    expect(new Set(countries.map((c) => c.id)).size, 'unique ids').toBe(51)
    const nameOf = (id: string) => countries.filter((c) => c.id === id).map((c) => c.name)
    expect(nameOf('NG')).toEqual(['Nigeria'])
    expect(nameOf('KE')).toEqual(['Kenya'])
    expect(nameOf('ZA')).toEqual(['South Africa'])
    expect(
      countries.filter((c) => !/^M/.test(c.d) || !/Z$/.test(c.d)).map((c) => c.id),
      'ids whose d does not start with M and end with Z',
    ).toEqual([])
    expect(createHash('sha256').update(JSON.stringify(AFRICA_MAP)).digest('hex')).toBe(V_DIGEST)
  })

  it('MP-02 importing the map sets no global', async () => {
    vi.resetModules()
    const mod = await import('./africaMap')
    // Positive guard: the module under test loaded real data, so a skeleton cannot pass.
    expect(mod.AFRICA_MAP.countries.length, 'the imported map holds countries').toBeGreaterThan(0)
    expect((globalThis as { window?: unknown }).window).toBeUndefined()
    expect('AFRICA_MAP' in globalThis).toBe(false)
  })
})
