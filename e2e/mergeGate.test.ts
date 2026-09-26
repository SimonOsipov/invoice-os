import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { type GateInput, mergeGateVerdict } from './mergeGate'

const input = (o: Partial<GateInput>): GateInput => ({
  changesResult: 'success',
  relevant: 'true',
  draft: 'false',
  e2eResult: 'success',
  ...o,
})

describe('mergeGateVerdict', () => {
  it('passes a PR outside the path list', () => {
    const v = mergeGateVerdict(input({ relevant: 'false', e2eResult: 'skipped' }))
    expect(v.pass).toBe(true)
    expect(v.reason).toContain('no E2E-relevant path')
  })

  it('passes a draft PR outside the path list', () => {
    const v = mergeGateVerdict(input({ relevant: 'false', draft: 'true', e2eResult: 'skipped' }))
    expect(v.pass).toBe(true)
    expect(v.reason).toContain('no E2E-relevant path')
  })

  it('fails closed when the filter produced no verdict', () => {
    // 'False' too: only the exact string 'false' may pass (AC-1).
    for (const relevant of ['', 'False']) {
      const v = mergeGateVerdict(input({ relevant, e2eResult: 'skipped' }))
      expect(v.pass, relevant).toBe(false)
      expect(v.reason, relevant).toContain('no verdict')
    }
  })

  it('passes a ready relevant PR whose E2E succeeded', () => {
    const v = mergeGateVerdict(input({}))
    expect(v.pass).toBe(true)
  })

  it('fails a relevant PR whose E2E was skipped (the skip-is-success trap)', () => {
    const v = mergeGateVerdict(input({ e2eResult: 'skipped' }))
    expect(v.pass).toBe(false)
    expect(v.reason).toContain('skipped')
  })

  it('fails on failure and on cancelled', () => {
    for (const e2eResult of ['failure', 'cancelled']) {
      const v = mergeGateVerdict(input({ e2eResult }))
      expect(v.pass, e2eResult).toBe(false)
      expect(v.reason, e2eResult).toContain(e2eResult)
    }
  })

  it('fails closed on an empty or unknown E2E result', () => {
    for (const e2eResult of ['', 'neutral']) {
      const v = mergeGateVerdict(input({ e2eResult }))
      expect(v.pass, e2eResult).toBe(false)
      expect(v.reason, e2eResult).toContain('E2E concluded')
      expect(v.reason, e2eResult).toContain(e2eResult)
    }
  })

  it('fails a relevant draft even with a green E2E', () => {
    const v = mergeGateVerdict(input({ draft: 'true' }))
    expect(v.pass).toBe(false)
    expect(v.reason).toContain('ready')
  })

  it('fails when path detection did not succeed', () => {
    const v = mergeGateVerdict(input({ changesResult: 'failure', relevant: '' }))
    expect(v.pass).toBe(false)
    expect(v.reason).toContain('path detection did not succeed')
  })

  it('rule order: changes failure wins over relevant=false', () => {
    const v = mergeGateVerdict(input({ changesResult: 'cancelled', relevant: 'false', e2eResult: 'skipped' }))
    expect(v.pass).toBe(false)
    expect(v.reason).toContain('path detection did not succeed')
  })
})

describe('mergeGate CLI', () => {
  const gate = resolve(import.meta.dirname, 'mergeGate.ts')
  const VARS = ['CHANGES_RESULT', 'E2E_RELEVANT', 'PR_DRAFT', 'E2E_RESULT']

  const cli = (vars: Record<string, string>) => {
    // Strip the four vars from the inherited env so a CI runner's values cannot leak in.
    const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !VARS.includes(k)))
    try {
      return { code: 0, out: execFileSync(process.execPath, [gate], { encoding: 'utf8', env: { ...env, ...vars } }) }
    } catch (err) {
      const e = err as { status: number; stdout: string }
      return { code: e.status, out: e.stdout }
    }
  }

  it('script exits 1 with no env', () => {
    const { code, out } = cli({})
    expect(code).toBe(1)
    expect(out).toContain('::error::')
  })

  it('script exits 0 on a pass', () => {
    const { code, out } = cli({ CHANGES_RESULT: 'success', E2E_RELEVANT: 'false' })
    expect(code).toBe(0)
    expect(out).toContain('no E2E-relevant path')
    expect(out).not.toContain('::error::')
  })
})
