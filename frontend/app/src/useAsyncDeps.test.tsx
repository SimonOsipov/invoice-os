// @vitest-environment jsdom
//
// useAsync's effect cleanup bumps runId, so a deps change voids a run still in flight.
import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { useAsync } from '@invoice-os/api-client'

function deferred<T>() {
  let settle!: (v: T) => void
  const promise = new Promise<T>((res) => {
    settle = res
  })
  return { promise, settle }
}

describe('useAsync deps change', () => {
  it('a deps change discards the in-flight result of a non-immediate run', async () => {
    const pending = deferred<string[]>()
    let calls = 0
    const producer = () => {
      calls++
      return pending.promise
    }

    const { result, rerender } = renderHook(
      ({ k }: { k: string }) => useAsync<string[]>(producer, { immediate: false, deps: [k] }),
      { initialProps: { k: 'a' } },
    )
    act(() => result.current.run())
    expect(calls, 'run must call the producer').toBe(1)
    expect(result.current.status).toBe('loading')

    rerender({ k: 'b' })
    await act(async () => pending.settle(['stale']))

    expect(result.current.status, 'a result from before the deps change landed').toBe('loading')
    expect(result.current.data).toBeNull()
  })

  it('without a deps change the same run lands', async () => {
    const pending = deferred<string[]>()
    const producer = () => pending.promise

    const { result, rerender } = renderHook(
      ({ k }: { k: string }) => useAsync<string[]>(producer, { immediate: false, deps: [k] }),
      { initialProps: { k: 'a' } },
    )
    act(() => result.current.run())
    rerender({ k: 'a' })
    await act(async () => pending.settle(['fresh']))

    expect(result.current.status).toBe('ready')
    expect(result.current.data).toEqual(['fresh'])
  })
})
