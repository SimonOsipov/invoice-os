// Proves .claude/hooks/guard-git-history.py denies what it claims to deny AND
// allows everything the RALPH lifecycle depends on. A PreToolUse hook that
// over-blocks gets switched off, at which point it guards nothing — so the ALLOW
// cases below matter at least as much as the DENY ones.
//
// The commands marked "historical" are copied from this project's own session
// transcripts, so the guard is measured against what actually happened rather
// than against what a rule imagined.
//
// The worktree cases run against a THROWAWAY repository built in beforeAll, not
// against the checkout the suite happens to be sitting in. CI clones this repo
// flat — no linked worktree — so a test anchored on the ambient layout reports
// "allowed" for every worktree case and passes as a false green locally, where a
// worktree does exist. That is how the first version of this file shipped red.
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, realpathSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO = dirname(dirname(fileURLToPath(import.meta.url)))
const HOOK = join(REPO, '.claude/hooks/guard-git-history.py')

let fixture = ''
let MAIN = ''
let WORKTREE = ''
const WORKTREE_ARG = '/tmp/example-worktree'

function git(cwd: string, ...args: string[]) {
  const res = spawnSync('git', ['-C', cwd, ...args], { encoding: 'utf8' })
  if (res.status !== 0) throw new Error(`git ${args.join(' ')} failed: ${res.stderr}`)
  return res.stdout.trim()
}

beforeAll(() => {
  // realpath: on macOS the temp dir is a symlink (/var -> /private/var) and git
  // reports the resolved path, so an unresolved cwd would never compare equal.
  fixture = realpathSync(mkdtempSync(join(tmpdir(), 'guard-git-history-')))
  MAIN = join(fixture, 'main')
  WORKTREE = join(fixture, 'wt')
  spawnSync('git', ['init', '-q', MAIN], { encoding: 'utf8' })
  git(MAIN, 'config', 'user.email', 'test@example.invalid')
  git(MAIN, 'config', 'user.name', 'Test')
  git(MAIN, 'commit', '-q', '--allow-empty', '-m', 'init')
  git(MAIN, 'worktree', 'add', '-q', '--detach', WORKTREE)
})

afterAll(() => {
  if (fixture) rmSync(fixture, { recursive: true, force: true })
})

function runHook(command: string, cwd: string) {
  const payload = JSON.stringify({
    hook_event_name: 'PreToolUse',
    tool_name: 'Bash',
    cwd,
    tool_input: { command },
  })
  const res = spawnSync('python3', [HOOK], { input: payload, encoding: 'utf8' })
  return { code: res.status, stderr: res.stderr }
}

describe('guard-git-history hook', () => {
  // A hook file that no settings block runs is the same defect workspaceCoverage
  // guards against on the pnpm side: it exists, it passes its tests, and it never
  // executes.
  //
  // The path must resolve from the SESSION's repository, not from a fixed root.
  // `${CLAUDE_PROJECT_DIR}` is the directory the session started in, which for a
  // session that entered a worktree later is still the main checkout — measured
  // 2026-08-13, when that spelling pointed at a copy of this file that did not
  // exist.
  const hookCommand = (): string => {
    const settings = JSON.parse(readFileSync(join(REPO, '.claude/settings.json'), 'utf8'))
    const commands = (settings.hooks?.PreToolUse ?? [])
      .filter((entry: { matcher?: string }) => /Bash/.test(entry.matcher ?? ''))
      .flatMap((entry: { hooks?: Array<{ command?: string }> }) => entry.hooks ?? [])
      .map((h: { command?: string }) => h.command ?? '')
      .filter((c: string) => c.includes('guard-git-history.py'))
    expect(commands, 'no PreToolUse(Bash) hook runs guard-git-history.py').toHaveLength(1)
    return commands[0]
  }

  it('is registered as a PreToolUse(Bash) hook, resolved from the session repo', () => {
    expect(hookCommand(), 'the hook path must resolve from the session repository').toContain(
      'git rev-parse --show-toplevel',
    )
  })

  // The lockout this pins actually happened, 2026-08-13, immediately after PR #155
  // merged. The session's repo root moved to a checkout that had not pulled the
  // hook file yet; python3 exits 2 on a missing file, 2 is the ONE code that BLOCKS
  // a PreToolUse hook, and so EVERY Bash call in the session was denied — including
  // the `git pull` that would have fixed it. Any checkout predating the file does
  // this: an old worktree, a `git checkout` of an earlier commit, a fresh clone.
  //
  // So the command must fail OPEN on a missing file. Deletion is caught instead by
  // the test below, at merge time, where it is safe to be strict.
  it('does not deny every Bash call when the hook file is absent', () => {
    const bare = realpathSync(mkdtempSync(join(tmpdir(), 'guard-no-hook-')))
    spawnSync('git', ['init', '-q', bare])
    try {
      const res = spawnSync('sh', ['-c', hookCommand()], {
        cwd: bare,
        input: '{"tool_name":"Bash","tool_input":{"command":"echo hi"}}',
        encoding: 'utf8',
      })
      expect(
        res.status,
        `exit 2 here locks the session out of Bash entirely. stderr: ${res.stderr}`,
      ).not.toBe(2)
      expect(res.status, 'a repo with no hook file must be allowed through').toBe(0)
    } finally {
      rmSync(bare, { recursive: true, force: true })
    }
  })

  // The runtime fails open, so THIS is what keeps the guard from silently vanishing.
  it('the hook file it points at is present in the repo', () => {
    expect(existsSync(HOOK), `${HOOK} is missing — the guard would silently do nothing`).toBe(true)
  })

  // Every other case here invokes the python directly. This one goes through the
  // exact string settings.json registers, so a shell-quoting slip that makes the
  // command always exit 0 cannot pass as "fails open, as designed".
  it('still denies through the registered command when the file IS present', () => {
    const res = spawnSync('sh', ['-c', hookCommand()], {
      cwd: REPO,
      input: JSON.stringify({
        tool_name: 'Bash',
        cwd: REPO,
        tool_input: { command: 'git push --force origin main' },
      }),
      encoding: 'utf8',
    })
    expect(res.status, 'the registered command let a force push through').toBe(2)
    expect(res.stderr).toMatch(/^Blocked:/)
  })

  // Floor. If `git worktree add` ever stops producing a LINKED worktree, the hook
  // sees cwd == main, exempts itself by design, and every worktree case below
  // passes as "allowed" — a green suite proving nothing.
  it('built a real linked worktree to test against', () => {
    const common = git(WORKTREE, 'rev-parse', '--path-format=absolute', '--git-common-dir')
    expect(common, 'the fixture worktree does not share MAIN.git').toBe(join(MAIN, '.git'))
    expect(WORKTREE).not.toBe(MAIN)
  })

  describe('denies', () => {
    it('a plain force push', () => expect(runHook('git push --force origin feature/x', WORKTREE).code).toBe(2))
    it('the short flag', () => expect(runHook('git push -f', WORKTREE).code).toBe(2))
    it('force-with-lease', () => expect(runHook('git push --force-with-lease origin HEAD', WORKTREE).code).toBe(2))
    it('the refspec form', () => expect(runHook('git push origin +main:main', WORKTREE).code).toBe(2))
    it('a force push after a cd', () => expect(runHook('cd /tmp && git push --force', WORKTREE).code).toBe(2))

    it('historical M4-10: a hard reset of main from a worktree session', () => {
      const cmd = `MAIN=${MAIN}\necho "### before: $(git -C "$MAIN" rev-parse HEAD)"\ngit -C "$MAIN" reset --hard b9f014284a40f29b2295a3b8`
      const { code, stderr } = runHook(cmd, WORKTREE)
      expect(code, stderr).toBe(2)
      expect(stderr).toContain('git reset')
    })

    it('a merge into main from a worktree session', () =>
      expect(runHook(`git -C ${MAIN} merge origin/main`, WORKTREE).code).toBe(2))
    it('a checkout in main from a worktree session', () =>
      expect(runHook(`cd ${MAIN} && git checkout main`, WORKTREE).code).toBe(2))
    it('a pull in main reached through a nested variable', () =>
      expect(runHook(`ROOT=${MAIN}\ngit -C "$ROOT" pull --ff-only`, WORKTREE).code).toBe(2))
  })

  describe('allows', () => {
    // Both arguments are lazy: MAIN and WORKTREE do not exist until beforeAll runs.
    const allowed = (label: string, command: () => string, cwd: () => string) =>
      it(label, () => {
        const { code, stderr } = runHook(command(), cwd())
        expect(code, `denied something it should allow:\n${command()}\n${stderr}`).toBe(0)
      })

    allowed('an ordinary push', () => 'git push origin feature/x', () => WORKTREE)
    allowed('a commit in the worktree', () => 'git add -A && git commit -m "fix: thing"', () => WORKTREE)
    allowed('an amend in the worktree', () => 'git commit --amend -m "fix: thing"', () => WORKTREE)
    allowed('a hard reset inside the worktree itself', () => 'git reset --hard HEAD~1', () => WORKTREE)
    allowed('a checkout inside the worktree itself', () => 'git checkout -- .', () => WORKTREE)
    allowed('RALPH Phase 0 fetch on main', () => `git -C ${MAIN} fetch origin main`, () => WORKTREE)
    allowed('RALPH Phase 0 worktree add', () => `git -C ${MAIN} worktree add -b feature/x ${WORKTREE_ARG}`, () => WORKTREE)
    allowed('post-merge worktree remove', () => `git -C ${MAIN} worktree remove ${WORKTREE_ARG}`, () => WORKTREE)
    allowed('post-merge branch delete', () => `git -C ${MAIN} branch -d feature/x`, () => WORKTREE)
    allowed('reading main', () => `git -C ${MAIN} rev-parse HEAD`, () => WORKTREE)
    // The user's own session lives in the main checkout; the guard stays out of it.
    allowed('a checkout in main from a main-checkout session', () => 'git checkout main', () => MAIN)
    allowed('a hard reset in main from a main-checkout session', () => 'git reset --hard origin/main', () => MAIN)
    // Must not match its own documentation or a search for the banned strings.
    allowed(
      'grepping for the banned strings',
      () => 'grep -rn "force-push|--force|git rebase|reset --hard" RALPH_PROMPT.md',
      () => WORKTREE,
    )
    allowed('no git at all', () => 'pnpm -r test', () => WORKTREE)
  })
})
