import { afterEach, describe, expect, it, vi } from 'vitest'

const savedCI = process.env.CI

async function load(file: string, target?: string) {
  process.env.CI = 'true'
  if (target === undefined) delete process.env.E2E_TARGET
  else process.env.E2E_TARGET = target
  vi.resetModules()
  return (await import(file)).default
}

afterEach(() => {
  if (savedCI === undefined) delete process.env.CI
  else process.env.CI = savedCI
  delete process.env.E2E_TARGET
})

const configs = [
  ['smoke', './playwright.config', 'smoke'],
  ['api', './playwright.api.config', 'api'],
] as const

describe('smoke and api configs', () => {
  it.each(configs)('%s adds the json reporter on the nightly', async (_n, file, label) => {
    const { reporter, retries } = await load(file, 'nightly')
    expect(reporter).toContainEqual(['json'])
    expect(reporter).toContainEqual(['html', { open: 'never' }])
    expect(reporter).toContainEqual(['./flakyReporter.ts', { label }])
    expect(retries).toBe(1)
  })

  it.each(configs)('%s keeps its reporters on a PR', async (_n, file) => {
    const { reporter, retries } = await load(file)
    expect(reporter).not.toContainEqual(['json'])
    expect(reporter).toContainEqual(['html', { open: 'never' }])
    expect(retries).toBe(1)
  })
})

describe('mail config', () => {
  it('never retries and records nothing', async () => {
    const { reporter, retries, testMatch, workers, use } = await load('./playwright.mail.config', 'nightly')
    expect(testMatch).toBe('**/*.spec.ts')
    expect(retries).toBe(0)
    expect(workers).toBe(1)
    expect(use.trace).toBe('off')
    expect(use.video).toBe('off')
    expect(use.screenshot).toBe('off')
    expect(reporter).toEqual([['list']])
  })
})
