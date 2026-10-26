// @vitest-environment jsdom
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import type { AuthedFetch } from './portfolio'
import { useWorkspaceExport } from './useWorkspaceExport'

const base = 'https://gw.test'

afterEach(() => vi.restoreAllMocks())

describe('useWorkspaceExport', () => {
  it('useWorkspaceExport: busy, then started', async () => {
    const submit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => {})
    let release!: (v: { code: string }) => void
    const mint = new Promise<{ code: string }>((r) => (release = r))
    let mints = 0
    const f = (async (url: string) => {
      if (!url.endsWith('/auth/export-code')) return undefined
      mints++
      return mint
    }) as AuthedFetch
    const { result } = renderHook(() => useWorkspaceExport(f, base))

    act(() => void result.current.start())
    await waitFor(() => expect(mints).toBe(1))
    expect(result.current.busy).toBe(true)
    act(() => void result.current.start())
    expect(mints).toBe(1)

    await act(async () => release({ code: 'c' }))
    expect(result.current.status).toEqual({ kind: 'started' })
    expect(result.current.busy).toBe(false)
    expect(submit).toHaveBeenCalledTimes(1)
  })

  it('useWorkspaceExport: a failed mint shows the sentence', async () => {
    const submit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => {})
    const f = (async (url: string) => {
      if (url.endsWith('/auth/export-code')) throw new ApiError('http', 'unauthorized', 401)
      return undefined
    }) as AuthedFetch
    const { result } = renderHook(() => useWorkspaceExport(f, base))
    await act(async () => result.current.start())
    expect(result.current.status).toEqual({ kind: 'error', message: 'unauthorized' })
    expect(submit).not.toHaveBeenCalled()
  })

  it('useWorkspaceExport: a preflight refusal shows the sentence', async () => {
    const sentence = 'an export of this workspace is already running'
    const f = (async () => {
      throw new ApiError('http', sentence, 409)
    }) as AuthedFetch
    const { result } = renderHook(() => useWorkspaceExport(f, base))
    await act(async () => result.current.start())
    expect(result.current.status).toEqual({ kind: 'error', message: sentence })
    expect(result.current.busy).toBe(false)
  })

  it('useWorkspaceExport: no request before start, then the latest authedFetch is used', async () => {
    vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => {})
    const old = vi.fn(async () => undefined) as unknown as AuthedFetch
    const urls: string[] = []
    const fresh = (async (url: string) => {
      urls.push(url)
      return url.endsWith('/auth/export-code') ? { code: 'c' } : undefined
    }) as AuthedFetch
    const { result, rerender } = renderHook(({ f }) => useWorkspaceExport(f, base), { initialProps: { f: old } })
    expect(old).not.toHaveBeenCalled()
    rerender({ f: fresh })
    expect(urls).toEqual([])
    await act(async () => result.current.start())
    expect(old).not.toHaveBeenCalled()
    expect(urls).toHaveLength(2)
    expect(result.current.status).toEqual({ kind: 'started' })
  })
})
