// @vitest-environment jsdom
import { act, renderHook, waitFor } from '@testing-library/react'
import { Component, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AuthedFetch } from './portfolio'
import { useCustomRuleNames } from './useCustomRuleNames'

// A hook that throws on a failed read unmounts here; a result held from before the throw would hide it.
class Catch extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false }
  static getDerivedStateFromError() {
    return { failed: true }
  }
  render() {
    return this.state.failed ? <span data-testid="hook-threw" /> : this.props.children
  }
}

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
    const b = renderHook(() => useCustomRuleNames(bad as unknown as AuthedFetch), { wrapper: Catch })
    await waitFor(() => expect(bad).toHaveBeenCalled())
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(document.querySelector('[data-testid="hook-threw"]')).toBeNull()
    expect(b.result.current).toEqual({})

    vi.stubEnv('VITE_GATEWAY_URL', '')
    const none = vi.fn(async (_url: string) => ({ names: { k: 'n' } }))
    const c = renderHook(() => useCustomRuleNames(none as unknown as AuthedFetch))
    expect(c.result.current).toEqual({})
    expect(none).not.toHaveBeenCalled()
  })
})
