// @vitest-environment jsdom
//
// useAsync's effect cleanup bumps runId, so a deps change voids a run still in flight.
import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { useAsync } from '@invoice-os/api-client'

function deferred<T>() {
  let settle!: (v: T) => void
  let fail!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    settle = res
    fail = rej
  })
  return { promise, settle, fail }
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

    expect(result.current.data, 'a result from before the deps change landed').toBeNull()
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

  it('a deps change discards the in-flight rejection of a non-immediate run', async () => {
    const pending = deferred<string[]>()
    const { result, rerender } = renderHook(
      ({ k }: { k: string }) => useAsync<string[]>(() => pending.promise, { immediate: false, deps: [k] }),
      { initialProps: { k: 'a' } },
    )
    act(() => result.current.run())
    rerender({ k: 'b' })
    await act(async () => pending.fail(new Error('stale')))

    expect(result.current.error, 'a rejection from before the deps change landed').toBeNull()
  })

  it('without a deps change the same rejection lands', async () => {
    const pending = deferred<string[]>()
    const { result } = renderHook(() => useAsync<string[]>(() => pending.promise, { immediate: false, deps: ['a'] }))
    act(() => result.current.run())
    await act(async () => pending.fail(new Error('boom')))

    expect(result.current.status).toBe('error')
    expect(result.current.error?.message).toBe('boom')
  })

  it('an immediate run superseded by a deps change never overwrites the newer result', async () => {
    const runs = [deferred<string[]>(), deferred<string[]>()]
    let calls = 0
    const { result, rerender } = renderHook(
      ({ k }: { k: string }) => useAsync<string[]>(() => runs[calls++]!.promise, { deps: [k] }),
      { initialProps: { k: 'a' } },
    )
    rerender({ k: 'b' })
    expect(calls, 'mount and the deps change each start a run').toBe(2)

    await act(async () => runs[1]!.settle(['new']))
    await act(async () => runs[0]!.settle(['old']))

    expect(result.current.status).toBe('ready')
    expect(result.current.data).toEqual(['new'])
  })
})
