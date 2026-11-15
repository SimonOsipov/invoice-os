import { afterEach, describe, expect, it, vi } from 'vitest'

const savedCI = process.env.CI
const savedArgos = process.env.ARGOS_SHOTS

async function loadConfig(ci: string | undefined, target?: string) {
  if (ci === undefined) delete process.env.CI
  else process.env.CI = ci
  if (target === undefined) delete process.env.E2E_TARGET
  else process.env.E2E_TARGET = target
  vi.resetModules()
  return (await import('./playwright.topology.config')).default
}

afterEach(() => {
  if (savedCI === undefined) delete process.env.CI
  else process.env.CI = savedCI
  delete process.env.E2E_TARGET
  if (savedArgos === undefined) delete process.env.ARGOS_SHOTS
  else process.env.ARGOS_SHOTS = savedArgos
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

  it('stops at no failure on the nightly', async () => {
    expect((await loadConfig('true', 'nightly')).maxFailures).toBe(0)
  })

  it('keeps one retry on the nightly', async () => {
    expect((await loadConfig('true', 'nightly')).retries).toBe(1)
  })

  it('adds the json reporter on the nightly only', async () => {
    expect((await loadConfig('true', 'nightly')).reporter).toContainEqual(['json'])
    expect((await loadConfig('true')).reporter).not.toContainEqual(['json'])
  })

  it('bypasses the CSP while Argos takes screenshots', async () => {
    process.env.ARGOS_SHOTS = '1'
    expect((await loadConfig(undefined)).use?.bypassCSP).toBe(true)
  })

  it('enforces the CSP when ARGOS_SHOTS is unset', async () => {
    delete process.env.ARGOS_SHOTS
    expect((await loadConfig(undefined)).use?.bypassCSP).toBeFalsy()
  })

  it('enforces the CSP when ARGOS_SHOTS is empty', async () => {
    process.env.ARGOS_SHOTS = ''
    expect((await loadConfig(undefined)).use?.bypassCSP).toBeFalsy()
  })
})
