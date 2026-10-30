import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join, matchesGlob } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ARGOS_SCREENS, changedOf, main, mergeName, run, type Build, type Deps, type Stats } from './argosBuild'

let dir: string
let out: string
beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), 'argos-test-'))
  out = join(dir, 'out', 'argos.json')
})
afterEach(async () => {
  await rm(dir, { recursive: true, force: true })
})

const dl = () => join(dir, 'dl')
async function put(rel: string, body = 'x') {
  const p = join(dl(), rel)
  await mkdir(dirname(p), { recursive: true })
  await writeFile(p, body)
}
const allNames = () => ARGOS_SCREENS.flatMap((s) => [1440, 375].map((w) => `${s}/${w}`))
async function putShots(names: string[], artifact = 'argos-shots-e2e') {
  for (const n of names) await put(`${artifact}/chromium/${n}.png`)
}

const build = (over: Partial<Build> = {}): Build => ({
  number: 7,
  url: 'https://app.argos-ci.com/o/p/builds/7',
  status: 'changes-detected',
  stats: { added: 0, removed: 0, changed: 0 },
  ...over,
})

function deps(over: Partial<Deps> & { builds?: Array<Partial<Build> | number> } = {}) {
  let t = 0
  const upload = vi.fn(async (_a: Record<string, unknown>) => ({ build: build() }))
  const reads: string[] = []
  const queue = over.builds ?? [{ status: 'changes-detected', stats: { added: 0, removed: 0, changed: 7 } }]
  const fetchStub = vi.fn(async (url: string | URL | Request) => {
    const u = String(url)
    if (u.endsWith('/project')) return Response.json({ account: { slug: 'o' }, name: 'p' })
    reads.push(u)
    const next = queue.length > 1 ? queue.shift()! : queue[0]
    if (typeof next === 'number') return new Response('no', { status: next })
    return Response.json(build(next))
  })
  const warn = vi.fn()
  const d: Deps = {
    env: { ARGOS_TOKEN: 't', ARGOS_COMMIT: 'abc', ARGOS_BRANCH: 'main' },
    upload,
    fetch: fetchStub as unknown as typeof fetch,
    sleep: async (ms) => void (t += ms),
    now: () => t,
    log: vi.fn(),
    warn,
    ...over,
  }
  return { d, upload, reads, warn }
}
const read = async () => JSON.parse(await readFile(out, 'utf8'))

describe('count', () => {
  it('161 shots are refused', async () => {
    const names = Array.from({ length: 161 }, (_, i) => `app/s${i}/1440`)
    await putShots(names)
    const { d, upload } = deps()
    const s = await run(dl(), out, d)
    expect(upload).not.toHaveBeenCalled()
    expect(s.ok).toBe(false)
    expect(s.failures?.[0]).toContain('161')
  })

  it('160 shots are uploaded', async () => {
    await putShots(Array.from({ length: 160 }, (_, i) => `app/s${i}/1440`))
    const { d, upload } = deps()
    const s = await run(dl(), out, d)
    expect(upload).toHaveBeenCalledTimes(1)
    expect(s.ok).toBe(true)
  })

  it('a metadata file is not counted', async () => {
    const names = Array.from({ length: 160 }, (_, i) => `app/s${i}/1440`)
    await putShots(names)
    for (const n of names) await put(`argos-shots-e2e/chromium/${n}.png.argos.json`, '{}')
    const { d, upload } = deps()
    await run(dl(), out, d)
    expect(upload).toHaveBeenCalledTimes(1)
  })
})

describe('names', () => {
  it('merge drops the artifact and project directories', () => {
    expect(mergeName('argos-shots-e2e/chromium/landing/home/1440.png')).toBe('landing/home/1440.png')
    expect(mergeName('argos-shots-topology-blog/blog/blog/home/375.png')).toBe('blog/home/375.png')
  })

  it('a name in two artifacts is refused', async () => {
    await put('argos-shots-e2e/chromium/landing/home/1440.png')
    await put('argos-shots-topology-auth/auth/landing/home/1440.png')
    const { d, upload } = deps()
    const s = await run(dl(), out, d)
    expect(upload).not.toHaveBeenCalled()
    expect(s.failures?.[0]).toContain('landing/home/1440.png')
  })
})

describe('upload', () => {
  it('upload is in monitoring mode', async () => {
    await putShots(['landing/home/1440'])
    const { d, upload } = deps()
    await run(dl(), out, d)
    expect(upload.mock.calls[0][0]).toMatchObject({ mode: 'monitoring', commit: 'abc', branch: 'main', token: 't' })
    expect(upload.mock.calls[0][0].files).toEqual(['**/*.png'])
  })

  it('the uploaded tree holds the PNGs with their metadata and no failed note', async () => {
    await put('argos-shots-e2e/chromium/landing/home/1440.png')
    await put('argos-shots-e2e/chromium/landing/home/1440.png.argos.json', '{}')
    await put('argos-shots-topology-blog/blog/blog/home/375.png')
    await put('argos-shots-topology-blog/blog/blog/home/1440.failed.txt', 'boom\n')
    const { d, upload } = deps()
    let tree: string[] = []
    let files: string[] = []
    upload.mockImplementationOnce(async (a: Record<string, unknown>) => {
      const root = a.root as string
      tree = (await readdir(root, { recursive: true, withFileTypes: true })).filter((e) => e.isFile()).map((e) => join(e.parentPath, e.name).slice(root.length + 1)).sort()
      files = a.files as string[]
      return { build: build() }
    })
    await run(dl(), out, d)
    expect(tree).toEqual(['blog/home/375.png', 'landing/home/1440.png', 'landing/home/1440.png.argos.json'])
    expect(tree.filter((f) => files.some((g) => matchesGlob(f, g)))).toEqual(['blog/home/375.png', 'landing/home/1440.png'])
  })

  it('differences keep ok true', async () => {
    await putShots(['landing/home/1440'])
    const { d } = deps()
    expect(await run(dl(), out, d)).toMatchObject({ ok: true, changed: 7, build_url: build().url })
  })

  it('a failed upload gives ok false', async () => {
    await putShots(['landing/home/1440'])
    const { d } = deps()
    d.upload = vi.fn(async () => {
      throw new Error('boom\nstack line')
    })
    const code = await main([dl(), out], d)
    expect(code).toBe(0)
    expect(await read()).toMatchObject({ ok: false, failures: ['boom'] })
  })
})

describe('no build', () => {
  it('no token gives ok false', async () => {
    await putShots(['landing/home/1440'])
    const { d, upload } = deps()
    d.env = { ...d.env, ARGOS_TOKEN: '' }
    expect(await main([dl(), out], d)).toBe(0)
    expect(upload).not.toHaveBeenCalled()
    expect(await read()).toEqual({ ok: false, build_url: null, changed: null, failures: ['ARGOS_TOKEN is not set'] })
  })

  it('no shots gives ok false', async () => {
    const { d, upload } = deps()
    await mkdir(dl(), { recursive: true })
    expect(await main([dl(), out], d)).toBe(0)
    expect((await read()).failures).toEqual(['no shots'])
    expect(await main([join(dir, 'absent'), out], d)).toBe(0)
    expect((await read()).failures).toEqual(['no shots'])
    expect(upload).not.toHaveBeenCalled()
  })

  it('an empty commit gives ok false', async () => {
    await putShots(['landing/home/1440'])
    const { d, upload } = deps()
    d.env = { ...d.env, ARGOS_COMMIT: '' }
    expect(await main([dl(), out], d)).toBe(0)
    expect(upload).not.toHaveBeenCalled()
    expect((await read()).failures[0]).toContain('ARGOS_COMMIT')
  })

  it('an empty branch gives ok false', async () => {
    await putShots(['landing/home/1440'])
    const { d, upload } = deps()
    d.env = { ...d.env, ARGOS_BRANCH: '' }
    expect(await main([dl(), out], d)).toBe(0)
    expect(upload).not.toHaveBeenCalled()
    expect((await read()).failures[0]).toContain('ARGOS_BRANCH')
  })
})

describe('changed', () => {
  it('changed sums added, removed and changed', () => {
    expect(changedOf({ added: 2, removed: 1, changed: 4 })).toBe(7)
    expect(changedOf({ added: 104, removed: 0, changed: 0 })).toBe(104)
    expect(changedOf(null as Stats)).toBeNull()
  })

  it('a pending build is polled', async () => {
    await putShots(['landing/home/1440'])
    const zero = { added: 0, removed: 0, changed: 0 }
    const { d, reads } = deps({
      builds: [
        { status: 'pending', stats: null },
        { status: 'progress', stats: null },
        { status: 'no-changes', stats: zero },
      ],
    })
    expect(await run(dl(), out, d)).toMatchObject({ ok: true, changed: 0 })
    expect(reads).toHaveLength(3)
  })

  it('an unfinished build gives changed null', async () => {
    await putShots(['landing/home/1440'])
    const { d, reads } = deps({ builds: [{ status: 'progress', stats: null }] })
    expect(await run(dl(), out, d)).toMatchObject({ ok: true, changed: null })
    expect(reads.length, 'polls for the whole 5 min at 10 s').toBeGreaterThanOrEqual(30)
  })

  it('a refused read gives changed null', async () => {
    await putShots(['landing/home/1440'])
    const { d, warn } = deps({ builds: [403] })
    expect(await run(dl(), out, d)).toMatchObject({ ok: true, changed: null })
    expect(warn.mock.calls.flat().join('\n')).toContain('403')
  })

  it('a 503 during polling is retried', async () => {
    await putShots(['landing/home/1440'])
    const { d, reads } = deps({ builds: [503, 502, { status: 'changes-detected', stats: { added: 0, removed: 0, changed: 4 } }] })
    expect(await run(dl(), out, d)).toMatchObject({ ok: true, changed: 4 })
    expect(reads).toHaveLength(3)
  })

  it('a hung read ends at the deadline', async () => {
    await putShots(['landing/home/1440'])
    const { d, reads, warn } = deps()
    d.fetch = (async (url: string | URL | Request) => {
      reads.push(String(url))
      throw new DOMException('timed out', 'TimeoutError')
    }) as unknown as typeof fetch
    expect(await run(dl(), out, d)).toMatchObject({ ok: true, changed: null })
    expect(reads.length).toBeGreaterThan(1)
    expect(reads.length).toBeLessThanOrEqual(32)
    expect(warn.mock.calls.flat().join('\n')).toContain('timed out')
  })

  it('a 401 during polling ends at once', async () => {
    await putShots(['landing/home/1440'])
    const { d, reads } = deps({ builds: [401] })
    expect(await run(dl(), out, d)).toMatchObject({ ok: true, changed: null })
    expect(reads).toHaveLength(1)
  })

  it('an errored build gives changed null', async () => {
    await putShots(['landing/home/1440'])
    const { d } = deps({ builds: [{ status: 'error', stats: null }] })
    expect(await run(dl(), out, d)).toMatchObject({ ok: true, changed: null })
  })
})

describe('missing', () => {
  it('a lost shard is listed as missing', async () => {
    const lost = allNames().filter((n) => n.startsWith('blog/'))
    await putShots(allNames().filter((n) => !n.startsWith('blog/')))
    const { d, upload } = deps()
    const s = await run(dl(), out, d)
    expect(upload).toHaveBeenCalledTimes(1)
    expect(s.ok).toBe(true)
    expect(s.missing).toEqual(lost.map((n) => `${n}: no shot`))
  })

  it('a failed shot gives its reason', async () => {
    await putShots(allNames().filter((n) => n !== 'app/firm-invoices/375'))
    await put('argos-shots-e2e/chromium/app/firm-invoices/375.failed.txt', 'Failed to stabilize screenshot, found the following issues:\n- 2 fonts are loading\n')
    const { d } = deps()
    expect((await run(dl(), out, d)).missing).toEqual([
      'app/firm-invoices/375: Failed to stabilize screenshot, found the following issues: - 2 fonts are loading',
    ])
  })

  it('a retried shot is not missing', async () => {
    await putShots(allNames())
    await put('argos-shots-e2e/chromium/app/firm-invoices/375.failed.txt', 'old failure\n')
    const { d } = deps()
    expect('missing' in (await run(dl(), out, d))).toBe(false)
  })

  it('nothing missing omits the key', async () => {
    await putShots(allNames())
    const { d } = deps()
    expect('missing' in (await run(dl(), out, d))).toBe(false)
  })

  it('an unexpected name is uploaded', async () => {
    await putShots([...allNames(), 'app/new-screen/1440'])
    const { d, upload } = deps()
    const s = await run(dl(), out, d)
    expect(upload).toHaveBeenCalledTimes(1)
    expect('missing' in s).toBe(false)
  })
})
