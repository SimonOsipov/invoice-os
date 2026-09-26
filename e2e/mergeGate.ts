// Fails the required "E2E gate" check unless E2E passed or was not needed.
// Usage: node e2e/mergeGate.ts  (reads CHANGES_RESULT, E2E_RELEVANT, PR_DRAFT, E2E_RESULT)
// Node >= 22.18 runs this file without flags, so keep it to erasable TypeScript.

import { fileURLToPath } from 'node:url'

export type GateInput = { changesResult: string; relevant: string; draft: string; e2eResult: string }

/** mergeGateVerdict applies the rules in order and fails closed on any unrecognised input. */
export function mergeGateVerdict(i: GateInput): { pass: boolean; reason: string } {
  if (i.changesResult !== 'success') {
    return { pass: false, reason: `path detection did not succeed (${i.changesResult || 'no result'})` }
  }
  if (i.relevant === 'false') return { pass: true, reason: 'no E2E-relevant path changed' }
  if (i.relevant !== 'true') {
    return { pass: false, reason: `path detection produced no verdict (e2e output '${i.relevant}')` }
  }
  if (i.draft === 'true') return { pass: false, reason: 'draft PRs run no E2E; mark the PR ready' }
  if (i.e2eResult === 'success') return { pass: true, reason: 'E2E concluded success' }
  // A skipped E2E on a relevant PR means the deploy never reached it.
  return { pass: false, reason: `E2E concluded '${i.e2eResult}'` }
}

function main(env: NodeJS.ProcessEnv): number {
  const v = mergeGateVerdict({
    changesResult: env.CHANGES_RESULT ?? '',
    relevant: env.E2E_RELEVANT ?? '',
    draft: env.PR_DRAFT ?? '',
    e2eResult: env.E2E_RESULT ?? '',
  })
  console.log(v.pass ? v.reason : `::error::${v.reason}`)
  return v.pass ? 0 : 1
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  process.exit(main(process.env))
}
