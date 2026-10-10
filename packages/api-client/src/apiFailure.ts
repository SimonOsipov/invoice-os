import { captureApiFailure } from '@invoice-os/monitoring/report'
import { ApiError } from './client'

const isNamed = (v: unknown, name: string): boolean => v instanceof DOMException && v.name === name

// Q12: a caller cancel is not an issue; a timeout is a network failure.
export function countsAsIssue(err: unknown, signal?: AbortSignal): boolean {
  if (signal?.aborted) {
    if (!isNamed(signal.reason, 'TimeoutError')) return false
    // Some browsers reject a timed-out fetch with AbortError; the reason names the cause.
    if (isNamed(err, 'AbortError')) return true
  }
  if (isNamed(err, 'AbortError')) return false
  if (err instanceof ApiError) {
    return err.kind === 'http' ? err.status !== null && err.status >= 500 && err.status <= 599 : true
  }
  return isNamed(err, 'TimeoutError') || err instanceof TypeError
}

export function reportApiFailure(err: unknown, req: { method: string; url: string; signal?: AbortSignal }): void {
  try {
    if (!countsAsIssue(err, req.signal)) return
    const api = err instanceof ApiError ? err : null
    captureApiFailure({ kind: api?.kind ?? 'network', status: api?.status ?? null, method: req.method, url: req.url, error: err })
  } catch {
    // reporting must never change what the transport throws (D-29)
  }
}

// A body read that rejects after headers arrive is the same network failure as a fetch rejection; the same value is rethrown.
export async function readBody<T>(read: () => Promise<T>, req: { method: string; url: string; signal?: AbortSignal }): Promise<T> {
  try {
    return await read()
  } catch (e) {
    reportApiFailure(e, req)
    throw e
  }
}
