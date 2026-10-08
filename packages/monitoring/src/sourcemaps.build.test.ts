/// <reference types="node" />
import { createRequire } from 'node:module'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { FRONTEND, monitoredSpas } from './monitoredSpas.testutil'

type Item = { type: 'chunk' | 'asset'; fileName: string; isEntry?: boolean; code?: string; map?: { sources: string[] } | null; source?: string | Uint8Array }
type Built = { appDir: string; output: Item[]; dist: string[]; warns: string[]; errors: string[]; failure?: unknown }

const SPAS = monitoredSpas()
const TIMEOUT = 120_000
// "No org provided. Will not upload source maps." also fires for a token the builder failed to trim.
const NO_TOKEN_WARNING = 'No auth token provided. Will not upload source maps'
const builds = new Map<string, Built>()

const walk = (dir: string): string[] =>
  existsSync(dir)
    ? readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? walk(join(dir, e.name)) : [join(dir, e.name)]))
    : []

// Builds with the vite each SPA resolves, as `pnpm --filter <spa> build` does.
async function buildSpa(name: string): Promise<Built> {
  const appDir = join(FRONTEND, name)
  const viteUrl = pathToFileURL(createRequire(join(appDir, 'package.json')).resolve('vite')).href
  const { build } = (await import(viteUrl)) as { build: (o: object) => Promise<unknown> }
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  const error = vi.spyOn(console, 'error').mockImplementation(() => {})
  warn.mockClear()
  error.mockClear()
  const built: Built = { appDir, output: [], dist: [], warns: [], errors: [] }
  try {
    const res = (await build({ root: appDir, configFile: join(appDir, 'vite.config.ts'), logLevel: 'silent' })) as
      | { output: Item[] }
      | { output: Item[] }[]
    built.output = (Array.isArray(res) ? res : [res]).flatMap((r) => r.output)
  } catch (e) {
    built.failure = e
  }
  built.dist = walk(join(appDir, 'dist')).map((f) => relative(join(appDir, 'dist'), f))
  built.warns = warn.mock.calls.map((c) => String(c[0]))
  built.errors = error.mock.calls.map((c) => String(c[0]))
  return built
}

function built(name: string): Built {
  expect(SPAS).toEqual(['app', 'landing', 'library', 'ops-console', 'support-console'])
  const b = builds.get(name)!
  expect(b, `${name}: no build result`).toBeDefined()
  expect(b.failure, `${name}: build failed`).toBeUndefined()
  return b
}

const chunks = (b: Built) => b.output.filter((o) => o.type === 'chunk')
const entries = (b: Built) => chunks(b).filter((c) => c.isEntry)

function mapOf(b: Built, entry: Item): { sources: string[] } | undefined {
  const asset = b.output.find((o) => o.type === 'asset' && o.fileName === `${entry.fileName}.map`)
  if (asset?.source !== undefined) return JSON.parse(Buffer.from(asset.source).toString('utf8'))
  return entry.map ?? undefined
}

describe('sourcemaps (real SPA builds)', () => {
  beforeAll(async () => {
    // NODE_ENV=production: the plugin skips release and upload silently in development.
    vi.stubEnv('SENTRY_AUTH_TOKEN', '')
    vi.stubEnv('NODE_ENV', 'production')
    for (const name of SPAS) builds.set(name, await buildSpa(name))
  }, TIMEOUT * 4)

  afterAll(() => {
    vi.unstubAllEnvs()
    vi.restoreAllMocks()
  })

  it('sourcemaps_everyMonitoredSpaBuildsAHiddenMapForItsEntry', () => {
    for (const name of SPAS) {
      const b = built(name)
      expect(entries(b).length, `${name}: no entry chunk`).toBeGreaterThan(0)
      for (const entry of entries(b)) {
        const map = mapOf(b, entry)
        expect(map, `${name}: entry ${entry.fileName} has no map`).toBeDefined()
        const sources = map!.sources.map((s) => resolve(b.appDir, 'dist/assets', s))
        expect(sources, `${name}: map does not name the SPA's own main.tsx`).toContain(join(b.appDir, 'src/main.tsx'))
      }
      expect(chunks(b).length).toBeGreaterThan(0)
      for (const c of chunks(b)) {
        const has = /\/\/[#@]\s*sourceMappingURL=/.test(c.code ?? '')
        expect(has, `${name}: ${c.fileName} carries a sourceMappingURL comment`).toBe(false)
      }
    }
  }, TIMEOUT)

  it('sourcemaps_everyMonitoredSpaStampsADebugId', () => {
    for (const name of SPAS) {
      const b = built(name)
      expect(entries(b).length, `${name}: no entry chunk`).toBeGreaterThan(0)
      for (const entry of entries(b)) {
        // Boolean, so a failure does not print the whole bundle.
        expect((entry.code ?? '').includes('sentry-dbid-'), `${name}: ${entry.fileName} has no debug id`).toBe(true)
      }
    }
  }, TIMEOUT)

  it('sourcemaps_noTokenSkipsTheUploadAndTheBuildSucceeds', () => {
    for (const name of SPAS) {
      const b = built(name)
      expect(b.output.length, `${name}: empty build output`).toBeGreaterThan(0)
      expect(
        b.warns.some((w) => w.includes(NO_TOKEN_WARNING)),
        `${name}: no "${NO_TOKEN_WARNING}" warning; got ${JSON.stringify(b.warns)}`,
      ).toBe(true)
      expect(b.errors.filter((e) => e.includes('[sentry-vite-plugin] Error')), `${name}: plugin error`).toEqual([])
    }
  }, TIMEOUT)

  it('sourcemaps_whitespaceTokenAlsoSkips', async () => {
    // The builder trims, so the plugin never sees the raw whitespace.
    vi.stubEnv('SENTRY_AUTH_TOKEN', '  \n')
    const b = await buildSpa('ops-console')
    expect(b.failure, 'ops-console: build failed').toBeUndefined()
    expect(b.output.length).toBeGreaterThan(0)
    expect(
      b.warns.some((w) => w.includes(NO_TOKEN_WARNING)),
      `no skip warning; got ${JSON.stringify(b.warns)}`,
    ).toBe(true)
    expect(b.errors.filter((e) => e.includes('[sentry-vite-plugin] Error'))).toEqual([])
  }, TIMEOUT)

  it('sourcemaps_noMapIsLeftInDist', () => {
    for (const name of SPAS) {
      const b = built(name)
      // Precondition: maps existed, so their absence from dist is the deletion's doing.
      expect(b.output.filter((o) => o.type === 'asset' && o.fileName.endsWith('.map')).length, `${name}: the build emitted no .map`).toBeGreaterThan(0)
      expect(b.dist.length, `${name}: dist is empty`).toBeGreaterThan(0)
      expect(b.dist.filter((f) => f.endsWith('.map')), `${name}: maps left in dist`).toEqual([])
      expect(entries(b).length).toBeGreaterThan(0)
      for (const entry of entries(b)) expect(b.dist, `${name}: entry chunk missing from dist`).toContain(entry.fileName)
      // The shipped JS on disk, not the in-memory chunk: no comment invites a browser to fetch a map.
      const js = b.dist.filter((f) => f.endsWith('.js'))
      expect(js.length, `${name}: no .js in dist`).toBeGreaterThan(0)
      for (const f of js) {
        const text = readFileSync(join(b.appDir, 'dist', f), 'utf8')
        expect(/\/\/[#@]\s*sourceMappingURL=/.test(text), `${name}: dist/${f} carries a sourceMappingURL comment`).toBe(false)
      }
    }
  }, TIMEOUT)

  it('bundle_theLibraryShipsNoHubSpotOrGatewayClient', () => {
    const js = (name: string) => {
      const b = built(name)
      return b.dist.filter((f) => f.endsWith('.js')).map((f) => readFileSync(join(b.appDir, 'dist', f), 'utf8')).join('\n')
    }
    const lib = js('library')
    expect(lib.length, 'library: no shipped JS').toBeGreaterThan(0)
    expect(lib.includes('hsforms.com'), 'library bundle carries hsforms.com').toBe(false)
    expect(lib.includes('/submissions/v3/integration/submit/'), 'library bundle carries the HubSpot submit path').toBe(false)
    expect(js('landing').includes('hsforms.com'), 'control: the landing bundle carries hsforms.com').toBe(true)
  }, TIMEOUT)
})
