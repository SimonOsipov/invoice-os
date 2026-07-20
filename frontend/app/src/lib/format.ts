// Formatters ported exactly from the prototype (Platform.dc.html ~L1008-1011, 1089).

import type { LineItem } from '../types'

export function fmt(n: number): string {
  return '₦' + Math.round(Number(n) || 0).toLocaleString('en-NG')
}

export function fmtPlain(n: number): string {
  return Number(Math.round(n)).toLocaleString('en-NG')
}

export function fmtShort(n: number): string {
  return n >= 1e6 ? '₦' + (n / 1e6).toFixed(1) + 'M' : n >= 1e3 ? '₦' + Math.round(n / 1e3) + 'k' : '₦' + Math.round(n)
}

export function pad2(n: number): string {
  return String(n).padStart(2, '0')
}

export function fmtDate(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return isNaN(d.getTime()) ? '—' : d.toLocaleDateString('en-NG')
}

export function amount(items: LineItem[]): number {
  return items.reduce((s, it) => s + (Number(it.qty) || 0) * (Number(it.price) || 0), 0)
}
