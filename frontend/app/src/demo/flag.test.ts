// DEMO_MODE binds at module scope (see flag.ts), so every stubbed case needs
// vi.stubEnv + vi.resetModules() + a dynamic import to re-evaluate the binding.
import { afterEach, describe, expect, it, vi } from 'vitest'

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('DEMO_MODE', () => {
  it('is false when VITE_DEMO_MODE is unset', async () => {
    const { DEMO_MODE } = await import('./flag')
    expect(DEMO_MODE).toBe(false)
  })

  it.each(['true', 'TRUE', '1', 'yes', '', ' true', 'true '])(
    'is true only for the exact string "true" (got %j)',
    async (value) => {
      vi.stubEnv('VITE_DEMO_MODE', value)
      vi.resetModules()
      const { DEMO_MODE } = await import('./flag')
      expect(DEMO_MODE).toBe(value === 'true')
    },
  )
})
