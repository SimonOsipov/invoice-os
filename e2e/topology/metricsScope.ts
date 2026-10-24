export type ScopedMetrics = {
  panels: { key: string; series: { name: string }[] }[]
  limits: { services?: { name: string }[] }
  deploys: { revisions?: { service: string }[] }
}

// Names of the eight Cloud Run panels, limits and deploys that are not in `allowed`.
export function scopeStrays(m: ScopedMetrics, allowed: readonly string[]): string[] {
  const names = [
    ...m.panels.filter((p) => !p.key.startsWith('sql_')).flatMap((p) => p.series.map((s) => s.name)),
    ...(m.limits.services ?? []).map((s) => s.name),
    ...(m.deploys.revisions ?? []).map((r) => r.service),
  ]
  return names.filter((n) => !allowed.includes(n))
}
