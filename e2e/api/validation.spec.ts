// M3-14-03 (Core AC 3): validation collect-all + live kill-switch, over the wire —
// through the SAME typed seam (api/client.ts) every api/ spec shares. Re-drives
// M3-10's collect-all engine (internal/validation) and its per-rule kill-switch
// across gateway -> validation service -> DB, against the migration-seeded v1
// rule set (db/seed.dev.sql).
//
// This file mutates the GLOBAL, shared `rules` table on the dev fleet (there is
// one `rules` row per key, not per-tenant — Decision A3). Every other api/ spec
// (and every other engineer/CI run hitting the same dev fleet) depends on the
// seeded rules being enabled, so the D3 robustness protocol below is not
// optional polish: a crashed run that leaves `vat-standard-rate` or
// `currency-allowed` disabled would silently break unrelated specs until
// someone notices and manually re-enables the rule. Hence:
//   - beforeAll SELF-HEALS both target rules to enabled before any test runs,
//     curing a prior crashed run's leak. Unlike the afterAll/finally restore
//     path, the self-heal RE-THROWS on a genuinely unexpected failure (not
//     409, not network) so a broken precondition aborts the file loudly here
//     instead of surfacing as confusing mid-test assertion failures.
//   - afterAll RESTORES both target rules to enabled unconditionally, and can
//     never itself throw (a throwing cleanup would both mask the real
//     assertion failure that triggered it AND still leave the rule disabled).
//     Playwright still runs afterAll even when beforeAll throws, so this
//     backstop holds regardless of which hook failed.
//   - Both directions tolerate 409 ErrRedundantTransition (PATCH enabled:true
//     on an already-enabled rule, or enabled:false on an already-disabled one)
//     as success, since idempotent-looking retries are expected here.
import { test, expect } from '@playwright/test'
import { login, validate, toggleRule, PERSONAS, ApiError, type ValidateResult } from './client'
import { validInvoice, badInvoice, manyViolations, currencyUsdInvoice, BAD_INVOICE_KEYS, MANY_VIOLATION_KEYS } from './fixtures'

// keysOf(): the sorted rule_key set of a ValidateResult (Engine.Evaluate sorts
// its output — Decision N16 — but we sort again here so this assertion doesn't
// silently depend on that ordering guarantee holding).
function keysOf(result: ValidateResult): string[] {
  return result.violations.map((v) => v.rule_key).sort()
}

// toggleEnabledResilient(): the shared core of ensureEnabled/selfHeal below.
// Always tolerates 409 ErrRedundantTransition (already enabled) and a single
// network-class retry (logging-and-returning if the retry itself fails —
// never throwing on a network error, in either caller). `throwOnUnexpected`
// controls only what happens for a genuinely unexpected failure (5xx,
// malformed body): swallow-and-log (ensureEnabled, used from cleanup) or
// re-throw (selfHeal, used from beforeAll) — see the rationale on each
// wrapper below.
async function toggleEnabledResilient(token: string, key: string, throwOnUnexpected: boolean): Promise<void> {
  try {
    await toggleRule(token, key, true)
    return
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      // ErrRedundantTransition — already enabled. Success.
      return
    }
    if (err instanceof ApiError && err.kind === 'network') {
      try {
        await toggleRule(token, key, true)
        return
      } catch (retryErr) {
        if (retryErr instanceof ApiError && retryErr.status === 409) {
          return
        }
        console.error(`toggleEnabledResilient(${key}): retry-once after network error still failed; leaving rule state as-is`, retryErr)
        return
      }
    }
    if (throwOnUnexpected) {
      throw err
    }
    // Any other unexpected failure (5xx, malformed body, etc): log and swallow.
    console.error(`toggleEnabledResilient(${key}): unexpected failure; leaving rule state as-is`, err)
  }
}

// ensureEnabled(): force-enable `key`, NEVER throwing. Used as the afterAll
// restore and each kill-switch test's own `finally` restore (D3) — a cleanup
// that can throw is worse than useless here, since Playwright would still
// report the ORIGINAL assertion failure but the rule would be left disabled
// AND that failure would be masked by whatever this throws next.
async function ensureEnabled(token: string, key: string): Promise<void> {
  return toggleEnabledResilient(token, key, false)
}

// selfHeal(): the beforeAll-only variant — same 409/network tolerance, but
// RE-THROWS any other unexpected failure instead of swallowing it (per
// product-advisor review). ensureEnabled must stay silent because it backstops
// afterAll/finally, but silently swallowing an unexpected failure in the
// beforeAll self-heal would convert a genuine setup bug into confusing
// mid-test false-negative assertions instead of one clear, loud setup
// failure. This is safe: Playwright still runs afterAll (which uses the
// non-throwing ensureEnabled) even when beforeAll throws, so a thrown error
// here can never skip the restore or leave a rule disabled.
async function selfHeal(token: string, key: string): Promise<void> {
  return toggleEnabledResilient(token, key, true)
}

// disableRule(): toggle `key` off, tolerating 409 (already disabled — e.g. a
// prior crashed run's leak that beforeAll's self-heal hadn't yet run against,
// or a re-run of this same test). Any other failure propagates: this is
// called mid-test, not from cleanup, so a real failure here is a genuine
// assertion-relevant signal and must not be swallowed.
async function disableRule(token: string, key: string): Promise<void> {
  try {
    await toggleRule(token, key, false)
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      return
    }
    throw err
  }
}

// Serial within this file: test 2 (bad invoice) and the kill-switch tests all
// assume the seeded v1 rule set is in its baseline (all-enabled) state at
// start, and the kill-switch tests explicitly mutate shared global state one
// at a time. (playwright.api.config.ts already runs the whole suite with
// workers: 1 / fullyParallel: false across files — this is belt-and-braces
// for this file's internal ordering.)
test.describe.configure({ mode: 'serial' })

test.describe('validation collect-all + live kill-switch (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
    // D3 self-heal: force BOTH target rules enabled before any test/toggle
    // runs, healing a prior crashed run's leak on this shared dev fleet.
    // Uses selfHeal (not ensureEnabled) so a genuinely unexpected failure
    // aborts the file loudly here, rather than surfacing as confusing
    // mid-test assertion failures against a rule that never got re-enabled.
    await selfHeal(token, 'vat-standard-rate')
    await selfHeal(token, 'currency-allowed')
  })

  test.afterAll(async () => {
    // D3 restore: unconditional, 409-tolerant, retry-once-on-network, and
    // (per ensureEnabled) never throws — the ultimate backstop even if a
    // test's own try/finally restore never ran (e.g. the process crashed
    // mid-test rather than merely failing an assertion).
    await ensureEnabled(token, 'vat-standard-rate')
    await ensureEnabled(token, 'currency-allowed')
  })

  test('valid invoice -> zero violations, stamped rule_set_version 1', async () => {
    const result = await validate(token, validInvoice)
    expect(result.rule_set_version).toBe(1)
    expect(result.violations).toEqual([])
  })

  test('bad invoice -> exactly [supplier-tin-format, vat-standard-rate], each fully stamped', async () => {
    const result = await validate(token, badInvoice)
    expect(keysOf(result)).toEqual(BAD_INVOICE_KEYS)
    expect(result.rule_set_version).toBe(1)
    for (const violation of result.violations) {
      expect(violation.rule_key.length).toBeGreaterThan(0)
      expect(violation.severity.length).toBeGreaterThan(0)
      expect(violation.message.length).toBeGreaterThan(0)
    }
  })

  test('manyViolations -> exactly the 8 sorted keys (collect-all breadth, not fail-fast)', async () => {
    const result = await validate(token, manyViolations)
    expect(keysOf(result)).toEqual(MANY_VIOLATION_KEYS)
  })

  test('kill-switch: disabling vat-standard-rate drops only it — supplier-tin-format (control) still fires; reversible', async () => {
    const baseline = await validate(token, badInvoice)
    expect(keysOf(baseline)).toContain('vat-standard-rate')
    expect(keysOf(baseline)).toContain('supplier-tin-format')

    try {
      await disableRule(token, 'vat-standard-rate')
      const disabled = await validate(token, badInvoice)
      expect(keysOf(disabled)).not.toContain('vat-standard-rate')
      // Control: the OTHER violation on the same bad invoice still fires —
      // proves only the toggled rule dropped, not that the engine went dark.
      expect(keysOf(disabled)).toContain('supplier-tin-format')
    } finally {
      await ensureEnabled(token, 'vat-standard-rate')
    }

    const restored = await validate(token, badInvoice)
    expect(keysOf(restored)).toContain('vat-standard-rate')
  })

  test('kill-switch: disabling currency-allowed drops it; reversible', async () => {
    const baseline = await validate(token, currencyUsdInvoice)
    expect(keysOf(baseline)).toContain('currency-allowed')

    try {
      await disableRule(token, 'currency-allowed')
      const disabled = await validate(token, currencyUsdInvoice)
      expect(keysOf(disabled)).not.toContain('currency-allowed')
    } finally {
      await ensureEnabled(token, 'currency-allowed')
    }

    const restored = await validate(token, currencyUsdInvoice)
    expect(keysOf(restored)).toContain('currency-allowed')
  })
})
