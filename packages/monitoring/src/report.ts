import { captureException } from '@sentry/react'
import { markReported } from './reported'
import { apiRoute } from './scrub'

export interface ApiFailureInput {
  kind: 'network' | 'http' | 'malformed'
  status: number | null
  method: string
  url: string
  error: unknown
}

// Fixed message and fingerprint so one failing route is one issue (D-10); the raw error never leaves.
export function captureApiFailure(f: ApiFailureInput): void {
  const method = f.method.toUpperCase()
  const route = apiRoute(f.url)
  const status = String(f.status ?? '-')
  markReported(f.error)
  const err = new Error(`${f.kind} ${status} ${method} ${route}`)
  err.name = 'ApiFailure'
  captureException(err, { fingerprint: ['api-failure', f.kind, status, method, route], tags: { 'api.kind': f.kind, 'api.status': status } })
}
