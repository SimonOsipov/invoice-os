// Shared per-environment target resolution (M4-21-10). Each PR now deploys to its OWN
// ephemeral Railway environment (M4-23) with a Railway-generated domain suffix that
// cannot be predicted or constructed ahead of time (F7) — so a hardcoded dev-URL fallback
// is actively dangerous: a missing env var would silently point a PR's E2E run at the
// shared `development` fleet instead of failing loudly, producing cross-PR interference
// indistinguishable from flakiness. Every target is therefore MANDATORY: resolveTarget
// throws, naming the variable, rather than defaulting (Decision [fail-loud-targets]).
//
// Single seam for e2e/topology/targets.ts's GATEWAY_URL/APP_URL, e2e/smoke/apps.ts's
// LANDING_URL/OPS_CONSOLE_URL, and e2e/api/client.ts's apiBase() — all four previously
// duplicated the same trim-or-fallback logic against a different hardcoded literal.
export function resolveTarget(envVar: string): string {
  const raw = process.env[envVar]?.trim()
  if (!raw) {
    throw new Error(
      `${envVar} is not set. Per-PR E2E targets are discovered fresh per Railway environment ` +
        `(M4-23) and must never fall back to a hardcoded default — a missing value would ` +
        `silently point this run at the wrong environment. Set ${envVar} explicitly.`,
    )
  }
  return raw.replace(/\/+$/, '')
}
