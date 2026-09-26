// STUB for the red tests; TEST-04-01 replaces it with the real verdict.
import { fileURLToPath } from 'node:url'

export type GateInput = { changesResult: string; relevant: string; draft: string; e2eResult: string }

export function mergeGateVerdict(_i: GateInput): { pass: boolean; reason: string } {
  return { pass: false, reason: '' }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  process.exit(2)
}
