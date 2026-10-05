import { readdirSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { partitionErrors, UNITS } from './shards'

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
})
