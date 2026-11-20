// @vitest-environment jsdom
import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AuthedFetch } from './portfolio'
import { useCustomRuleNames } from './useCustomRuleNames'

beforeEach(() => vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test'))
afterEach(() => vi.unstubAllEnvs())

describe('useCustomRuleNames', () => {
  it('useCustomRuleNames_returnsTheMapAndSwallowsAFailure', async () => {
    const ok = vi.fn(async (_url: string) => ({ names: { k: 'n' } }))
    const a = renderHook(() => useCustomRuleNames(ok as unknown as AuthedFetch))
    expect(a.result.current).toEqual({})
    await waitFor(() => expect(a.result.current).toEqual({ k: 'n' }))
    expect(String(ok.mock.calls[0]![0])).toBe('https://gw.test/api/invoice/v1/custom-rules/names')

    const bad = vi.fn(async () => {
      throw new Error('boom')
    })
    const b = renderHook(() => useCustomRuleNames(bad as unknown as AuthedFetch))
    await waitFor(() => expect(bad).toHaveBeenCalled())
    expect(b.result.current).toEqual({})

    vi.stubEnv('VITE_GATEWAY_URL', '')
    const none = vi.fn(async (_url: string) => ({ names: { k: 'n' } }))
    const c = renderHook(() => useCustomRuleNames(none as unknown as AuthedFetch))
    expect(c.result.current).toEqual({})
    expect(none).not.toHaveBeenCalled()
  })
})
