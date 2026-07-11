// RED specs (M3-06-02, A1-A7) — pin the asyncReducer/resolveStatus/toApiError contract
// (async-state.ts) before the executor implements the bodies. Every function under test
// here is synchronous and pure, so calling the (currently throwing) stub inside an
// `expect(...)` argument fails the test with "Error: not implemented" — a normal Vitest
// assertion failure, not a compile/module error. No async/throw-tolerant wrappers are
// needed here (unlike client.test.ts's apiFetch specs): these stubs throw synchronously
// and the real implementations return synchronously, so the same call shape works both
// RED and green.
//
// useAsync itself is NOT unit-tested here — Decision (i) in the story: hook logic is
// covered by testing the extracted pure reducer + helpers; the hook's runtime path is
// exercised once a live surface wires it (M3-08/09).
import { describe, expect, it } from 'vitest'

import { ApiError } from './client'
import { asyncReducer, type AsyncState, initialState, resolveStatus, toApiError } from './async-state'

describe('initialState', () => {
  it('A1: immediate:false starts idle with null data/error; immediate:true starts loading', () => {
    expect(initialState<unknown>(false)).toEqual({ status: 'idle', data: null, error: null })
    expect(initialState<unknown>(true)).toEqual({ status: 'loading', data: null, error: null })
  })
})

describe('asyncReducer', () => {
  it('A2: start transitions idle -> loading, clearing data/error', () => {
    const idle: AsyncState<unknown> = { status: 'idle', data: null, error: null }

    expect(asyncReducer(idle, { type: 'start' })).toEqual({ status: 'loading', data: null, error: null })
  })

  it('A3: success with a non-empty array (default isEmpty) resolves ready with data, matching resolveStatus for the same input', () => {
    const loading: AsyncState<{ x: number }[]> = { status: 'loading', data: null, error: null }
    const data = [{ x: 1 }]

    const result = asyncReducer(loading, { type: 'success', data })

    expect(result.status).toBe(resolveStatus(data))
    expect(result).toEqual({ status: 'ready', data, error: null })
  })

  it('A4: success with an empty array (default isEmpty) resolves empty, clearing data (mirrors start/error)', () => {
    const loading: AsyncState<unknown[]> = { status: 'loading', data: null, error: null }

    const result = asyncReducer(loading, { type: 'success', data: [] })

    expect(result.status).toBe(resolveStatus([]))
    expect(result).toEqual({ status: 'empty', data: null, error: null })
  })

  it('A5: error transitions loading -> error, setting the same ApiError instance and clearing data', () => {
    const loading: AsyncState<unknown> = { status: 'loading', data: null, error: null }
    const err = new ApiError('http', 'boom', 500)

    const result = asyncReducer(loading, { type: 'error', error: err })

    // Split out of toEqual on purpose: toEqual's Error-object handling can compare only
    // `.message` on nested Error/ApiError fields, which would under-assert "same
    // instance". toBe (Object.is identity) is the precise check the spec calls for.
    expect(result.status).toBe('error')
    expect(result.data).toBeNull()
    expect(result.error).toBe(err)
  })
})

describe('resolveStatus (default isEmpty predicate)', () => {
  it('A6: null and [] resolve empty; a non-empty array and a plain object resolve ready', () => {
    expect(resolveStatus(null)).toBe('empty')
    expect(resolveStatus([])).toBe('empty')
    expect(resolveStatus([{ x: 1 }])).toBe('ready')
    expect(resolveStatus({})).toBe('ready')
  })
})

describe('toApiError', () => {
  it('A7a: passes an existing ApiError through unchanged (same instance, kind preserved)', () => {
    const original = new ApiError('http', 'x', 400)

    const result = toApiError(original)

    expect(result).toBe(original)
    expect(result.kind).toBe('http')
  })

  it('A7b: wraps a generic Error as ApiError{kind:"network"}', () => {
    const result = toApiError(new Error('generic'))

    expect(result).toBeInstanceOf(ApiError)
    expect(result.kind).toBe('network')
  })
})
