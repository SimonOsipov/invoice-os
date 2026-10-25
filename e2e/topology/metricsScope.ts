export type ScopedMetrics = {
  panels: { key: string; series: { name: string }[] }[]
  limits: { services?: { name: string }[] }
  deploys: { revisions?: { service: string }[] }
}

// Names of the Cloud Run panels, limits and deploys that are not in `allowed`; the fleet panel's one series is named `fleet`.
export function scopeStrays(m: ScopedMetrics, allowed: readonly string[]): string[] {
  const names = [
    ...m.panels.filter((p) => !p.key.startsWith('sql_') && p.key !== 'latency_p95_fleet_ms').flatMap((p) => p.series.map((s) => s.name)),
    ...(m.limits.services ?? []).map((s) => s.name),
    ...(m.deploys.revisions ?? []).map((r) => r.service),
  ]
  return names.filter((n) => !allowed.includes(n))
}
