// Fails the deploy gate when a spec file this PR changed passed only on retry.
// Usage: node e2e/flakyGate.ts <FLAKY_JSON_DIR> <file listing the PR's changed paths> [<base sha> <head sha>]
// With the two shas, a spec whose PR diff changes only comment or blank lines is exempt.
// Node >= 22.18 runs this file without flags, so keep it to erasable TypeScript.

import { spawnSync } from 'node:child_process'
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

function isCommentOrBlank(line: string): boolean {
  const t = line.trim()
  if (t === '' || t.startsWith('//')) return true
  if (!t.startsWith('/*') && !t.startsWith('*')) return false
  const end = t.indexOf('*/')
  return end < 0 || t.slice(end + 2).trim() === ''
}

/** commentOnlyDiff: every added or removed line of a `git diff -U0` is a comment or blank. */
export function commentOnlyDiff(diff: string): boolean {
  return diff
    .split('\n')
    .filter((l) => (l.startsWith('+') || l.startsWith('-')) && !l.startsWith('+++') && !l.startsWith('---'))
    .every((l) => isCommentOrBlank(l.slice(1)))
}

/** gitDiff returns `git diff -U0 base...head -- file`, or undefined when git fails. */
function gitDiff(base: string, head: string, file: string, repoRoot: string): string | undefined {
  const r = spawnSync('git', ['diff', '-U0', `${base}...${head}`, '--', file], { cwd: repoRoot, encoding: 'utf8' })
  return r.status === 0 ? r.stdout : undefined
}

/** readFlakyDir merges every suite's JSON. A missing dir means no suite reported. */
export function readFlakyDir(dir: string): FlakySpec[] {
  if (!existsSync(dir)) return []
  return readdirSync(dir)
    .filter((name) => name.endsWith('.json'))
    .sort()
    .flatMap((name) => JSON.parse(readFileSync(join(dir, name), 'utf8')) as FlakySpec[])
}

/** gateHits drops the hits whose diff is comment-only; diffOf undefined (git failed) keeps the hit. */
export function gateHits(
  hits: readonly FlakySpec[],
  repoRoot: string,
  diffOf?: (file: string) => string | undefined,
): FlakySpec[] {
  if (!diffOf) return [...hits]
  return hits.filter((h) => {
    const diff = diffOf(repoRelative(h.file, repoRoot))
    return diff === undefined || !commentOnlyDiff(diff)
  })
}

function main([dir, changedList, base, head]: string[]): number {
  if (!dir || !changedList) {
    console.error('usage: node e2e/flakyGate.ts <flaky-json-dir> <changed-files-list> [<base> <head>]')
    return 2
  }
  const repoRoot = fileURLToPath(new URL('..', import.meta.url))
  const flaky = readFlakyDir(dir)
  const changed = flakyInChangedFiles(flaky, readFileSync(changedList, 'utf8').split('\n'), repoRoot)
  const diffOf = base && head ? (f: string) => gitDiff(base, head, f, repoRoot) : undefined
  const hits = gateHits(changed, repoRoot, diffOf)
  for (const h of changed.filter((c) => !hits.includes(c))) {
    console.log(`"${h.title}" passed only on retry; ${repoRelative(h.file, repoRoot)} changed only in comments, so it is exempt.`)
  }
  if (hits.length === 0) {
    console.log(`${flaky.length} flaky spec(s); none is in a file this PR changed beyond comments.`)
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
