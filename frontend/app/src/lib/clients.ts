// Per-company invoice generation + dashboard build, ported exactly from the prototype's
// `genInvoices`, `buildClients`, `defaultDraft`, `statusStyle` methods
// (Platform.dc.html ~L1040-1189).

import { CFG, SECTORS } from '../data'
import { amount, fmtShort, pad2 } from './format'
import { failuresFrom } from './charts'
import { hash, mulberry } from './prng'
import { validate } from './validation'
import type { Client, ClientCfg, Draft, Invoice, InvoiceStatus, LineItem, StatusStyle } from '../types'

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

export function buildClients(): Client[] {
  return CFG.map((c) => {
    const rnd = mulberrySeed(c.name)
    const invoices = genInvoices(c, rnd)
    const errs = invoices.map((i) => validate(i))
    const failing = invoices.filter((_i, k) => errs[k].errors.length > 0).length
    const pending = invoices.filter((i) => i.status === 'Pending').length
    const vatNum = invoices.reduce((s, i) => s + amount(i.items) * 0.075, 0)
    const vatLabel = fmtShort(vatNum)
    const statusCounts: Record<string, number> = {}
    invoices.forEach((i) => (statusCounts[i.status] = (statusCounts[i.status] || 0) + 1))
    const head = Object.keys(statusCounts).sort((a, b) => statusCounts[b] - statusCounts[a])[0] || 'Draft'

    if (c.onboarding) {
      return { ...c, invoices, failing: 0, pending: 0, vatNum: 0, vatLabel: '₦0', count: 0, head: 'Draft', dash: null }
    }

    const failures = failuresFrom(invoices, errs)
    const dash = { failures }
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
