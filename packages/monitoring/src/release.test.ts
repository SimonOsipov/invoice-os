import { afterEach, describe, expect, it, vi } from 'vitest'
import { releaseName } from './release'

const SHA = '0123456789abcdef'.repeat(3).slice(0, 40)

describe('releaseName', () => {
  it('releaseName_followsTheBackendRule', () => {
    const rows: Array<[string, string, string]> = [
      [SHA, '', SHA],
      [`${SHA}\n`, 'abc', SHA],
      [SHA, 'abc123', SHA],
      ['dev', 'abc123', 'unstamped-abc123'],
      ['dev\n', ' abc123\n', 'unstamped-abc123'],
      ['', 'abc123', 'unstamped-abc123'],
      ['', '', 'unstamped'],
      ['  ', '  ', 'unstamped'],
      ['dev', '', 'unstamped'],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [build, platform, want] of rows) {
      const got = releaseName(build, platform)
      expect(got, `releaseName(${JSON.stringify(build)}, ${JSON.stringify(platform)})`).toBe(want)
      expect(['dev', '', 'unstamped-']).not.toContain(got)
    }
  })

  it('releaseName_trimsEveryKindOfWhitespaceAndMatchesDevExactly', () => {
    const rows: Array<[string, string, string]> = [
      ['\t' + SHA + ' \r\n', '', SHA],
      [' dev ', 'abc', 'unstamped-abc'],
      ['dev\r\n', '\tabc\n', 'unstamped-abc'],
      ['\n', '\t', 'unstamped'],
      ['Dev', 'abc', 'Dev'],
      ['devx', 'abc', 'devx'],
      ['xdev', 'abc', 'xdev'],
      ['unstamped', 'abc', 'unstamped'],
      ['', 'abc def', 'unstamped-abc def'],
    ]
    for (const [build, platform, want] of rows) {
      expect(releaseName(build, platform), `releaseName(${JSON.stringify(build)}, ${JSON.stringify(platform)})`).toBe(want)
    }
  })
})

const RAW = '../../../internal/platform/buildsha.txt?raw'
const ENV = 'VITE_BUILD_SHA'

describe('RELEASE', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
    vi.doUnmock(RAW)
    vi.resetModules()
  })

  async function load(stamp: string, env: string | undefined): Promise<string> {
    vi.resetModules()
    vi.doMock(RAW, () => ({ default: stamp }))
    if (env === undefined) vi.stubEnv(ENV, undefined as unknown as string)
    else vi.stubEnv(ENV, env)
    return (await import('./release')).RELEASE
  }

  it('RELEASE_isBuiltFromTheStampAndTheBuildShaEnv', async () => {
    expect(await load(`${SHA}\n`, ' abc ')).toBe(SHA)
    expect(await load('dev\n', ' abc123\n')).toBe('unstamped-abc123')
    expect(await load('', 'abc123')).toBe('unstamped-abc123')
    expect(await load('dev\n', undefined)).toBe('unstamped')
  })

  it('RELEASE_ofTheCheckedInStampIsNeverDevOrEmpty', async () => {
    vi.resetModules()
    const { RELEASE } = await import('./release')
    expect(RELEASE).toMatch(/^([0-9a-f]{40}|unstamped(-\S+)?)$/)
  })
})
