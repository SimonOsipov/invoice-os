// RED specs (AUDIT-08-01, task-664, EB-01-1..EB-01-13) -- pin the evidence-bundle wire
// mirror, the frozen request builder and the two fetches before evidenceBundle.ts's stub
// bodies are filled in. Every spec below fails today on an assertion or on the stub's
// `not implemented` throw -- that IS the correct red reason, not an import/compile error.
// Preview specs mock the injected authedFetch (audit.test.ts); download specs stub the
// global fetch (approvals.test.ts), because the download is a bare fetch by D-08-06.

/// <reference types="node" />

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { ApiError } from '@invoice-os/api-client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { AuditRange } from './auditFilters'
import {
  bundleRequestFor,
  evidenceBundleUrl,
  fetchEvidenceBundle,
  getEvidenceBundlePreview,
  type BundleRequest,
  type EvidenceBundlePreview,
} from './evidenceBundle'

const REPO_ROOT = resolve(__dirname, '../../../..')
const PREVIEW_GO = resolve(REPO_ROOT, 'internal/archive/preview.go')
const MANIFEST_GO = resolve(REPO_ROOT, 'internal/archive/manifest.go')
const MIRROR_TS = resolve(__dirname, 'evidenceBundle.ts')

const BASE = 'https://api.test'
const REQ: BundleRequest = {
  entityId: '11111111-1111-1111-1111-111111111111',
  from: '2026-01-01T00:00:00.000Z',
  to: '2026-03-31T23:59:59.999Z',
}
const FALLBACK = 'ASComply_evidence_fallback_20260101_20260331.zip'

// Copied verbatim from audit.test.ts:14-20. The mirror is hand-maintained and no compiler
// links the two sides, so the tags are the only shared contract.
function jsonTagsOf(source: string, structName: string): string[] {
  const start = source.indexOf(`type ${structName} struct {`)
  if (start === -1) return []
  const end = source.indexOf('}', start)
  const body = source.slice(start, end)
  return [...body.matchAll(/json:"([^",]+)/g)].map((m) => m[1])
}

// Lifts each named interface's own body out of the mirror. Scanning the WHOLE file text
// (audit.test.ts's shape) lets six tags pass for free on unrelated declarations:
// BundleRequest's entityId/from/to cover id/entity/name/from/to, and
// fetchEvidenceBundle's `Promise<{ blob: Blob; filename: string }>` covers filename.
function interfaceBodiesOf(source: string, names: string[]): string[] {
  return names.flatMap((name) => {
    const head = `export interface ${name} {`
    const open = source.indexOf(head)
    if (open === -1) return []
    const end = source.indexOf('}', open + head.length)
    if (end === -1) return []
    return [source.slice(open + head.length, end)]
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('evidence-bundle wire mirror', () => {
  it('EB-01-1 bundleWire_mirrorsPreviewFieldForField', () => {
    const preview = readFileSync(PREVIEW_GO, 'utf8')
    const manifest = readFileSync(MANIFEST_GO, 'utf8')
    const ts = readFileSync(MIRROR_TS, 'utf8')

    const sources: Array<[string, string]> = [
      [preview, 'Preview'],
      [manifest, 'manifestEntity'],
      [manifest, 'manifestPeriod'],
      [manifest, 'manifestCounts'],
    ]

    // Floor: a scan that stops matching returns zero misses, which reads exactly like a
    // clean mirror. Requiring a populated scan is what makes a green result mean something.
    const scanned = sources.filter(([src, name]) => jsonTagsOf(src, name).length > 0)
    expect(scanned).toHaveLength(4)

    // Control needle per Go file -- prove the scan still finds something in each before
    // trusting it to report an absence.
    expect(jsonTagsOf(preview, 'Preview')).toContain('over_limit')
    expect(jsonTagsOf(manifest, 'manifestCounts')).toContain('body_files')

    const tags = sources.flatMap(([src, name]) => jsonTagsOf(src, name))
    expect(tags).toHaveLength(17)

    // TS side: the haystack is the four wire interfaces' bodies, nothing else in the file.
    const bodies = interfaceBodiesOf(ts, ['BundleEntity', 'BundlePeriod', 'BundleCounts', 'EvidenceBundlePreview'])
    expect(bodies, 'all four wire interfaces must still be found in the mirror').toHaveLength(4)

    // Positive control on the extractor itself: prove it still lifts a populated body.
    // A fieldless mirror yields four EMPTY bodies, so a non-empty-haystack check cannot
    // tell "extractor broke" from "stub not implemented" -- this fixture can.
    const probe = interfaceBodiesOf('export interface X {\n  over_limit: boolean\n}\n', ['X'])
    expect(probe).toHaveLength(1)
    expect(probe[0]).toContain('over_limit')

    const haystack = bodies.join('\n')
    const missingOf = (candidates: string[]) => candidates.filter((tag) => !haystack.includes(tag))

    // Negative control: the comparison must still be able to REPORT a miss.
    expect(missingOf(['not_a_real_tag'])).toEqual(['not_a_real_tag'])

    expect(missingOf(tags)).toEqual([])
  })
})

describe('bundleRequestFor', () => {
  it('EB-01-2 bundleRequest_relativePresetClosesAtNow', () => {
    const now = new Date('2026-08-24T10:15:30.123Z')
    const cases: Array<{ preset: AuditRange['preset']; from: string }> = [
      { preset: '24h', from: '2026-08-23T10:15:30.123Z' },
      { preset: '7d', from: '2026-08-17T10:15:30.123Z' },
      { preset: '30d', from: '2026-07-25T10:15:30.123Z' },
    ]
    expect(cases).toHaveLength(3)

    for (const c of cases) {
      const req = bundleRequestFor('e1', { preset: c.preset }, now)
      // The server 400s an open range (`to is required`); closing it at now is the point.
      expect(req, `${c.preset} must produce a closed range`).not.toBeNull()
      expect(req!.from, c.preset).toBe(c.from)
      expect(req!.to, c.preset).toBe('2026-08-24T10:15:30.123Z')
      expect(req!.entityId, c.preset).toBe('e1')
    }
  })

  it('EB-01-3 bundleRequest_customUsesInclusiveDayBounds', () => {
    const now = new Date('2026-08-24T10:15:30.123Z')
    const req = bundleRequestFor('e1', { preset: 'custom', from: '2026-07-01', to: '2026-07-31' }, now)

    expect(req).not.toBeNull()
    // Byte-identical to auditFilters.ts:69,71 -- a date-only endpoint is a 400 (RFC3339).
    expect(req!.from).toBe('2026-07-01T00:00:00.000Z')
    expect(req!.to).toBe('2026-07-31T23:59:59.999Z')
    expect(req!.from).not.toBe('2026-07-01')
    expect(req!.to).not.toBe('2026-07-31')
  })

  it('EB-01-4 bundleRequest_nullWithoutACompanyOrAPeriod', () => {
    const now = new Date('2026-08-24T10:15:30.123Z')
    const complete: AuditRange = { preset: 'custom', from: '2026-07-01', to: '2026-07-31' }
    const nulls: Array<[string, string | null, AuditRange]> = [
      ['null entityId', null, complete],
      ['empty entityId', '', complete],
      ['custom missing from', 'e1', { preset: 'custom', to: '2026-07-31' }],
      ['custom missing to', 'e1', { preset: 'custom', from: '2026-07-01' }],
      ['bare custom', 'e1', { preset: 'custom' }],
    ]
    expect(nulls).toHaveLength(5)

    for (const [label, entityId, range] of nulls) {
      expect(bundleRequestFor(entityId, range, now), label).toBeNull()
    }

    // Positive control: without it every row above is satisfied by a function returning
    // null unconditionally, i.e. it proves nothing.
    const ok = bundleRequestFor('e1', complete, now)
    expect(ok).not.toBeNull()
    expect(ok).toEqual({ entityId: 'e1', from: '2026-07-01T00:00:00.000Z', to: '2026-07-31T23:59:59.999Z' })
  })
})

describe('getEvidenceBundlePreview', () => {
  it('EB-01-5 bundlePreview_sendsAllThreeParamsOnce', async () => {
    const payload = {
      entity: { id: REQ.entityId, name: 'Honeywell Group', tin: null },
      period: {
        from: '2026-01-01T00:00:00Z',
        to: '2026-03-31T23:59:59Z',
        bounds: 'inclusive',
        basis: 'invoices.created_at',
      },
      filename: 'ASComply_evidence_Honeywell-Group_20260101_20260331.zip',
      counts: { invoices: 3, status_transitions: 4, submissions: 2, exchange_attempts: 2, body_files: 1 },
      over_limit: false,
    } as unknown as EvidenceBundlePreview
    const authedFetch = vi.fn().mockResolvedValue(payload)

    const got = await getEvidenceBundlePreview(authedFetch, BASE, REQ)

    expect(authedFetch).toHaveBeenCalledTimes(1)
    const url = new URL(String(authedFetch.mock.calls[0][0]))
    // Pathname, not a substring: a stub hitting the download route would otherwise pass.
    expect(url.pathname.endsWith('/api/invoice/v1/evidence-bundle/preview')).toBe(true)
    expect(url.searchParams.get('entity_id')).toBe(REQ.entityId)
    expect(url.searchParams.get('from')).toBe(REQ.from)
    expect(url.searchParams.get('to')).toBe(REQ.to)
    expect([...url.searchParams.keys()]).toHaveLength(3)
    // Passthrough, not a transform -- no nil-slice field exists to normalise (D-49).
    expect(got).toBe(payload)
  })

  it('EB-01-11 bundlePreview_rethrowsTheApiErrorUnchanged', async () => {
    const err = new ApiError('http', 'not found', 404, { error: 'not found' })
    const authedFetch = vi.fn().mockRejectedValue(err)

    // Same object, not a copy: apiFetch already lifted the server sentence, so this
    // function carries no try/catch of its own (AC-3).
    await expect(getEvidenceBundlePreview(authedFetch, BASE, REQ)).rejects.toBe(err)
  })
})

describe('evidenceBundleUrl', () => {
  it('EB-01-13 bundleUrl_pointsAtTheDownloadRouteWithAllThreeParams', () => {
    const raw = evidenceBundleUrl(BASE, REQ)
    const url = new URL(raw)

    // The download route, never /preview -- the one substitution worth catching, since a
    // preview URL here would still return 200 JSON and only fail at res.blob().
    expect(url.pathname).toBe('/api/invoice/v1/evidence-bundle')
    expect(url.pathname.endsWith('/preview')).toBe(false)

    expect(url.searchParams.get('entity_id')).toBe(REQ.entityId)
    expect(url.searchParams.get('from')).toBe(REQ.from)
    expect(url.searchParams.get('to')).toBe(REQ.to)
    expect([...url.searchParams.keys()]).toHaveLength(3)

    // A value needing encoding: a literal space is not legal in a URL, so it must reach
    // the wire encoded and round-trip back unchanged.
    const odd = evidenceBundleUrl(BASE, { entityId: 'a b/c', from: REQ.from, to: REQ.to })
    expect(odd).not.toContain(' ')
    expect(new URL(odd).searchParams.get('entity_id')).toBe('a b/c')
  })
})

interface MockResponse {
  ok: boolean
  status: number
  statusText: string
  headers: { get: (name: string) => string | null }
  blob: ReturnType<typeof vi.fn>
  arrayBuffer: ReturnType<typeof vi.fn>
  json: () => Promise<unknown>
}

function okBundle(disposition: string | null, blob = new Blob(['zip'], { type: 'application/zip' })): MockResponse {
  return {
    ok: true,
    status: 200,
    statusText: 'OK',
    headers: { get: (name: string) => (name.toLowerCase() === 'content-disposition' ? disposition : null) },
    blob: vi.fn().mockResolvedValue(blob),
    arrayBuffer: vi.fn().mockResolvedValue(new ArrayBuffer(3)),
    json: () => Promise.resolve({}),
  }
}

function errorBundle(status: number, statusText: string, body: unknown, jsonThrows = false): MockResponse {
  return {
    ok: false,
    status,
    statusText,
    headers: { get: () => null },
    blob: vi.fn(),
    arrayBuffer: vi.fn(),
    json: jsonThrows
      ? () => Promise.reject(new SyntaxError('Unexpected token < in JSON'))
      : () => Promise.resolve(body),
  }
}

function stubFetch(res: MockResponse | Error) {
  const fetchMock = res instanceof Error ? vi.fn().mockRejectedValue(res) : vi.fn().mockResolvedValue(res)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('fetchEvidenceBundle', () => {
  it('EB-01-6 bundleFetch_neverBuffersViaArrayBuffer', async () => {
    const body = new Blob(['zip'], { type: 'application/zip' })
    const res = okBundle('attachment; filename=ASComply_evidence_Honeywell-Group_20260101_20260331.zip', body)
    stubFetch(res)

    const out = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK)

    // Both halves are mandatory: arrayBuffer-never alone is satisfied by touching nothing.
    expect(res.arrayBuffer).not.toHaveBeenCalled()
    expect(res.blob).toHaveBeenCalledTimes(1)
    expect(out.blob).toBe(body)
  })

  it('EB-01-7 bundleFetch_readsTheUnquotedDisposition', async () => {
    const name = 'ASComply_evidence_Honeywell-Group_20260101_20260331.zip'
    stubFetch(okBundle(`attachment; filename=${name}`))

    const out = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK)

    // Whole-string unquoted grammar; contract 8.1 forbids a filename="([^"]+)" regex.
    expect(out.filename).toBe(name)
    expect(out.filename).not.toBe(FALLBACK)
  })

  it('EB-01-9 bundleFetch_fallsBackToPreviewFilename', async () => {
    stubFetch(okBundle(null))
    const absent = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK)
    expect(absent.filename).toBe(FALLBACK)

    vi.unstubAllGlobals()
    stubFetch(okBundle('attachment; filename="x y.zip"'))
    const quoted = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK)
    // A quoted value fails the grammar outright -- fall back, never mangle, never invent.
    expect(quoted.filename).toBe(FALLBACK)
  })

  it('EB-01-8 bundleFetch_rejectsWithTheServersErrorString', async () => {
    const sentence = '12345 invoices exceeds the bundle limit of 10000'
    stubFetch(errorBundle(400, 'Bad Request', { error: sentence }))

    // The repo type, not a bare Error: subtask 06 branches on status and renders message.
    const err = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK).then(
      () => null,
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(400)
    expect((err as ApiError).message).toBe(sentence)
  })

  it('EB-01-12 bundleFetch_mapsEveryServerStatus', async () => {
    const cases: Array<[number, string, string]> = [
      [401, 'Unauthorized', 'unauthorized'],
      [404, 'Not Found', 'not found'],
      [500, 'Internal Server Error', 'internal server error'],
    ]
    expect(cases).toHaveLength(3)

    for (const [status, statusText, message] of cases) {
      vi.unstubAllGlobals()
      stubFetch(errorBundle(status, statusText, { error: message }))
      const err = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK).then(
        () => null,
        (e: unknown) => e,
      )
      expect(err, String(status)).toBeInstanceOf(ApiError)
      expect((err as ApiError).status, String(status)).toBe(status)
      // Verbatim -- the uniform 404 must not be softened into a friendlier sentence (D-26).
      expect((err as ApiError).message, String(status)).toBe(message)
    }

    vi.unstubAllGlobals()
    stubFetch(errorBundle(502, 'Bad Gateway', null, true))
    const nonJson = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK).then(
      () => null,
      (e: unknown) => e,
    )
    expect(nonJson).toBeInstanceOf(ApiError)
    expect((nonJson as ApiError).message).toBe('Bad Gateway')
  })

  it('EB-01-10 bundleFetch_forwardsTheAbortSignal', async () => {
    const controller = new AbortController()
    const aborted = new DOMException('aborted', 'AbortError')
    const fetchMock = stubFetch(aborted)

    const err = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK, controller.signal).then(
      () => null,
      (e: unknown) => e,
    )

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect(init.signal).toBe(controller.signal)
    // Untranslated: subtask 06 checks controller.signal.aborted first, so the lib must
    // not wrap an abort into an ApiError.
    expect(err).not.toBeInstanceOf(ApiError)
    expect((err as DOMException).name).toBe('AbortError')
    expect((err as DOMException).name).not.toBe('Error')
  })
})
