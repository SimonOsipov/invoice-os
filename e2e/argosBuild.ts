// Merges the nightly's shot artifacts, uploads one Argos build and writes the `argos` section of summary.json.
// Usage: node e2e/argosBuild.ts <downloadDir> <outFile>  (reads ARGOS_TOKEN, ARGOS_COMMIT, ARGOS_BRANCH)
// Node >= 22.18 runs this file without flags, so keep it to erasable TypeScript.

import { copyFile, mkdir, mkdtemp, readFile, readdir, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'

export const ARGOS_MAX_SHOTS = 160
export const POLL_MS = 10_000
export const POLL_LIMIT_MS = 300_000
const API = 'https://api.argos-ci.com/v2'
const WIDTHS = [1440, 375]

const FIRM = ['overview', 'invoices', 'approvals', 'rules', 'reports', 'workflows', 'clients', 'customers', 'audit', 'settings']
const INHOUSE = ['overview', 'invoices', 'workflows', 'rules', 'approvals', 'customers', 'reports', 'audit', 'settings']
const SCREENS = [
  ...FIRM.map((s) => `app/firm-${s}`),
  ...INHOUSE.map((s) => `app/inhouse-${s}`),
  ...['firm-account', 'inhouse-account', 'firm-detail', 'firm-create', 'firm-extraction', 'inhouse-create', 'inhouse-detail'].map((s) => `app/${s}`),
  ...['overview', 'submissions', 'evidence', 'api', 'billing', 'status'].map((s) => `ops/${s}`),
  ...['submissions', 'rules', 'audit', 'tenants', 'system-health', 'ai-usage', 'blog'].map((s) => `support/${s}`),
  ...['home', 'privacy', 'invite'].map((s) => `landing/${s}`),
  ...['home', 'group', 'feature'].map((s) => `library/${s}`),
  ...['home', 'search', 'subscribe', 'confirm', 'category', 'post', 'not-found'].map((s) => `blog/${s}`),
]
/** ARGOS_SCREENS: every shot name without its width. */
export const ARGOS_SCREENS: readonly string[] = SCREENS

export type Stats = { added: number; removed: number; changed: number } | null
export type Section = {
  ok: boolean
  build_url: string | null
  changed: number | null
  failures?: string[]
  missing?: string[]
}
export type Build = { number: number; url: string; status: string; stats: Stats }
export type Deps = {
  env: Record<string, string | undefined>
  upload: (args: Record<string, unknown>) => Promise<{ build: Build }>
  fetch: typeof fetch
  sleep: (ms: number) => Promise<void>
  now: () => number
  log: (line: string) => void
  warn: (line: string) => void
}

export type Shots = {
  pngs: Map<string, string>
  files: Map<string, string>
  failed: Map<string, string>
  duplicates: string[]
}

/** listFiles: every file under dir as a path relative to it; empty when dir is absent. */
async function listFiles(dir: string): Promise<string[]> {
  let entries
  try {
    entries = await readdir(dir, { recursive: true, withFileTypes: true })
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === 'ENOENT') return []
    throw err
  }
  return entries
    .filter((e) => e.isFile())
    .map((e) => join(e.parentPath, e.name).slice(dir.length + 1))
}

/** mergeName: drops the artifact and project directories; undefined for a path too shallow to be a shot. */
export function mergeName(rel: string): string | undefined {
  const parts = rel.split('/')
  return parts.length > 3 ? parts.slice(2).join('/') : undefined
}

/** collect: maps merged names to source paths, and reads the reason of each `.failed.txt`. */
export async function collect(downloadDir: string): Promise<Shots> {
  const shots: Shots = { pngs: new Map(), files: new Map(), failed: new Map(), duplicates: [] }
  for (const rel of (await listFiles(downloadDir)).sort()) {
    const name = mergeName(rel)
    if (!name) continue
    const src = join(downloadDir, rel)
    if (name.endsWith('.failed.txt')) {
      const reason = reasonOf(await readFile(src, 'utf8'))
      shots.failed.set(name.replace(/\.failed\.txt$/, ''), reason)
      continue
    }
    if (name.endsWith('.png')) {
      if (shots.pngs.has(name)) shots.duplicates.push(name)
      shots.pngs.set(name, src)
    }
    shots.files.set(name, src)
  }
  return shots
}

/** countError: why the count may not be uploaded; undefined when it may. */
export function countError(n: number): string | undefined {
  if (n === 0) return 'no shots'
  if (n > ARGOS_MAX_SHOTS) return `${n} shots, the cap is ${ARGOS_MAX_SHOTS}`
  return undefined
}

/** missingOf: each expected name with no PNG, with the reason of its `.failed.txt` or `no shot`. */
export function missingOf(pngs: Iterable<string>, failed: Map<string, string>): string[] {
  const have = new Set(pngs)
  const out: string[] = []
  for (const screen of ARGOS_SCREENS) {
    for (const w of WIDTHS) {
      const name = `${screen}/${w}`
      if (!have.has(`${name}.png`)) out.push(`${name}: ${failed.get(name) || 'no shot'}`)
    }
  }
  return out
}

/** changedOf: changed, added and removed summed; null when the stats were not read. */
export function changedOf(stats: Stats): number | null {
  return stats ? stats.changed + stats.added + stats.removed : null
}

/** sectionOf: the `argos` section; `failures` only on ok:false, `missing` only when non-empty. */
export function sectionOf(r: { failure?: string; url?: string; changed?: number | null; missing?: string[] }): Section {
  if (r.failure !== undefined) return { ok: false, build_url: null, changed: null, failures: [r.failure] }
  const s: Section = { ok: true, build_url: r.url ?? null, changed: r.changed ?? null }
  if (r.missing?.length) s.missing = r.missing
  return s
}

export const FETCH_TIMEOUT_MS = 30_000

/** reasonOf: the text's lines joined into one line of at most 300 characters. */
export function reasonOf(text: string): string {
  return text.split('\n').map((l) => l.trim()).filter(Boolean).join(' ').slice(0, 300)
}

class HttpError extends Error {
  status: number
  constructor(status: number) {
    super(`Argos refused the read: HTTP ${status}`)
    this.status = status
  }
}

async function readChanged(build: Build, token: string, d: Deps): Promise<number | null> {
  const get = async (path: string) => {
    const res = await d.fetch(`${API}${path}`, {
      headers: { Authorization: `Bearer ${token}` },
      signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
    })
    if (!res.ok) throw new HttpError(res.status)
    return res.json()
  }
  const deadline = d.now() + POLL_LIMIT_MS
  let path = ''
  try {
    for (;;) {
      try {
        if (!path) {
          const project = (await get('/project')) as { account: { slug: string }; name: string }
          path = `/projects/${project.account.slug}/${project.name}/builds/${build.number}`
        }
        const b = (await get(path)) as Build
        if (b.status !== 'pending' && b.status !== 'progress') return changedOf(b.stats)
        if (d.now() >= deadline) {
          d.warn(`argos: build ${build.number} still ${b.status} after 5 min`)
          return null
        }
      } catch (err) {
        const retry = !(err instanceof HttpError) || err.status >= 500
        if (!retry || d.now() >= deadline) throw err
      }
      await d.sleep(POLL_MS)
    }
  } catch (err) {
    d.warn(`argos: changed not read: ${(err as Error).message}`)
    return null
  }
}

/** run: builds the section and writes it to outFile. */
export async function run(downloadDir: string, outFile: string, d: Deps): Promise<Section> {
  const section = await build(downloadDir, d)
  await mkdir(dirname(outFile), { recursive: true })
  await writeFile(outFile, JSON.stringify(section) + '\n')
  return section
}

async function build(downloadDir: string, d: Deps): Promise<Section> {
  const token = d.env.ARGOS_TOKEN
  const commit = d.env.ARGOS_COMMIT
  const branch = d.env.ARGOS_BRANCH
  for (const [k, v] of [['ARGOS_TOKEN', token], ['ARGOS_COMMIT', commit], ['ARGOS_BRANCH', branch]]) {
    if (!v) return sectionOf({ failure: `${k} is not set` })
  }
  const shots = await collect(downloadDir)
  if (shots.duplicates.length) return sectionOf({ failure: `${shots.duplicates[0]} is shot twice` })
  const bad = countError(shots.pngs.size)
  if (bad) return sectionOf({ failure: bad })

  const merged = await mkdtemp(join(tmpdir(), 'argos-merged-'))
  for (const [name, src] of shots.files) {
    await mkdir(dirname(join(merged, name)), { recursive: true })
    await copyFile(src, join(merged, name))
  }
  const missing = missingOf(shots.pngs.keys(), shots.failed)
  let built: Build
  try {
    built = (await d.upload({ root: merged, files: ['**/*.png'], mode: 'monitoring', commit, branch, token })).build
  } catch (err) {
    return sectionOf({ failure: String((err as Error)?.message ?? err).split('\n')[0] })
  }
  if (missing.length) d.warn(`::warning::argos: ${missing.length} shots missing, first: ${missing[0]}`)
  const changed = await readChanged(built, token as string, d)
  return sectionOf({ url: built.url, changed, missing })
}

/** main: always returns 0, because a written section carries the verdict. */
export async function main(argv: string[], d: Deps): Promise<number> {
  const [downloadDir, outFile] = argv
  if (!downloadDir || !outFile) throw new Error('usage: argosBuild.ts <downloadDir> <outFile>')
  await run(downloadDir, outFile, d)
  return 0
}

if (import.meta.main) {
  const { upload } = await import('@argos-ci/core')
  const code = await main(process.argv.slice(2), {
    env: process.env,
    upload: upload as Deps['upload'],
    fetch,
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    now: Date.now,
    log: console.log,
    warn: console.error,
  })
  process.exit(code)
}
