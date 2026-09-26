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

describe('mergeGateVerdict adversarial', () => {
  const E2E = ['success', 'skipped', 'failure', 'cancelled', '', 'neutral', 'SUCCESS']
  const DRAFT = ['true', 'false', '', 'True']

  it('passes relevant=false for every E2E result and draft state', () => {
    expect(E2E.length * DRAFT.length).toBeGreaterThan(0)
    for (const e2eResult of E2E) {
      for (const draft of DRAFT) {
        const v = mergeGateVerdict(input({ relevant: 'false', draft, e2eResult }))
        expect(v.pass, `${draft}/${e2eResult}`).toBe(true)
      }
    }
  })

  it('fails case and whitespace variants of relevant even with a green E2E', () => {
    const variants = ['False', 'FALSE', ' false', 'false ', 'True', 'TRUE', ' true', 'yes', '0', 'null']
    expect(variants.length).toBeGreaterThan(0)
    for (const relevant of variants) {
      const v = mergeGateVerdict(input({ relevant, e2eResult: 'success' }))
      expect(v.pass, relevant).toBe(false)
      expect(v.reason, relevant).toContain('no verdict')
    }
  })

  it('fails case and whitespace variants of an E2E success', () => {
    const variants = ['SUCCESS', 'Success', ' success', 'success ', 'succeeded']
    expect(variants.length).toBeGreaterThan(0)
    for (const e2eResult of variants) {
      const v = mergeGateVerdict(input({ e2eResult }))
      expect(v.pass, e2eResult).toBe(false)
      expect(v.reason, e2eResult).toContain(`'${e2eResult}'`)
    }
  })

  it('fails every non-success changesResult even when every other input would pass', () => {
    const variants = ['failure', 'cancelled', 'skipped', '', 'SUCCESS', ' success']
    expect(variants.length).toBeGreaterThan(0)
    for (const changesResult of variants) {
      for (const relevant of ['true', 'false']) {
        const v = mergeGateVerdict(input({ changesResult, relevant, e2eResult: 'success' }))
        expect(v.pass, `${changesResult}/${relevant}`).toBe(false)
        expect(v.reason, changesResult).toContain('path detection did not succeed')
        expect(v.reason, changesResult).toContain(changesResult || 'no result')
      }
    }
  })

  it('reads only the exact draft string true as a draft', () => {
    // Fail-closed: a non-'true' draft value is treated as ready, so it can pass only on a green E2E.
    for (const draft of ['True', 'TRUE', ' true', '', 'yes']) {
      expect(mergeGateVerdict(input({ draft, e2eResult: 'success' })).pass, draft).toBe(true)
      expect(mergeGateVerdict(input({ draft, e2eResult: 'skipped' })).pass, draft).toBe(false)
    }
  })

  it('relevant draft fails before the E2E result is read', () => {
    for (const e2eResult of E2E) {
      const v = mergeGateVerdict(input({ draft: 'true', e2eResult }))
      expect(v.pass, e2eResult).toBe(false)
      expect(v.reason, e2eResult).toContain('mark the PR ready')
    }
  })

  it('passes exactly the spec truth table over every input combination', () => {
    const CH = ['success', 'failure', '']
    const REL = ['true', 'false', '', 'False']
    let passes = 0
    let n = 0
    for (const changesResult of CH)
      for (const relevant of REL)
        for (const draft of DRAFT)
          for (const e2eResult of E2E) {
            n++
            const want =
              changesResult === 'success' &&
              (relevant === 'false' || (relevant === 'true' && draft !== 'true' && e2eResult === 'success'))
            const v = mergeGateVerdict({ changesResult, relevant, draft, e2eResult })
            expect(v.pass, JSON.stringify({ changesResult, relevant, draft, e2eResult })).toBe(want)
            expect(v.reason.length).toBeGreaterThan(0)
            if (v.pass) passes++
          }
    expect(n).toBe(CH.length * REL.length * DRAFT.length * E2E.length)
    // 28 relevant=false rows + 3 ready-draft values with green E2E.
    expect(passes).toBe(31)
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
  it('script treats an empty var like an unset one', () => {
    const { code, out } = cli({ CHANGES_RESULT: '', E2E_RELEVANT: '', PR_DRAFT: '', E2E_RESULT: '' })
    expect(code).toBe(1)
    expect(out).toContain('::error::path detection did not succeed (no result)')
  })

  it('script exits 0 on a ready relevant PR with a green E2E', () => {
    const { code, out } = cli({ CHANGES_RESULT: 'success', E2E_RELEVANT: 'true', PR_DRAFT: 'false', E2E_RESULT: 'success' })
    expect(code).toBe(0)
    expect(out).toContain('E2E concluded success')
  })

  it('script exits 1 on a relevant draft PR with a green E2E', () => {
    const { code, out } = cli({ CHANGES_RESULT: 'success', E2E_RELEVANT: 'true', PR_DRAFT: 'true', E2E_RESULT: 'success' })
    expect(code).toBe(1)
    expect(out).toContain('::error::draft PRs run no E2E')
  })

  it('script exits 1 on a skipped E2E and names it', () => {
    const { code, out } = cli({ CHANGES_RESULT: 'success', E2E_RELEVANT: 'true', PR_DRAFT: 'false', E2E_RESULT: 'skipped' })
    expect(code).toBe(1)
    expect(out).toContain("::error::E2E concluded 'skipped'")
  })
})
