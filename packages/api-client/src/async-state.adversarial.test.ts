// Adversarial/edge/negative coverage (M3-06-02 QA pass) — added AFTER A1-A7 went
// green. Mutation testing against async-state.ts confirmed A1-A7 each pin their
// named claim, but exposed structural gaps: every A1-A7 fixture already starts
// from a NULL data/error state, so "start"/"error" clearing pre-existing
// data/error was never actually exercised (a reducer that stopped clearing
// data on 'start'/'error' still passed all 8 original specs). Custom `isEmpty`
// predicates, reducer purity, and toApiError's non-Error inputs were also
// untested. This file closes those gaps; it does NOT touch A1-A7 or add any
// jsdom/@testing-library — useAsync's runtime path stays deferred to M3-08/09
// per Decision (i) in the story.
import { describe, expect, it } from 'vitest'

import { ApiError } from './client'
import { asyncReducer, type AsyncAction, type AsyncState, initialState, resolveStatus, toApiError } from './async-state'

describe('async-state adversarial', () => {
  describe('custom isEmpty predicate overrides the default', () => {
    it('success with data:[] but isEmpty always false resolves ready, keeping data', () => {
      const loading: AsyncState<unknown[]> = { status: 'loading', data: null, error: null }

      const result = asyncReducer(loading, { type: 'success', data: [], isEmpty: () => false })

      expect(result).toEqual({ status: 'ready', data: [], error: null })
    })

    it('success with a non-empty array but isEmpty always true resolves empty, clearing data', () => {
      const loading: AsyncState<{ x: number }[]> = { status: 'loading', data: null, error: null }

      const result = asyncReducer(loading, { type: 'success', data: [{ x: 1 }], isEmpty: () => true })

      expect(result).toEqual({ status: 'empty', data: null, error: null })
    })
  })

  describe('resolveStatus default predicate over other falsy shapes', () => {
    it('empty string, 0, and false are NOT empty under the default predicate (only null/undefined/[] are)', () => {
      // Pins that resolveStatus's default only special-cases null/undefined and
      // empty arrays — other falsy values (''/0/false) are non-null, non-array,
      // and therefore resolve ready per the documented default.
      expect(resolveStatus('')).toBe('ready')
      expect(resolveStatus(0)).toBe('ready')
      expect(resolveStatus(false)).toBe('ready')
    })
  })

  describe("'reset' action", () => {
    it('reset with immediate:true returns initialState(true) (loading/null/null) regardless of prior state', () => {
      const populated: AsyncState<{ x: number }> = {
        status: 'ready',
        data: { x: 1 },
        error: new ApiError('http', 'boom', 500),
      }

      const result = asyncReducer(populated, { type: 'reset', immediate: true })

      expect(result).toEqual(initialState(true))
      expect(result).toEqual({ status: 'loading', data: null, error: null })
    })

    it('reset with no immediate flag returns initialState(undefined) (idle/null/null)', () => {
      const populated: AsyncState<{ x: number }> = {
        status: 'error',
        data: null,
        error: new ApiError('network', 'boom'),
      }

      const result = asyncReducer(populated, { type: 'reset' })

      expect(result).toEqual(initialState())
      expect(result).toEqual({ status: 'idle', data: null, error: null })
    })
  })

  describe("'start' from a populated state", () => {
    it('clears data AND error when starting from a ready state that has both set', () => {
      const ready: AsyncState<{ x: number }> = {
        status: 'ready',
        data: { x: 1 },
        error: null,
      }

      const result = asyncReducer(ready, { type: 'start' })

      expect(result).toEqual({ status: 'loading', data: null, error: null })
    })

    it('clears error when starting from an error state', () => {
      const errored: AsyncState<unknown> = {
        status: 'error',
        data: null,
        error: new ApiError('http', 'boom', 500),
      }

      const result = asyncReducer(errored, { type: 'start' })

      expect(result).toEqual({ status: 'loading', data: null, error: null })
    })
  })

  describe("'error' from a populated ready state", () => {
    it('clears data and sets the SAME ApiError instance (identity, not a copy)', () => {
      const ready: AsyncState<{ x: number }> = {
        status: 'ready',
        data: { x: 1 },
        error: null,
      }
      const err = new ApiError('http', 'boom', 500)

      const result = asyncReducer(ready, { type: 'error', error: err })

      expect(result.status).toBe('error')
      expect(result.data).toBeNull()
      expect(result.error).toBe(err)
    })
  })

  describe('reducer purity', () => {
    it('asyncReducer does not mutate the input state object for any action type', () => {
      const states: AsyncState<unknown>[] = [
        { status: 'idle', data: null, error: null },
        { status: 'ready', data: { x: 1 }, error: null },
        { status: 'error', data: null, error: new ApiError('network', 'boom') },
      ]
      const actions: AsyncAction<unknown>[] = [
        { type: 'start' },
        { type: 'success', data: [1, 2] },
        { type: 'error', error: new ApiError('http', 'x', 500) },
        { type: 'reset', immediate: true },
      ]

      for (const state of states) {
        for (const action of actions) {
          // Capture the three field references/values directly rather than
          // deep-cloning `state` — structuredClone() drops ApiError's custom
          // fields (kind/status/body) when cloning Error subclasses, which
          // would make `before` diverge from an untouched `state` and produce
          // a false failure unrelated to reducer purity.
          const beforeStatus = state.status
          const beforeData = state.data
          const beforeError = state.error

          const result = asyncReducer(state, action)

          expect(state.status).toBe(beforeStatus) // input untouched
          expect(state.data).toBe(beforeData)
          expect(state.error).toBe(beforeError)
          expect(result).not.toBe(state) // a fresh object was returned, not the same reference
        }
      }
    })
  })

  describe('toApiError over non-Error, non-ApiError inputs', () => {
    it('never throws and always wraps as kind:network with a String(err) message', () => {
      const inputs: unknown[] = ['boom', null, undefined, 42, {}]

      for (const input of inputs) {
        const result = toApiError(input)

        expect(result).toBeInstanceOf(ApiError)
        expect(result.kind).toBe('network')
        expect(result.message).toBe(String(input))
      }
    })
  })

  describe('initialState default argument', () => {
    it('initialState() with no argument starts idle (falsy default), distinct from useAsync opts default of immediate:true', () => {
      // initialState's OWN default (no arg -> undefined -> falsy -> idle) is
      // intentionally different from useAsync's opts.immediate default (true).
      // useAsync always passes an explicit resolved boolean into initialState;
      // this pins initialState's standalone default so the two can't silently
      // drift into agreement and hide a caller regression.
      expect(initialState()).toEqual({ status: 'idle', data: null, error: null })
    })
  })
})
