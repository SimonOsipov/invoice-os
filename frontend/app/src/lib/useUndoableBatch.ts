import { useEffect, useRef } from 'react'

import { gatewayBase, useAsync } from '@invoice-os/api-client'

import type { PlatformCtx } from '../types'
import { getImportBatch, type ImportBatch } from './importApi'

// Reads the import batch behind an invoice or extraction. A null id makes no request.
// Keeps the last answer through a refetch so an armed control is not unmounted mid-refresh.
export function useUndoableBatch(ctx: PlatformCtx, batchId: string | null) {
  const base = gatewayBase()
  const batch = useAsync<ImportBatch>(
    () => (base && batchId ? getImportBatch(ctx.authedFetch, base, batchId) : Promise.reject(new Error('no import to read'))),
    { immediate: base != null && batchId != null, deps: [batchId] },
  )
  const last = useRef<ImportBatch | null>(null)
  useEffect(() => {
    if (batchId == null || batch.error != null) last.current = null
    else if (batch.data != null) last.current = batch.data
  }, [batchId, batch.data, batch.error])
  const current = batchId == null ? null : (batch.data ?? last.current)
  return { undoable: current?.undoable === true && current.id === batchId, refetch: batch.run }
}
