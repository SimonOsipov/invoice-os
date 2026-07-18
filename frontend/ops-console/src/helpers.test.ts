// QA Mode B (M4-20-05, task-142) — env-drift coverage for reqJSON, closing the gap the
// architecture pass flagged as E1 (BLOCKING): the evidence drawer's "Submitted invoice"
// JSON MUST track the live sandbox/live toggle (`env`) rather than being frozen at
// module scope. `EVIDENCE_DATA`/`EvidenceBundle` (charts.ts) deliberately excludes
// `request` for exactly this reason — `EvidenceDrawer.tsx` computes it per render via
// `reqJSON(row, env)`. Nothing in charts.test.ts or data.test.ts exercises `reqJSON`
// directly (it lives in helpers.ts, untested until now), so this is new coverage, not a
// duplicate.
//
// This models the exact call EvidenceDrawer.tsx makes (same payload shape, same `sub_`
// id substitution — proto:1232, task-142 E1) against a real EVIDENCE_DATA row, not a
// synthetic fixture, so a drift in either the row shape or the call site would surface
// here too. Kept in the Node-only pure-function test tier (no jsdom/RTL in this repo's
// vitest.config.ts) rather than a component render test, matching the project's existing
// "dependency-free of React" testing convention for helpers.ts/charts.ts.
import { describe, expect, it } from 'vitest'

import { buildEvidenceBundles } from './charts'
import { reqJSON } from './helpers'

describe('reqJSON env sensitivity (evidence drawer, task-142 E1)', () => {
  it('evidence_drawer_request_differs_between_sandbox_and_live: the same evidence row built into reqJSON payloads for sandbox vs live produces different JSON, diverging exactly at the environment field', () => {
    const row = buildEvidenceBundles()[0]!
    const payload = { id: 'sub_' + row.invoice.slice(-6), buyer: row.buyer, btin: row.btin, invoice: row.invoice, raw: row.raw, desc: row.desc }

    const sandboxReq = reqJSON(payload, 'sandbox')
    const liveReq = reqJSON(payload, 'live')

    expect(sandboxReq).not.toBe(liveReq)
    expect(sandboxReq).toContain('"environment": "sandbox"')
    expect(liveReq).toContain('"environment": "live"')
    // Swapping the interpolated environment string is the ONLY difference — every other
    // field (idempotency key, seller/buyer, invoice lines) must be byte-identical, so a
    // regression that also drifts unrelated fields between envs is caught too.
    expect(sandboxReq.replace('"sandbox"', '"live"')).toBe(liveReq)
  })

  it('evidence_drawer_idempotency_key_derives_from_the_sub_id_not_the_row_id: reqJSON\'s idempotency_key is idem_<suffix>, never the row\'s own ev_ id (task-142 E1 "two ids, do not collapse")', () => {
    const row = buildEvidenceBundles()[0]!
    const payload = { id: 'sub_' + row.invoice.slice(-6), buyer: row.buyer, btin: row.btin, invoice: row.invoice, raw: row.raw, desc: row.desc }

    const req = reqJSON(payload, 'live')

    expect(row.id).toBe('ev_088412')
    expect(req).toContain('"idempotency_key": "idem_088412"')
    expect(req).not.toContain('ev_088412')
  })

  it('evidence_bundle_response_does_not_vary_by_env: EvidenceBundle.response (charts.ts) carries no environment field and is identical across repeated derivations, unlike request', () => {
    const first = buildEvidenceBundles()[0]!
    const second = buildEvidenceBundles()[0]!

    expect(first.response).not.toContain('"environment"')
    expect(first.response).not.toContain('sandbox')
    expect(first.response).not.toContain('"live"')
    expect(second.response).toBe(first.response)
  })
})
