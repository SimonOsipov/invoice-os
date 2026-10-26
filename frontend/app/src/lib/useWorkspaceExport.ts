import { useCallback, useEffect, useRef, useState } from 'react'

import { toApiError } from '@invoice-os/api-client'

import type { AuthedFetch } from './portfolio'
import { startWorkspaceExport } from './workspaceExport'

export type WorkspaceExportStatus = null | { kind: 'started' } | { kind: 'error'; message: string }

// Issues no request until `start`; reads the latest authedFetch through a ref at click time.
export function useWorkspaceExport(authedFetch: AuthedFetch, base: string) {
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState<WorkspaceExportStatus>(null)
  const latest = useRef({ authedFetch, base })
  const running = useRef(false)
  const mounted = useRef(true)
  useEffect(() => {
    latest.current = { authedFetch, base }
  })
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  const start = useCallback(async () => {
    if (running.current) return
    running.current = true
    setBusy(true)
    setStatus(null)
    let next: WorkspaceExportStatus
    try {
      await startWorkspaceExport(latest.current.authedFetch, latest.current.base)
      next = { kind: 'started' }
    } catch (e) {
      next = { kind: 'error', message: toApiError(e).message }
    }
    running.current = false
    if (mounted.current) {
      setStatus(next)
      setBusy(false)
    }
  }, [])

  return { start, busy, status }
}
