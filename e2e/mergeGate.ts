// Fails the required "E2E gate" check unless the PR needs no E2E, or CI, E2E and every topology shard succeeded.
// Usage: node e2e/mergeGate.ts  (reads CHANGES_RESULT, E2E_RELEVANT, PR_AUTHOR, COMMITS_ONLY_DEPENDABOT, PR_DRAFT, E2E_RESULT, CI_RESULT, CI_CONCLUSION, TOPOLOGY_RESULT)
// Node >= 22.18 runs this file without flags, so keep it to erasable TypeScript.

import { fileURLToPath } from 'node:url'

export type GateInput = {
  author: string
  commitsOnlyDependabot: string
  changesResult: string
  relevant: string
  draft: string
  e2eResult: string
  topologyResult: string
  ciResult: string
  ciConclusion: string
}

/** mergeGateVerdict applies the rules in order and fails closed on any unrecognised input. */
export function mergeGateVerdict(i: GateInput): { pass: boolean; reason: string } {
  if (i.changesResult !== 'success') {
    return { pass: false, reason: `path detection did not succeed (${i.changesResult || 'no result'})` }
  }
  // A Dependabot PR with a human commit gets a preview, so it needs E2E; only the exact 'true' skips.
  if (i.author === 'dependabot[bot]' && i.commitsOnlyDependabot === 'true') {
    return { pass: true, reason: 'Dependabot PR: the preview and E2E are skipped by design' }
  }
  if (i.relevant === 'false') return { pass: true, reason: 'no E2E-relevant path changed' }
  if (i.relevant !== 'true') {
    return { pass: false, reason: `path detection produced no verdict (e2e output '${i.relevant}')` }
  }
  if (i.draft === 'true') return { pass: false, reason: 'draft PRs run no E2E; mark the PR ready' }
  if (i.ciResult !== 'success') {
    return {
      pass: false,
      reason: `CI did not succeed on this commit (CI conclusion '${i.ciConclusion}', watch job '${i.ciResult}')`,
    }
  }
  // A skipped E2E on a relevant PR means the deploy never reached it.
  if (i.e2eResult !== 'success') return { pass: false, reason: `E2E concluded '${i.e2eResult}'` }
  // The matrix job's result is 'success' only when every shard succeeded.
  if (i.topologyResult !== 'success') {
    return { pass: false, reason: `topology shards concluded '${i.topologyResult}'` }
  }
  return { pass: true, reason: 'E2E concluded success; every topology shard succeeded' }
}

function main(env: NodeJS.ProcessEnv): number {
  const v = mergeGateVerdict({
    author: env.PR_AUTHOR ?? '',
    commitsOnlyDependabot: env.COMMITS_ONLY_DEPENDABOT ?? '',
    changesResult: env.CHANGES_RESULT ?? '',
    relevant: env.E2E_RELEVANT ?? '',
    draft: env.PR_DRAFT ?? '',
    e2eResult: env.E2E_RESULT ?? '',
    topologyResult: env.TOPOLOGY_RESULT ?? '',
    ciResult: env.CI_RESULT ?? '',
    ciConclusion: env.CI_CONCLUSION ?? '',
  })
  console.log(v.pass ? v.reason : `::error::${v.reason}`)
  return v.pass ? 0 : 1
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  process.exit(main(process.env))
}
