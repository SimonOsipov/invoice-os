import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it, vi } from 'vitest'

import { getAuditLog, normaliseAuditResponse, type AuditResponse } from './audit'

const REPO_ROOT = resolve(__dirname, '../../../..')
const READER_GO = resolve(REPO_ROOT, 'internal/audit/reader.go')
const MIRROR_TS = resolve(__dirname, 'audit.ts')

// Pulls `json:"name"` tags out of one Go struct body. The mirror is hand-maintained and no
// compiler links the two sides, so the tags are the only shared contract.
function jsonTagsOf(source: string, structName: string): string[] {
  const start = source.indexOf(`type ${structName} struct {`)
  if (start === -1) return []
  const end = source.indexOf('}', start)
  const body = source.slice(start, end)
  return [...body.matchAll(/json:"([^",]+)/g)].map((m) => m[1])
}

describe('audit wire mirror', () => {
  it('auditWire_mirrorsReaderFieldForField', () => {
    const go = readFileSync(READER_GO, 'utf8')
    const ts = readFileSync(MIRROR_TS, 'utf8')
    const structs = ['Event', 'PageInfo', 'Facet', 'Facets', 'Response']

    const scanned = structs.filter((s) => jsonTagsOf(go, s).length > 0)
    // Floor: a scan that stops matching returns zero misses, which reads exactly like a
    // clean mirror. Requiring a populated scan is what makes a green result mean something.
    expect(scanned.length).toBeGreaterThanOrEqual(4)

    // Control needle: prove the scan can find a tag before trusting it to report absences.
    expect(jsonTagsOf(go, 'Event')).toContain('company_scope')

    const missing = structs.flatMap((s) => jsonTagsOf(go, s).filter((tag) => !ts.includes(tag)))
    expect(missing).toEqual([])
  })

  it('auditWire_nullArraysDecodeAsEmpty', () => {
    // The Go store coerces nil slices to [], but Response's struct tags carry no
    // omitempty guarantee, so a bare Response{} still marshals them as null.
    const raw = {
      events: null,
      page: { limit: 25, has_more: false, next_cursor: null },
      total: 0,
      log_is_empty: true,
      facets: { event: null, actor: null, company: null },
    } as unknown as AuditResponse

    const out = normaliseAuditResponse(raw)
    expect(out.events).toEqual([])
    expect(out.facets.event).toEqual([])
    expect(out.facets.actor).toEqual([])
    expect(out.facets.company).toEqual([])
  })

  it('getAuditLog_sendsLimitAndCursorOnly', async () => {
    const authedFetch = vi.fn().mockResolvedValue({
      events: [],
      page: { limit: 25, has_more: false, next_cursor: null },
      total: 0,
      log_is_empty: true,
      facets: { event: [], actor: [], company: [] },
    })

    await getAuditLog(authedFetch, 'https://api.test', { limit: 50, cursor: 'abc' })

    const url = String(authedFetch.mock.calls[0][0])
    expect(url).toContain('/api/invoice/v1/audit-log')
    expect(url).toContain('limit=50')
    expect(url).toContain('cursor=abc')
    // An omitted param is absent entirely -- never sent as an empty value.
    expect(url).not.toContain('from=')
    expect(url).not.toContain('to=')
    expect(url).not.toContain('q=')
  })
})
