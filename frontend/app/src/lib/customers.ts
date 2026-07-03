// Customer aggregation from an invoice list — shared by the Customers and Reports views
// (both derive `custList` from `active.invoices` in the prototype's renderVals(),
// ~L1462-1466 / ~L1472-1473).

import { amount } from './format'
import type { Invoice } from '../types'

export type CustomerAgg = {
  name: string
  tin: string
  totalNum: number
  count: number
  last: string
  valid: boolean
}

const validTin = (t: string) => /^\d{8}-\d{4}$/.test(t)

export function aggregateCustomers(invoices: Invoice[]): CustomerAgg[] {
  const cm: Record<string, CustomerAgg> = {}
  invoices.forEach((i) => {
    const k = i.buyer
    const amt = amount(i.items) * 1.075
    if (!cm[k]) cm[k] = { name: k, tin: i.buyerTin, totalNum: 0, count: 0, last: i.date, valid: true }
    const o = cm[k]
    o.count++
    o.totalNum += amt
    if (i.date > o.last) o.last = i.date
    if (!validTin(i.buyerTin)) {
      o.valid = false
      o.tin = i.buyerTin
    }
  })
  return Object.keys(cm)
    .map((k) => cm[k])
    .sort((a, b) => b.totalNum - a.totalNum)
}

export function initials(name: string): string {
  return name
    .replace(/[^A-Za-z ]/g, '')
    .split(' ')
    .filter(Boolean)
    .map((w) => w[0])
    .join('')
    .slice(0, 2)
    .toUpperCase()
}
