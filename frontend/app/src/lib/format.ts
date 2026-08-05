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

// Prefix match, not full match: source is RFC3339 with trailing time. No Date
// construction -- new Date(iso).toLocaleDateString() shifts a day in negative
// UTC offsets (task-391, BUG-03-02).
export function toDateInputValue(iso: string | null | undefined): string {
  if (!iso) return ''
  const match = /^\d{4}-\d{2}-\d{2}/.exec(iso)
  return match ? match[0] : iso
}

// Date + HH:MM:SS via toLocaleString('en-NG') (the same idiom fmt/fmtPlain already use),
// mirroring fmtDate's null/empty/NaN guard exactly (M5-09-03, task-253).
export function fmtDateTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return isNaN(d.getTime()) ? '—' : d.toLocaleString('en-NG')
}

export function amount(items: LineItem[]): number {
  return items.reduce((s, it) => s + (Number(it.qty) || 0) * (Number(it.price) || 0), 0)
}
