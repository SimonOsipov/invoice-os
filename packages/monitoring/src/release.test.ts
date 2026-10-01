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
    for (const [build, railway, want] of rows) {
      const got = releaseName(build, railway)
      expect(got, `releaseName(${JSON.stringify(build)}, ${JSON.stringify(railway)})`).toBe(want)
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
    for (const [build, railway, want] of rows) {
      expect(releaseName(build, railway), `releaseName(${JSON.stringify(build)}, ${JSON.stringify(railway)})`).toBe(want)
    }
  })
})

const RAW = '../../../internal/platform/buildsha.txt?raw'
const ENV = 'VITE_RAILWAY_GIT_COMMIT_SHA'

describe('RELEASE', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
    vi.doUnmock(RAW)
    vi.resetModules()
  })

  async function load(stamp: string, railway: string | undefined): Promise<string> {
    vi.resetModules()
    vi.doMock(RAW, () => ({ default: stamp }))
    if (railway === undefined) vi.stubEnv(ENV, undefined as unknown as string)
    else vi.stubEnv(ENV, railway)
    return (await import('./release')).RELEASE
  }

  it('RELEASE_isBuiltFromTheStampAndTheRailwayEnv', async () => {
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
