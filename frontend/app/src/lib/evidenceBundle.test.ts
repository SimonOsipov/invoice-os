// AUDIT-08-01 (task-664, EB-01-1..EB-01-24) -- the evidence-bundle wire mirror, the frozen
// request builder and the two fetches. EB-01-1..EB-01-13 were authored red against a stub
// and are green against the shipped module; EB-01-14..EB-01-24 are the QA pass.
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

// ---------------------------------------------------------------------------
// AUDIT-08-01 QA (Stage 4) adversarial coverage, EB-01-14..EB-01-24. Authored green
// against the shipped implementation; each was shown red under a mutation of
// evidenceBundle.ts. EB-01-14 exists because EB-01-1's substring haystack leaves 4 of
// the 17 tags unguarded (measured: counts, name, from, to).
// ---------------------------------------------------------------------------

// Field names actually DECLARED by one interface: comments stripped, anchored at the
// line start. EB-01-1's `haystack.includes(tag)` cannot do this -- `name` matches inside
// `filename`, and BundlePeriod's own comment supplies `from` and `to`.
function declaredFieldsOf(source: string, name: string): string[] {
  const head = `export interface ${name} {`
  const open = source.indexOf(head)
  if (open === -1) return []
  const end = source.indexOf('}', open + head.length)
  if (end === -1) return []
  return source
    .slice(open + head.length, end)
    .split('\n')
    .map((line) => line.replace(/\/\/.*$/, '').trim())
    .map((line) => line.match(/^([A-Za-z0-9_]+)\??\s*:/)?.[1] ?? '')
    .filter(Boolean)
}

describe('evidence-bundle wire mirror (placement)', () => {
  it('EB-01-14 bundleWire_eachTagLivesInItsOwnInterface', () => {
    const preview = readFileSync(PREVIEW_GO, 'utf8')
    const manifest = readFileSync(MANIFEST_GO, 'utf8')
    const ts = readFileSync(MIRROR_TS, 'utf8')

    const pairs: Array<[string, string, string]> = [
      [preview, 'Preview', 'EvidenceBundlePreview'],
      [manifest, 'manifestEntity', 'BundleEntity'],
      [manifest, 'manifestPeriod', 'BundlePeriod'],
      [manifest, 'manifestCounts', 'BundleCounts'],
    ]
    expect(pairs).toHaveLength(4)

    // Positive control on the extractor, with the two traps built in: the fixture's
    // comment holds `to`/`from` and its one field is a superstring of `name`.
    expect(declaredFieldsOf('export interface X {\n  // to and from live here\n  filename: string\n}\n', 'X')).toEqual([
      'filename',
    ])
    // Negative control: the comparison must still be able to report a miss.
    expect(declaredFieldsOf(ts, 'BundleEntity')).not.toContain('not_a_real_tag')

    const misplaced: string[] = []
    let checked = 0
    for (const [go, struct, iface] of pairs) {
      const tags = jsonTagsOf(go, struct)
      expect(tags.length, struct).toBeGreaterThan(0)
      const fields = declaredFieldsOf(ts, iface)
      expect(fields.length, iface).toBeGreaterThan(0)
      for (const tag of tags) {
        checked++
        // Exact equality, so a near-miss rename (body_files -> body_files_count) is a
        // miss too. EB-01-1's substring test passes that one.
        if (!fields.includes(tag)) misplaced.push(`${iface}.${tag}`)
      }
    }
    expect(checked).toBe(17)
    expect(misplaced).toEqual([])
  })
})

describe('bundleRequestFor (boundaries)', () => {
  it('EB-01-15 bundleRequest_customRangeIsUnvalidatedHere', () => {
    const now = new Date('2026-08-24T10:15:30.123Z')
    const cases: Array<[string, string, string, string, string]> = [
      ['inverted', '2026-07-31', '2026-07-01', '2026-07-31T00:00:00.000Z', '2026-07-01T23:59:59.999Z'],
      ['same day', '2026-07-01', '2026-07-01', '2026-07-01T00:00:00.000Z', '2026-07-01T23:59:59.999Z'],
      ['malformed', 'not-a-date', '2026-07-01', 'not-a-dateT00:00:00.000Z', '2026-07-01T23:59:59.999Z'],
      ['leap day', '2028-02-29', '2028-02-29', '2028-02-29T00:00:00.000Z', '2028-02-29T23:59:59.999Z'],
    ]
    expect(cases).toHaveLength(4)

    for (const [label, from, to, wantFrom, wantTo] of cases) {
      const req = bundleRequestFor('e1', { preset: 'custom', from, to }, now)
      // Pins the CURRENT behaviour: no refusal here, no Date round-trip that could
      // normalise a bad day. Subtask 02's bundleBlockFor owns the inverted range (the
      // server answers `from must not be after to`) -- so its change lands as a diff
      // against these rows rather than as a surprise.
      expect(req, label).not.toBeNull()
      expect(req!.from, label).toBe(wantFrom)
      expect(req!.to, label).toBe(wantTo)
    }
  })

  it('EB-01-16 bundleRequest_relativePresetAtMidnightAcrossAYearBoundary', () => {
    const now = new Date('2026-01-01T00:00:00.000Z')
    const before = now.getTime()

    const day = bundleRequestFor('e1', { preset: '24h' }, now)
    const month = bundleRequestFor('e1', { preset: '30d' }, now)

    expect(day).not.toBeNull()
    expect(month).not.toBeNull()
    expect(day!.to).toBe('2026-01-01T00:00:00.000Z')
    expect(day!.from).toBe('2025-12-31T00:00:00.000Z')
    expect(month!.from).toBe('2025-12-02T00:00:00.000Z')
    // The caller's clock is an input, never a scratchpad -- epoch arithmetic, no setTime.
    expect(now.getTime()).toBe(before)
  })
})

describe('getEvidenceBundlePreview (passthrough and abort)', () => {
  it('EB-01-22 bundlePreview_returnsThePayloadUntouched', async () => {
    const counts = { invoices: 3, status_transitions: 4, submissions: 2, exchange_attempts: 2, body_files: 1 }
    const payload = {
      entity: { id: REQ.entityId, name: 'Honeywell Group', tin: null },
      period: {
        from: '2026-01-01T00:00:00Z',
        to: '2026-03-31T23:59:59Z',
        bounds: 'inclusive',
        basis: 'invoices.created_at',
      },
      filename: 'ASComply_evidence_Honeywell-Group_20260101_20260331.zip',
      counts,
      over_limit: false,
      // A field the mirror does not declare. No Go field on this response is a slice, so
      // evidenceBundle.ts carries no normalise...(); this pins that a future one cannot
      // silently drop server data.
      server_added_later: 'kept',
    }
    const snapshot = JSON.parse(JSON.stringify(payload))
    const authedFetch = vi.fn().mockResolvedValue(payload as unknown as EvidenceBundlePreview)

    const got = await getEvidenceBundlePreview(authedFetch, BASE, REQ)

    expect(got).toBe(payload)
    expect(got.counts).toBe(counts)
    expect((got as unknown as Record<string, unknown>).server_added_later).toBe('kept')
    expect(payload).toEqual(snapshot)
  })

  it('EB-01-23 bundlePreview_forwardsTheAbortSignal', async () => {
    const controller = new AbortController()
    const authedFetch = vi.fn().mockResolvedValue({} as EvidenceBundlePreview)

    await getEvidenceBundlePreview(authedFetch, BASE, REQ, controller.signal)
    expect(authedFetch).toHaveBeenCalledTimes(1)
    expect((authedFetch.mock.calls[0][1] as { signal?: AbortSignal }).signal).toBe(controller.signal)

    // Without one the option is present but undefined; apiFetch reads opts?.signal either
    // way. An abort HERE returns as ApiError('network', ...) (client.ts:61), unlike the
    // download path (EB-01-10) -- there is no single error-shape predicate across the two.
    authedFetch.mockClear()
    await getEvidenceBundlePreview(authedFetch, BASE, REQ)
    expect((authedFetch.mock.calls[0][1] as { signal?: AbortSignal }).signal).toBeUndefined()
  })
})

describe('fetchEvidenceBundle (hostile input and failure modes)', () => {
  it('EB-01-17 bundleFetch_hostileDispositionsFallBack', async () => {
    // Only two of these can leave this server: mime.FormatMediaType emits the canonical
    // unquoted form, and contentDisposition (handlers.go:123-128) emits a bare
    // `attachment` when the name is empty. The rest defend against a proxy or a future
    // header change -- a path separator must never reach subtask 06's download anchor.
    const hostile = [
      'attachment',
      'attachment; filename=',
      'attachment; filename=../../etc/passwd',
      'attachment; filename=a.zip; filename=b.zip',
      'Attachment; FileName=a.zip',
      ' attachment; filename=a.zip',
      'attachment; filename=a.zip ',
      'inline; filename=a.zip',
    ]
    expect(hostile).toHaveLength(8)

    for (const header of hostile) {
      vi.unstubAllGlobals()
      stubFetch(okBundle(header))
      const out = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK)
      expect(out.filename, header).toBe(FALLBACK)
    }

    // Positive control: a parser that returns the fallback unconditionally passes every
    // row above and fails here.
    const canonical = 'ASComply_evidence_Honeywell-Group_20260101_20260331.zip'
    vi.unstubAllGlobals()
    stubFetch(okBundle(`attachment; filename=${canonical}`))
    const good = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK)
    expect(good.filename).toBe(canonical)
  })

  it('EB-01-18 bundleFetch_errorBodiesWithoutAnErrorKey', async () => {
    const cases: Array<[string, unknown, string]> = [
      ['no error key', { detail: 'nope' }, 'Bad Gateway'],
      ['json null', null, 'Bad Gateway'],
      ['json array', [], 'Bad Gateway'],
      // String() coercion, inherited from apiFetch (client.ts:70). Subtask 06 renders
      // message verbatim, so a server sending {"error":null} shows the word "null".
      ['non-string error', { error: 42 }, '42'],
      ['null error', { error: null }, 'null'],
    ]
    expect(cases).toHaveLength(5)

    for (const [label, body, message] of cases) {
      vi.unstubAllGlobals()
      stubFetch(errorBundle(502, 'Bad Gateway', body))
      const err = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK).then(
        () => null,
        (e: unknown) => e,
      )
      expect(err, label).toBeInstanceOf(ApiError)
      expect((err as ApiError).status, label).toBe(502)
      expect((err as ApiError).message, label).toBe(message)
      // The parsed envelope reaches the caller even when no sentence could be lifted.
      expect((err as ApiError).body, label).toEqual(body)
    }
  })

  it('EB-01-19 bundleFetch_treatsA204AsASuccessfulEmptyDownload', async () => {
    const res = okBundle(null, new Blob([], { type: 'application/zip' }))
    res.status = 204
    stubFetch(res)

    const out = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK)

    // res.ok is true for 204, so nothing here refuses it. Neither route emits one today;
    // if a proxy ever does, subtask 06 hands the user a 0-byte file named FALLBACK.
    expect(res.blob).toHaveBeenCalledTimes(1)
    expect(out.blob.size).toBe(0)
    expect(out.filename).toBe(FALLBACK)
  })

  it('EB-01-20 bundleFetch_doesNotWrapAFailedBodyRead', async () => {
    const boom = new TypeError('body stream already read')
    const res = okBundle('attachment; filename=a.zip')
    res.blob = vi.fn().mockRejectedValue(boom)
    stubFetch(res)

    const err = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK).then(
      () => null,
      (e: unknown) => e,
    )

    // apiFetch turns an unreadable body into ApiError('malformed', ...) (client.ts:79-83);
    // this path has no equivalent.
    expect(err).toBe(boom)
    expect(err).not.toBeInstanceOf(ApiError)
  })

  it('EB-01-21 bundleFetch_networkFailureIsNotAnApiError', async () => {
    const controller = new AbortController()
    const offline = new TypeError('Failed to fetch')
    stubFetch(offline)

    const err = await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK, controller.signal).then(
      () => null,
      (e: unknown) => e,
    )

    // There is no try/catch at all in fetchEvidenceBundle: apiFetch would have produced
    // ApiError('network', ...) here (client.ts:61), this path propagates the raw TypeError.
    expect(err).toBe(offline)
    expect(err).not.toBeInstanceOf(ApiError)
    expect((err as Error).name).toBe('TypeError')
    // Subtask 06's "check signal.aborted first" predicate falls through on this one, so
    // its error branch must handle a rejection that is NOT an ApiError.
    expect(controller.signal.aborted).toBe(false)
  })

  it('EB-01-24 bundleFetch_sendsTheBearerHeaderEvenWhenTheTokenIsNull', async () => {
    const withToken = stubFetch(okBundle(null))
    await fetchEvidenceBundle(() => 'tok', BASE, REQ, FALLBACK)
    expect(String(withToken.mock.calls[0][0])).toContain('/api/invoice/v1/evidence-bundle?')
    expect((withToken.mock.calls[0][1] as { headers: Record<string, string> }).headers.Authorization).toBe('Bearer tok')

    vi.unstubAllGlobals()
    const noToken = stubFetch(okBundle(null))
    await fetchEvidenceBundle(() => null, BASE, REQ, FALLBACK)
    // Current behaviour, matching sourceDocument.ts:93: a null token is template-
    // stringified, so the gateway sees the literal `Bearer null` and answers 401. The lib
    // does not short-circuit -- subtask 06 must not read that 401 as "your session ended".
    expect((noToken.mock.calls[0][1] as { headers: Record<string, string> }).headers.Authorization).toBe('Bearer null')
  })
})
