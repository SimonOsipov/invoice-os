// Pure presentation helpers. Everything here returns design-system token NAMES, never literals.

import type { Severity } from './types'

export interface StatusStyle {
  bg: string
  border: string
  text: string
  label: string
}

// Rule severity: warn is amber, info is muted, there is no green.
const SEVERITY: Record<Severity, StatusStyle> = {
  error: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'ERROR' },
  warn: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'WARN' },
  info: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'INFO' },
}

export function severityStyle(severity: Severity): StatusStyle {
  return SEVERITY[severity]
}

// Sparkline paths in a fixed 220x44 box; the range floors at 1 so a flat series draws.
export const SPARK_WIDTH = 220
export const SPARK_HEIGHT = 44

export function sparkline(points: number[]): { line: string; area: string } {
  if (points.length < 2) {
    return { line: '', area: '' }
  }
  const max = Math.max(...points)
  const min = Math.min(...points)
  const range = max - min || 1
  const step = SPARK_WIDTH / (points.length - 1)
  const xy = points.map((p, i) => [i * step, SPARK_HEIGHT - 4 - ((p - min) / range) * (SPARK_HEIGHT - 10)] as const)
  const line = xy.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`).join(' ')
  const area = `${line} L${SPARK_WIDTH} ${SPARK_HEIGHT} L0 ${SPARK_HEIGHT} Z`
  return { line, area }
}

// Dot, stroke and fill derive from one tone so a tile cannot mix them.
export function healthTone(tone: 'green' | 'amber' | 'red'): { dot: string; stroke: string; fill: string } {
  if (tone === 'amber') {
    return { dot: 'var(--status-amber-text)', stroke: 'var(--status-amber-text)', fill: 'var(--status-amber-bg)' }
  }
  if (tone === 'red') {
    return { dot: 'var(--status-red-text)', stroke: 'var(--status-red-text)', fill: 'var(--status-red-bg)' }
  }
  return { dot: 'var(--status-green-text)', stroke: 'var(--action)', fill: 'var(--action-tint)' }
}

const SENTRY_ISSUES = 'https://as-comply.sentry.io/issues/'
export const sentryTenantUrl = (id: string) => `${SENTRY_ISSUES}?query=tenant_id:${encodeURIComponent(id)}&project=4512190351474768&statsPeriod=14d`
