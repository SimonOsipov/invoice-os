import { execFileSync } from 'node:child_process'
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { main, type Deps } from './fileFlakyBugs'
import type { FlakyRow } from './flakyReporter'

const REPO_URL = 'https://github.com/SimonOsipov/invoice-os/actions/runs'
const PR = 460
const KEY = 'e2e/topology/x.spec.ts › session › signs out'

const flaky = (over: Partial<FlakyRow> = {}): FlakyRow => ({
  file: '/home/runner/work/invoice-os/invoice-os/e2e/topology/x.spec.ts',
  line: 58,
  title: 'signs out',
  attempts: 2,
  repoFile: 'e2e/topology/x.spec.ts',
  titlePath: ['session', 'signs out'],
  error: 'Error: boom',
  ...over,
})

type FakeRun = {
  id: number
  created_at: string
  artifacts: { name: string; created_at: string; expired?: boolean; files: Record<string, string> }[]
}
const run = (
  id: number,
  rows: FlakyRow[] | null,
  o: { name?: string; file?: string; artAt?: string; runAt?: string } = {},
): FakeRun => ({
  id,
  created_at: o.runAt ?? '2026-10-10T00:00:00Z',
  artifacts:
    rows === null
      ? [{ name: 'smoke-playwright-report', created_at: '2026-10-12T14:00:00Z', files: { 'index.html': '<html>' } }]
      : [
          {
            name: o.name ?? 'flaky-results-e2e-1',
            created_at: o.artAt ?? '2026-10-12T14:00:00Z',
            files: { [o.file ?? 'smoke.json']: JSON.stringify(rows) },
          },
        ],
})

type World = {
  runs: FakeRun[]
  failPrView?: boolean
  failDownload?: number
  mergedAt?: string | null
  closedAt?: string | null
}

function fakeGh(w: World): Deps & { calls: string[] } {
  const calls: string[] = []
  let downloads = 0
  return {
    calls,
    gh(args) {
      calls.push(args.join(' '))
      if (args[0] === 'pr') {
        if (w.failPrView) throw new Error('gh pr view: HTTP 502 from fake')
        return JSON.stringify({
          headRefName: 'feature/x',
          createdAt: '2026-10-01T00:00:00Z',
          mergedAt: w.mergedAt ?? null,
          closedAt: w.closedAt ?? null,
        })
      }
      if (args[0] === 'api') {
        const path = args.find((a) => a.startsWith('repos/'))!
        if (path.includes('/workflows/')) {
          const list = args.includes('--paginate') ? w.runs : w.runs.slice(0, 20)
          return list
            .map((r) => JSON.stringify({ id: r.id, created_at: r.created_at, html_url: `${REPO_URL}/${r.id}` }))
            .join('\n')
        }
        const id = Number(/runs\/(\d+)\/artifacts/.exec(path)![1])
        return w.runs
          .find((r) => r.id === id)!
          .artifacts.map((a) => JSON.stringify({ name: a.name, created_at: a.created_at, expired: a.expired ?? false }))
          .join('\n')
      }
      downloads++
      if (w.failDownload === downloads) throw new Error('gh run download: boom')
      const id = Number(args[2])
      const art = w.runs.find((r) => r.id === id)!.artifacts.find((a) => a.name === args[4])!
      mkdirSync(args[6]!, { recursive: true })
      for (const [f, c] of Object.entries(art.files)) writeFileSync(join(args[6]!, f), c)
      return ''
    },
  }
}

let vault: string
let bugDir: string
let archiveDir: string
let out: string[]
let err: string[]

beforeEach(() => {
  vault = mkdtempSync(join(tmpdir(), 'file-flaky-test-'))
  bugDir = join(vault, 'BUG')
  archiveDir = join(vault, 'Archive', 'BUG')
  mkdirSync(bugDir, { recursive: true })
  out = []
  err = []
  vi.spyOn(console, 'log').mockImplementation((...a) => void out.push(a.join(' ')))
  vi.spyOn(console, 'error').mockImplementation((...a) => void err.push(a.join(' ')))
})
afterEach(() => {
  vi.restoreAllMocks()
  rmSync(vault, { recursive: true, force: true })
})

const put = (dir: string, name: string, text = '# x\n') => {
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, name), text)
}
const go = (w: World, extra: string[] = []) => {
  const g = fakeGh(w)
  const code = main([String(PR), bugDir, ...extra], g)
  return { code, g, text: out.join('\n') }
}
const snapshot = (dir: string) =>
  Object.fromEntries(
    readdirSync(dir, { recursive: true, encoding: 'utf8' })
      .sort()
      .map((p) => [p, statSync(join(dir, p)).isDirectory() ? '<dir>' : readFileSync(join(dir, p), 'utf8')]),
  )
const read = (dir: string, prefix: string) => {
  const names = readdirSync(dir).filter((n) => n.startsWith(prefix))
  expect(names.length).toBeGreaterThan(0)
  return { name: names[0]!, text: readFileSync(join(dir, names[0]!), 'utf8') }
}

const evidenceRow = (id: number, date = '2026-10-11', attempt = 1) =>
  `| [${id} attempt ${attempt}](${REPO_URL}/${id}/attempts/${attempt}) | ${date} | #${PR} | smoke | 58 | 2 | Error: boom |`
const openStory = (key: string, status: string, rows: string[], tail = '') =>
  `---\nstatus: ${status}\ndepends: []\nbase: main\nlayer: e2e\nsize: S\nflaky_key: ${JSON.stringify(key)}\n---\n\n# BUG-38 signs out passes only on retry\n\n## Objective\n\nText.\n\n## Evidence\n\n| Run | Date | PR | Suite | Line | Attempts | First-attempt error |\n|---|---|---|---|---|---|---|\n${rows.join('\n')}\n${tail}`

describe('new key', () => {
  it('writes the next free BUG number across BUG and Archive', () => {
    put(bugDir, 'BUG-37 a.md')
    put(archiveDir, 'BUG-36 b.md')
    const { code, text } = go({ runs: [run(100, [flaky()])] })
    expect(code).toBe(0)
    const { name, text: body } = read(bugDir, 'BUG-38 ')
    expect(name).toBe('BUG-38 signs out passes only on retry.md')
    const front = body.split('---')[1]!
    for (const line of ['status: Planned', 'depends: []', 'base: main', 'layer: e2e', 'size: S', `flaky_key: ${JSON.stringify(KEY)}`])
      expect(front.split('\n')).toContain(line)
    expect(body).toContain('e2e/topology/x.spec.ts:58')
    expect(body).not.toContain('flakyGate')
    expect(body).toContain(`](${REPO_URL}/100/attempts/1)`)
    expect(body).toContain('| 2026-10-12 | #460 | smoke | 58 | 2 | Error: boom |')
    expect(text).toContain('wrote BUG-38 ')
  })

  it('the highest number may sit in the archive', () => {
    put(bugDir, 'BUG-37 a.md')
    put(archiveDir, 'BUG-40 c.md')
    go({ runs: [run(100, [flaky()])] })
    expect(read(bugDir, 'BUG-41 ').name).toContain('signs out')
  })
})

describe('open key', () => {
  it('a second run appends a row and keeps the status', () => {
    const before = openStory(KEY, 'Building', [evidenceRow(100)])
    put(bugDir, 'BUG-38 signs out passes only on retry.md', before)
    const { text } = go({ runs: [run(200, [flaky()])] })
    expect(readdirSync(bugDir)).toHaveLength(1)
    const after = readFileSync(join(bugDir, 'BUG-38 signs out passes only on retry.md'), 'utf8')
    expect(after).toContain(`/100/attempts/1)`)
    expect(after).toContain(`/200/attempts/1)`)
    const added = after.split('\n').filter((l) => l.includes('/200/attempts/1'))
    expect(added).toHaveLength(1)
    expect(after.split('\n').filter((l) => !l.includes('/200/attempts/1'))).toEqual(before.split('\n'))
    expect(after).toContain('status: Building')
    expect(text).toContain('updated BUG-38 ')
    expect(text).toContain('(+1 runs)')
  })

  it('a run already listed changes nothing', () => {
    const before = openStory(KEY, 'Building', [evidenceRow(200)])
    put(bugDir, 'BUG-38 s.md', before)
    const { text } = go({ runs: [run(200, [flaky()])] })
    expect(readFileSync(join(bugDir, 'BUG-38 s.md'), 'utf8')).toBe(before)
    expect(text).toContain('unchanged BUG-38 ')
  })

  it('appends after the last Evidence row when a later section follows', () => {
    const before = openStory(KEY, 'Building', [evidenceRow(100)], '\n## Cause\n\nUnknown.\n')
    put(bugDir, 'BUG-38 s.md', before)
    go({ runs: [run(200, [flaky()])] })
    const lines = readFileSync(join(bugDir, 'BUG-38 s.md'), 'utf8').split('\n')
    const i = lines.findIndex((l) => l.includes('/100/attempts/1'))
    expect(lines[i + 1]).toContain('/200/attempts/1')
    expect(lines.slice(i + 2)).toEqual(before.split('\n').slice(i + 1))
  })

  it('two new runs append two rows in date order', () => {
    put(bugDir, 'BUG-38 s.md', openStory(KEY, 'Planned', [evidenceRow(100)]))
    const { text } = go({
      runs: [run(300, [flaky()], { artAt: '2026-10-14T01:00:00Z' }), run(200, [flaky()], { artAt: '2026-10-12T01:00:00Z' })],
    })
    const after = readFileSync(join(bugDir, 'BUG-38 s.md'), 'utf8')
    const at = (id: number) => after.indexOf(`/runs/${id}/attempts/1)`)
    expect(at(100)).toBeGreaterThan(0)
    expect(at(100)).toBeLessThan(at(200))
    expect(at(200)).toBeLessThan(at(300))
    expect(text).toContain('updated BUG-38 ')
    expect(text).toContain('(+2 runs)')
  })

  it('a story with no Evidence table gets the section at the end', () => {
    const before = `---\nstatus: Planned\nflaky_key: ${JSON.stringify(KEY)}\n---\n\n## Objective\n\nText.\n`
    put(bugDir, 'BUG-38 s.md', before)
    go({ runs: [run(200, [flaky()])] })
    const after = readFileSync(join(bugDir, 'BUG-38 s.md'), 'utf8')
    expect(after.startsWith(before)).toBe(true)
    const tail = after.slice(before.length).split('\n')
    expect(tail.filter((l) => l !== '')).toEqual([
      '## Evidence',
      '| Run | Date | PR | Suite | Line | Attempts | First-attempt error |',
      '|---|---|---|---|---|---|---|',
      expect.stringContaining('/200/attempts/1'),
    ])
  })
})

describe('Evidence section shapes', () => {
  const head = '| Run | Date | PR | Suite | Line | Attempts | First-attempt error |'
  it('an Evidence heading with no table rows gets the table inside it', () => {
    const before = `---\nstatus: Planned\nflaky_key: ${JSON.stringify(KEY)}\n---\n\n## Evidence\n\nNone yet.\n\n## Cause\n\nUnknown.\n`
    put(bugDir, 'BUG-38 s.md', before)
    go({ runs: [run(200, [flaky()])] })
    const after = readFileSync(join(bugDir, 'BUG-38 s.md'), 'utf8')
    expect(after.match(/^## Evidence\s*$/gm)).toHaveLength(1)
    const lines = after.split('\n')
    const h = lines.indexOf(head)
    expect(h).toBeGreaterThan(lines.indexOf('## Evidence'))
    expect(h).toBeLessThan(lines.indexOf('## Cause'))
    expect(lines[h + 2]).toContain('/200/attempts/1')
  })

  it('rows never land in the table of a ### subsection', () => {
    const before = openStory(KEY, 'Building', [evidenceRow(100)], `\n### Older\n\n${head}\n|---|---|---|---|---|---|---|\n${evidenceRow(1)}\n`)
    put(bugDir, 'BUG-38 s.md', before)
    go({ runs: [run(200, [flaky()])] })
    const lines = readFileSync(join(bugDir, 'BUG-38 s.md'), 'utf8').split('\n')
    expect(lines.findIndex((l) => l.includes('/200/attempts/1'))).toBe(lines.findIndex((l) => l.includes('/100/attempts/1')) + 1)
    expect(lines.findIndex((l) => l.includes('/200/attempts/1'))).toBeLessThan(lines.indexOf('### Older'))
  })
})

describe('archived key', () => {
  it('writes a new story that links the archived one', () => {
    const archived = openStory(KEY, 'Done', [evidenceRow(50)])
    put(archiveDir, 'BUG-12 x passes only on retry.md', archived)
    go({ runs: [run(200, [flaky()])] })
    expect(read(bugDir, 'BUG-13 ').text).toContain('[[BUG-12 x passes only on retry]]')
    expect(readFileSync(join(archiveDir, 'BUG-12 x passes only on retry.md'), 'utf8')).toBe(archived)
  })
})

describe('archived runs', () => {
  const archivedName = 'BUG-12 x passes only on retry.md'
  it('writes no story when every run is already in the archived story', () => {
    const archived = openStory(KEY, 'Done', [evidenceRow(200)])
    put(archiveDir, archivedName, archived)
    const before = snapshot(vault)
    const { code, text } = go({ runs: [run(200, [flaky()])] })
    expect(code).toBe(0)
    expect(snapshot(vault)).toEqual(before)
    expect(text).toContain('unchanged BUG-12 ')
    expect(text).toContain(archivedName)
  })

  it('a new story lists only the runs the archived story lacks', () => {
    put(archiveDir, archivedName, openStory(KEY, 'Done', [evidenceRow(200)]))
    go({ runs: [run(200, [flaky()]), run(300, [flaky()])] })
    const { text } = read(bugDir, 'BUG-13 ')
    expect(text).toContain('/300/attempts/1)')
    expect(text).not.toContain('/200/attempts/1)')
  })
})

describe('stories', () => {
  const withKeyLine = (line: string) => openStory(KEY, 'Building', [evidenceRow(100)]).replace(/^flaky_key: .*$/m, line)
  const appended = (name: string) => readFileSync(join(bugDir, name), 'utf8').includes('/200/attempts/1)')

  it('an unquoted flaky_key matches', () => {
    put(bugDir, 'BUG-50 s.md', withKeyLine(`flaky_key: ${KEY}`))
    go({ runs: [run(200, [flaky()])] })
    expect(readdirSync(bugDir)).toHaveLength(1)
    expect(appended('BUG-50 s.md')).toBe(true)
  })

  it('a single-quoted flaky_key matches', () => {
    put(bugDir, 'BUG-50 s.md', withKeyLine(`flaky_key: '${KEY}'`))
    go({ runs: [run(200, [flaky()])] })
    expect(readdirSync(bugDir)).toHaveLength(1)
    expect(appended('BUG-50 s.md')).toBe(true)
  })

  it('a flaky_key JSON cannot read is kept raw, never an exit 1', () => {
    put(bugDir, 'BUG-60 t.md', withKeyLine('flaky_key: "broken \\q'))
    put(bugDir, 'BUG-61 u.md', withKeyLine('flaky_key:'))
    const { code } = go({ runs: [run(200, [flaky()])] })
    expect(code).toBe(0)
    expect(appended('BUG-60 t.md')).toBe(false)
    expect(appended('BUG-61 u.md')).toBe(false)
    expect(read(bugDir, 'BUG-62 ').text).toContain('/200/attempts/1)')
  })

  it('an archived key with two archived stories links the highest number', () => {
    put(archiveDir, 'BUG-12 low.md', openStory(KEY, 'Done', [evidenceRow(50)]))
    put(archiveDir, 'BUG-20 high.md', openStory(KEY, 'Done', [evidenceRow(60)]))
    go({ runs: [run(200, [flaky()])] })
    const { text } = read(bugDir, 'BUG-21 ')
    expect(text).toContain('[[BUG-20 high]]')
    expect(text).not.toContain('[[BUG-12 low]]')
  })

  it('a story with no flaky_key is never matched or changed', () => {
    const hand = '---\nstatus: Ready\n---\n\n# BUG-30 hand written\n'
    put(bugDir, 'BUG-30 hand written.md', hand)
    go({ runs: [run(100, [flaky()])] })
    expect(read(bugDir, 'BUG-31 ').text).toContain('flaky_key')
    expect(readFileSync(join(bugDir, 'BUG-30 hand written.md'), 'utf8')).toBe(hand)
  })

  it('a key with a quote and a backslash round-trips', () => {
    const row = flaky({ titlePath: ['session', 'says "hi" \\ bye'], title: 'says "hi" \\ bye' })
    go({ runs: [run(100, [row])] })
    expect(readdirSync(bugDir)).toHaveLength(1)
    const key = `${row.repoFile} › session › says "hi" \\ bye`
    expect(read(bugDir, 'BUG-1 ').text.split('\n')).toContain(`flaky_key: ${JSON.stringify(key)}`)
    out.length = 0
    const { text } = go({ runs: [run(100, [row]), run(200, [row])] })
    expect(text).toContain('updated BUG-1 ')
    expect(readdirSync(bugDir)).toHaveLength(1)
    expect(read(bugDir, 'BUG-1 ').text).toContain('/200/attempts/1)')
  })

  it('a missing Archive folder reads as empty', () => {
    put(bugDir, 'BUG-37 a.md')
    go({ runs: [run(100, [flaky()])] })
    expect(read(bugDir, 'BUG-38 ').name).toContain('signs out')
    expect(existsSync(join(vault, 'Archive'))).toBe(false)
  })
})

describe('same last title, different title paths', () => {
  it('two stories', () => {
    const rows = [flaky({ titlePath: ['A', 'saves'] }), flaky({ titlePath: ['B', 'saves'] })]
    go({ runs: [run(100, rows)] })
    const names = readdirSync(bugDir).sort()
    expect(names).toHaveLength(2)
    expect(names[0]).toMatch(/^BUG-1 /)
    expect(names[1]).toMatch(/^BUG-2 /)
    const keys = names.map((n) => /^flaky_key: (.*)$/m.exec(readFileSync(join(bugDir, n), 'utf8'))![1])
    expect(new Set(keys).size).toBe(2)
  })
})

describe('same title path, different spec files', () => {
  it('two stories, one per spec file', () => {
    go({ runs: [run(100, [flaky(), flaky({ repoFile: 'e2e/topology/y.spec.ts' })])] })
    const names = readdirSync(bugDir).sort()
    expect(names).toHaveLength(2)
    const keys = names.map((n) => /^flaky_key: (.*)$/m.exec(readFileSync(join(bugDir, n), 'utf8'))![1])
    expect(new Set(keys).size).toBe(2)
  })
})

describe('one key in two runs', () => {
  it('a key repeated inside one run attempt is one row', () => {
    const r = run(100, [flaky()])
    r.artifacts.push({ name: 'flaky-results-topology-a-1', created_at: '2026-10-12T14:00:00Z', files: { 'topology.json': JSON.stringify([flaky()]) } })
    go({ runs: [r] })
    const rows = read(bugDir, 'BUG-1 ').text.split('\n').filter((l) => l.includes('/100/attempts/1)'))
    expect(rows).toHaveLength(1)
  })

  it('one story with two rows', () => {
    go({ runs: [run(100, [flaky()], { artAt: '2026-10-13T00:00:00Z' }), run(200, [flaky()], { artAt: '2026-10-12T00:00:00Z' })] })
    expect(readdirSync(bugDir)).toHaveLength(1)
    const { text } = read(bugDir, 'BUG-1 ')
    expect(text.indexOf('/runs/200/')).toBeGreaterThan(0)
    expect(text.indexOf('/runs/200/')).toBeLessThan(text.indexOf('/runs/100/'))
  })
})

describe('empty artifact set', () => {
  it('writes nothing and says so', () => {
    put(bugDir, 'BUG-37 a.md')
    const before = snapshot(vault)
    const { code, text } = go({ runs: [run(100, null), run(200, [])] })
    expect(code).toBe(0)
    expect(snapshot(vault)).toEqual(before)
    expect(text).toBe('no retry-pass in the preview runs of PR #460 (2 runs read); wrote nothing')
  })
})

describe('artifacts', () => {
  it('ignores report artifacts and expired flaky artifacts', () => {
    const expired = run(100, [flaky()])
    expired.artifacts[0]!.expired = true
    const { g, text } = go({ runs: [run(200, null), expired] })
    expect(g.calls.length).toBeGreaterThan(0)
    expect(g.calls.filter((c) => c.startsWith('run download'))).toEqual([])
    expect(text).toContain('no retry-pass')
  })

  it('counts expired flaky artifacts in the summary and per run', () => {
    const expired = run(100, [flaky()])
    expired.artifacts[0]!.expired = true
    const { code, text } = go({ runs: [run(200, null), expired] })
    expect(code).toBe(0)
    expect(text).toContain(`expired flaky artifacts: 1 in ${REPO_URL}/100`)
    expect(text).toContain('(2 runs read); 1 flaky artifacts expired; wrote nothing')
  })

  it('suite and attempt come from the artifact', () => {
    go({ runs: [run(100, [flaky()], { name: 'flaky-results-topology-serial-lane-2', file: 'topology.json' })] })
    const { text } = read(bugDir, 'BUG-1 ')
    expect(text).toContain('| topology/serial-lane |')
    expect(text).toContain('/100/attempts/2)')
  })
})

describe('runs', () => {
  it('reads more than 20 runs', () => {
    const runs = Array.from({ length: 25 }, (_, i) => run(1001 + i, i === 24 ? [flaky()] : null))
    go({ runs })
    expect(read(bugDir, 'BUG-1 ').text).toContain('/runs/1025/attempts/1)')
  })

  it("the date is the artifact's, not the run's", () => {
    go({ runs: [run(100, [flaky()], { name: 'flaky-results-e2e-2', runAt: '2026-10-10T03:00:00Z', artAt: '2026-10-12T14:00:00Z' })] })
    const { text } = read(bugDir, 'BUG-1 ')
    expect(text).toContain('| 2026-10-12 |')
    expect(text).not.toContain('2026-10-10')
    expect(text).toContain('/100/attempts/2)')
  })

  it('ignores a run created before the PR', () => {
    const { text } = go({ runs: [run(100, [flaky()], { runAt: '2026-09-30T23:59:59Z' })] })
    expect(readdirSync(bugDir)).toEqual([])
    expect(text).toContain('no retry-pass')
  })

  it('ignores a run created after the PR merged', () => {
    const { text } = go({ mergedAt: '2026-10-11T00:00:00Z', runs: [run(100, [flaky()], { runAt: '2026-10-12T00:00:00Z' })] })
    expect(readdirSync(bugDir)).toEqual([])
    expect(text).toContain('no retry-pass')
  })

  it('an unmerged closed PR ends at closedAt; an open PR has no upper bound', () => {
    go({ closedAt: '2026-10-11T00:00:00Z', runs: [run(100, [flaky()], { runAt: '2026-10-12T00:00:00Z' })] })
    expect(readdirSync(bugDir)).toEqual([])
    go({ runs: [run(100, [flaky()], { runAt: '2026-10-12T00:00:00Z' })] })
    expect(readdirSync(bugDir)).toHaveLength(1)
  })
})

describe('story text', () => {
  it('file name drops unsafe characters and the table escapes a pipe', () => {
    go({ runs: [run(100, [flaky({ titlePath: ['session', 'a/b: c?'], error: 'x | y' })])] })
    const { name, text } = read(bugDir, 'BUG-1 ')
    expect(name).toBe('BUG-1 ab c passes only on retry.md')
    expect(name).not.toMatch(/[/\\:*?"<>|#^[\]]/)
    const row = text.split('\n').find((l) => l.includes('/100/attempts/1'))!
    expect(row).toContain('x \\| y')
    expect(row.split(/(?<!\\)\|/)).toHaveLength(9)
  })
})

describe('dry run', () => {
  it('prints each planned write and writes nothing', () => {
    const openKey = { titlePath: ['session', 'opens'] }
    put(bugDir, 'BUG-38 opens.md', openStory('e2e/topology/x.spec.ts › session › opens', 'Building', [evidenceRow(100)]))
    put(archiveDir, 'BUG-5 old.md')
    const before = snapshot(vault)
    const { code, text } = go({ runs: [run(200, [flaky(), flaky(openKey)])] }, ['--dry-run'])
    expect(code).toBe(0)
    expect(text).toContain('would write BUG-39 ')
    expect(text).toContain('# BUG-39 signs out passes only on retry')
    expect(text).toContain('would update BUG-38 ')
    const upd = text.indexOf('would update BUG-38 ')
    const planned = text.slice(upd, text.indexOf('would write BUG-39 ')).split('\n')
    expect(planned.some((l) => l.startsWith('| [200 attempt 1]'))).toBe(true)
    expect(snapshot(vault)).toEqual(before)
  })
})

describe('errors', () => {
  it('a missing BUG folder exits 1 and creates nothing', () => {
    const missing = join(vault, 'nope')
    const code = main([String(PR), missing], fakeGh({ runs: [] }))
    expect(code).toBe(1)
    expect(err.join('\n')).toContain(missing)
    expect(existsSync(missing)).toBe(false)
  })

  it('a failing gh call exits 1 before any write', () => {
    const before = snapshot(vault)
    const { code } = go({ runs: [run(100, [flaky()]), run(200, [flaky({ titlePath: ['B'] })])], failDownload: 2 })
    expect(code).toBe(1)
    expect(err.join('\n')).toContain('boom')
    expect(snapshot(vault)).toEqual(before)
  })

  it('a failing gh pr view exits 1', () => {
    const before = snapshot(vault)
    const { code } = go({ runs: [], failPrView: true })
    expect(code).toBe(1)
    expect(err.join('\n')).toContain('HTTP 502 from fake')
    expect(snapshot(vault)).toEqual(before)
  })

  it('malformed artifact JSON exits 1 naming the artifact and file', () => {
    const bad = run(100, [])
    bad.artifacts[0]!.files = { 'smoke.json': '{not json' }
    const before = snapshot(vault)
    const { code } = go({ runs: [bad] })
    expect(code).toBe(1)
    expect(err.join('\n')).toContain('flaky-results-e2e-1')
    expect(err.join('\n')).toContain('smoke.json')
    expect(snapshot(vault)).toEqual(before)
  })
})

describe('CLI', () => {
  it('a single-file .mts copy runs under plain node and exits 2 on a missing argument', () => {
    const dir = mkdtempSync(join(tmpdir(), 'file-flaky-cli-'))
    try {
      const copy = join(dir, 'fileFlakyBugs.mts')
      copyFileSync(join(__dirname, 'fileFlakyBugs.ts'), copy)
      let status: number | null = null
      let stderr = ''
      try {
        execFileSync(process.execPath, [copy], { stdio: 'pipe', encoding: 'utf8' })
      } catch (e) {
        const x = e as { status: number; stderr: string }
        status = x.status
        stderr = x.stderr
      }
      expect(status).toBe(2)
      expect(stderr).toContain('usage: node e2e/fileFlakyBugs.ts')
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})
