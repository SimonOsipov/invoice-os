import { ApiError, gatewayBase, reportApiFailure } from '@invoice-os/api-client'
import { CONSENT_TEXT } from './copy'
import { toImportMapping, type Mapping, type Suggestion } from './fields'

export type Result<T> = { ok: true; value: T } | { ok: false; status: number | null; error: string | null }

export interface RunInput {
  email: string
  file: File
  mapping?: { headerRow: number; fields: Mapping }
}

export interface RunAnswer {
  status: string
  checks_left: number
}

// Part order is FREECHK-03 D4: the gateway refuses a file part before email and consent_text.
export function runForm(i: RunInput): FormData {
  const f = new FormData()
  f.append('email', i.email.trim())
  f.append('consent_text', CONSENT_TEXT)
  if (i.mapping) {
    f.append('header_row', String(i.mapping.headerRow))
    f.append('mapping', JSON.stringify(toImportMapping(i.mapping.fields)))
  }
  f.append('file', i.file, i.file.name)
  return f
}

// No Content-Type header: the browser sets the multipart boundary.
async function post<T>(path: string, body: FormData): Promise<Result<T>> {
  const base = gatewayBase()
  if (base === null) return { ok: false, status: null, error: null }
  const req = { method: 'POST', url: `${base}${path}` }
  let res: Response
  try {
    res = await fetch(req.url, { method: 'POST', body })
  } catch (e) {
    reportApiFailure(e, req)
    return { ok: false, status: null, error: null }
  }
  let json: unknown = null
  try {
    json = await res.json()
  } catch {
    // an HTML or empty body carries no error word
  }
  if (res.ok && json !== null) return { ok: true, value: json as T }
  if (res.status >= 500) reportApiFailure(new ApiError('http', `HTTP ${res.status}`, res.status), req)
  const error = (json as { error?: unknown } | null)?.error
  return { ok: false, status: res.status, error: typeof error === 'string' ? error : null }
}

export function suggestMapping(file: File): Promise<Result<Suggestion>> {
  const f = new FormData()
  f.append('file', file, file.name)
  return post<Suggestion>('/readiness-check/mapping', f)
}

export const runCheck = (i: RunInput): Promise<Result<RunAnswer>> => post<RunAnswer>('/readiness-check/run', runForm(i))
