import { describe, expect, it } from 'vitest'
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
})
