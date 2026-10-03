/// <reference types="node" />
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { sourcemapUploadOptions } from './upload'

const REPO = fileURLToPath(new URL('../../../', import.meta.url))
const SHA = '0123456789abcdef'.repeat(3).slice(0, 40)
const ENV = 'VITE_RAILWAY_GIT_COMMIT_SHA'

const roots: string[] = []

// Builds <root>/frontend/<name>/ and, when stamp is given, <root>/internal/platform/buildsha.txt.
function tree(stamp: string | null, name = 'x'): { root: string; appDir: string } {
  const root = mkdtempSync(join(tmpdir(), 'upload-test-'))
  roots.push(root)
  const appDir = join(root, 'frontend', name)
  mkdirSync(appDir, { recursive: true })
  if (stamp !== null) {
    mkdirSync(join(root, 'internal', 'platform'), { recursive: true })
    writeFileSync(join(root, 'internal', 'platform', 'buildsha.txt'), stamp)
  }
  return { root, appDir }
}

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
  for (const r of roots.splice(0)) rmSync(r, { recursive: true, force: true })
})

describe('sourcemapUploadOptions', () => {
  it('uploadRelease_equalsTheSdkReleaseForTheSameBuild', async () => {
    vi.stubEnv(ENV, 'abc123')
    vi.resetModules()
    const { RELEASE } = await import('./release')
    const got = sourcemapUploadOptions(join(REPO, 'frontend/ops-console'), { [ENV]: 'abc123' }).release.name
    expect(RELEASE).not.toBe('')
    expect(got).toBe(RELEASE)
  })

  it('uploadRelease_readsTheStampedFileBesideTheApp', () => {
    const rows: Array<[string, Record<string, string>, string]> = [
      [`${SHA}\n`, {}, SHA],
      ['dev\n', {}, 'unstamped'],
      ['dev', { [ENV]: ' f00 ' }, 'unstamped-f00'],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [stamp, env, want] of rows) {
      const { appDir } = tree(stamp)
      expect(sourcemapUploadOptions(appDir, env).release.name, `stamp ${JSON.stringify(stamp)}`).toBe(want)
    }
  })

  it('uploadRelease_readsTheVariableTheSdkReads', () => {
    const { appDir } = tree('dev')
    expect(sourcemapUploadOptions(appDir, { RAILWAY_GIT_COMMIT_SHA: 'zzz', [ENV]: 'abc' }).release.name).toBe(
      'unstamped-abc',
    )
    expect(sourcemapUploadOptions(appDir, { RAILWAY_GIT_COMMIT_SHA: 'zzz' }).release.name).toBe('unstamped')
  })

  it('uploadRelease_missingStampFileThrows', () => {
    const { appDir } = tree(null)
    expect(() => sourcemapUploadOptions(appDir, {})).toThrow(/buildsha\.txt/)
  })

  it('uploadToken_blankMeansSkip', () => {
    const { appDir } = tree('dev')
    const envs: Array<Record<string, string>> = [{}, { SENTRY_AUTH_TOKEN: '' }, { SENTRY_AUTH_TOKEN: ' \t\n' }]
    expect(envs.length).toBeGreaterThan(0)
    for (const env of envs) {
      const { authToken } = sourcemapUploadOptions(appDir, env)
      expect(typeof authToken, JSON.stringify(env)).toBe('string')
      expect(authToken, JSON.stringify(env)).toBe('')
    }
  })

  it('uploadToken_isTrimmedAndNeverLogged', () => {
    const { appDir } = tree('dev')
    const spies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) =>
      vi.spyOn(console, m).mockImplementation(() => {}),
    )
    const { authToken } = sourcemapUploadOptions(appDir, { SENTRY_AUTH_TOKEN: ' sntrys_fixture \n' })
    expect(authToken).toBe('sntrys_fixture')
    expect(spies.length).toBe(5)
    for (const s of spies) expect(s).not.toHaveBeenCalled()
  })

  it('uploadTarget_isTheEuFrontendProject', () => {
    const { appDir } = tree('dev')
    const opts = sourcemapUploadOptions(appDir, {})
    expect(opts.url).toBe('https://de.sentry.io')
    expect(opts.project).toBe('asc-frontend')
    expect('org' in opts).toBe(false)
    expect(opts.telemetry).toBe(false)
  })

  it('uploadRelease_isNotInjectedAndCarriesNoCommits', () => {
    const { appDir } = tree('dev')
    const { release } = sourcemapUploadOptions(appDir, {})
    expect(release.inject).toBe(false)
    expect(release.setCommits).toBe(false)
  })

  it('uploadCleanup_deletesEveryMapUnderTheAppDist', () => {
    const { root, appDir } = tree('dev', 'app')
    const want = [`${root}/frontend/app/dist/**/*.map`]
    expect(sourcemapUploadOptions(appDir, {}).sourcemaps.filesToDeleteAfterUpload).toEqual(want)
    expect(sourcemapUploadOptions(`${appDir}/`, {}).sourcemaps.filesToDeleteAfterUpload).toEqual(want)
  })

  it('uploadDefaults_readProcessEnvWhenNoEnvIsGiven', () => {
    const { appDir } = tree('dev')
    vi.stubEnv('SENTRY_AUTH_TOKEN', ' sntrys_proc \n')
    vi.stubEnv(ENV, 'procsha')
    const opts = sourcemapUploadOptions(appDir)
    expect(opts.authToken).toBe('sntrys_proc')
    expect(opts.release.name).toBe('unstamped-procsha')
  })

  it('uploadBuilder_isNotInThePackageEntry', async () => {
    const entry = await import('./index')
    expect(typeof entry.releaseName).toBe('function')
    expect(Object.keys(entry)).not.toContain('sourcemapUploadOptions')
  })
})
