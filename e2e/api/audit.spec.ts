// AUDIT-04-08 (AC #4): the audit reader over the deployed gateway — the one claim no Go test
// in this repo can make.
//
// TWO things only this file observes:
//   1. THE ROUTE ITSELF. cmd/invoice/main.go registers `GET /v1/audit-log` on app.Mux, and
//      nothing in the Go tree reads that pattern — the handler tests call audit.ListHandler
//      directly against a synthetic httptest request. Changing GET to POST, or misspelling the
//      path, leaves go build, go vet and the whole internal/audit suite green. Here it fails:
//      measured, ServeMux answers a wrong METHOD on a matching path with 405 and an
//      unregistered PATH with 404, and apiFetch throws ApiError on either.
//   2. JWT -> tenant -> RLS end to end. The Go RLS tests set app.current_tenant themselves;
//      here the tenant comes from a real token through the real gateway.
//
// NO COUNT IS ASSERTED ANYWHERE. audit_log is append-only to the app role and is truncated
// only by db.Reset at deploy time, never between the specs of one run (smoke, api and topology
// all run against one deployment — docs/e2e-convention.md:66-85). Every assertion below is
// containment of a row this spec itself caused, reached by ?invoice_id=, so concurrent writes
// from other specs cannot move it either way.
import { test, expect } from '@playwright/test'
import { createEntity, createInvoice, getAuditLog, login, PERSONAS } from './client'
import { freshTin } from './fixtures'
import type { Persona } from './client'

// causeAnInvoiceCreatedRow: internal/invoice/store.go:269 records `invoice.created` with
// payload {"id": <invoice id>, "invoice_number": …}, and that event is on the list
// audit_log.invoice_id's generated column dispatches on — so the row it writes is
// addressable by ?invoice_id= rather than only findable by scanning the page.
//
// The name must sort AFTER both tenants' own names. GET /v1/entities is `ORDER BY name ASC`
// (internal/portfolio/store.go:115) and the SPA's resolveActiveClient falls back to
// clients[0], so an entity this suite leaves behind that sorts first silently becomes the
// DEFAULT ACTIVE ENTITY for the topology suite that runs after it — and
// topology/persona-surfaces.spec.ts asserts the Workflows subtitle names the tenant.
// "Audit E2E …" sorted before "Honeywell Group" and broke exactly that. The two other
// specs that seed tenant B ("M3-14-02 isolation B", "M4-07-05 tenant-B") clear it only by
// luck of the alphabet.
async function causeAnInvoiceCreatedRow(persona: Persona): Promise<{ token: string; invoiceId: string }> {
  const token = await login(persona)
  const entity = await createEntity(token, { name: `Zz AUDIT-04 ${freshTin()}`, tin: freshTin() })
  const invoice = await createInvoice(token, {
    entity_id: entity.id,
    invoice_number: `INV-AUDIT-${freshTin()}`,
  })
  return { token, invoiceId: invoice.id }
}

test.describe('audit reader (API E2E, over the deployed gateway)', () => {
  test("returns the caller's own audit rows through the gateway", async () => {
    const { token, invoiceId } = await causeAnInvoiceCreatedRow(PERSONAS.A)

    const scoped = await getAuditLog(token, { invoice_id: invoiceId })

    const events = scoped.events.map((e) => e.event)
    expect(events, `no audit row came back for the invoice this spec just created`).toContain(
      'invoice.created',
    )

    // The envelope, asserted over the wire rather than against a hand-built Response: the Go
    // handler test injects its body through a spy store, so this is the first place the real
    // store's shape is seen.
    expect(scoped.page.limit).toBeGreaterThan(0)
    expect(typeof scoped.total).toBe('number')
    expect(scoped.log_is_empty).toBe(false)
    expect(scoped.facets.event.length).toBeGreaterThan(0)

    // company_scope is present and classified on every row, never absent or empty.
    for (const row of scoped.events) {
      expect(['company', 'workspace', 'unattributed'], `row ${row.id} (${row.event})`).toContain(
        row.company_scope,
      )
    }
  })

  test("a second tenant cannot see the first tenant's rows", async () => {
    const a = await causeAnInvoiceCreatedRow(PERSONAS.A)
    const b = await causeAnInvoiceCreatedRow(PERSONAS.B)

    // Each tenant reads its OWN caused row first. Without these two, the isolation assertions
    // below could both pass on an endpoint that returns nothing to anyone.
    const ownA = await getAuditLog(a.token, { invoice_id: a.invoiceId })
    expect(ownA.events.map((e) => e.event), "tenant A cannot see its own row").toContain(
      'invoice.created',
    )
    const ownB = await getAuditLog(b.token, { invoice_id: b.invoiceId })
    expect(ownB.events.map((e) => e.event), "tenant B cannot see its own row").toContain(
      'invoice.created',
    )

    // Neither can reach the other's. RLS filters a list rather than hiding a named row, so the
    // correct answer is a 200 with nothing in it — never a 404, and never an error.
    const aLooksAtB = await getAuditLog(a.token, { invoice_id: b.invoiceId })
    expect(aLooksAtB.events, "tenant A can see tenant B's audit rows").toEqual([])

    const bLooksAtA = await getAuditLog(b.token, { invoice_id: a.invoiceId })
    expect(bLooksAtA.events, "tenant B can see tenant A's audit rows").toEqual([])
  })
})
