// Async-state primitives (M3-06-02): a pure state machine — AsyncStatus/AsyncState<T>,
// the pure asyncReducer, and the pure helpers resolveStatus/toApiError — plus the
// useAsync hook, a thin useReducer+useEffect wrapper over the reducer.
//
// STUB (Mode A / RED phase, M3-06-02): types/signatures below are the fixed contract —
// async-state.test.ts (A1-A7) compiles and asserts against this exact shape. Every
// LOGIC-bearing body throws 'not implemented'; the executor replaces the throws with
// real logic without changing these signatures.
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
import type { ApiError } from './client'

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

export function initialState<T>(_immediate?: boolean): AsyncState<T> {
  throw new Error('not implemented')
}

export function asyncReducer<T>(_state: AsyncState<T>, _action: AsyncAction<T>): AsyncState<T> {
  throw new Error('not implemented')
}

// Pure: empty vs ready classification for a resolved value. Default predicate:
// data == null || (Array.isArray(data) && data.length === 0).
export function resolveStatus<T>(_data: T, _isEmpty?: (data: T) => boolean): AsyncStatus {
  throw new Error('not implemented')
}

// Pure: normalizes an unknown catch-value into an ApiError. An existing ApiError passes
// through unchanged; anything else wraps as kind:'network'.
export function toApiError(_err: unknown): ApiError {
  throw new Error('not implemented')
}

// Thin useReducer+useEffect wrapper over asyncReducer — NOT unit-tested here (Decision
// (i) in the story): hook logic is covered via the extracted pure reducer + helpers
// above; the hook's runtime path is exercised once a live surface wires it (M3-08/09).
export function useAsync<T>(
  _producer: () => Promise<T>,
  _opts?: { immediate?: boolean; isEmpty?: (data: T) => boolean; deps?: unknown[] },
): AsyncState<T> & { run: () => void } {
  throw new Error('not implemented')
}
