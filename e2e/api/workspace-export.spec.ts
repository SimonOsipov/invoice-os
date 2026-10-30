// Workspace export over the deployed gateway: preflight, export code, form redeem, ZIP contents, audit rows.
// Every workspace is registered here.
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test, expect } from '@playwright/test'
import { unzipSync } from 'fflate'
import {
  acceptById,
  apiBase,
  cancelDeletion,
  createEntity,
  createInvoice,
  exportCode,
  exportDownload,
  exportPreflight,
  getAuditLog,
  inviteWithToken,
  listMine,
  me,
  mintSignInState,
  registerFormAccount,
  registerFresh,
  scheduleDeletion,
  signInSession,
  subjectOf,
  type PendingInvite,
} from './client'
import { assertErrorEnvelope } from './contract-helpers'
import { freshTin } from './fixtures'

// internal/archive/handlers.go statusForErr: ErrExportNotPermitted.
const EXPORT_NOT_PERMITTED = 'only an admin can download the workspace export'
// internal/gateway/export_download.go exportRefusalMessage: 400 and 403 arms.
const PAGE_EXPIRED = 'This download link has expired.'
const PAGE_NOT_ADMIN = 'Only an active admin can download the workspace export.'
// internal/archive/workspace.go audit.Record event names.
const EVENT_EXPORTED = 'workspace.exported'
const EVENT_FAILED = 'workspace.export_failed'
// internal/document/document.go audit.Record event name.
const EVENT_DOC_READ = 'document.read'
// internal/archive/workspace.go onStart filename prefix.
const FILENAME_PREFIX = 'ASComply_workspace_'
// internal/audit/export_csv.go ExportCSVHeader, joined.
const AUDIT_HEADER = 'id,created_at,event,actor,actor_name,actor_kind,entity_id,company_name,company_scope,invoice_id,payload'

const PDF_FIXTURE = join(dirname(fileURLToPath(import.meta.url)), '../fixtures/documents/native_invoice.pdf')
// A trailing PDF comment gives this run its own content hash, so dedupe cannot reuse another run's row.
const PDF_BYTES = (() => {
  const base = new Uint8Array(readFileSync(PDF_FIXTURE))
  const marker = new TextEncoder().encode(`%e2e-${crypto.randomUUID()}\n`)
  const out = new Uint8Array(base.length + marker.length)
  out.set(base, 0)
  out.set(marker, base.length)
  return out
})()
const PDF_SHA = createHash('sha256').update(PDF_BYTES).digest('hex')

interface ManifestEntry {
  name: string
  bytes: number
  sha256: string
}

const utf8 = (b: Uint8Array): string => new TextDecoder().decode(b)
const sha256 = (b: Uint8Array): string => createHash('sha256').update(b).digest('hex')
const errorOf = (res: { body: unknown }): string => (res.body as { error: string }).error
const codeOf = (res: { status: number; body: unknown }): string => {
  expect(res.status, JSON.stringify(res.body)).toBe(200)
  return (res.body as { code: string }).code
}

async function redeem(token: string, state = mintSignInState()) {
  return exportDownload(codeOf(await exportCode(token, state)), state)
}

async function upload(token: string): Promise<void> {
  const form = new FormData()
  form.set('file', new Blob([PDF_BYTES], { type: 'application/pdf' }), 'export-e2e.pdf')
  const res = await fetch(`${apiBase()}/api/submission/v1/documents`, { method: 'POST', headers: { Authorization: `Bearer ${token}` }, body: form })
  expect(res.status, await res.text()).toBeLessThan(300)
}

async function auditRows(token: string, event: string[]) {
  return (await getAuditLog(token, { event, limit: 100 })).events
}

test.describe.serial('workspace export (API E2E, over the deployed gateway)', () => {
  let adminToken: string
  let workspaceName: string
  let adminId: string
  let preparerToken: string
  let zip: Record<string, Uint8Array>
  let manifest: { entries: ManifestEntry[] }
  let readsBefore: number

  test.beforeAll(async () => {
    const reg = await registerFormAccount('export-admin')
    adminToken = reg.token
    workspaceName = reg.account.workspaceName
    adminId = subjectOf(adminToken)
    const tenantId = (await me(adminToken)).tenant.id

    const entity = await createEntity(adminToken, { name: `Export Co ${freshTin()}`, tin: freshTin() })
    await createInvoice(adminToken, { entity_id: entity.id, invoice_number: `INV-EXPORT-${freshTin()}` })
    await upload(adminToken)

    const preparer = await registerFresh('export-preparer')
    await inviteWithToken(adminToken, tenantId, preparer.email, 'preparer')
    const session = (await signInSession(preparer.email, preparer.password)).access_token
    const [pending] = ((await listMine(session)).body as { invitations: PendingInvite[] }).invitations
    const accepted = await acceptById(session, pending.id, 'Export Preparer')
    expect(accepted.status, JSON.stringify(accepted.body)).toBe(200)
    preparerToken = (await signInSession(preparer.email, preparer.password)).access_token

    readsBefore = (await auditRows(adminToken, [EVENT_DOC_READ])).length
  })

  test('workspace export: an admin downloads every company, upload and audit row', async () => {
    const preflight = await exportPreflight(adminToken)
    expect(preflight.status, JSON.stringify(preflight.body)).toBe(204)

    const res = await redeem(adminToken)
    expect(res.status, utf8(res.body).slice(0, 300)).toBe(200)
    expect(res.headers.get('content-type')).toBe('application/zip')
    expect(res.headers.get('content-disposition')).toMatch(new RegExp(`^attachment; filename="?${FILENAME_PREFIX}`))

    zip = unzipSync(res.body)
    const names = Object.keys(zip)
    expect(names.some((n) => /^companies\/[^/]+\/invoices\.csv$/.test(n)), 'a company invoices.csv').toBe(true)
    const invoices = names.filter((n) => /^companies\/[^/]+\/invoices\.csv$/.test(n)).map((n) => utf8(zip[n])).join('\n')
    expect(invoices).toContain('INV-EXPORT-')
    for (const n of names.filter((n) => /^companies\/[^/]+\/invoices\.csv$/.test(n))) {
      const header = utf8(zip[n]).split('\n')[0].replace(/\r$/, '').split(',')
      expect(header.slice(-2), `${n} header ends with the type pair`).toEqual(['document_type', 'original_invoice_number'])
    }

    expect(names, 'the upload is named by its SHA-256').toContain(`documents/${PDF_SHA}.pdf`)
    expect(Buffer.from(zip[`documents/${PDF_SHA}.pdf`]).equals(Buffer.from(PDF_BYTES)), 'the upload bytes are intact').toBe(true)

    const audit = utf8(zip['audit_log.csv'])
    expect(audit.startsWith(AUDIT_HEADER)).toBe(true)
    expect(audit).toContain('portfolio.entity.created')
    expect(audit).toContain('invoice.created')

    manifest = JSON.parse(utf8(zip['manifest.json']))
    expect(manifest.entries.length).toBeGreaterThan(0)
    for (const e of manifest.entries) {
      expect(zip[e.name], `manifest names ${e.name}`).toBeDefined()
      expect(sha256(zip[e.name]), e.name).toBe(e.sha256)
    }
  })

  test('workspace export: one audit row per export and no document reads', async () => {
    const exported = (await auditRows(adminToken, [EVENT_EXPORTED])).filter((e) => e.actor === adminId)
    expect(exported, 'one row for the one export').toHaveLength(1)
    expect(exported[0].payload).toEqual({
      files: manifest.entries.length,
      bytes: manifest.entries.reduce((n, e) => n + e.bytes, 0),
    })
    expect((await auditRows(adminToken, [EVENT_FAILED])).filter((e) => e.actor === adminId)).toEqual([])
    expect((await auditRows(adminToken, [EVENT_DOC_READ])).length, 'the export reads no document through the reader').toBe(readsBefore)
  })

  test('workspace export: a preparer cannot export', async () => {
    const preflight = await exportPreflight(preparerToken)
    assertErrorEnvelope(preflight, 403, 'a preparer preflight')
    expect(errorOf(preflight)).toBe(EXPORT_NOT_PERMITTED)

    const res = await redeem(preparerToken)
    expect(res.status).toBe(403)
    expect(res.headers.get('content-type')).toContain('text/html')
    expect(utf8(res.body)).toContain(PAGE_NOT_ADMIN)
  })

  test('workspace export: a locked workspace still exports', async () => {
    const scheduled = await scheduleDeletion(adminToken, workspaceName)
    expect(scheduled.status, JSON.stringify(scheduled.body)).toBe(200)
    try {
      const res = await redeem(adminToken)
      expect(res.status, utf8(res.body).slice(0, 300)).toBe(200)
      expect(unzipSync(res.body)['manifest.json'], 'the locked ZIP opens').toBeDefined()
    } finally {
      const cancelled = await cancelDeletion(adminToken)
      expect(cancelled.status, JSON.stringify(cancelled.body)).toBe(200)
    }
    const exported = (await auditRows(adminToken, [EVENT_EXPORTED])).filter((e) => e.actor === adminId)
    expect(exported.length, 'the locked export left its row').toBeGreaterThanOrEqual(2)
  })

  test('workspace export: a code works once and only with its state', async () => {
    const state = mintSignInState()
    const code = codeOf(await exportCode(adminToken, state))
    expect((await exportDownload(code, state)).status).toBe(200)

    const spent = await exportDownload(code, state)
    expect(spent.status).toBe(400)
    expect(spent.headers.get('content-type')).toContain('text/html')
    expect(utf8(spent.body)).toContain(PAGE_EXPIRED)

    const code2 = codeOf(await exportCode(adminToken, mintSignInState()))
    const wrong = await exportDownload(code2, mintSignInState())
    expect(wrong.status).toBe(400)
    expect(wrong.headers.get('content-type')).toContain('text/html')
    expect(utf8(wrong.body)).toContain(PAGE_EXPIRED)

    const noBearer = await exportCode(null, mintSignInState())
    expect(noBearer.status, JSON.stringify(noBearer.body)).toBe(401)
  })
})
