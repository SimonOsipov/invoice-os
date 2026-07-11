// Shared typed gateway client (M3-06). gatewayBase() reads the configured gateway
// base URL; apiFetch<T>() is the single authenticated-request path every wired
// surface uses — it injects the Bearer auth header, JSON-serializes the request
// body, and normalizes every failure mode (network / non-2xx / malformed body)
// into a typed ApiError so callers handle gateway errors uniformly.

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
  const v = (import.meta.env.VITE_GATEWAY_URL ?? '').trim().replace(/\/+$/, '')
  return v || null
}

// Resolves parsed JSON on 2xx; REJECTS with ApiError on network / non-2xx / malformed-body.
export async function apiFetch<T>(url: string, opts?: ApiFetchOptions): Promise<T> {
  const headers = new Headers()
  if (opts?.token) {
    headers.set('Authorization', 'Bearer ' + opts.token)
  }

  let body: string | undefined
  if (opts?.body !== undefined) {
    headers.set('Content-Type', 'application/json')
    body = JSON.stringify(opts.body)
  }

  let res: Response
  try {
    res = await fetch(url, {
      method: opts?.method ?? 'GET',
      headers,
      body,
      signal: opts?.signal,
    })
  } catch (e) {
    throw new ApiError('network', e instanceof Error ? e.message : String(e), null)
  }

  if (!res.ok) {
    let responseBody: unknown
    let msg = res.statusText
    try {
      responseBody = await res.json()
      if (responseBody && typeof responseBody === 'object' && 'error' in responseBody) {
        msg = String((responseBody as { error: unknown }).error)
      }
    } catch {
      // best-effort — no JSON body to read; fall back to statusText.
    }
    throw new ApiError('http', msg, res.status, responseBody)
  }

  try {
    return (await res.json()) as T
  } catch {
    throw new ApiError('malformed', 'malformed response body', res.status)
  }
}
