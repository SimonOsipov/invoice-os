// Shared bare-fetch download: the body is a file, and apiFetch always res.json()s.
import { ApiError, readBody, reportApiFailure } from '@invoice-os/api-client'

import { isPromiseLike } from './authedFetch'

// Whole-string unquoted grammar (contract 8.1 forbids a filename="([^"]+)" regex): a quoted
// value or a trailing parameter fails outright and falls back. EB-01-7, EB-01-9.
const DISPOSITION_RE = /^attachment; filename=([A-Za-z0-9._-]+)$/

function dispositionFilename(header: string | null, fallback: string): string {
  return header?.match(DISPOSITION_RE)?.[1] ?? fallback
}

// Bare fetch, not authedFetch: the body is a file and apiFetch always res.json()s. blob(),
// not arrayBuffer(). The fetch is wrapped only to report; the same error is rethrown, so an
// abort reaches the caller that owns the controller untranslated. EB-01-6, EB-01-10.
export async function fetchAttachment(
  getToken: () => string | null | Promise<string | null>,
  url: string,
  fallbackFilename: string,
  signal?: AbortSignal,
): Promise<{ blob: Blob; filename: string }> {
  // A SessionEndedError from the getter rejects here, before any request.
  const pending = getToken()
  const token = isPromiseLike(pending) ? await pending : pending
  let res: Response
  try {
    res = await fetch(url, { headers: { Authorization: `Bearer ${token}` }, signal })
  } catch (e) {
    reportApiFailure(e, { method: 'GET', url, signal })
    throw e
  }

  if (!res.ok) {
    // apiFetch's !res.ok block, not fetchDocumentBytes -- that one never
    // reads the body and loses the sentence EB-01-8 and EB-01-12 require.
    let body: unknown
    let msg = res.statusText
    try {
      body = await res.json()
      if (body && typeof body === 'object' && 'error' in body) {
        msg = String((body as { error: unknown }).error)
      }
    } catch {
      // best-effort -- no JSON body to read; fall back to statusText.
    }
    const error = new ApiError('http', msg, res.status, body)
    reportApiFailure(error, { method: 'GET', url, signal })
    throw error
  }

  return {
    blob: await readBody(() => res.blob(), { method: 'GET', url, signal }),
    filename: dispositionFilename(res.headers.get('Content-Disposition'), fallbackFilename),
  }
}
