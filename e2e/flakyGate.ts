// Fails the deploy gate when a spec file this PR changed passed only on retry.
// Usage: node e2e/flakyGate.ts <FLAKY_JSON_DIR> <file listing the PR's changed paths>
// Node >= 22.18 runs this file without flags, so keep it to erasable TypeScript.

import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { FlakySpec } from './flakyReporter'

/** Repo-relative, '/'-separated form of a reporter path, matching the PR file list. */
export function repoRelative(file: string, repoRoot: string): string {
  return relative(repoRoot, file).split(sep).join('/')
}

/** flakyInChangedFiles keeps the flaky specs whose file is in changedFiles (repo-relative). */
export function flakyInChangedFiles(
  flaky: readonly FlakySpec[],
  changedFiles: readonly string[],
  repoRoot: string,
): FlakySpec[] {
  const changed = new Set(changedFiles.map((f) => f.trim()).filter(Boolean))
  return flaky.filter((f) => changed.has(repoRelative(f.file, repoRoot)))
}

/** readFlakyDir merges every suite's JSON. A missing dir means no suite reported. */
export function readFlakyDir(dir: string): FlakySpec[] {
  if (!existsSync(dir)) return []
  return readdirSync(dir)
    .filter((name) => name.endsWith('.json'))
    .sort()
    .flatMap((name) => JSON.parse(readFileSync(join(dir, name), 'utf8')) as FlakySpec[])
}

function main([dir, changedList]: string[]): number {
  if (!dir || !changedList) {
    console.error('usage: node e2e/flakyGate.ts <flaky-json-dir> <changed-files-list>')
    return 2
  }
  const repoRoot = fileURLToPath(new URL('..', import.meta.url))
  const flaky = readFlakyDir(dir)
  const hits = flakyInChangedFiles(flaky, readFileSync(changedList, 'utf8').split('\n'), repoRoot)
  if (hits.length === 0) {
    console.log(`${flaky.length} flaky spec(s); none is in a file this PR changed.`)
    return 0
  }
  for (const h of hits) {
    const file = repoRelative(h.file, repoRoot)
    console.log(
      `::error file=${file},line=${h.line}::"${h.title}" passed only on retry (${h.attempts} attempts), and this PR changed ${file}. Fix the spec before merging.`,
    )
  }
  return 1
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  process.exit(main(process.argv.slice(2)))
}
