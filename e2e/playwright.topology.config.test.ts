import { afterEach, describe, expect, it, vi } from 'vitest'

const savedCI = process.env.CI

async function loadConfig(ci: string | undefined) {
  if (ci === undefined) delete process.env.CI
  else process.env.CI = ci
  vi.resetModules()
  return (await import('./playwright.topology.config')).default
}

afterEach(() => {
  if (savedCI === undefined) delete process.env.CI
  else process.env.CI = savedCI
})

describe('topology config', () => {
  it('stops a shard at its first failed test when CI is set', async () => {
    expect((await loadConfig('true')).maxFailures).toBe(1)
  })

  it('has no failure limit locally', async () => {
    expect((await loadConfig(undefined)).maxFailures).toBe(0)
  })

  it('keeps one worker with and without CI', async () => {
    expect((await loadConfig('true')).workers).toBe(1)
    expect((await loadConfig(undefined)).workers).toBe(1)
  })

  it('keeps retries at 1 in CI and 0 locally', async () => {
    expect((await loadConfig('true')).retries).toBe(1)
    expect((await loadConfig(undefined)).retries).toBe(0)
  })

  it('keeps the html and flaky reporters in CI', async () => {
    const { reporter } = await loadConfig('true')
    expect(reporter).toContainEqual(['html', { open: 'never' }])
    expect(reporter).toContainEqual(['./flakyReporter.ts', { label: 'topology' }])
  })

  it('keeps only the list and flaky reporters locally', async () => {
    const { reporter } = await loadConfig(undefined)
    expect(reporter).not.toContainEqual(['html', { open: 'never' }])
    expect(reporter).toContainEqual(['./flakyReporter.ts', { label: 'topology' }])
  })

  it('reads an empty CI as unset', async () => {
    const config = await loadConfig('')
    expect(config.maxFailures).toBe(0)
    expect(config.retries).toBe(0)
  })
})
