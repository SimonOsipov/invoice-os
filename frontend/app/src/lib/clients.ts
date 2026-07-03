// Per-company invoice generation + dashboard build, ported exactly from the prototype's
// `genInvoices`, `buildClients`, `defaultDraft`, `statusStyle`, `pillFor` methods
// (Platform.dc.html ~L1040-1189).

import { CFG, SECTORS } from '../data'
import { amount, fmtShort, pad2 } from './format'
import { chartScore, donutFrom, failuresFrom, spark, trend } from './charts'
import { hash, mulberry } from './prng'
import { validate } from './validation'
import type { Client, ClientCfg, Draft, Invoice, InvoiceStatus, LineItem, StatusStyle, Taxpayer } from '../types'

function mulberrySeed(name: string) {
  return mulberry(hash(name))
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

export function pillFor(taxpayer: Taxpayer): StatusStyle {
  if (taxpayer === 'Large') return { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'LARGE TAXPAYER · MBS LIVE' }
  if (taxpayer === 'Small') return { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'SMALL TAXPAYER · ROLLOUT PLANNED' }
  return { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'MEDIUM TAXPAYER · ROLLOUT NEXT' }
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

const ACTIVITY_DOT: Record<string, string> = {
  green: 'var(--status-green-text)',
  red: 'var(--status-red-text)',
  teal: 'var(--accent)',
  amber: 'var(--status-amber-text)',
  muted: 'var(--line-3)',
}

export function buildClients(): Client[] {
  return CFG.map((c) => {
    const rnd = mulberrySeed(c.name)
    const invoices = genInvoices(c, rnd)
    const errs = invoices.map((i) => validate(i))
    const failing = invoices.filter((_i, k) => errs[k].errors.length > 0).length
    const pending = invoices.filter((i) => i.status === 'Pending').length
    const transmitted = invoices.filter((i) => i.status === 'Transmitted').length
    const vatNum = invoices.reduce((s, i) => s + amount(i.items) * 0.075, 0)
    const vatLabel = fmtShort(vatNum)
    const statusCounts: Record<string, number> = {}
    invoices.forEach((i) => (statusCounts[i.status] = (statusCounts[i.status] || 0) + 1))
    const head = Object.keys(statusCounts).sort((a, b) => statusCounts[b] - statusCounts[a])[0] || 'Draft'

    if (c.onboarding) {
      return { ...c, invoices, failing: 0, pending: 0, vatNum: 0, vatLabel: '₦0', count: 0, head: 'Draft', dash: null }
    }

    const score = c.score as number
    const circ = 2 * Math.PI * 50
    const ringColor = score >= 85 ? 'var(--accent)' : score >= 70 ? 'var(--status-amber-text)' : 'var(--status-red-text)'
    const ring = { circ: circ.toFixed(1), offset: (circ * (1 - score / 100)).toFixed(1), color: ringColor }
    const readinessMetrics = [
      { label: 'Field completeness', pct: c.readiness[0] + '%', color: c.readiness[0] >= 85 ? 'var(--status-green-text)' : 'var(--status-amber-text)' },
      { label: 'Tax accuracy · VAT / WHT', pct: c.readiness[1] + '%', color: c.readiness[1] >= 85 ? 'var(--status-green-text)' : 'var(--status-amber-text)' },
      { label: 'Transmit-ready', pct: c.readiness[2] + '%', color: c.readiness[2] >= 85 ? 'var(--status-green-text)' : 'var(--status-amber-text)' },
    ]
    const kpis = [
      { label: 'Invoices', value: String(invoices.length), delta: transmitted + ' transmitted', deltaColor: 'var(--fg-3)', stroke: 'var(--accent)', spark: spark(trend(rnd, 1)) },
      { label: 'VAT tracked', value: vatLabel, delta: 'incl. 7.5% VAT', deltaColor: 'var(--fg-3)', stroke: 'var(--accent)', spark: spark(trend(rnd, 1)) },
      { label: 'Failing invoices', value: String(failing), delta: failing ? 'needs fixing' : 'all clear', deltaColor: failing ? 'var(--status-red-text)' : 'var(--status-green-text)', stroke: 'var(--status-red-text)', spark: spark(trend(rnd, -1)) },
      { label: 'Pending approval', value: String(pending), delta: pending ? 'awaiting review' : 'none pending', deltaColor: pending ? 'var(--status-amber-text)' : 'var(--fg-3)', stroke: 'var(--status-amber-text)', spark: spark(trend(rnd, 1)) },
    ]
    const chart = chartScore(rnd, score)
    const donut = donutFrom(statusCounts)
    const failures = failuresFrom(invoices, errs)
    const inv = invoices
    const fIdx = invoices.findIndex((_i, k) => errs[k].errors.length > 0)
    const pick = (i: number) => inv[i % inv.length]?.number || 'INV-2026-00480'
    const activityRaw: [string, string, string, string, string][] = [
      ['You', 'approved', pick(0), '2m ago', 'teal'],
      failing ? ['Engine', 'flagged errors on', inv[fIdx]?.number || pick(1), '24m ago', 'red'] : ['Engine', 'validated', pick(1), '24m ago', 'green'],
      ['T. Adeyemi', 'transmitted', pick(2), '1h ago', 'green'],
      ['Import', 'added ' + (8 + inv.length) + ' invoices', 'via CSV', '3h ago', 'muted'],
      ['Engine', 'validated', pick(3), '5h ago', 'amber'],
    ]
    const activity = activityRaw.map((a, i) => ({ who: a[0], action: a[1], target: a[2], time: a[3], dot: ACTIVITY_DOT[a[4]], line: i === 4 ? '0px' : '18px' }))
    const resolveLabel = failing ? 'Resolve ' + failing + ' failing ' + (failing === 1 ? 'invoice' : 'invoices') + ' →' : 'All invoices compliant'
    const dash = {
      score,
      ring,
      readinessNote: c.readinessNote,
      readinessMetrics,
      failing,
      resolveLabel,
      kpis,
      chart,
      donut: donut.seg,
      donutMeta: donut.meta,
      failures,
      hasFailures: failures.length > 0,
      noFailures: failures.length === 0,
      activity,
      pill: pillFor(c.taxpayer),
    }
    return { ...c, invoices, failing, pending, vatNum, vatLabel, count: invoices.length, head, dash }
  })
}

export function defaultDraft(client: ClientCfg): Draft {
  const sd = SECTORS[client.sector] || SECTORS.foods
  return {
    number: 'INV-2026-00482',
    buyer: sd.buyers[0],
    buyerTin: '198477',
    buyerAddress: '',
    date: '2026-06-16',
    currency: 'NGN',
    wht: false,
    docType: 'B2B',
    items: [
      { desc: 'Logistics consulting — Q2', qty: 1, price: 2500000 },
      { desc: sd.items[1] || 'Supply', qty: 12, price: 85000 },
    ],
  }
}
