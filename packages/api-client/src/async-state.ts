// Async-state primitives (M3-06-02): a pure state machine — AsyncStatus/AsyncState<T>,
// the pure asyncReducer, and the pure helpers resolveStatus/toApiError — plus the
// useAsync hook, a thin useReducer+useEffect wrapper over the reducer.
//
// Semantics pinned by the story ("Async-state machine", AC #4) + choices this stub pins
// where the story was silent (flagged to the executor/QA):
// - initialState(immediate): idle when !immediate, loading when immediate. (The
//   immediate:true default lives in useAsync's opts, not here — initialState just takes
//   the already-resolved boolean.)
// - start ⇒ loading, data:null, error:null.
// - success ⇒ status via resolveStatus(data, isEmpty); data:action.data on the 'ready'
//   branch, data:null on the 'empty' branch (PINNED CHOICE: mirrors start/error clearing
//   data on every non-ready transition — the story says "else ready with data" but does
//   not spell out the empty branch's data value); error:null.
// - error ⇒ status:'error', error:action.error (same instance, not a copy), data:null.
// - resolveStatus default isEmpty: data == null || (Array.isArray(data) && data.length === 0).
// - toApiError: an ApiError instance passes through unchanged (same instance); anything
//   else wraps as new ApiError('network', ...).
import { useCallback, useEffect, useReducer, useRef } from 'react'

import { ApiError } from './client'

export type AsyncStatus = 'idle' | 'loading' | 'error' | 'empty' | 'ready'

export interface AsyncState<T> {
  status: AsyncStatus
  data: T | null
  error: ApiError | null
}

// Action shape for the pure reducer. 'success' carries the caller's isEmpty predicate
// (sourced from useAsync's opts) so the reducer itself stays pure — no closure captured.
export type AsyncAction<T> =
  | { type: 'reset'; immediate?: boolean }
  | { type: 'start' }
  | { type: 'success'; data: T; isEmpty?: (data: T) => boolean }
  | { type: 'error'; error: ApiError }

export function initialState<T>(immediate?: boolean): AsyncState<T> {
  return { status: immediate ? 'loading' : 'idle', data: null, error: null }
}

export function asyncReducer<T>(_state: AsyncState<T>, action: AsyncAction<T>): AsyncState<T> {
  switch (action.type) {
    case 'reset':
      return initialState<T>(action.immediate)
    case 'start':
      return { status: 'loading', data: null, error: null }
    case 'success': {
      const status = resolveStatus(action.data, action.isEmpty)
      return { status, data: status === 'ready' ? action.data : null, error: null }
    }
    case 'error':
      return { status: 'error', data: null, error: action.error }
    default: {
      const _exhaustive: never = action
      throw new Error(`asyncReducer: unhandled action type ${JSON.stringify(_exhaustive)}`)
    }
  }
}

// Pure: empty vs ready classification for a resolved value. Default predicate:
// data == null || (Array.isArray(data) && data.length === 0).
export function resolveStatus<T>(data: T, isEmpty?: (data: T) => boolean): AsyncStatus {
  const empty = isEmpty
    ? isEmpty(data)
    : data == null || (Array.isArray(data) && data.length === 0)
  return empty ? 'empty' : 'ready'
}

// Pure: normalizes an unknown catch-value into an ApiError. An existing ApiError passes
// through unchanged; anything else wraps as kind:'network'.
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) {
    return err
  }
  return new ApiError('network', err instanceof Error ? err.message : String(err))
}

// Thin useReducer+useEffect wrapper over asyncReducer — NOT unit-tested here (Decision
// (i) in the story): hook logic is covered via the extracted pure reducer + helpers
// above; the hook's runtime path is exercised once a live surface wires it (M3-08/09).
export function useAsync<T>(
  producer: () => Promise<T>,
  opts?: { immediate?: boolean; isEmpty?: (data: T) => boolean; deps?: unknown[] },
): AsyncState<T> & { run: () => void } {
  const immediate = opts?.immediate ?? true
  const [state, dispatch] = useReducer(asyncReducer<T>, immediate, initialState)
  const runId = useRef(0)

  const run = useCallback(() => {
    const id = ++runId.current
    dispatch({ type: 'start' })
    producer().then(
      (data) => {
        if (id === runId.current) {
          dispatch({ type: 'success', data, isEmpty: opts?.isEmpty })
        }
      },
      (err) => {
        if (id === runId.current) {
          dispatch({ type: 'error', error: toApiError(err) })
        }
      },
    )
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [producer, opts?.isEmpty])

  useEffect(() => {
    if (immediate) {
      run()
    }
    return () => {
      runId.current++
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, opts?.deps ?? [])

  return { ...state, run }
}
