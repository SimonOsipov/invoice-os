// Proves .claude/hooks/require-mutation-replay.py blocks `gh pr create` without a
// passing full replay of HEAD, and allows it otherwise. Runs in a throwaway repo.
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO = dirname(dirname(fileURLToPath(import.meta.url)))
const HOOK = join(REPO, '.claude/hooks/require-mutation-replay.py')
const PR = 'gh pr create --draft --title x --body y'
const ROWS = '{"ac":"A-1-01 x"}\n{"ac":"A-1-01 y"}\n'

let repo = ''
let head = ''

function git(...args: string[]) {
  const res = spawnSync('git', ['-C', repo, ...args], { encoding: 'utf8' })
  if (res.status !== 0) throw new Error(`git ${args.join(' ')} failed: ${res.stderr}`)
  return res.stdout.trim()
}

function runHook(command: string, cwd = repo, toolName = 'Bash') {
  const payload = JSON.stringify({ tool_name: toolName, cwd, tool_input: { command } })
  const res = spawnSync('python3', [HOOK], { input: payload, encoding: 'utf8' })
  return { code: res.status, stderr: res.stderr }
}

function writeRows(story: string, rows = ROWS) {
  writeFileSync(join(repo, '.ralph', `mutations-${story}.jsonl`), rows)
}

function writeReplay(story: string, over: Record<string, unknown> = {}, rows = ROWS) {
  const summary = {
    sha: head,
    rows: rows.split('\n').filter((l) => l.trim()).length,
    rows_sha256: createHash('sha256').update(rows).digest('hex'),
    proven: 2,
    not_proven: 0,
    invalid: 0,
    ...over,
  }
  writeFileSync(join(repo, '.ralph', `replay-${story}.json`), JSON.stringify(summary))
}

beforeEach(() => {
  repo = realpathSync(mkdtempSync(join(tmpdir(), 'mutation-replay-guard-')))
  spawnSync('git', ['init', '-q', '-b', 'feature/a-1-some-title', repo])
  git('config', 'user.email', 'test@example.invalid')
  git('config', 'user.name', 'Test')
  git('commit', '-q', '--allow-empty', '-m', 'init')
  head = git('rev-parse', 'HEAD')
  mkdirSync(join(repo, '.ralph'))
})

afterEach(() => {
  if (repo) rmSync(repo, { recursive: true, force: true })
})

describe('require-mutation-replay hook', () => {
  it('is registered as a PreToolUse(Bash) hook that fails open on a missing file', () => {
    const settings = JSON.parse(readFileSync(join(REPO, '.claude/settings.json'), 'utf8'))
    const commands: string[] = (settings.hooks?.PreToolUse ?? [])
      .filter((e: { matcher?: string }) => /Bash/.test(e.matcher ?? ''))
      .flatMap((e: { hooks?: Array<{ command?: string }> }) => e.hooks ?? [])
      .map((h: { command?: string }) => h.command ?? '')
      .filter((c: string) => c.includes('require-mutation-replay.py'))
    expect(commands).toHaveLength(1)
    expect(commands[0]).toContain('git rev-parse --show-toplevel')
    expect(commands[0]).toContain('|| exit 0')
  })

  it('allows a PR when .ralph holds no mutations file', () => {
    expect(runHook(PR).code).toBe(0)
  })

  it('allows a PR when the mutations file has no rows', () => {
    writeRows('A-1', '\n')
    expect(runHook(PR).code).toBe(0)
  })

  it('allows a PR with a passing replay of HEAD', () => {
    writeRows('A-1')
    writeReplay('A-1')
    expect(runHook(PR).code).toBe(0)
  })

  it('ignores commands that do not create a PR, and other tools', () => {
    writeRows('A-1')
    expect(runHook('gh pr view 12').code).toBe(0)
    expect(runHook('git commit -m "gh pr create later"', repo, 'Edit').code).toBe(0)
  })

  it.each([
    ['no replay file', null, /no replay proof for A-1/],
    ['a replay of another commit', { sha: 'f'.repeat(40) }, /not HEAD/],
    ['a NOT-PROVEN row', { not_proven: 1, proven: 1 }, /1 NOT-PROVEN/],
    ['an INVALID row', { invalid: 1, proven: 1 }, /1 INVALID/],
    ['a row count that misses a row', { rows: 1 }, /does not cover the current 2 rows/],
    ['rows edited since the replay', { rows_sha256: '0'.repeat(64) }, /does not cover/],
  ])('denies a PR with %s', (_name, over, reason) => {
    writeRows('A-1')
    if (over) writeReplay('A-1', over)
    const res = runHook(`cd ${repo} && ${PR}`)
    expect(res.code).toBe(2)
    expect(res.stderr).toMatch(reason)
    expect(res.stderr.trim().split('\n')).toHaveLength(1)
  })

  it('denies a PR made after a new commit on top of the replayed one', () => {
    writeRows('A-1')
    writeReplay('A-1')
    git('commit', '-q', '--allow-empty', '-m', 'later')
    expect(runHook(PR).code).toBe(2)
  })

  it('checks only the story the branch names', () => {
    writeRows('A-1')
    writeReplay('A-1')
    writeRows('B-2')
    expect(runHook(PR).code).toBe(0)
    git('checkout', '-q', '-b', 'chore/unrelated')
    expect(runHook(PR).code, 'off a story branch, every mutations file must pass').toBe(2)
  })

  it('fails open on its own error', () => {
    writeRows('A-1')
    writeFileSync(join(repo, '.ralph', 'replay-A-1.json'), '{not json')
    const res = runHook(PR)
    expect(res.code).toBe(0)
    expect(res.stderr).toContain('skipped after error')
  })
})
