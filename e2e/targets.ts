// Shared per-environment target resolution (M4-21-10). Each PR deploys its OWN preview,
// so a hardcoded fallback is actively dangerous: a missing env var would silently point a
// PR's E2E run at another environment instead of failing loudly, producing cross-PR
// interference indistinguishable from flakiness. Every target is therefore MANDATORY: resolveTarget
// throws, naming the variable, rather than defaulting (Decision [fail-loud-targets]).
//
// Single seam for e2e/topology/targets.ts's GATEWAY_URL/APP_URL, e2e/smoke/apps.ts's
// LANDING_URL/OPS_CONSOLE_URL, and e2e/api/client.ts's apiBase() — all four previously
// duplicated the same trim-or-fallback logic against a different hardcoded literal.
export function resolveTarget(envVar: string): string {
  const raw = process.env[envVar]?.trim()
  if (!raw) {
    throw new Error(
      `${envVar} is not set. Per-PR E2E targets are set per PR preview ` +
        `and must never fall back to a hardcoded default — a missing value would ` +
        `silently point this run at the wrong environment. Set ${envVar} explicitly.`,
    )
  }
  return raw.replace(/\/+$/, '')
}

// The one E2E variable the nightly sets; any other value is a typo that must not pass as a PR run.
export function nightlyTarget(): boolean {
  const raw = process.env.E2E_TARGET
  if (raw === undefined || raw === '') return false
  if (raw === 'nightly') return true
  throw new Error(`E2E_TARGET must be unset or exactly "nightly", got "${raw}".`)
}

// The contacts delivery mode notifications reports: real on the nightly, fake on a PR preview.
export function expectedContactsMode(): 'real' | 'fake' {
  return nightlyTarget() ? 'real' : 'fake'
}
