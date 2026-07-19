// RED specs (M4-08-02, task-171, IMPAPI-01..20) — pin the SPA import API module
// (previewImport/createImport's XHR transport, the UploadPhase progress contract,
// normalizeReport's D1 null-coercion, and the rowErrorRows union reader) before the
// executor implements the bodies in importApi.ts. Plan §C is authoritative; the task
// description's stale "IMPAPI-01…12" count and "injected authedFetch" clause do not
// apply here — see importApi.ts's doc comment for D1/D2/D3.
//
// vitest environment is 'node' (vitest.config.ts) — no jsdom, no Testing Library.
// FormData/Blob/File are real node globals; XMLHttpRequest is NOT, which is why every
// XHR-touching spec below injects FakeXhr through the `xhrCtor` parameter rather than
// touching globalThis.XMLHttpRequest.
//
// Spec map (AC coverage complete — plan §C):
//   IMPAPI-01  previewImport multipart: exactly one FormData entry, key "file"   (AC1)
//   IMPAPI-02  preview 200 resolves parsed; xlsx delimiter/encoding survive null (AC1)
//   IMPAPI-03  ragged sample_rows: true (short) length, no padding               (AC1)
//   IMPAPI-04  createImport FormData: exactly entity_id/mapping/file             (AC2)
//   IMPAPI-05  opened URL exact, no query string, no dry_run                    (AC2)
//   IMPAPI-06  both calls: Authorization set, Content-Type never hand-set        (AC3)
//   IMPAPI-07  two progress events -> sending,sending,processing,done            (AC4)
//   IMPAPI-08  zero progress events is legal -> processing,done                  (AC4)
//   IMPAPI-09  lengthComputable:false -> sending{total:0} -> uploadPercent null  (AC4)
//   IMPAPI-10  uploadPercent table, every UploadPhase variant                    (AC4)
//   IMPAPI-11  201 resolves ImportReport; done phase emitted before settle       (AC2,4)
//   IMPAPI-12  201 with errors/invoice_violations/rule_set_version JSON null     (AC6)
//              resolves [], [], null (D1, closes the demo-crashing defect)
//   IMPAPI-13  normalizeReport keeps channels distinct (rule_key stays in errors) (Core AC3)
//   IMPAPI-14  rowErrorRows union reader: rows[] / row / neither                 (Core AC3)
//   IMPAPI-15  400 {"error":...} -> ApiError{http,400,message}; final phase error (AC5)
//   IMPAPI-16  413 non-JSON body -> message falls back to statusText             (AC5)
//   IMPAPI-17  onerror / ontimeout -> ApiError{kind:"network"}                   (AC5)
//   IMPAPI-18  200 unparseable body -> ApiError{kind:"malformed", status:200}    (AC5)
//   IMPAPI-19  createImport 401 -> onUnauthorized once, still rejects            (AC5)
//   IMPAPI-20  previewImport 401 -> anti-fork guard, field-for-field vs IMPAPI-19 (AC5)
//
// Every spec below currently fails because previewImport/createImport/uploadPercent/
// rowErrorRows/normalizeReport/makeImportAuth's stub bodies throw `new Error('not
// implemented')` before ever constructing an XHR or returning anything — that IS the
// correct RED reason (assertion / not-implemented), not an import/compile/setup error.
// Because previewImport/createImport are declared `async` (mirroring the portfolio.ts/
// validationApi.ts stub idiom), calling them today does not throw synchronously — it
// returns an already-rejected promise, so no FakeXhr instance is ever constructed.
// Every spec below drives FakeXhr.last() with optional chaining (`?.`) BEFORE its single
// `await`/`captureRejection` point, so that point is always the first (and only)
// failure during RED — a clean "not implemented" — while becoming the real assertion
// once the executor wires up a genuine XHR call.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { APP_PERSONAS, type Session } from '../auth'
import {
  createImport,
  makeImportAuth,
  normalizeReport,
  previewImport,
  rowErrorRows,
  uploadPercent,
  type CreateImportRequest,
  type ImportAuth,
  type ImportReport,
  type UploadPhase,
  type XhrCtor,
} from './importApi'

// ---------------------------------------------------------------------------
// Fake XHR harness — records open(method,url), every setRequestHeader(k,v), and the
// send(body) FormData; exposes fireProgress/fireUploadLoad/respond/fireError/
// fireTimeout so a test can drive the exact XHR event sequence importApi.ts's
// (not-yet-written) xhrJson transport must react to. Cast at the call site
// (`FakeXhr as unknown as XhrCtor`) rather than declared to extend XMLHttpRequest —
// under strictFunctionTypes a narrower `upload.onprogress` property would fail to
// satisfy the real DOM type (task-171 plan §B).
class FakeXhr {
  static instances: FakeXhr[] = []
  static last(): FakeXhr | undefined {
    return FakeXhr.instances[FakeXhr.instances.length - 1]
  }
  static reset(): void {
    FakeXhr.instances = []
  }

  method = ''
  url = ''
  headers: Array<[string, string]> = []
  body: FormData | undefined
  status = 0
  statusText = ''
  responseText = ''
  upload: { onprogress: ((e: { loaded: number; total: number; lengthComputable: boolean }) => void) | null; onload: (() => void) | null } = {
    onprogress: null,
    onload: null,
  }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  ontimeout: (() => void) | null = null

  constructor() {
    FakeXhr.instances.push(this)
  }

  open(method: string, url: string): void {
    this.method = method
    this.url = url
  }

  setRequestHeader(name: string, value: string): void {
    this.headers.push([name, value])
  }

  send(body: FormData): void {
    this.body = body
  }

  fireProgress(loaded: number, total: number, lengthComputable = true): void {
    this.upload.onprogress?.({ loaded, total, lengthComputable })
  }

  fireUploadLoad(): void {
    this.upload.onload?.()
  }

  respond(status: number, responseText: string, statusText = ''): void {
    this.status = status
    this.responseText = responseText
    this.statusText = statusText
    this.onload?.()
  }

  fireError(): void {
    this.onerror?.()
  }

  fireTimeout(): void {
    this.ontimeout?.()
  }
}

const FakeXhrCtor = FakeXhr as unknown as XhrCtor

// Calls a (currently rejecting) thunk and returns the caught error, mirroring
// portfolio.test.ts's / validationApi.test.ts's captureRejection helper — tolerates
// both a synchronous throw and an eventual async rejection.
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected the call to reject, but it resolved')
}

function fakeAuth(token: string | null = 'tok', onUnauthorized = vi.fn()): ImportAuth {
  return { getToken: () => token, onUnauthorized }
}

function makeFile(name = 'invoices.csv'): File {
  return new File(['a,b\n1,2\n'], name, { type: 'text/csv' })
}

function makeReq(): CreateImportRequest {
  return {
    file: makeFile(),
    entityId: 'entity-1',
    mapping: { invoice_number: 'Invoice No', issue_date: 'Issue Date' },
  }
}

beforeEach(() => {
  FakeXhr.reset()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const base = 'https://gw'

const PREVIEW_BODY_CSV = {
  format: 'csv',
  delimiter: ',',
  encoding: 'utf-8',
  columns: ['Invoice No', 'Issue Date', 'Buyer TIN', 'Currency', 'VAT', 'Total', 'Qty'],
  sample_rows: [['INV-1', '2026-01-01', '123', 'NGN', '10', '100', '2']],
  rows_total: 9,
}

const PREVIEW_BODY_XLSX = {
  format: 'xlsx',
  delimiter: null,
  encoding: null,
  columns: ['A', 'B'],
  sample_rows: [['1', '2']],
  rows_total: 1,
}

// The clean, non-null happy-path import report — REPORT_BODY's field values are the
// wire shape verbatim (no normalization needed since nothing here is null).
const REPORT_BODY = {
  id: 'batch-1',
  status: 'completed',
  format: 'csv',
  delimiter: ',',
  encoding: 'utf-8',
  rows_total: 9,
  rows_valid: 9,
  rows_invalid: 0,
  ready_invoices: 5,
  quarantined_invoices: 0,
  errors: [],
  rule_set_version: 3,
  invoices_clean: 5,
  invoices_with_violations: 0,
  invoice_violations: [],
}

// The D1 defect's exact shape: a fully clean import (AC7's 500-invoice demo) where the
// server's nil slices marshal as JSON null, not [].
const CLEAN_REPORT_BODY_WITH_NULLS = {
  id: 'batch-2',
  status: 'completed',
  format: 'csv',
  delimiter: ',',
  encoding: 'utf-8',
  rows_total: 500,
  rows_valid: 500,
  rows_invalid: 0,
  ready_invoices: 500,
  quarantined_invoices: 0,
  errors: null,
  rule_set_version: null,
  invoices_clean: 500,
  invoices_with_violations: 0,
  invoice_violations: null,
}

const UNAUTHORIZED_BODY = { error: 'token expired' }

describe('previewImport', () => {
  it('IMPAPI-01: POSTs multipart with exactly one FormData entry, key "file", the passed File — no entity_id/mapping', async () => {
    const file = makeFile('sample.csv')
    const promise = previewImport(fakeAuth(), base, file, FakeXhrCtor)
    FakeXhr.last()?.respond(200, JSON.stringify(PREVIEW_BODY_CSV))
    await promise

    const xhr = FakeXhr.last()!
    expect(xhr.method).toBe('POST')
    expect(xhr.url).toBe(`${base}/api/invoice/v1/imports/preview`)
    const entries = Array.from(xhr.body!.entries())
    expect(entries).toHaveLength(1)
    expect(entries[0][0]).toBe('file')
    expect((entries[0][1] as File).name).toBe(file.name)
  })

  it('IMPAPI-02: a 200 preview body resolves parsed; an xlsx body\'s delimiter/encoding survive as null, not \'\'/undefined', async () => {
    const promise = previewImport(fakeAuth(), base, makeFile(), FakeXhrCtor)
    FakeXhr.last()?.respond(200, JSON.stringify(PREVIEW_BODY_XLSX))

    const result = await promise

    expect(result).toEqual(PREVIEW_BODY_XLSX)
    expect(result.delimiter).toBeNull()
    expect(result.encoding).toBeNull()
  })

  it('IMPAPI-03: a ragged sample row (shorter than columns) resolves at its true length, unpadded — cell access past its end is undefined', async () => {
    const raggedBody = {
      format: 'csv',
      delimiter: ',',
      encoding: 'utf-8',
      columns: ['A', 'B', 'C', 'D'],
      sample_rows: [['1', '2']],
      rows_total: 1,
    }
    const promise = previewImport(fakeAuth(), base, makeFile(), FakeXhrCtor)
    FakeXhr.last()?.respond(200, JSON.stringify(raggedBody))

    const result = await promise

    expect(result.sample_rows[0]).toHaveLength(2)
    expect(result.sample_rows[0][2]).toBeUndefined()
  })
})

describe('createImport', () => {
  it('IMPAPI-04: FormData carries exactly entity_id, mapping, file; mapping === JSON.stringify(req.mapping)', async () => {
    const req = makeReq()
    const promise = createImport(fakeAuth(), base, req, () => {}, FakeXhrCtor)
    FakeXhr.last()?.respond(201, JSON.stringify(REPORT_BODY))
    await promise

    const xhr = FakeXhr.last()!
    const entries = Array.from(xhr.body!.entries())
    expect(entries.map(([k]) => k).sort()).toEqual(['entity_id', 'file', 'mapping'])
    expect(xhr.body!.get('entity_id')).toBe(req.entityId)
    expect(xhr.body!.get('mapping')).toBe(JSON.stringify(req.mapping))
    expect((xhr.body!.get('file') as File).name).toBe(req.file.name)
  })

  it('IMPAPI-05: opened URL is exactly ${base}/api/invoice/v1/imports — no ?, no dry_run', async () => {
    const promise = createImport(fakeAuth(), base, makeReq(), () => {}, FakeXhrCtor)
    FakeXhr.last()?.respond(201, JSON.stringify(REPORT_BODY))
    await promise

    const xhr = FakeXhr.last()!
    expect(xhr.url).toBe(`${base}/api/invoice/v1/imports`)
    expect(xhr.url).not.toContain('?')
  })

  it('IMPAPI-11: a 201 valid body resolves the ImportReport; the done phase is emitted before the promise settles', async () => {
    const order: string[] = []
    const promise = createImport(
      fakeAuth(),
      base,
      makeReq(),
      (p) => {
        if (p.kind === 'done') order.push('phase:done')
      },
      FakeXhrCtor,
    )
    FakeXhr.last()?.respond(201, JSON.stringify(REPORT_BODY))

    const result = await promise
    order.push('resolved')

    expect(result).toEqual(REPORT_BODY)
    expect(order).toEqual(['phase:done', 'resolved'])
  })

  it('IMPAPI-12: a 201 with errors/invoice_violations/rule_set_version JSON null resolves errors:[], invoice_violations:[], rule_set_version:null — the clean-import happy path (D1)', async () => {
    const promise = createImport(fakeAuth(), base, makeReq(), () => {}, FakeXhrCtor)
    FakeXhr.last()?.respond(201, JSON.stringify(CLEAN_REPORT_BODY_WITH_NULLS))

    const result = await promise

    expect(result.errors).toEqual([])
    expect(result.invoice_violations).toEqual([])
    expect(result.rule_set_version).toBeNull()
    // both channels must be .map-able, never a crash on the commonest outcome (D1)
    expect(() => result.errors.map((e) => e.message)).not.toThrow()
    expect(() => result.invoice_violations.map((v) => v.invoice_number)).not.toThrow()
  })
})

describe('progress contract ([progress-two-phase])', () => {
  it('IMPAPI-06: both previewImport and createImport set Authorization: Bearer <token> and never call setRequestHeader with Content-Type (case-insensitive)', async () => {
    const previewPromise = previewImport(fakeAuth('tok-a'), base, makeFile(), FakeXhrCtor)
    const previewXhr = FakeXhr.last()
    previewXhr?.respond(200, JSON.stringify(PREVIEW_BODY_CSV))
    await previewPromise

    const createPromise = createImport(fakeAuth('tok-b'), base, makeReq(), () => {}, FakeXhrCtor)
    const createXhr = FakeXhr.last()
    createXhr?.respond(201, JSON.stringify(REPORT_BODY))
    await createPromise

    for (const [xhr, token] of [
      [previewXhr, 'tok-a'],
      [createXhr, 'tok-b'],
    ] as const) {
      const headers = xhr!.headers
      const authHeader = headers.find(([k]) => k.toLowerCase() === 'authorization')
      expect(authHeader?.[1]).toBe(`Bearer ${token}`)
      expect(headers.some(([k]) => k.toLowerCase() === 'content-type')).toBe(false)
    }
  })

  it('IMPAPI-07: two progress events then normal completion — phases are exactly sending, sending, processing, done, in order', async () => {
    const phases: UploadPhase[] = []
    const promise = createImport(fakeAuth(), base, makeReq(), (p) => phases.push(p), FakeXhrCtor)
    FakeXhr.last()?.fireProgress(50, 200, true)
    FakeXhr.last()?.fireProgress(150, 200, true)
    FakeXhr.last()?.fireUploadLoad()
    FakeXhr.last()?.respond(201, JSON.stringify(REPORT_BODY))

    await promise

    expect(phases).toEqual([
      { kind: 'sending', loaded: 50, total: 200 },
      { kind: 'sending', loaded: 150, total: 200 },
      { kind: 'processing' },
      { kind: 'done' },
    ])
  })

  it('IMPAPI-08: zero progress events is legal — phases are exactly processing, done; no sending; the promise still resolves', async () => {
    const phases: UploadPhase[] = []
    const promise = createImport(fakeAuth(), base, makeReq(), (p) => phases.push(p), FakeXhrCtor)
    FakeXhr.last()?.fireUploadLoad()
    FakeXhr.last()?.respond(201, JSON.stringify(REPORT_BODY))

    const result = await promise

    expect(phases.map((p) => p.kind)).toEqual(['processing', 'done'])
    expect(result).toEqual(REPORT_BODY)
  })

  it('IMPAPI-09: lengthComputable:false yields sending{total:0}, and uploadPercent of it is null (no fallback to file.size)', async () => {
    const phases: UploadPhase[] = []
    const promise = createImport(fakeAuth(), base, makeReq(), (p) => phases.push(p), FakeXhrCtor)
    // total is deliberately large/irrelevant — lengthComputable:false must force total:0
    // regardless, proving there is no fallback to a size the browser never confirmed.
    FakeXhr.last()?.fireProgress(1234, 999_999, false)
    FakeXhr.last()?.fireUploadLoad()
    FakeXhr.last()?.respond(201, JSON.stringify(REPORT_BODY))

    await promise

    const sendingPhases = phases.filter((p): p is Extract<UploadPhase, { kind: 'sending' }> => p.kind === 'sending')
    expect(sendingPhases).toHaveLength(1)
    expect(sendingPhases[0].total).toBe(0)
    expect(uploadPercent(sendingPhases[0])).toBeNull()
  })
})

describe('uploadPercent', () => {
  it('IMPAPI-10: maps every UploadPhase to a 0-100 percent or null (indeterminate) — never NaN/Infinity', () => {
    expect(uploadPercent({ kind: 'idle' })).toBeNull()
    expect(uploadPercent({ kind: 'sending', loaded: 50, total: 200 })).toBe(25)
    expect(uploadPercent({ kind: 'sending', loaded: 0, total: 200 })).toBe(0)
    expect(uploadPercent({ kind: 'sending', loaded: 200, total: 200 })).toBe(100)
    expect(uploadPercent({ kind: 'sending', loaded: 50, total: 0 })).toBeNull()
    expect(uploadPercent({ kind: 'processing' })).toBeNull()
    expect(uploadPercent({ kind: 'done' })).toBe(100)
    expect(uploadPercent({ kind: 'error', error: new ApiError('network', 'boom') })).toBeNull()
  })
})

// IMPAPI-13 tests normalizeReport directly rather than via the full createImport/XHR
// flow (unlike IMPAPI-12): it pins the pure transform's own channel-distinction
// contract in isolation, which is both more precise and independent of the XHR
// harness — normalizeReport is an exported name in its own right (task-171 plan §B).
describe('normalizeReport', () => {
  it('IMPAPI-13: keeps channels distinct — a RowError carrying rule_key/severity (store-duplicate) stays in errors and does not appear in invoice_violations', () => {
    const raw = {
      ...CLEAN_REPORT_BODY_WITH_NULLS,
      errors: [{ row: 4, rule_key: 'no-duplicate-invoice-number', severity: 'error', message: 'duplicate invoice number' }],
      invoice_violations: null,
    }

    const result: ImportReport = normalizeReport(raw)

    expect(result.errors).toEqual([{ row: 4, rule_key: 'no-duplicate-invoice-number', severity: 'error', message: 'duplicate invoice number' }])
    expect(result.invoice_violations).toEqual([])
  })
})

describe('rowErrorRows', () => {
  it('IMPAPI-14: reads the RowError union — rows[] when present, else row, else empty', () => {
    expect(rowErrorRows({ rows: [5, 6], message: 'x' })).toEqual([5, 6])
    expect(rowErrorRows({ row: 12, message: 'x' })).toEqual([12])
    expect(rowErrorRows({ message: 'x' })).toEqual([])
  })
})

describe('createImport / previewImport: non-2xx / transport failures reject with the correspondingly-kinded ApiError', () => {
  it('IMPAPI-15: a 400 {"error":"mapping is required"} rejects ApiError{kind:"http", status:400, message:"mapping is required"}; the final phase is error carrying that same ApiError', async () => {
    const phases: UploadPhase[] = []
    const promise = createImport(fakeAuth(), base, makeReq(), (p) => phases.push(p), FakeXhrCtor)
    FakeXhr.last()?.respond(400, JSON.stringify({ error: 'mapping is required' }), 'Bad Request')

    const err = await captureRejection(() => promise)

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(400)
    expect(apiErr.message).toBe('mapping is required')
    const lastPhase = phases[phases.length - 1]
    expect(lastPhase).toEqual({ kind: 'error', error: apiErr })
  })

  it('IMPAPI-16: a 413 with a non-JSON body rejects ApiError{kind:"http", status:413}, message falls back to statusText, the parse attempt does not throw', async () => {
    const promise = createImport(fakeAuth(), base, makeReq(), () => {}, FakeXhrCtor)
    FakeXhr.last()?.respond(413, 'Request Entity Too Large', 'Payload Too Large')

    const err = await captureRejection(() => promise)

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(413)
    expect(apiErr.message).toBe('Payload Too Large')
  })

  it('IMPAPI-17a: onerror rejects ApiError{kind:"network"}', async () => {
    const promise = createImport(fakeAuth(), base, makeReq(), () => {}, FakeXhrCtor)
    FakeXhr.last()?.fireError()

    const err = await captureRejection(() => promise)

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('network')
  })

  // QA Stage 4 note (task-171 orchestrator ruling, importApi.ts's xhrJson ontimeout
  // comment): this pins the ERROR-MAPPING CONTRACT only (ontimeout -> ApiError{kind:
  // network}), not live coverage. FakeXhr.fireTimeout() invokes the handler directly;
  // xhrJson never sets `xhr.timeout`, so a real browser defaults it to 0 (infinite) and
  // this handler cannot fire in production today. No AC in this subtask specifies a
  // timeout duration; M4-08-07's deploy-gate e2e (60s/500-invoice perf budget) is the
  // evidence base a future duration would come from.
  it('IMPAPI-17b: ontimeout rejects ApiError{kind:"network"}', async () => {
    const promise = createImport(fakeAuth(), base, makeReq(), () => {}, FakeXhrCtor)
    FakeXhr.last()?.fireTimeout()

    const err = await captureRejection(() => promise)

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('network')
  })

  it('IMPAPI-18: a 200 with an unparseable body rejects ApiError{kind:"malformed", status:200}', async () => {
    const promise = createImport(fakeAuth(), base, makeReq(), () => {}, FakeXhrCtor)
    FakeXhr.last()?.respond(200, '{not valid json', 'OK')

    const err = await captureRejection(() => promise)

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('malformed')
    expect(apiErr.status).toBe(200)
  })

  it('IMPAPI-19: a 401 on createImport calls onUnauthorized exactly once and still rejects (kind:"http", status:401)', async () => {
    const onUnauthorized = vi.fn()
    const promise = createImport(fakeAuth('tok', onUnauthorized), base, makeReq(), () => {}, FakeXhrCtor)
    FakeXhr.last()?.respond(401, JSON.stringify(UNAUTHORIZED_BODY), 'Unauthorized')

    const err = await captureRejection(() => promise)

    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(401)
    expect(apiErr.message).toBe('token expired')
    expect(apiErr.body).toEqual(UNAUTHORIZED_BODY)
  })

  // Anti-fork guard (D2). Falsification condition (task-171 plan §C): this spec MUST go
  // RED against a previewImport reimplemented over raw fetch that omits the
  // onUnauthorized call and/or builds its own error object — hence asserting the call
  // COUNT and EVERY ApiError field (not a bare rejects.toThrow(), which would stay
  // green against that fork). Precedent: M4-08-01's PRV-16 claimed this role but QA
  // proved by mutation it stayed green against a hand-rolled second parser.
  it('IMPAPI-20: a 401 on previewImport calls onUnauthorized exactly once and rejects with an ApiError matching IMPAPI-19 field for field', async () => {
    const onUnauthorized = vi.fn()
    const promise = previewImport(fakeAuth('tok', onUnauthorized), base, makeFile(), FakeXhrCtor)
    FakeXhr.last()?.respond(401, JSON.stringify(UNAUTHORIZED_BODY), 'Unauthorized')

    const err = await captureRejection(() => promise)

    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(401)
    expect(apiErr.message).toBe('token expired')
    expect(apiErr.body).toEqual(UNAUTHORIZED_BODY)
  })
})

// ---------------------------------------------------------------------------
// QA Stage 4 (Mode B) additions — adversarial / edge / negative coverage. These
// are NOT part of the architect-pinned IMPAPI-01..20 spec map above (task-171
// plan §C) and do not renumber into that series; they extend coverage into code
// paths and boundary conditions the pinned specs do not exercise, found while
// mutation-testing D1/uploadPercent/channel-separation for Stage 4 verification.

function buildSession(token: string | null): Session {
  return { persona: APP_PERSONAS.firm, token, me: null, verified: false }
}

describe('makeImportAuth: token-read semantics + onUnauthorized wiring (QA)', () => {
  // No IMPAPI-01..20 spec ever imports or calls makeImportAuth — every one of them
  // builds an ImportAuth by hand via the fakeAuth() helper. That leaves makeImportAuth's
  // own lazy-token-read contract (D3, mirroring makeAuthedFetch's A5/A6 in
  // portfolio.authedfetch.test.ts) completely unexercised. These three close that gap.

  it('QA-01: reads session.token at CALL time, not construction time — mutating session.token after makeImportAuth() still picks up the new value on the next request', async () => {
    const session = buildSession('old')
    const auth = makeImportAuth(session, vi.fn())
    session.token = 'new'

    const promise = previewImport(auth, base, makeFile(), FakeXhrCtor)
    FakeXhr.last()?.respond(200, JSON.stringify(PREVIEW_BODY_CSV))
    await promise

    const authHeader = FakeXhr.last()!.headers.find(([k]) => k.toLowerCase() === 'authorization')
    expect(authHeader?.[1]).toBe('Bearer new')
  })

  it('QA-02: a null session.token issues the request with no Authorization header at all', async () => {
    const auth = makeImportAuth(buildSession(null), vi.fn())

    const promise = previewImport(auth, base, makeFile(), FakeXhrCtor)
    FakeXhr.last()?.respond(200, JSON.stringify(PREVIEW_BODY_CSV))
    await promise

    const headers = FakeXhr.last()!.headers
    expect(headers.some(([k]) => k.toLowerCase() === 'authorization')).toBe(false)
  })

  it('QA-03: onUnauthorized forwards to the caller-supplied onSignOut on a real 401, exactly once', async () => {
    const onSignOut = vi.fn()
    const auth = makeImportAuth(buildSession('tok'), onSignOut)

    const promise = createImport(auth, base, makeReq(), () => {}, FakeXhrCtor)
    FakeXhr.last()?.respond(401, JSON.stringify(UNAUTHORIZED_BODY), 'Unauthorized')
    await captureRejection(() => promise)

    expect(onSignOut).toHaveBeenCalledTimes(1)
  })
})

describe('normalizeReport: malformed/edge raw inputs (QA)', () => {
  it('QA-04: undefined resolves with empty arrays and a null rule_set_version, never throws', () => {
    expect(() => normalizeReport(undefined)).not.toThrow()
    const result = normalizeReport(undefined)
    expect(result.errors).toEqual([])
    expect(result.invoice_violations).toEqual([])
    expect(result.rule_set_version).toBeNull()
  })

  it('QA-05: null resolves identically to undefined', () => {
    const result = normalizeReport(null)
    expect(result.errors).toEqual([])
    expect(result.invoice_violations).toEqual([])
    expect(result.rule_set_version).toBeNull()
  })

  it('QA-06: an empty object resolves with empty arrays and a null rule_set_version', () => {
    const result = normalizeReport({})
    expect(result.errors).toEqual([])
    expect(result.invoice_violations).toEqual([])
    expect(result.rule_set_version).toBeNull()
  })

  it('QA-07: a bare array raw value never throws — documents current behavior (normalizeReport is not a validator per its own SCOPE comment); both channels still come out array-shaped', () => {
    expect(() => normalizeReport(['a', 'b'])).not.toThrow()
    const result = normalizeReport(['a', 'b'])
    expect(result.errors).toEqual([])
    expect(result.invoice_violations).toEqual([])
    expect(result.rule_set_version).toBeNull()
  })
})

describe('transport edge cases beyond IMPAPI-15..20 (QA)', () => {
  it('QA-08: previewImport also rejects malformed on an unparseable 200 body — IMPAPI-18 only exercises this via createImport; same xhrJson code path, verified independently for the other caller', async () => {
    const promise = previewImport(fakeAuth(), base, makeFile(), FakeXhrCtor)
    FakeXhr.last()?.respond(200, '{not valid json', 'OK')

    const err = await captureRejection(() => promise)

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('malformed')
    expect((err as ApiError).status).toBe(200)
  })

  it('QA-09: a 200 with well-formed JSON of the wrong shape resolves without crashing — xhrJson only checks that the body PARSES, it does not validate shape', async () => {
    const promise = previewImport(fakeAuth(), base, makeFile(), FakeXhrCtor)
    FakeXhr.last()?.respond(200, JSON.stringify({ unexpected: 'shape' }))

    const result = await promise

    expect(result).toEqual({ unexpected: 'shape' })
  })

  it('QA-10: a non-2xx with an HTML proxy error page (embedded quotes/braces that could confuse a naive parser) still surfaces as a usable ApiError, message falling back to statusText, body undefined', async () => {
    const promise = createImport(fakeAuth(), base, makeReq(), () => {}, FakeXhrCtor)
    const html = '<html><body><h1>502 Bad Gateway</h1><p>nginx says: "upstream {timed out}"</p></body></html>'
    FakeXhr.last()?.respond(502, html, 'Bad Gateway')

    const err = await captureRejection(() => promise)

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(502)
    expect(apiErr.message).toBe('Bad Gateway')
    expect(apiErr.body).toBeUndefined()
  })
})

describe('progress event ordering — edge cases (QA)', () => {
  it('QA-11: previewImport surfaces no phase to any observer — firing progress/upload-load events on its underlying XHR before responding does not throw, and preview still resolves normally afterward', async () => {
    const promise = previewImport(fakeAuth(), base, makeFile(), FakeXhrCtor)
    const xhr = FakeXhr.last()!

    expect(() => xhr.fireProgress(10, 100)).not.toThrow()
    expect(() => xhr.fireUploadLoad()).not.toThrow()
    xhr.respond(200, JSON.stringify(PREVIEW_BODY_CSV))

    const result = await promise
    expect(result).toEqual(PREVIEW_BODY_CSV)
  })

  // DEFECT (documented, not fixed — QA does not fix implementation code): xhrJson's
  // xhr.upload.onprogress handler has no post-settle guard, unlike xhr.onload (which
  // checks `if (settled) return`). A progress event fired after the response already
  // resolved the promise is still forwarded to onPhase, breaking the documented
  // `sending* -> processing -> done` terminal-phase contract. NOT reachable via a
  // spec-compliant real browser XHR — the platform's event ordering guarantees
  // upload.onprogress/upload.onload complete before the main onload fires for a given
  // request — so this is a latent robustness gap (e.g. against a buggy XHR polyfill),
  // not a live production defect. Reported to the executor for a possible `settled`
  // guard on upload.onprogress; QA does not add that guard itself.
  it('QA-12: a stray progress event fired after the response already settled the promise is still forwarded to onPhase — no post-settle guard on upload.onprogress (documents current behavior; not reachable via a real, spec-compliant browser XHR)', async () => {
    const phases: UploadPhase[] = []
    const promise = createImport(fakeAuth(), base, makeReq(), (p) => phases.push(p), FakeXhrCtor)
    const xhr = FakeXhr.last()!
    xhr.fireUploadLoad()
    xhr.respond(201, JSON.stringify(REPORT_BODY))
    await promise

    xhr.fireProgress(1, 1) // a real XHR would never emit this after its onload fired

    expect(phases.map((p) => p.kind)).toEqual(['processing', 'done', 'sending'])
  })
})

describe('concurrency (QA)', () => {
  it('QA-13: previewImport and createImport in flight simultaneously, sharing one ImportAuth, do not cross-talk — each gets its own XHR instance, resolves its own body, and (for createImport) its own phase stream', async () => {
    const auth = fakeAuth()
    const createPhases: UploadPhase[] = []

    const previewPromise = previewImport(auth, base, makeFile('a.csv'), FakeXhrCtor)
    const previewXhr = FakeXhr.last()!
    const createPromise = createImport(auth, base, makeReq(), (p) => createPhases.push(p), FakeXhrCtor)
    const createXhr = FakeXhr.last()!

    expect(previewXhr).not.toBe(createXhr)

    // Resolve out of start order (create responds first) to prove no shared/ordered state.
    createXhr.fireUploadLoad()
    createXhr.respond(201, JSON.stringify(REPORT_BODY))
    previewXhr.respond(200, JSON.stringify(PREVIEW_BODY_CSV))

    const [previewResult, createResult] = await Promise.all([previewPromise, createPromise])

    expect(previewResult).toEqual(PREVIEW_BODY_CSV)
    expect(createResult).toEqual(REPORT_BODY)
    expect(createPhases.map((p) => p.kind)).toEqual(['processing', 'done'])
  })
})

describe('rowErrorRows: boundary case (QA)', () => {
  it('QA-14: row 0 is read via a strict `!== undefined` check, not truthiness — {row:0} resolves [0], not [] (guards against a future truthy-check regression; row 0 cannot occur today since sheet rows are 1-based, but the reader must not silently rely on that)', () => {
    expect(rowErrorRows({ row: 0, message: 'x' })).toEqual([0])
  })
})
