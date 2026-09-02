// The live per-rule kill-switch over the wire, through the SAME typed seam (api/client.ts)
// every api/ spec shares: create a draft, validate it via POST /v1/invoices/{id}/validate,
// toggle a rule, validate a second draft, restore the rule, validate a third.
//
// This file mutates the GLOBAL, shared `rules` table on the dev fleet (there is one `rules`
// row per key, not per-tenant -- Decision A3). Every other api/ spec (and every other
// engineer/CI run hitting the same dev fleet) depends on the seeded rules being enabled, so
// the D3 robustness protocol below is not optional polish: a crashed run that leaves
// `vat-standard-rate` or `currency-allowed` disabled would silently break unrelated specs
// until someone notices and manually re-enables the rule. Hence:
//   - beforeAll SELF-HEALS both target rules to enabled before any test runs, curing a
//     prior crashed run's leak. Unlike the afterAll/finally restore path, the self-heal
//     RE-THROWS on a genuinely unexpected failure (not 409, not network) so a broken
//     precondition aborts the file loudly here instead of surfacing as confusing mid-test
//     assertion failures.
//   - afterAll RESTORES both target rules to enabled unconditionally, and can never itself
//     throw (a throwing cleanup would both mask the real assertion failure that triggered
//     it AND still leave the rule disabled). Playwright still runs afterAll even when
//     beforeAll throws, so this backstop holds regardless of which hook failed.
//   - Both directions tolerate 409 ErrRedundantTransition (PATCH enabled:true on an
//     already-enabled rule, or enabled:false on an already-disabled one) as success, since
//     idempotent-looking retries are expected here.
import { test, expect } from '@playwright/test'
import { login, toggleRule, createEntity, createInvoice, validateInvoice, PERSONAS, ApiError, type Violation } from './client'
import { freshTin } from './fixtures'

// keysOf(): the sorted rule_key set of a validate response (Engine.Evaluate sorts its
// output -- Decision N16 -- but we sort again here so this assertion doesn't silently
// depend on that ordering guarantee holding).
function keysOf(violations: Violation[]): string[] {
  return violations.map((v) => v.rule_key).sort()
}

// A draft that fires BOTH target rules at once, so disabling either leaves the other as a
// live control: currency USD trips currency-allowed (enum {"values":["NGN"]}) and a VAT
// that is not 7.5% of subtotal trips vat-standard-rate. supplier-tin-format is NOT usable
// as a third rule through this path -- Store.Create re-derives supplier_tin from the
// invoice's own entity ([supplier-from-entity]), so a malformed TIN never reaches storage.
function killSwitchFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'USD',
    subtotal: '1000',
    vat: '70',
    total: '1070',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

// A FRESH draft per validate call, for two independent 409s: the gate refuses a non-draft
// invoice (ErrNotDraft), and invoice_number is unique per (tenant, entity) so re-using one
// trips invoices_tenant_entity_number_uq. Each test mints its own entity, so the sequence
// number below only has to be unique within that entity -- it cannot collide across runs.
async function validateFreshDraft(token: string, entityId: string, seq: number): Promise<string[]> {
  const draft = await createInvoice(token, { entity_id: entityId, ...killSwitchFields(`INV-RMV01-KS-${seq}`) })
  const result = await validateInvoice(token, draft.id)
  return keysOf(result.violations)
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
        if (throwOnUnexpected && !(retryErr instanceof ApiError && retryErr.kind === 'network')) {
          throw retryErr
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

// Serial within this file: the kill-switch tests explicitly mutate shared global state one
// at a time, and the closing restore check reads the state they leave behind.
// (playwright.api.config.ts already runs the whole suite with workers: 1 /
// fullyParallel: false across files — this is belt-and-braces for this file's ordering.)
test.describe.configure({ mode: 'serial' })

test.describe('live kill-switch (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
    // D3 self-heal: force BOTH target rules enabled before any test/toggle
    // runs, healing a leaked kill-switch. `rules` is EXCLUDED from the per-PR
    // reset (resetTables) -- seed.dev.sql re-enables every rule instead -- so
    // the leak this heals is one a crashed spec left EARLIER IN THIS RUN, after
    // the seed had already converged.
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

  test('kill-switch: disabling vat-standard-rate drops only it — currency-allowed (control) still fires; reversible', async () => {
    const entity = await createEntity(token, { name: `RMV-01 kill-switch vat ${Date.now()}`, tin: freshTin() })

    const baselineKeys = await validateFreshDraft(token, entity.id, 1)
    expect(baselineKeys).toContain('vat-standard-rate')
    expect(baselineKeys).toContain('currency-allowed')

    try {
      await disableRule(token, 'vat-standard-rate')
      const disabled = await validateFreshDraft(token, entity.id, 2)
      // Exact set, not just "still contains the control": proves ONLY the
      // toggled key changed, not that the engine went dark or dropped extras.
      expect(disabled).toEqual(baselineKeys.filter((k) => k !== 'vat-standard-rate'))
    } finally {
      await ensureEnabled(token, 'vat-standard-rate')
    }

    const restored = await validateFreshDraft(token, entity.id, 3)
    expect(restored).toContain('vat-standard-rate')
  })

  test('kill-switch: disabling currency-allowed drops it; vat-standard-rate (control) still fires; reversible', async () => {
    const entity = await createEntity(token, { name: `RMV-01 kill-switch currency ${Date.now()}`, tin: freshTin() })

    const baselineKeys = await validateFreshDraft(token, entity.id, 1)
    expect(baselineKeys).toContain('currency-allowed')
    expect(baselineKeys).toContain('vat-standard-rate')

    try {
      await disableRule(token, 'currency-allowed')
      const disabled = await validateFreshDraft(token, entity.id, 2)
      // Exact set: proves ONLY the toggled key changed.
      expect(disabled).toEqual(baselineKeys.filter((k) => k !== 'currency-allowed'))
    } finally {
      await ensureEnabled(token, 'currency-allowed')
    }

    const restored = await validateFreshDraft(token, entity.id, 3)
    expect(restored).toContain('currency-allowed')
  })

  test('kill-switch self-heal and restore survive the rewrite', async () => {
    // Runs last under serial mode: both rules must be enabled by now (beforeAll self-healed
    // them, each test's finally restored them). Re-enabling an already-enabled rule is a
    // 409 ErrRedundantTransition and performs no write, so this observes without mutating.
    for (const key of ['vat-standard-rate', 'currency-allowed']) {
      let status: number | null | undefined
      try {
        await toggleRule(token, key, true)
      } catch (err) {
        if (!(err instanceof ApiError)) throw err
        status = err.status
      }
      expect(status, `${key} should already be enabled (409 ErrRedundantTransition)`).toBe(409)
    }
  })
})
