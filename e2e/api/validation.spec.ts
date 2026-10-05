// A customer cannot change a golden rule: tenant A's PATCH on a rule is refused
// 403, and neither tenant's evaluation moves off B's baseline.
import { test, expect } from '@playwright/test'
import { login, rawFetch, createEntity, createInvoice, validateInvoice, PERSONAS, type Violation } from './client'
import { freshTin } from './fixtures'

function keysOf(violations: Violation[]): string[] {
  return violations.map((v) => v.rule_key).sort()
}

// Fires both target rules: USD trips currency-allowed, a VAT off 7.5% of subtotal trips
// vat-standard-rate. supplier-tin-format is not usable here: Store.Create re-derives
// supplier_tin from the invoice's entity ([supplier-from-entity]).
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

// A fresh draft per validate: the gate refuses a non-draft invoice, and invoice_number is
// unique per (tenant, entity).
async function validateFreshDraft(token: string, entityId: string, seq: number): Promise<string[]> {
  const draft = await createInvoice(token, { entity_id: entityId, ...killSwitchFields(`INV-AUTH12-${seq}`) })
  const result = await validateInvoice(token, draft.id)
  return keysOf(result.violations)
}

// rulesManagedMessage in internal/validation/handlers.go.
const RULES_MANAGED_MESSAGE = 'rules are managed by ASComply'

test.describe.configure({ mode: 'serial' })

test.describe('a customer cannot change a golden rule (API E2E, over the deployed gateway)', () => {
  let tokenA: string
  let tokenB: string
  let entityA: string
  let entityB: string
  let baselineB: string[]
  const patches: Array<{ key: string; enabled: boolean; res: { status: number; body: unknown } }> = []

  test.beforeAll(async () => {
    tokenA = await login(PERSONAS.A)
    tokenB = await login(PERSONAS.B)
    // Tenant B's name must sort after "Honeywell Group" ([e2e-entity-name-ordering-trap]).
    entityA = (await createEntity(tokenA, { name: `Rule lock AUTH-12 A ${Date.now()}`, tin: freshTin() })).id
    entityB = (await createEntity(tokenB, { name: `Rule lock AUTH-12 B ${Date.now()}`, tin: freshTin() })).id
    baselineB = await validateFreshDraft(tokenB, entityB, 1)
    for (const key of ['vat-standard-rate', 'currency-allowed']) {
      for (const enabled of [false, true]) {
        const res = await rawFetch(`/api/validation/v1/rules/${key}`, {
          method: 'PATCH',
          headers: { Authorization: `Bearer ${tokenA}` },
          body: { enabled },
        })
        patches.push({ key, enabled, res })
      }
    }
  })

  test('B baseline fires both target rules', () => {
    expect(baselineB).toContain('vat-standard-rate')
    expect(baselineB).toContain('currency-allowed')
  })

  test('each of A PATCHes is refused 403 with the managed-rules body', () => {
    expect(patches).toHaveLength(4)
    for (const { key, enabled, res } of patches) {
      expect(res.status, `PATCH ${key} enabled=${enabled}`).toBe(403)
      expect(res.body, `PATCH ${key} enabled=${enabled}`).toEqual({ error: RULES_MANAGED_MESSAGE })
    }
  })

  test('B evaluation is unchanged after A tried to toggle', async () => {
    expect(await validateFreshDraft(tokenB, entityB, 2)).toEqual(baselineB)
  })

  test('A evaluation equals the same set', async () => {
    expect(await validateFreshDraft(tokenA, entityA, 1)).toEqual(baselineB)
  })
})
