// Proves .claude/hooks/guard-git-history.py denies what it claims to deny AND
// allows everything the RALPH lifecycle depends on. A PreToolUse hook that
// over-blocks gets switched off, at which point it guards nothing — so the ALLOW
// cases below matter at least as much as the DENY ones.
//
// The commands marked "historical" are copied from this project's own session
// transcripts, so the guard is measured against what actually happened rather
// than against what a rule imagined.
import { describe, expect, it } from 'vitest'
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO = dirname(dirname(fileURLToPath(import.meta.url)))
const HOOK = join(REPO, '.claude/hooks/guard-git-history.py')
const MAIN = spawnSync('git', ['-C', REPO, 'rev-parse', '--path-format=absolute', '--git-common-dir'], {
  encoding: 'utf8',
}).stdout.trim().replace(/\/\.git$/, '')
// The session cwd must be a real linked worktree: the guard derives the main
// checkout by asking git, and git cannot answer for a path that does not exist.
// REPO is this suite's own worktree, so it is one by construction.
const WORKTREE = REPO
const WORKTREE_ARG = join(MAIN, '.claude/worktrees/example')

function runHook(command: string, cwd: string = WORKTREE) {
  const payload = JSON.stringify({
    hook_event_name: 'PreToolUse',
    tool_name: 'Bash',
    cwd,
    tool_input: { command },
  })
  const res = spawnSync('python3', [HOOK], { input: payload, encoding: 'utf8' })
  return { code: res.status, stderr: res.stderr }
}

const DENIED: Array<[string, string]> = [
  ['plain force push', 'git push --force origin feature/x'],
  ['short flag', 'git push -f'],
  ['force-with-lease', 'git push --force-with-lease origin HEAD'],
  ['refspec form', 'git push origin +main:main'],
  ['force push after a cd', 'cd /tmp && git push --force'],
  [
    'historical M4-10: hard-reset of main from a worktree session',
    'MAIN=' + MAIN + '\necho "### before: $(git -C "$MAIN" rev-parse main)"\ngit -C "$MAIN" reset --hard b9f014284a40f29b2295a3b8',
  ],
  ['merge into main from a worktree session', 'git -C ' + MAIN + ' merge origin/main'],
  ['checkout in main from a worktree session', 'cd ' + MAIN + ' && git checkout main'],
  ['pull in main via a nested variable', 'ROOT=' + MAIN + '\ngit -C "$ROOT" pull --ff-only'],
]

const ALLOWED: Array<[string, string, string?]> = [
  ['an ordinary push', 'git push origin feature/x'],
  ['a commit in the worktree', 'git add -A && git commit -m "fix: thing"'],
  ['an amend in the worktree', 'git commit --amend -m "fix: thing"'],
  ['RALPH Phase 0 fetch on main', 'git -C ' + MAIN + ' fetch origin main'],
  ['RALPH Phase 0 worktree add', 'git -C ' + MAIN + ' worktree add -b feature/x ' + WORKTREE_ARG + ' origin/main'],
  ['post-merge worktree remove', 'git -C ' + MAIN + ' worktree remove ' + WORKTREE_ARG],
  ['post-merge branch delete', 'git -C ' + MAIN + ' branch -d feature/x'],
  ['reading main', 'git -C ' + MAIN + ' rev-parse main'],
  ['a hard reset inside the worktree itself', 'git reset --hard HEAD~1'],
  ['a checkout inside the worktree itself', 'git checkout -- .'],
  // The user's own session lives in the main checkout; the guard stays out of it.
  ['a checkout in main from a main-checkout session', 'git checkout main', MAIN],
  ['a hard reset in main from a main-checkout session', 'git reset --hard origin/main', MAIN],
  // Must not match its own documentation or a search for the banned strings.
  ['grepping for the banned strings', 'grep -rn "force-push|--force|git rebase|reset --hard" RALPH_PROMPT.md'],
  ['no git at all', 'pnpm -r test'],
]

describe('guard-git-history hook', () => {
  // A hook file that no settings block runs is the same defect workspaceCoverage
  // guards against on the pnpm side: it exists, it passes its tests, and it never
  // executes.
  //
  // The path must resolve from the SESSION's repository, not from a fixed root.
  // `${CLAUDE_PROJECT_DIR}` is the directory the session started in, which for a
  // session that entered a worktree later is still the main checkout — measured
  // 2026-08-13, when that spelling pointed at a copy of this file that did not
  // exist. Missing file is not a soft failure: python3 exits 2, the one code that
  // BLOCKS, so every Bash call in the session is denied until it is fixed.
  // `git rev-parse --show-toplevel` answers for the worktree the session is in.
  it('is actually registered as a PreToolUse(Bash) hook, resolved from the session repo', () => {
    const settings = JSON.parse(readFileSync(join(REPO, '.claude/settings.json'), 'utf8'))
    const commands = (settings.hooks?.PreToolUse ?? [])
      .filter((entry: { matcher?: string }) => /Bash/.test(entry.matcher ?? ''))
      .flatMap((entry: { hooks?: Array<{ command?: string }> }) => entry.hooks ?? [])
      .map((h: { command?: string }) => h.command ?? '')
    const mine = commands.filter((c: string) => c.includes('guard-git-history.py'))
    expect(mine, 'no PreToolUse(Bash) hook runs guard-git-history.py').toHaveLength(1)
    expect(mine[0], 'the hook path must resolve from the session repository').toContain(
      'git rev-parse --show-toplevel',
    )
  })

  it('resolved the main checkout it is guarding', () => {
    expect(MAIN, 'could not derive the main checkout from git').toMatch(/\/[^/]+$/)
    expect(MAIN.endsWith('/.git')).toBe(false)
  })

  it.each(DENIED)('denies: %s', (_label, command) => {
    const { code, stderr } = runHook(command)
    expect(code, `expected a denial, hook allowed it:\n${command}`).toBe(2)
    expect(stderr).toMatch(/^Blocked:/)
  })

  it.each(ALLOWED)('allows: %s', (_label, command, cwd) => {
    const { code, stderr } = runHook(command, cwd ?? WORKTREE)
    expect(code, `expected this to be allowed but the hook denied it:\n${command}\n${stderr}`).toBe(0)
  })

  // Floor: if the payload shape ever changes and the hook stops seeing commands,
  // every case above would pass as an "allow" and the suite would read as green.
  it('is actually reading the command out of the payload', () => {
    expect(runHook('git push --force').code, 'the hook denied nothing at all').toBe(2)
    expect(DENIED.length).toBeGreaterThanOrEqual(8)
    expect(ALLOWED.length).toBeGreaterThanOrEqual(12)
  })
})
