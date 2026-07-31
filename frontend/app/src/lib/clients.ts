// Per-company invoice generation + dashboard build, ported (in spirit) from the
// prototype's `genInvoices`, `buildClients`, `defaultDraft`, `statusStyle` methods
// (Platform.dc.html ~L1040-1189).
//
// [entity-picker] step 1 of 3: buildClients() used to fan out over the static CFG mock
// roster; it now fans out over the LIVE portfolio entity list (lib/portfolio.ts), so the
// workspace switcher renders the same companies as the Clients page and the import
// picker (previously three independent lists, zero overlap). Every field with no backend
// source (score/readiness/readinessNote/taxpayer/vol/failTarget/sector, and the invoices/
// dash built from them) is KEPT, per product's explicit call — see the Readiness card's
// own SAMPLE chip (DashboardActive.tsx) for the existing precedent — but it is now a demo
// OVERLAY cycled onto each real entity from CFG, not that entity's own data
// (business_entities carries no sector/score/etc at all, db/seed.dev.sql).

import { CFG, SECTORS } from '../data'
import { amount, fmtShort, pad2 } from './format'
import { failuresFrom } from './charts'
import { initials } from './customers'
import { hash, mulberry } from './prng'
import { validate } from './validation'
import type { Entity } from './portfolio'
import type { Client, ClientCfg, Draft, Invoice, InvoiceStatus, LineItem, StatusStyle } from '../types'

function mulberrySeed(name: string) {
  return mulberry(hash(name))
}

// The 5 non-onboarding CFG rows, cycled as the SAMPLE demo profile (sector/score/vol/
// failTarget/readiness/readinessNote/taxpayer) for every real entity. The 6th row (Kano
// Textile, onboarding:true) is excluded from the cycle on purpose: cycled in, it would
// flag an arbitrary real entity as "onboarding" purely by its position in the fetched
// list — a demo artifact, not a real signal. onboarding now means "no client resolved
// yet" (see emptyClient below), not "this particular row of a 6-row mock".
const DEMO_TEMPLATES: ClientCfg[] = CFG.filter((c) => !c.onboarding)

// Strips a trailing legal suffix off a real entity's registered name for a shorter
// display label. The mock CFG shorts were hand-curated (e.g. "Lagos Freight & Logistics
// Ltd" -> "Lagos Freight" drops more than just the suffix) — a real entity has no such
// curation available, so this is a plain, honest approximation, not an attempt to
// replicate it.
const LEGAL_SUFFIX = /\s+(ltd\.?|limited|plc)$/i
function shortName(name: string): string {
  return name.replace(LEGAL_SUFFIX, '').trim() || name
}

export function statusStyle(s: string): StatusStyle {
  const map: Record<string, [string, string, string, string]> = {
    Transmitted: ['var(--status-green-bg)', 'var(--status-green-border)', 'var(--status-green-text)', 'TRANSMITTED'],
    Approved: ['var(--status-green-bg)', 'var(--status-green-border)', 'var(--status-green-text)', 'APPROVED'],
    Pending: ['var(--status-amber-bg)', 'var(--status-amber-border)', 'var(--status-amber-text)', 'PENDING'],
    Rejected: ['var(--status-red-bg)', 'var(--status-red-border)', 'var(--status-red-text)', 'REJECTED'],
    Draft: ['var(--status-muted-bg)', 'var(--status-muted-border)', 'var(--status-muted-text)', 'DRAFT'],
  }
  const m = map[s] || map.Draft
  return { bg: m[0], border: m[1], text: m[2], label: m[3] }
}

export function genInvoices(client: ClientCfg, rnd: () => number): Invoice[] {
  if (client.onboarding) return []
  const sd = SECTORS[client.sector]
  const n = Math.max(6, Math.min(9, Math.round(client.vol / 7) + 5))
  const validTin = () => String(10000000 + Math.floor(rnd() * 89999999)) + '-0001'
  const out: Invoice[] = []
  const idxs: number[] = []
  for (let k = 2; k < n; k++) idxs.push(k)
  for (let i = idxs.length - 1; i > 0; i--) {
    const j = Math.floor(rnd() * (i + 1))
    const t = idxs[i]
    idxs[i] = idxs[j]
    idxs[j] = t
  }
  const failSet = new Set(idxs.slice(0, Math.min(client.failTarget || 0, idxs.length)))
  for (let k = 0; k < n; k++) {
    const buyer = sd.buyers[Math.floor(rnd() * sd.buyers.length)]
    const ni = 1 + Math.floor(rnd() * 2)
    const items: LineItem[] = Array.from({ length: ni }, () => ({
      desc: sd.items[Math.floor(rnd() * sd.items.length)],
      qty: 1 + Math.floor(rnd() * 12),
      price: Math.round((sd.min + rnd() * (sd.max - sd.min)) / 1000) * 1000,
    }))
    let status: InvoiceStatus
    let tin = validTin()
    let addr = sd.addr[Math.floor(rnd() * sd.addr.length)]
    let wht = true
    if (failSet.has(k)) {
      status = 'Rejected'
      if (rnd() < 0.55) tin = String(100000 + Math.floor(rnd() * 899999))
      else addr = ''
      wht = false
    } else if (k === 0) {
      status = 'Approved'
    } else {
      const pool: InvoiceStatus[] = ['Transmitted', 'Transmitted', 'Approved', 'Pending', 'Approved', 'Transmitted', 'Pending', 'Draft']
      status = pool[Math.floor(rnd() * pool.length)]
    }
    out.push({ number: 'INV-2026-00' + (481 - k), buyer, buyerTin: tin, buyerAddress: addr, date: '2026-06-' + pad2(15 - k), items, status, wht })
  }
  return out
}

// Shared tail of buildClientForEntity/emptyClient below: generate the SAMPLE invoices
// off the (possibly synthetic) cfg + compute the same derived dashboard fields the
// original CFG-driven buildClients always did — unchanged from before this story except
// that entityId is now threaded through onto the result.
function finishClient(cfg: ClientCfg, entityId: string | null): Client {
  const rnd = mulberrySeed(cfg.name)
  const invoices = genInvoices(cfg, rnd)

  if (cfg.onboarding) {
    return { ...cfg, entityId, invoices, failing: 0, pending: 0, vatNum: 0, vatLabel: '₦0', count: 0, head: 'Draft', dash: null }
  }

  const errs = invoices.map((i) => validate(i))
  const failing = invoices.filter((_i, k) => errs[k].errors.length > 0).length
  const pending = invoices.filter((i) => i.status === 'Pending').length
  const vatNum = invoices.reduce((s, i) => s + amount(i.items) * 0.075, 0)
  const vatLabel = fmtShort(vatNum)
  const statusCounts: Record<string, number> = {}
  invoices.forEach((i) => (statusCounts[i.status] = (statusCounts[i.status] || 0) + 1))
  const head = Object.keys(statusCounts).sort((a, b) => statusCounts[b] - statusCounts[a])[0] || 'Draft'
  const failures = failuresFrom(invoices, errs)
  return { ...cfg, entityId, invoices, failing, pending, vatNum, vatLabel, count: invoices.length, head, dash: { failures } }
}

// Builds ONE Client from a real portfolio entity + its cycled demo template. Exported
// (not just buildClients() below) so a caller can rebuild a single client without
// regenerating every other one — see App.tsx's entities-sync effect.
//
// The template is picked by a hash of the entity's OWN id, not its position in the
// fetched array: keying by array position would make an entity's SAMPLE score/sector/
// readiness silently change across refetches whenever the server's list order shifts
// (e.g. a newly-created entity lands in the middle) — reading as a bug, since nothing
// about the real entity actually changed. Hashing the id keeps the assignment stable
// for as long as that entity exists, refetch after refetch.
export function buildClientForEntity(e: Entity): Client {
  const template = DEMO_TEMPLATES[hash(e.id) % DEMO_TEMPLATES.length]
  const cfg: ClientCfg = {
    ...template,
    name: e.name,
    short: shortName(e.name),
    initials: initials(e.name),
    tin: e.tin ?? '—',
    onboarding: false,
  }
  return finishClient(cfg, e.id)
}

export function buildClients(entities: Entity[]): Client[] {
  return entities.map((e) => buildClientForEntity(e))
}

// The window before the live entity list first resolves (loading/error/no-gateway), or a
// tenant (either persona, [entity-picker] trap 2) with genuinely zero entities — every
// one of the ~15 places reading ctx.active needs SOMETHING defined, never `undefined`.
// onboarding: true reuses the existing "nothing here yet" dashboard rather than
// inventing a second empty state; entityId stays null since no real entity backs this
// placeholder either.
export function emptyClient(): Client {
  const cfg: ClientCfg = {
    name: 'No client yet',
    short: 'No client',
    initials: '—',
    tin: '—',
    taxpayer: 'Small',
    sector: 'foods',
    score: null,
    vol: 0,
    readiness: [0, 0, 0],
    readinessNote: '',
    onboarding: true,
  }
  return finishClient(cfg, null)
}

// [in-house-degenerate-case]: the ONE resolution path for BOTH workspace modes,
// deleting App.tsx's old `if (mode === 'inhouse') return inhouseClient(...)` special
// case (task-304 AC-2), which never even looked at the live entity list — so a real
// business_entities row seeded for in-house (AC-1) still resolved to entityId: null.
// An explicit id match wins first; with nothing explicitly chosen (first load, or a
// stale/cleared id), the only defensible default is the server's own row — portfolio
// store.go's List is `ORDER BY name ASC, id ASC`, so this is never incidental
// position-in-an-unordered-response. For a workspace with EXACTLY ONE entity (in-house's
// normal shape once it owns one) that default is simply THAT entity: there is no second
// row for a "clients[0] ordering" concern to be about. Zero entities anywhere (either
// persona — a brand-new in-house tenant before its first entity, same as a firm tenant
// with none) falls to emptyClient(), never a persona-flavoured placeholder: AC-3's
// "degrades honestly" reads off `activeEntity` (null either way) via the amber panel and
// filing refusal, never off which Client shape this returns.
export function resolveActiveClient(clients: Client[], activeEntityId: string | null): Client {
  if (activeEntityId != null) {
    const selected = clients.find((c) => c.entityId === activeEntityId)
    if (selected) return selected
  }
  return clients[0] ?? emptyClient()
}

// The manual create form's starting state. Every field here is now genuinely EDITABLE and
// every one of them crosses the wire on POST /v1/invoices (INVCR-01-03), so these are real
// defaults, not a mock fixture:
//
// - `buyerTin` seeds BLANK. It used to seed '198477' — six digits, deliberately malformed
//   so the deleted mock validator would demo its own error state. With the mock gone that
//   value became the default body of every REAL invoice, making the server flag a
//   buyer-tin-format violation the app itself had authored. Blank maps to `buyer_tin: null`
//   (nullIfBlank), which is legal and honest: nothing was entered.
// - `number` still seeds a fixed value, and the (tenant_id, entity_id, invoice_number)
//   UNIQUE index means the SECOND filing under one entity 409s on it — resolvable only
//   because the field is now an input the operator can change. Not auto-uniquified: an
//   invoice number is a fiscal identifier the product never guesses (§9).
// - `date` still seeds the demo '2026-06-16' rather than today's date. Reseeding to today
//   is a product call and would put nondeterminism into the flow INVCR-01-16's e2e drives;
//   the field is editable and issue_date is also fixable on the detail screen.
// - `currency` stays fixed at NGN with no editor: NGN-only is the real `currency-allowed`
//   rule parameter, not a UI simplification, so a picker would offer values the rule pack
//   rejects.
export function defaultDraft(client: ClientCfg): Draft {
  const sd = SECTORS[client.sector] || SECTORS.foods
  return {
    number: 'INV-2026-00482',
    buyer: sd.buyers[0],
    buyerTin: '',
    date: '2026-06-16',
    currency: 'NGN',
    items: [
      { desc: 'Logistics consulting — Q2', qty: 1, price: 2500000 },
      { desc: sd.items[1] || 'Supply', qty: 12, price: 85000 },
    ],
  }
}
