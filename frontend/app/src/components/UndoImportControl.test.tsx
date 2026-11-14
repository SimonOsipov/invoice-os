// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import * as importApi from '../lib/importApi'
import type { PlatformCtx } from '../types'
import { UndoImportControl } from './UndoImportControl'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})
beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

const ctx = { authedFetch: vi.fn() } as unknown as PlatformCtx
const OK = { id: 'b1', status: 'undone' as const, discarded_invoices: 2 }

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function arm(onSettled = vi.fn()) {
  const view = render(<UndoImportControl ctx={ctx} batchId="b1" onSettled={onSettled} />)
  fireEvent.click(screen.getByTestId('undo-import'))
  return { onSettled, ...view }
}

describe('UndoImportControl (SECAUD-50-04)', () => {
  it('arming then confirming posts once and refetches', async () => {
    const d = deferred<typeof OK>()
    const spy = vi.spyOn(importApi, 'undoImport').mockReturnValue(d.promise)
    const { onSettled } = arm()
    expect(screen.getByTestId('undo-import-prompt').textContent).toBe("Discard this import's invoices and release the file?")
    fireEvent.click(screen.getByTestId('undo-import-cancel'))
    fireEvent.click(screen.getByTestId('undo-import'))
    fireEvent.click(screen.getByTestId('undo-import-confirm'))

    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy.mock.calls[0][2]).toBe('b1')
    expect((screen.getByTestId('undo-import-confirm') as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByTestId('undo-import-cancel') as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByTestId('undo-import-confirm').textContent).toBe('Undoing…')
    expect(onSettled).not.toHaveBeenCalled()

    await act(async () => d.resolve(OK))
    expect(onSettled).toHaveBeenCalledTimes(1)
  })

  it('cancel returns to idle without a request', () => {
    const spy = vi.spyOn(importApi, 'undoImport')
    arm()
    fireEvent.click(screen.getByTestId('undo-import-cancel'))
    expect(screen.getByTestId('undo-import')).toBeTruthy()
    expect(screen.queryByTestId('undo-import-prompt')).toBeNull()
    expect(spy).not.toHaveBeenCalled()
  })

  it('double click posts once', async () => {
    const d = deferred<typeof OK>()
    const spy = vi.spyOn(importApi, 'undoImport').mockReturnValue(d.promise)
    arm()
    const confirm = screen.getByTestId('undo-import-confirm')
    act(() => {
      fireEvent.click(confirm)
      fireEvent.click(confirm)
    })
    expect(spy).toHaveBeenCalledTimes(1)
    await act(async () => d.resolve(OK))
  })

  it('a 409 shows the message and refetches', async () => {
    vi.spyOn(importApi, 'undoImport').mockRejectedValue(new ApiError('http', 'only a completed import can be undone', 409))
    const { onSettled } = arm()
    fireEvent.click(screen.getByTestId('undo-import-confirm'))
    await waitFor(() => expect(screen.getByTestId('undo-import-error').textContent).toBe('only a completed import can be undone'))
    expect(onSettled).toHaveBeenCalledTimes(1)
  })

  it('a lost response refetches', async () => {
    vi.spyOn(importApi, 'undoImport').mockRejectedValue(new ApiError('network', 'Network error'))
    const { onSettled } = arm()
    fireEvent.click(screen.getByTestId('undo-import-confirm'))
    await waitFor(() => expect(screen.getByTestId('undo-import-error').textContent).toBeTruthy())
    expect(onSettled).toHaveBeenCalledTimes(1)
  })

  it('a 401 does not refetch', async () => {
    vi.spyOn(importApi, 'undoImport').mockRejectedValue(new ApiError('http', 'unauthorized', 401))
    const { onSettled } = arm()
    fireEvent.click(screen.getByTestId('undo-import-confirm'))
    await waitFor(() => expect((screen.getByTestId('undo-import-confirm') as HTMLButtonElement).disabled).toBe(false))
    expect((screen.getByTestId('undo-import-cancel') as HTMLButtonElement).disabled).toBe(false)
    expect(onSettled).not.toHaveBeenCalled()
  })

  it('unmount while pending calls nothing', async () => {
    const d = deferred<typeof OK>()
    vi.spyOn(importApi, 'undoImport').mockReturnValue(d.promise)
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { onSettled, unmount } = arm()
    fireEvent.click(screen.getByTestId('undo-import-confirm'))
    unmount()
    await act(async () => d.resolve(OK))
    expect(onSettled).not.toHaveBeenCalled()
    expect(errors).not.toHaveBeenCalled()
  })

  it('a success shows the done line and refetches', async () => {
    vi.spyOn(importApi, 'undoImport').mockResolvedValue(OK)
    const { onSettled } = arm()
    fireEvent.click(screen.getByTestId('undo-import-confirm'))
    await waitFor(() =>
      expect(screen.getByTestId('undo-import-done').textContent).toBe('Import undone. The file can now be imported for another client.'),
    )
    expect(onSettled).toHaveBeenCalledTimes(1)
  })
})
