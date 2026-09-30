import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { type GateInput, mergeGateVerdict } from './mergeGate'

const input = (o: Partial<GateInput>): GateInput => ({
  changesResult: 'success',
  relevant: 'true',
  draft: 'false',
  e2eResult: 'success',
  ciResult: 'success',
  ciConclusion: 'success',
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

  it('passes a ready relevant PR whose CI and E2E succeeded', () => {
    const v = mergeGateVerdict(input({}))
    expect(v.pass).toBe(true)
  })

  it('fails a green E2E when CI did not succeed', () => {
    const variants = ['failure', 'cancelled', 'skipped', '', 'SUCCESS', ' success']
    expect(variants.length).toBeGreaterThan(0)
    for (const ciResult of variants) {
      const v = mergeGateVerdict(input({ ciResult, e2eResult: 'success' }))
      expect(v.pass, `'${ciResult}'`).toBe(false)
      expect(v.reason, `'${ciResult}'`).toContain('CI did not succeed')
    }
    expect(mergeGateVerdict(input({ ciResult: 'success', e2eResult: 'success' })).pass).toBe(true)
  })

  it('names the CI conclusion and the watch job result', () => {
    expect(mergeGateVerdict(input({ ciResult: 'failure', ciConclusion: 'timed_out' })).reason).toBe(
      "CI did not succeed on this commit (CI conclusion 'timed_out', watch job 'failure')",
    )
    expect(mergeGateVerdict(input({ ciResult: 'cancelled', ciConclusion: '' })).reason).toBe(
      "CI did not succeed on this commit (CI conclusion '', watch job 'cancelled')",
    )
    // An empty watch result prints as '', not as a placeholder.
    expect(mergeGateVerdict(input({ ciResult: '', ciConclusion: 'success' })).reason).toBe(
      "CI did not succeed on this commit (CI conclusion 'success', watch job '')",
    )
    expect(mergeGateVerdict(input({ ciResult: '', ciConclusion: '' })).reason).toBe(
      "CI did not succeed on this commit (CI conclusion '', watch job '')",
    )
    // Values are printed verbatim, quotes and whitespace included.
    expect(mergeGateVerdict(input({ ciResult: 'failure', ciConclusion: `it's "x" ` })).reason).toBe(
      `CI did not succeed on this commit (CI conclusion 'it's "x" ', watch job 'failure')`,
    )
  })

  it('rule order: a red CI is named before a skipped E2E', () => {
    const v = mergeGateVerdict(input({ ciResult: 'failure', ciConclusion: 'failure', e2eResult: 'skipped' }))
    expect(v.pass).toBe(false)
    expect(v.reason).toContain('CI did not succeed')
    expect(v.reason).not.toContain('E2E concluded')
    // A red CI wins over every E2E result, a green E2E included.
    for (const e2eResult of ['success', 'skipped', 'failure', 'cancelled', '', 'neutral']) {
      const r = mergeGateVerdict(input({ ciResult: 'failure', e2eResult }))
      expect(r.pass, e2eResult).toBe(false)
      expect(r.reason, e2eResult).toContain('CI did not succeed')
      expect(r.reason, e2eResult).not.toContain('E2E concluded')
    }
    // Green CI keeps the E2E reason.
    expect(mergeGateVerdict(input({ e2eResult: 'skipped' })).reason).toContain("E2E concluded 'skipped'")
  })

  it('CI verdict does not touch not-relevant, draft or failed-changes PRs', () => {
    const CI = ['success', 'failure', 'skipped', '']
    expect(CI.length).toBeGreaterThan(0)
    for (const ciResult of CI) {
      const notRelevant = mergeGateVerdict(input({ relevant: 'false', e2eResult: 'skipped', ciResult }))
      expect(notRelevant.pass, `relevant=false/'${ciResult}'`).toBe(true)
      expect(notRelevant.reason, ciResult).toContain('no E2E-relevant path')

      const draft = mergeGateVerdict(input({ draft: 'true', ciResult }))
      expect(draft.pass, `draft/'${ciResult}'`).toBe(false)
      expect(draft.reason, ciResult).toContain('mark the PR ready')

      const changes = mergeGateVerdict(input({ changesResult: 'failure', relevant: '', ciResult }))
      expect(changes.pass, `changes/'${ciResult}'`).toBe(false)
      expect(changes.reason, ciResult).toContain('path detection did not succeed')

      for (const relevant of ['', 'False']) {
        const noVerdict = mergeGateVerdict(input({ relevant, ciResult }))
        expect(noVerdict.pass, `relevant='${relevant}'/'${ciResult}'`).toBe(false)
        expect(noVerdict.reason, `relevant='${relevant}'/'${ciResult}'`).toContain('no verdict')
      }
    }
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

  it('decides on ciResult alone; ciConclusion is only printed', () => {
    for (const ciConclusion of ['failure', 'cancelled', '', 'SUCCESS']) {
      expect(mergeGateVerdict(input({ ciResult: 'success', ciConclusion })).pass, ciConclusion).toBe(true)
    }
    for (const ciResult of ['failure', '']) {
      expect(mergeGateVerdict(input({ ciResult, ciConclusion: 'success' })).pass, ciResult).toBe(false)
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
    const CI = ['success', 'failure', 'skipped', '', 'SUCCESS']
    let passes = 0
    let n = 0
    for (const changesResult of CH)
      for (const relevant of REL)
        for (const draft of DRAFT)
          for (const e2eResult of E2E)
            for (const ciResult of CI) {
              n++
              const want =
                changesResult === 'success' &&
                (relevant === 'false' ||
                  (relevant === 'true' && draft !== 'true' && ciResult === 'success' && e2eResult === 'success'))
              const v = mergeGateVerdict({ changesResult, relevant, draft, e2eResult, ciResult, ciConclusion: ciResult })
              expect(v.pass, JSON.stringify({ changesResult, relevant, draft, e2eResult, ciResult })).toBe(want)
              expect(v.reason.length).toBeGreaterThan(0)
              if (v.pass) passes++
            }
    expect(n).toBe(CH.length * REL.length * DRAFT.length * E2E.length * CI.length)
    expect(n).toBe(1680)
    // 140 relevant=false rows (4 drafts x 7 E2E x 5 CI) + 3 ready-draft values with green CI and E2E.
    expect(passes).toBe(143)
  })
})

describe('mergeGate CLI', () => {
  const gate = resolve(import.meta.dirname, 'mergeGate.ts')
  const VARS = ['CHANGES_RESULT', 'E2E_RELEVANT', 'PR_DRAFT', 'E2E_RESULT', 'CI_RESULT', 'CI_CONCLUSION']

  const cli = (vars: Record<string, string>) => {
    // Strip the gate vars from the inherited env so a CI runner's values cannot leak in.
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

  it('script exits 0 on a ready relevant PR with green CI and E2E', () => {
    const { code, out } = cli({
      CHANGES_RESULT: 'success',
      E2E_RELEVANT: 'true',
      PR_DRAFT: 'false',
      E2E_RESULT: 'success',
      CI_RESULT: 'success',
    })
    expect(code).toBe(0)
    expect(out).toContain('E2E concluded success')
  })

  it('script exits 1 on a relevant draft PR with a green E2E', () => {
    const { code, out } = cli({ CHANGES_RESULT: 'success', E2E_RELEVANT: 'true', PR_DRAFT: 'true', E2E_RESULT: 'success' })
    expect(code).toBe(1)
    expect(out).toContain('::error::draft PRs run no E2E')
  })

  it('script exits 1 on a skipped E2E and names it', () => {
    const { code, out } = cli({
      CHANGES_RESULT: 'success',
      E2E_RELEVANT: 'true',
      PR_DRAFT: 'false',
      E2E_RESULT: 'skipped',
      CI_RESULT: 'success',
    })
    expect(code).toBe(1)
    expect(out).toContain("::error::E2E concluded 'skipped'")
  })

  it('script exits 1 when CI did not succeed', () => {
    const { code, out } = cli({
      CHANGES_RESULT: 'success',
      E2E_RELEVANT: 'true',
      PR_DRAFT: 'false',
      E2E_RESULT: 'success',
      CI_RESULT: 'failure',
      CI_CONCLUSION: 'timed_out',
    })
    expect(code).toBe(1)
    expect(out).toContain('::error::CI did not succeed')
    expect(out).toContain("CI conclusion 'timed_out'")
    expect(out).toContain("watch job 'failure'")
  })

  it('script fails every non-exact CI_RESULT with a green E2E', () => {
    const ready = { CHANGES_RESULT: 'success', E2E_RELEVANT: 'true', PR_DRAFT: 'false', E2E_RESULT: 'success' }
    const variants = ['SUCCESS', 'Success', ' success', 'success ', 'success\n', 'cancelled', 'skipped', 'failure', '']
    for (const CI_RESULT of variants) {
      const { code, out } = cli({ ...ready, CI_RESULT, CI_CONCLUSION: 'success' })
      expect(code, JSON.stringify(CI_RESULT)).toBe(1)
      expect(out, JSON.stringify(CI_RESULT)).toContain('::error::CI did not succeed')
    }
    expect(cli({ ...ready, CI_RESULT: 'success', CI_CONCLUSION: 'success' }).code).toBe(0)
  })

  it('script prints an unset CI_CONCLUSION as empty quotes and reads CI_CONCLUSION alone as no CI', () => {
    const ready = { CHANGES_RESULT: 'success', E2E_RELEVANT: 'true', PR_DRAFT: 'false', E2E_RESULT: 'success' }
    const failed = cli({ ...ready, CI_RESULT: 'failure' })
    expect(failed.code).toBe(1)
    expect(failed.out).toContain("CI conclusion '', watch job 'failure'")
    const onlyConclusion = cli({ ...ready, CI_CONCLUSION: 'success' })
    expect(onlyConclusion.code).toBe(1)
    expect(onlyConclusion.out).toContain("CI conclusion 'success', watch job ''")
  })

  it('script ignores CI_RESULT for a not-relevant PR and keeps the draft reason', () => {
    const notRelevant = cli({ CHANGES_RESULT: 'success', E2E_RELEVANT: 'false', CI_RESULT: 'failure' })
    expect(notRelevant.code).toBe(0)
    expect(notRelevant.out).not.toContain('CI did not succeed')
    const draft = cli({ CHANGES_RESULT: 'success', E2E_RELEVANT: 'true', PR_DRAFT: 'true', CI_RESULT: 'failure' })
    expect(draft.code).toBe(1)
    expect(draft.out).toContain('::error::draft PRs run no E2E')
  })

  it('script treats unset CI vars as a failed CI on the deploy path', () => {
    const { code, out } = cli({ CHANGES_RESULT: 'success', E2E_RELEVANT: 'true', PR_DRAFT: 'false', E2E_RESULT: 'success' })
    expect(code).toBe(1)
    expect(out).toContain('CI did not succeed')
  })
})
