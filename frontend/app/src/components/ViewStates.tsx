// Red stub: RESKIN2-02-01 replaces the bodies.
import type { ReactNode } from 'react'
import type { ApiError } from '@invoice-os/api-client'

export function Loading(_props: { label?: string }): ReactNode {
  return null
}

export function ErrorState(_props: { error: ApiError; onRetry?: () => void }): ReactNode {
  return null
}

export function EmptyState(_props: {
  title?: string
  message?: string
  glyph?: ReactNode
  dense?: boolean
  messageMaxWidth?: number
  children?: ReactNode
}): ReactNode {
  return null
}
