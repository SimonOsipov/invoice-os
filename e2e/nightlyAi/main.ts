// The nightly AI check: calls each AI feature on the deployed fleet, scores the extractions against the key
// and writes the ai section of summary.json. Run: pnpm --filter @invoice-os/e2e nightly-ai --out <file>.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import { parseArgs } from 'node:util'

import type * as Client from '../api/client'
import { freshTin } from '../api/fixtures'
import { scrub } from '../mail/mailChecks'
import { accuracy, scoreDocument, type KeyRow, type Values } from './score'
import { buildSection, CALL_SETS, STEP_NAMES, judgeAccuracy, judgeCalls, addDecimals, type AiRow, type Step, type Verdict } from './section'
import { ACCURACY_THRESHOLD, THRESHOLD_PROVISIONAL } from './threshold'

const CORPUS_DIR = new URL('../../internal/extraction/testdata/', import.meta.url)
const KEY_FILE = 'nightly_key.json'
const EXTRACTION_FILE = 'corpus_inline_labels.pdf'
const POOL = 4
const MARGIN_MS = 60_000
const PAGE_LIMIT = '100'
const MAX_PAGES = 50
const ROW_POLLS = 30
const JOB_POLLS = 180
const POLL_MS = 1_000
const RULE_KEY = 'line-cost-non-negative'
const RULE_PATH = 'line_items[2]'
const IMPORT_CSV = ['Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price', 'INV-NIGHTLY-1,2026-01-15,87654321-0002,Nightly Buyer,NGN,1000.00,75.00,1075.00,Item 1,1,100.00'].join('\n')

type Page = { has_more: boolean; next_cursor: string | null }
export type CallsPage = { calls: AiRow[]; page: Page }
export type TotalsPage = { totals: { cost: string }[]; page: Page }

export type Deps = {
  now: () => Date
  sleep: (ms: number) => Promise<void>
  log: (line: string) => void
  readKey: () => KeyRow[]
  readPdf: (file: string) => Uint8Array
  provisionRealAccount: typeof Client.provisionRealAccount
  signInSession: typeof Client.signInSession
  provisionStaffAccount: typeof Client.provisionStaffAccount
  me: typeof Client.me
  createEntity: typeof Client.createEntity
  createInvoice: typeof Client.createInvoice
  validateInvoice: typeof Client.validateInvoice
  explainViolation: typeof Client.explainViolation
  suggestMapping: typeof Client.suggestMapping
  checkMapping: typeof Client.checkMapping
  getExtractions: typeof Client.getExtractions
  getExtractionDetail: typeof Client.getExtractionDetail
  previewCsv: (token: string, csv: string) => Promise<string>
  uploadPdf: (token: string, file: string, bytes: Uint8Array) => Promise<string>
  listCalls: (token: string, query: Record<string, string>) => Promise<CallsPage>
  listTotals: (token: string, query: Record<string, string>) => Promise<TotalsPage>
}

const bearer = (token: string): Record<string, string> => ({ Authorization: `Bearer ${token}` })

async function postForm(client: typeof Client, path: string, token: string, file: string, type: string, bytes: BlobPart): Promise<string> {
  const form = new FormData()
  form.set('file', new Blob([bytes], { type }), file)
  const res = await fetch(`${client.apiBase()}${path}`, { method: 'POST', headers: bearer(token), body: form })
  const body = (await res.json().catch(() => ({}))) as { document_id?: string }
  if (typeof body.document_id !== 'string') throw new Error(`${path} answered ${res.status} with no document_id`)
  return body.document_id
}

async function getJson<T>(client: typeof Client, path: string, token: string, query: Record<string, string>): Promise<T> {
  const res = await client.rawFetch(`${path}?${new URLSearchParams(query)}`, { headers: bearer(token) })
  if (res.status !== 200) throw new Error(`${path} answered ${res.status}`)
  return res.body as T
}

// Imported late: the client reads the seven target URLs at load, and a missing one must become a failed section.
export async function realDeps(): Promise<Deps> {
  const client = await import('../api/client')
  const dir = (f: string) => new URL(f, CORPUS_DIR)
  return {
    now: () => new Date(),
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    log: (line) => console.log(line),
    readKey: () => JSON.parse(readFileSync(dir(KEY_FILE), 'utf8')) as KeyRow[],
    readPdf: (file) => new Uint8Array(readFileSync(dir(file))),
    provisionRealAccount: client.provisionRealAccount,
    signInSession: client.signInSession,
    provisionStaffAccount: client.provisionStaffAccount,
    me: client.me,
    createEntity: client.createEntity,
    createInvoice: client.createInvoice,
    validateInvoice: client.validateInvoice,
    explainViolation: client.explainViolation,
    suggestMapping: client.suggestMapping,
    checkMapping: client.checkMapping,
    getExtractions: client.getExtractions,
    getExtractionDetail: client.getExtractionDetail,
    previewCsv: (token, csv) => postForm(client, '/api/invoice/v1/imports/preview', token, 'import.csv', 'text/csv', csv),
    uploadPdf: (token, file, bytes) => postForm(client, '/api/submission/v1/documents', token, file, 'application/pdf', bytes as BlobPart),
    listCalls: (token, query) => getJson(client, '/api/invoice/v1/staff/ai-calls', token, query),
    listTotals: (token, query) => getJson(client, '/api/invoice/v1/staff/ai-calls/totals', token, query),
  }
}

const short = (e: unknown): string => scrub(e instanceof Error ? e.message : String(e)).slice(0, 300)

async function pool<T, R>(items: T[], size: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length)
  let next = 0
  await Promise.all(
    Array.from({ length: Math.min(size, items.length) }, async () => {
      while (next < items.length) {
        const i = next++
        out[i] = await fn(items[i])
      }
    }),
  )
  return out
}

// Returns 0 whenever it wrote the file: the section carries the verdict.
export async function run(deps: Deps, out: string): Promise<number> {
  const started = deps.now()
  const from = new Date(started.getTime() - MARGIN_MS).toISOString()
  const steps: Step[] = []
  let acc = 0
  let cost: string | null = null

  let key: KeyRow[]
  let token = ''
  let staff = ''
  let tenant = ''
  let entityId = ''
  try {
    key = deps.readKey()
    const account = await deps.provisionRealAccount('nightly-ai')
    token = (await deps.signInSession(account.email, account.password)).access_token
    tenant = (await deps.me(token)).tenant.id
    const tin = freshTin()
    entityId = (await deps.createEntity(token, { name: `Nightly AI ${tin}`, tin })).id
    const staffAccount = await deps.provisionStaffAccount('nightly-ai-staff')
    staff = (await deps.signInSession(staffAccount.email, staffAccount.password)).access_token
  } catch (e) {
    return writeSetupFailure(out, e)
  }

  const timed = async (name: string, fn: () => Promise<Verdict>): Promise<void> => {
    const t0 = deps.now().getTime()
    let step: Step
    try {
      const v = await fn()
      step = { name, status: v.ok ? 'passed' : 'failed', duration_ms: 0, ...(v.detail && { detail: v.detail }) }
    } catch (e) {
      step = { name, status: 'failed', duration_ms: 0, detail: short(e) }
    }
    step.duration_ms = deps.now().getTime() - t0
    steps.push(step)
  }

  const listAll = async (): Promise<AiRow[]> => {
    const rows: AiRow[] = []
    let cursor: string | null = null
    for (let p = 0; p < MAX_PAGES; p++) {
      const page: CallsPage = await deps.listCalls(staff, { tenant, from, limit: PAGE_LIMIT, ...(cursor && { cursor }) })
      rows.push(...page.calls)
      if (!page.page.has_more) return rows
      cursor = page.page.next_cursor
    }
    throw new Error(`the call list ran past ${MAX_PAGES} pages`)
  }

  // The recorder writes after the response, so a required row can land late.
  const judge = async (name: keyof typeof CALL_SETS): Promise<Verdict> => {
    const set = CALL_SETS[name]
    let rows: AiRow[] = []
    for (let i = 0; i < ROW_POLLS; i++) {
      rows = await listAll()
      if (rows.some((r) => `${r.client}/${r.purpose}` === set.required)) break
      if (i < ROW_POLLS - 1) await deps.sleep(POLL_MS)
    }
    return judgeCalls(rows, set)
  }

  const documents = new Map<string, Promise<Values>>()
  const extract = (file: string): Promise<Values> => {
    let p = documents.get(file)
    if (!p) {
      p = (async () => {
        const documentId = await deps.uploadPdf(token, file, deps.readPdf(file))
        for (let i = 0; i < JOB_POLLS; i++) {
          const { jobs } = await deps.getExtractions(token, documentId)
          if (jobs.some((j) => j.state === 'dead_lettered')) throw new Error('extraction dead-lettered')
          const done = jobs.find((j) => j.state === 'succeeded')
          if (done) {
            const detail = await deps.getExtractionDetail(token, done.id)
            return Object.fromEntries(detail.fields.map((f) => [f.name, f.value]))
          }
          await deps.sleep(POLL_MS)
        }
        throw new Error(`no extraction succeeded within ${JOB_POLLS} polls`)
      })()
      documents.set(file, p)
    }
    return p
  }

  await timed('extraction', async () => {
    await extract(EXTRACTION_FILE)
    return judge('extraction')
  })

  let csvDocument: Promise<string> | undefined
  const csv = (): Promise<string> => (csvDocument ??= deps.previewCsv(token, IMPORT_CSV))

  await timed('suggest-mapping', async () => {
    await deps.suggestMapping(token, { entity_id: entityId, document_id: await csv() })
    return judge('suggest-mapping')
  })

  await timed('explain', async () => {
    const invoice = await deps.createInvoice(token, {
      entity_id: entityId,
      invoice_number: `INV-NIGHTLY-${freshTin()}`,
      issue_date: '2026-01-01T00:00:00Z',
      supplier_tin: freshTin(),
      supplier_name: 'Acme Nigeria Ltd',
      buyer_tin: '87654321-0002',
      buyer_name: 'Buyer Ltd',
      currency: 'NGN',
      subtotal: '100',
      vat: '7.50',
      total: '107.50',
      line_items: [
        { description: 'Widget A', quantity: '1', unit_price: '105.00', line_total: '105.00' },
        { description: 'Widget B', quantity: '1', unit_price: '-5.00', line_total: '-5.00' },
        { description: 'Widget C', quantity: '1', unit_price: '0.00', line_total: '0.00' },
      ],
    })
    await deps.validateInvoice(token, invoice.id)
    await deps.explainViolation(token, invoice.id, { rule_key: RULE_KEY, path: RULE_PATH })
    return judge('explain')
  })

  await timed('check-mapping', async () => {
    await deps.checkMapping(token, { document_id: await csv(), mapping: { invoice_number: 'Invoice No' } })
    return judge('check-mapping')
  })

  await timed('accuracy', async () => {
    const results = await pool(key, POOL, async (row) => {
      let values: Values | null = null
      try {
        values = await extract(row.file)
      } catch (e) {
        deps.log(`document ${row.file} failed: ${short(e)}`)
      }
      const score = scoreDocument(row, values)
      for (const m of score.misses) deps.log(`miss ${row.file} ${m.field}: want ${JSON.stringify(m.want)} got ${JSON.stringify(m.got)}`)
      return score
    })
    acc = accuracy(results)
    return judgeAccuracy(acc, ACCURACY_THRESHOLD, THRESHOLD_PROVISIONAL)
  })

  cost = await readCost(deps, staff)
  return write(out, buildSection(steps, acc, ACCURACY_THRESHOLD, THRESHOLD_PROVISIONAL, cost))
}

// The sum of every page of the totals; any failure is null and fails no step.
async function readCost(deps: Deps, staff: string): Promise<string | null> {
  try {
    const costs: string[] = []
    let cursor: string | null = null
    for (let p = 0; p < MAX_PAGES; p++) {
      const page: TotalsPage = await deps.listTotals(staff, { limit: PAGE_LIMIT, ...(cursor && { cursor }) })
      costs.push(...page.totals.map((t) => t.cost))
      if (!page.page.has_more) return addDecimals(costs)
      cursor = page.page.next_cursor
    }
    deps.log(`warning: the totals ran past ${MAX_PAGES} pages; cost_usd is null`)
  } catch (e) {
    deps.log(`warning: the totals read failed (${short(e)}); cost_usd is null`)
  }
  return null
}

export function writeSetupFailure(out: string, e: unknown): number {
  const detail = `setup failed: ${short(e)}`
  const steps = STEP_NAMES.map((name): Step => ({ name, status: 'failed', duration_ms: 0, detail }))
  return write(out, buildSection(steps, 0, ACCURACY_THRESHOLD, THRESHOLD_PROVISIONAL, null))
}

function write(out: string, section: unknown): number {
  mkdirSync(dirname(out), { recursive: true })
  writeFileSync(out, JSON.stringify(section) + '\n')
  return 0
}

if (import.meta.main) {
  const { values } = parseArgs({ options: { out: { type: 'string' } } })
  if (!values.out) {
    console.error('usage: nightly-ai --out <file>')
    process.exitCode = 2
  } else {
    const out = values.out
    void realDeps()
      .then(
        (deps) => run(deps, out),
        (e) => writeSetupFailure(out, e),
      )
      .then(
        (code) => {
          process.exitCode = code
        },
        (e) => {
          console.error(short(e))
          process.exitCode = 1
        },
      )
  }
}
