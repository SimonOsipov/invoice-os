// M4-22-06 (task-165, Test Spec #4): the demo suite's DB access is gone (see
// demo/no-db-access.test.ts), so the workspace-level pg dependency it existed for
// should be gone too. RED right now: e2e/package.json still lists pg + @types/pg in
// devDependencies -- goes GREEN once the executor drops both in Stage 3 (verified
// safe: pnpm-lock.yaml's importers show no other workspace package imports pg).
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const PACKAGE_JSON = join(dirname(fileURLToPath(import.meta.url)), 'package.json')

describe('e2e package declares no pg dependency', () => {
  const pkg = JSON.parse(readFileSync(PACKAGE_JSON, 'utf8'))

  it('has no pg or @types/pg in devDependencies', () => {
    const offenders = ['pg', '@types/pg'].filter((dep) => dep in (pkg.devDependencies ?? {}))
    expect(offenders, `still present in devDependencies: ${offenders.join(', ')}`).toEqual([])
  })
})
