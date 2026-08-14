// @vitest-environment jsdom
//
// APPR-09-03 QA (task-507). `publishPolicy` calls `policiesAsync.run()` — the FIRST refetch
// in the app. Members, roles and entities only ever fetch once and patch, so `useAsync`'s
// re-run path has never been exercised by anything: its own header records it as
// deliberately untested ("the hook's runtime path is exercised once a live surface wires
// it"). This subtask is that surface, so the two ways a refetch goes wrong are pinned here.
//
// The hook, not App.tsx: `Workspace` cannot mount in jsdom (it needs a session and a live
// entities fetch), and the behaviour under test belongs to the shared primitive either way.

import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { useAsync } from '@invoice-os/api-client'

import { newPolicy, type Policy } from './lib/workflows'

const A: Policy = { ...newPolicy(), id: 'polA', name: 'Standard' }
const B: Policy = { ...newPolicy(), id: 'polB', name: 'Capex' }

function deferred<T>() {
  let settle!: (v: T) => void
  const promise = new Promise<T>((res) => {
    settle = res
  })
  return { promise, settle }
}

afterEach(() => vi.restoreAllMocks())

describe('APPR-09-03 QA: two publishes in flight at once', () => {
  it('the LAST refetch wins, and the earlier one’s late answer is discarded', async () => {
    const first = deferred<Policy[]>()
    const second = deferred<Policy[]>()
    let calls = 0
    const producer = () => (++calls === 1 ? first.promise : second.promise)

    const { result } = renderHook(() => useAsync<Policy[]>(producer, { immediate: false }))
    expect(result.current.status, 'a non-immediate hook must start idle').toBe('idle')

    act(() => result.current.run())
    act(() => result.current.run())
    expect(calls, 'both refetches must actually have called the producer').toBe(2)

    await act(async () => second.settle([B]))
    expect(result.current.data?.map((p) => p.id), 'the second refetch must land').toEqual(['polB'])

    // The first request answers late — the tenant state it describes is one publish old.
    await act(async () => first.settle([A]))
    expect(result.current.data?.map((p) => p.id), 'a stale refetch overwrote a newer one').toEqual(['polB'])
    expect(result.current.status).toBe('ready')
  })

  it('a refetch that fails AFTER a newer one succeeded does not turn the surface into an error', async () => {
    const first = deferred<Policy[]>()
    const second = deferred<Policy[]>()
    let calls = 0
    // The rejection is attached before either run, so nothing is ever an unhandled rejection.
    const failing = first.promise.then(() => Promise.reject(new Error('gateway is down')))
    const producer = () => (++calls === 1 ? failing : second.promise)

    const { result } = renderHook(() => useAsync<Policy[]>(producer, { immediate: false }))
    act(() => result.current.run())
    act(() => result.current.run())

    await act(async () => second.settle([B]))
    expect(result.current.status).toBe('ready')

    await act(async () => first.settle([A]))
    expect(result.current.status, 'a stale failure blanked a landed list').toBe('ready')
    expect(result.current.error).toBeNull()
    expect(result.current.data?.map((p) => p.id)).toEqual(['polB'])
  })
})

describe('APPR-09-03 QA: a publish refetch that answers after the screen is gone', () => {
  it('resolving after unmount neither throws nor logs', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const late = deferred<Policy[]>()

    const { result, unmount } = renderHook(() => useAsync<Policy[]>(() => late.promise, { immediate: false }))
    act(() => result.current.run())
    unmount()

    await act(async () => late.settle([A]))

    // The cleanup bumps `runId`, so the resolution is dropped before it can dispatch into a
    // torn-down tree. React 18 no longer warns on that, so console.error is the only signal
    // an actual crash would leave.
    expect(spy, `unmounted refetch logged: ${spy.mock.calls.map((c) => String(c[0])).join(' | ')}`).not.toHaveBeenCalled()
  })
})
