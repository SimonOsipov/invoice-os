// Files one BUG story per E2E retry-pass key from a PR's flaky-results artifacts.
// Usage: node e2e/fileFlakyBugs.ts <pr-number> <vault BUG folder> [--dry-run]
// Node >= 22.18 runs this file without flags: erasable TypeScript, runtime imports from node: only.

import { spawnSync } from 'node:child_process'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { FlakyRow } from './flakyReporter'

export type Deps = { gh(args: string[]): string }

export type Row = FlakyRow & { url: string; date: string; suite: string }

export type Story = { path: string; name: string; number?: number; key?: string; text: string }

export type Plan = {
  kind: 'write' | 'update' | 'unchanged'
  label: string
  path: string
  text: string
  rows: string[]
}

const ARTIFACT = /^flaky-results-(.+)-(\d+)$/
const TABLE_HEAD = [
  '| Run | Date | PR | Suite | Line | Attempts | First-attempt error |',
  '|---|---|---|---|---|---|---|',
]

function ghDefault(args: string[]): string {
  const r = spawnSync('gh', args, { encoding: 'utf8', maxBuffer: 256 * 1024 * 1024 })
  if (r.error) throw r.error
  if (r.status !== 0) throw new Error(`gh ${args.join(' ')}: ${r.stderr.trim()}`)
  return r.stdout
}

const jsonLines = <T>(out: string): T[] =>
  out
    .split('\n')
    .filter((l) => l.trim() !== '')
    .map((l) => JSON.parse(l) as T)

export function storyKey(row: Pick<FlakyRow, 'repoFile' | 'titlePath'>): string {
  return [row.repoFile, ...row.titlePath].join(' › ')
}

/** readStories reads every .md in dir; a missing dir reads as empty. */
export function readStories(dir: string): Story[] {
  if (!existsSync(dir)) return []
  return readdirSync(dir)
    .filter((n) => n.endsWith('.md'))
    .sort()
    .map((name) => {
      const text = readFileSync(join(dir, name), 'utf8')
      const front = /^---\n([\s\S]*?)\n---/.exec(text)?.[1] ?? ''
      const raw = /^flaky_key: (".*")\s*$/m.exec(front)?.[1]
      const num = /^BUG-(\d+)\b/.exec(name)?.[1]
      return {
        path: join(dir, name),
        name,
        number: num === undefined ? undefined : Number(num),
        key: raw === undefined ? undefined : (JSON.parse(raw) as string),
        text,
      }
    })
}

// ceiling: no lock on numbering; two concurrent runs can take the same number, revisit if two cleanups run at once
export function nextNumber(stories: readonly Story[]): number {
  return 1 + Math.max(0, ...stories.map((s) => s.number ?? 0))
}

const cell = (s: string) => s.replace(/\|/g, '\\|')

function rowLine(r: Row, pr: number): string {
  const run = `[${r.url.split('/runs/')[1]!.replace('/attempts/', ' attempt ')}](${r.url})`
  return `| ${[run, r.date, `#${pr}`, r.suite, String(r.line), String(r.attempts), cell(r.error)].join(' | ')} |`
}

const byDate = (a: Row, b: Row) => (a.date === b.date ? (a.url < b.url ? -1 : 1) : a.date < b.date ? -1 : 1)

/** appendRows inserts rows after the last Evidence table row, else adds the section at the end. */
function appendRows(text: string, rows: string[]): string {
  const lines = text.split('\n')
  const head = lines.findIndex((l) => /^##\s+Evidence\s*$/.test(l))
  if (head >= 0) {
    let last = -1
    for (let i = head + 1; i < lines.length && !/^#{1,2}\s/.test(lines[i]!); i++) {
      if (lines[i]!.startsWith('|')) last = i
    }
    if (last >= 0) {
      lines.splice(last + 1, 0, ...rows)
      return lines.join('\n')
    }
  }
  const body = text.endsWith('\n') ? text : `${text}\n`
  return `${body}\n## Evidence\n\n${[...TABLE_HEAD, ...rows].join('\n')}\n`
}

const fileSafe = (s: string) => s.replace(/[/\\:*?"<>|#^[\]]/g, '').slice(0, 80).trim()

function newStory(num: number, rows: Row[], archived: Story | undefined, pr: number): { name: string; text: string } {
  const first = rows[0]!
  const last = first.titlePath[first.titlePath.length - 1]!
  const spec = first.titlePath.join(' › ')
  const link = archived ? `\n\nEarlier story: [[${archived.name.replace(/\.md$/, '')}]]` : ''
  const text = `---
status: Planned
depends: []
base: main
layer: e2e
size: S
flaky_key: ${JSON.stringify(storyKey(first))}
---

# BUG-${num} ${last} passes only on retry

## Objective

The E2E spec \`${spec}\` (\`${first.repoFile}:${first.line}\`) failed its first attempt and passed on retry in the Dev Env runs below. The flakyGate did not fail it, because the PR did not change the spec file.${link}

## Evidence

${[...TABLE_HEAD, ...rows.map((r) => rowLine(r, pr))].join('\n')}
`
  return { name: `BUG-${num} ${fileSafe(last)} passes only on retry.md`, text }
}

export function planWrites(rows: readonly Row[], bug: readonly Story[], archive: readonly Story[], pr: number, bugDir: string): Plan[] {
  const groups = new Map<string, Row[]>()
  for (const r of rows) {
    const k = storyKey(r)
    const g = groups.get(k) ?? []
    if (!g.some((x) => x.url === r.url)) g.push(r)
    groups.set(k, g)
  }
  let next = nextNumber([...bug, ...archive])
  const highest = (list: readonly Story[], key: string) =>
    list.filter((s) => s.key === key).sort((a, b) => (b.number ?? 0) - (a.number ?? 0))[0]
  return [...groups.keys()].sort().map((key): Plan => {
    const group = groups.get(key)!.sort(byDate)
    const open = highest(bug, key)
    if (open) {
      const fresh = group.filter((r) => !open.text.includes(`${r.url})`))
      const label = open.number === undefined ? open.name : `BUG-${open.number}`
      if (fresh.length === 0) return { kind: 'unchanged', label, path: open.path, text: open.text, rows: [] }
      const lines = fresh.map((r) => rowLine(r, pr))
      return { kind: 'update', label, path: open.path, text: appendRows(open.text, lines), rows: lines }
    }
    const made = newStory(next, group, highest(archive, key), pr)
    return { kind: 'write', label: `BUG-${next++}`, path: join(bugDir, made.name), text: made.text, rows: [] }
  })
}

type ArtifactInfo = { name: string; created_at: string; expired: boolean }

function readRows(gh: Deps['gh'], pr: number, tmp: string): { rows: Row[]; runs: number } {
  const { headRefName, createdAt } = JSON.parse(gh(['pr', 'view', String(pr), '--json', 'headRefName,createdAt'])) as {
    headRefName: string
    createdAt: string
  }
  const runs = jsonLines<{ id: number; created_at: string; html_url: string }>(
    gh([
      'api',
      '--paginate',
      `repos/{owner}/{repo}/actions/workflows/dev-env.yml/runs?branch=${encodeURIComponent(headRefName)}&event=pull_request&per_page=100`,
      '--jq',
      '.workflow_runs[] | {id, created_at, html_url}',
    ]),
  ).filter((r) => r.created_at >= createdAt)
  const rows: Row[] = []
  for (const run of runs) {
    const artifacts = jsonLines<ArtifactInfo>(
      gh([
        'api',
        '--paginate',
        `repos/{owner}/{repo}/actions/runs/${run.id}/artifacts?per_page=100`,
        '--jq',
        '.artifacts[] | {name, created_at, expired}',
      ]),
    )
    for (const art of artifacts) {
      const m = ARTIFACT.exec(art.name)
      if (!m || art.expired) continue
      const dir = join(tmp, String(run.id), art.name)
      gh(['run', 'download', String(run.id), '-n', art.name, '-D', dir])
      for (const file of readdirSync(dir).filter((n) => n.endsWith('.json')).sort()) {
        let parsed: FlakyRow[]
        try {
          parsed = JSON.parse(readFileSync(join(dir, file), 'utf8')) as FlakyRow[]
        } catch (e) {
          throw new Error(`malformed JSON in artifact ${art.name}, file ${file}: ${(e as Error).message}`)
        }
        const suite = m[1] === 'e2e' ? file.replace(/\.json$/, '') : m[1]!.replace(/^topology-/, 'topology/')
        for (const f of parsed) {
          rows.push({ ...f, url: `${run.html_url}/attempts/${m[2]}`, date: art.created_at.slice(0, 10), suite })
        }
      }
    }
  }
  return { rows, runs: runs.length }
}

export function main(argv: string[], deps: Deps = { gh: ghDefault }): number {
  const dry = argv.includes('--dry-run')
  const [prArg, bugDir] = argv.filter((a) => a !== '--dry-run')
  if (!prArg || !/^\d+$/.test(prArg) || !bugDir) {
    console.error('usage: node e2e/fileFlakyBugs.ts <pr-number> <vault BUG folder> [--dry-run]')
    return 2
  }
  const pr = Number(prArg)
  if (!existsSync(bugDir) || !statSync(bugDir).isDirectory()) {
    console.error(`BUG folder not found: ${bugDir}`)
    return 1
  }
  const tmp = mkdtempSync(join(tmpdir(), 'flaky-file-'))
  try {
    const { rows, runs } = readRows(deps.gh, pr, tmp)
    if (rows.length === 0) {
      console.log(`no retry-pass in the Dev Env runs of PR #${pr} (${runs} runs read); wrote nothing`)
      return 0
    }
    const archiveDir = join(dirname(bugDir), 'Archive', basename(bugDir))
    const plans = planWrites(rows, readStories(bugDir), readStories(archiveDir), pr, bugDir)
    for (const p of plans) {
      const would = dry ? 'would ' : ''
      if (p.kind === 'unchanged') {
        console.log(`unchanged ${p.label} ${p.path} (runs already listed)`)
      } else if (p.kind === 'update') {
        console.log(`${would}${dry ? 'update' : 'updated'} ${p.label} ${p.path} (+${p.rows.length} runs)`)
        if (dry) console.log(p.rows.join('\n'))
      } else {
        console.log(`${would}${dry ? 'write' : 'wrote'} ${p.label} ${p.path}`)
        if (dry) console.log(p.text)
      }
      if (!dry && p.kind !== 'unchanged') {
        mkdirSync(dirname(p.path), { recursive: true })
        writeFileSync(p.path, p.text)
      }
    }
    return 0
  } catch (e) {
    console.error((e as Error).message)
    return 1
  } finally {
    rmSync(tmp, { recursive: true, force: true })
  }
}

// realpath: a symlinked argv[1] (macOS /var -> /private/var) must still match import.meta.url.
if (process.argv[1] && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exit(main(process.argv.slice(2)))
}
