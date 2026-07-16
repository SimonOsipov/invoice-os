import { describe, expect, it } from 'vitest'

import { CONNECTOR_DEFS, CONNECTOR_TAX_CODES } from '../data'
import { connectorDetail, mappingFor } from './connectors'

describe('connectorDetail', () => {
  it('is deterministic per connector', () => {
    CONNECTOR_DEFS.forEach((def) => {
      expect(connectorDetail(def)).toEqual(connectorDetail(def))
    })
  })

  it('gives every connector distinct figures', () => {
    const funnels = CONNECTOR_DEFS.map((def) => JSON.stringify(connectorDetail(def).funnel))
    expect(new Set(funnels).size).toBe(CONNECTOR_DEFS.length)
  })

  // The screen only reads as real if the numbers agree with each other — every
  // relationship the detail view's captions claim is asserted here.
  it('holds the funnel identities the detail view narrates', () => {
    CONNECTOR_DEFS.forEach((def) => {
      const d = connectorDetail(def)
      const { inErp, validated, transmitted, accepted, drift } = d.funnel

      // The funnel only ever narrows.
      expect(inErp).toBeGreaterThan(validated)
      expect(validated).toBeGreaterThan(transmitted)
      expect(transmitted).toBeGreaterThanOrEqual(accepted)

      // "IN ERP" is the period the volume chart draws.
      expect(inErp).toBe(d.volume.reduce((s, v) => s + v, 0))
      expect(d.volumeTotal).toBe(inErp)
      expect(d.volume).toHaveLength(30)

      // The captions: drift = not yet acknowledged, held = pulled but not transmitted.
      expect(drift).toBe(transmitted - accepted)
      expect(d.heldTotal).toBe(inErp - transmitted)
      expect(d.held.length).toBe(Math.min(4, d.heldTotal))

      // Write-back covers exactly the FIRS-accepted documents.
      expect(d.writeBack.stamped + d.writeBack.pending + d.writeBack.failed).toBe(accepted)
      expect(d.writeBack.stamped).toBeGreaterThan(0)
      expect(d.writeBack.pct).toBeGreaterThanOrEqual(0)
      expect(d.writeBack.pct).toBeLessThanOrEqual(100)

      // The stat tile counts the tax codes rendered beside it.
      expect(d.master.taxCodes).toBe(CONNECTOR_TAX_CODES.length)
    })
  })

  it('yields both a clean and a drifting connector, so the badge shows either state', () => {
    const drifts = CONNECTOR_DEFS.map((def) => connectorDetail(def).funnel.drift)
    expect(drifts.some((n) => n === 0)).toBe(true)
    expect(drifts.some((n) => n > 0)).toBe(true)
  })
})

describe('mappingFor', () => {
  it('falls back to the connector default when unedited', () => {
    const def = CONNECTOR_DEFS[0]
    expect(mappingFor(def, {})).toEqual(def.mapping)
  })

  it('prefers a saved override, per connector', () => {
    const [sap, oracle] = CONNECTOR_DEFS
    const edited = [{ erp: 'ZINV-CUSTOM', ubl: 'cbc:ID' }]
    expect(mappingFor(sap, { sap: edited })).toEqual(edited)
    expect(mappingFor(oracle, { sap: edited })).toEqual(oracle.mapping)
  })
})
