// Shared typed gateway client (M3-06). gatewayBase() reads the configured gateway
// base URL; apiFetch<T>() is the single authenticated-request path every wired
// surface uses — it injects the Bearer auth header, JSON-serializes the request
// body, and normalizes every failure mode (network / non-2xx / malformed body)
// into a typed ApiError so callers handle gateway errors uniformly.
//
// NOTE (M3-06-01, QA Mode A): gatewayBase() and apiFetch() are intentionally
// stubbed to throw below — the RED specs in client.test.ts pin the contract;
// the executor implements the real bodies to turn them green.

export type ApiErrorKind = 'network' | 'http' | 'malformed'

export class ApiError extends Error {
  readonly kind: ApiErrorKind
  readonly status: number | null // HTTP status for 'http'; null for 'network' / 'malformed'
  readonly body: unknown // best-effort parsed gateway {error} envelope for 'http', else undefined

  constructor(kind: ApiErrorKind, message: string, status: number | null = null, body: unknown = undefined) {
    super(message)
    this.name = 'ApiError'
    this.kind = kind
    this.status = status
    this.body = body
  }
}

export interface ApiFetchOptions {
  method?: string // default 'GET'
  body?: unknown // JSON-serialized + Content-Type: application/json when present
  token?: string | null // when truthy, injects Authorization: Bearer <token>
  signal?: AbortSignal
}

// Configured gateway base (trailing slashes stripped) or null when VITE_GATEWAY_URL is empty/unset.
export function gatewayBase(): string | null {
  throw new Error('not implemented')
}

// Resolves parsed JSON on 2xx; REJECTS with ApiError on network / non-2xx / malformed-body.
export function apiFetch<T>(_url: string, _opts?: ApiFetchOptions): Promise<T> {
  throw new Error('not implemented')
}
