// QR-ish SVG mock + FIRS fiscal-record generator, ported exactly from the prototype's
// `qr(seed, px)` / `fiscalRecord(inv)` methods (Platform.dc.html ~L1012-1032).

import type { ReactNode } from 'react'
import { hash, mulberry } from './prng'
import type { Invoice } from '../types'

export function Qr({ seed, size = 100 }: { seed: string | number; size?: number }) {
  const n = 25
  const cell = size / n
  const rnd = mulberry(hash(String(seed)))
  const fz = (r: number, c: number) => (r < 8 && c < 8) || (r < 8 && c >= n - 8) || (r >= n - 8 && c < 8)
  const els: ReactNode[] = []
  for (let r = 0; r < n; r++) {
    for (let c = 0; c < n; c++) {
      if (fz(r, c)) continue
      if (rnd() > 0.52) {
        els.push(
          <rect key={r + '_' + c} x={(c * cell).toFixed(2)} y={(r * cell).toFixed(2)} width={cell.toFixed(2)} height={cell.toFixed(2)} fill="var(--fg-1)" />,
        )
      }
    }
  }
  const finder = (R: number, C: number) => [
    <rect key={'a' + R + '_' + C} x={(C * cell).toFixed(2)} y={(R * cell).toFixed(2)} width={(7 * cell).toFixed(2)} height={(7 * cell).toFixed(2)} fill="var(--fg-1)" />,
    <rect key={'b' + R + '_' + C} x={((C + 1) * cell).toFixed(2)} y={((R + 1) * cell).toFixed(2)} width={(5 * cell).toFixed(2)} height={(5 * cell).toFixed(2)} fill="#fff" />,
    <rect key={'d' + R + '_' + C} x={((C + 2) * cell).toFixed(2)} y={((R + 2) * cell).toFixed(2)} width={(3 * cell).toFixed(2)} height={(3 * cell).toFixed(2)} fill="var(--fg-1)" />,
  ]
  const all = els.concat(finder(0, 0), finder(0, n - 7), finder(n - 7, 0))
  return (
    <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} style={{ display: 'block' }}>
      {all}
    </svg>
  )
}

export type FiscalRecord = { irn: string; csid: string; stampedAt: string }

export function fiscalRecord(inv: Invoice): FiscalRecord {
  const rnd = mulberry(hash(inv.number + '|firs'))
  const pick = (set: string, len: number) => {
    let s = ''
    for (let i = 0; i < len; i++) s += set[Math.floor(rnd() * set.length)]
    return s
  }
  const HEX = '0123456789ABCDEF'
  const B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
  const irn = inv.number + '-' + pick(HEX, 8) + '-' + inv.date.replace(/-/g, '')
  const csid = [pick(B64, 8), pick(B64, 8), pick(B64, 8), pick(B64, 8), pick(B64, 8)].join('-')
  return { irn, csid, stampedAt: inv.date + ' 11:0' + (1 + Math.floor(rnd() * 8)) }
}
