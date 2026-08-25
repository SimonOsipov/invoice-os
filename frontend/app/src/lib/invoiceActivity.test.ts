// RED specs (AUDIT-09-03, T-1..T-16) -- pin the activity feed's chip derivation, slicing and
// overflow copy before the executor fills in invoiceActivity.ts's stub bodies. Every spec that
// calls activityChips/activityRows/activityToggleCopy fails on their `not implemented` throw,
// which IS the correct RED reason. T-9 and T-16 read no stub: they are standing guards over the
// vocabulary and the page-size cap, and they pass today.
//
// `9`, `100`, `412`, `5`, `6` are ungrouped, so toLocaleString('en-NG') cannot move them
// (format.test.ts:9). A four-digit total would have to have its expectation computed.

/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import type { AuditEvent } from './audit'
import { auditEventView } from './auditVocabulary'
import { AUDIT_PAGE_SIZES } from './auditView'
import {
  ACTIVITY_CHIP_LABELS,
  ACTIVITY_CHIP_ORDER,
  ACTIVITY_COPY,
  ACTIVITY_FETCH_LIMIT,
  ACTIVITY_REST_ROWS,
  activityChips,
  activityRows,
  activityToggleCopy,
  type ActivityChip,
  type ActivityChipKey,
} from './invoiceActivity'

function ev(partial: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 'ev-1',
    created_at: '2026-08-25T09:00:00Z',
    event: 'invoice.created',
    actor: '00000000-0000-0000-0000-000000000001',
    actor_name: 'Ada Okafor',
    actor_kind: 'person',
    entity_id: null,
    company_name: null,
    company_scope: 'company',
    payload: {},
    ...partial,
  }
}

// Positional ids, so a slice assertion can name the exact rows it expects.
function evs(names: string[]): AuditEvent[] {
  return names.map((event, i) => ev({ id: `ev-${i + 1}`, event }))
}

function chip(chips: ActivityChip[], key: ActivityChipKey): ActivityChip {
  const found = chips.find((c) => c.key === key)
  if (found == null) throw new Error(`no ${key} chip in [${chips.map((c) => c.key).join(', ')}]`)
  return found
}

function domainCountSum(chips: ActivityChip[]): number {
  return chips.filter((c) => c.key !== 'all').reduce((n, c) => n + c.count, 0)
}

// Copy of internal/audit/audit_trigger_test.go:29-48 -- the generated invoice_id column's two
// dispatch branches. A FIXTURE, not production logic: activityChips never enumerates the 17, so
// drift here weakens T-9 and cannot ship a bug.
const RULE_A = [
  'invoice.created',
  'invoice.updated',
  'invoice.transitioned',
  'invoice.validated',
  'invoice.kept_as_is',
  'invoice.unkept_as_is',
  'invoice.resolved_outside',
  'invoice.unresolved_outside',
  'invoice.approval_armed',
  'invoice.approval_cancelled',
]
const RULE_B = [
  'invoice.approval_approved',
  'invoice.approval_rejected',
  'submission.accepted',
  'submission.rejected',
  'submission.failed',
  'reconciliation.drift_detected',
  'reconciliation.auto_fixed',
]
const REACHABLE_EVENTS = [...RULE_A, ...RULE_B]

const CHIP_KEYS: ActivityChipKey[] = ['all', 'invoices', 'approvals', 'documents', 'submissions', 'reconciliation']
const DOMAIN_CHIP_KEYS = CHIP_KEYS.filter((k) => k !== 'all')

describe('activityChips', () => {
  it('invoiceActivity_sixChipsFixedOrder', () => {
    // Asserted against a literal, not ACTIVITY_CHIP_ORDER: an oracle the implementation reads
    // cannot see a wrong order. These two assertions pass today.
    expect(ACTIVITY_CHIP_ORDER).toEqual(CHIP_KEYS)
    expect(ACTIVITY_CHIP_LABELS).toEqual({
      all: 'Everything',
      invoices: 'Invoice & status',
      approvals: 'Approvals',
      documents: 'Documents',
      submissions: 'Transmission',
      reconciliation: 'Reconciliation',
    })

    const cases: AuditEvent[][] = [[], evs(['invoice.created']), evs(REACHABLE_EVENTS)]
    for (const events of cases) {
      const chips = activityChips(events)
      expect(chips.map((c) => c.key), `${events.length} events`).toEqual(CHIP_KEYS)
      expect(chips.map((c) => c.label)).toEqual(CHIP_KEYS.map((k) => ACTIVITY_CHIP_LABELS[k]))
    }

    // AC-2: `all` is inert only when there is nothing at all.
    expect(chip(activityChips([]), 'all')).toMatchObject({ count: 0, inert: true })
    expect(chip(activityChips(evs(['invoice.created'])), 'all')).toMatchObject({ count: 1, inert: false })
  })

  it('invoiceActivity_countsSumToTotal', () => {
    // 17 reachable types PLUS 5 repeats. One-of-each passes on an implementation that dedupes
    // by event type (a Set, a Map<event, ...>), which is why the repeats are here.
    expect(RULE_A).toHaveLength(10)
    expect(RULE_B).toHaveLength(7)
    const repeats = Array.from({ length: 5 }, () => 'invoice.updated')
    const events = evs([...REACHABLE_EVENTS, ...repeats])
    expect(events).toHaveLength(22)

    const chips = activityChips(events)
    const counts = Object.fromEntries(chips.map((c) => [c.key, c.count] as const))
    expect(counts).toEqual({ all: 22, invoices: 13, approvals: 4, documents: 0, submissions: 3, reconciliation: 2 })
    expect(domainCountSum(chips)).toBe(22)
  })

  it('invoiceActivity_documentsChipIsAlwaysInert', () => {
    const chips = activityChips(evs(REACHABLE_EVENTS))
    const docs = chip(chips, 'documents')
    expect(docs.count).toBe(0)
    expect(docs.inert).toBe(true)
    expect(docs.reason).toBe(ACTIVITY_COPY.documentsInert)
    expect(ACTIVITY_COPY.documentsInert.length).toBeGreaterThan(0)

    // Control: a chip whose zero would be incidental carries no reason.
    const inv = chip(chips, 'invoices')
    expect(inv.count).toBeGreaterThan(0)
    expect(inv.inert).toBe(false)
    expect(inv.reason).toBeNull()
  })

  it('invoiceActivity_reconciliationChipCountsItsTwoTypes', () => {
    const events = evs(['reconciliation.drift_detected', 'reconciliation.auto_fixed'])
    const rec = chip(activityChips(events), 'reconciliation')
    expect(rec.count).toBe(2)
    expect(rec.inert).toBe(false)
    // Not folded into `all` only -- the pair is reachable through its own chip.
    const rows = activityRows(events, 'reconciliation', true)
    expect(rows.map((r) => r.event)).toEqual(['reconciliation.drift_detected', 'reconciliation.auto_fixed'])
  })

  it('invoiceActivity_everyReachableDomainHasAChip', () => {
    // The standing guard the Reconciliation chip was added to satisfy: iterates all 17, never
    // a sample. Reads no stub, so it passes today.
    expect(REACHABLE_EVENTS).toHaveLength(17)
    expect(DOMAIN_CHIP_KEYS).toEqual(['invoices', 'approvals', 'documents', 'submissions', 'reconciliation'])
    expect(ACTIVITY_CHIP_ORDER.filter((k) => k !== 'all')).toEqual(DOMAIN_CHIP_KEYS)

    for (const name of REACHABLE_EVENTS) {
      const { domain } = auditEventView(name)
      expect(domain, `${name} has no domain in AUDIT_EVENTS`).not.toBeNull()
      expect(DOMAIN_CHIP_KEYS as string[], `${name} maps to '${domain}', which has no chip`).toContain(domain)
    }
  })

  it('invoiceActivity_unmappedEventCountsUnderEverythingOnly', () => {
    const UNMAPPED = 'not.a.real.event'
    // Verified, not assumed: the fixture is only unmapped while the vocabulary lacks it.
    expect(auditEventView(UNMAPPED).domain).toBeNull()

    const events = evs([UNMAPPED, 'invoice.created', 'invoice.created'])
    const chips = activityChips(events)
    expect(chip(chips, 'all').count).toBe(3)
    expect(chip(chips, 'invoices').count).toBe(2)
    // Never silently reattributed to a domain.
    expect(domainCountSum(chips)).toBe(2)
  })

  it('invoiceActivity_chipCountsIgnoreTheRestSlice', () => {
    const events = evs(REACHABLE_EVENTS.slice(0, 12))
    const before = activityChips(events).map((c) => ({ key: c.key, count: c.count }))
    expect(before).toHaveLength(6)
    expect(chip(activityChips(events), 'all').count).toBe(12)

    expect(activityRows(events, 'all', false)).toHaveLength(ACTIVITY_REST_ROWS)
    expect(activityRows(events, 'all', true)).toHaveLength(12)

    const after = activityChips(events).map((c) => ({ key: c.key, count: c.count }))
    expect(after).toEqual(before)
  })
})

describe('activityRows', () => {
  it('invoiceActivity_restIsFiveRows', () => {
    const events = evs(REACHABLE_EVENTS.slice(0, 12))
    expect(events).toHaveLength(12)

    const rest = activityRows(events, 'all', false)
    expect(rest.map((r) => r.id)).toEqual(events.slice(0, ACTIVITY_REST_ROWS).map((r) => r.id))
    expect(activityRows(events, 'all', true).map((r) => r.id)).toEqual(events.map((r) => r.id))
  })

  it('invoiceActivity_restBoundaryAtFive', () => {
    expect(ACTIVITY_REST_ROWS).toBe(5)

    const five = evs(REACHABLE_EVENTS.slice(0, 5))
    expect(activityRows(five, 'all', false).map((r) => r.id)).toEqual(five.map((r) => r.id))
    // A "Show all 5 events" button over a 5-row list is a no-op control.
    expect(activityToggleCopy({ shown: 5, total: 5, fetched: 5, showAll: false }).label).toBeNull()

    const six = evs(REACHABLE_EVENTS.slice(0, 6))
    expect(activityRows(six, 'all', false)).toHaveLength(ACTIVITY_REST_ROWS)
    expect(activityRows(six, 'all', true).map((r) => r.id)).toEqual(six.map((r) => r.id))
    expect(activityToggleCopy({ shown: 6, total: 6, fetched: 6, showAll: false }).label).toBe('Show all 6 events')
  })

  it('invoiceActivity_documentsChipRendersNoRows', () => {
    const events = evs(REACHABLE_EVENTS)
    // Control: the fixture is not empty, so [] below is the filter's answer, not the input's.
    expect(activityRows(events, 'all', true)).toHaveLength(17)
    expect(activityRows(events, 'documents', true)).toEqual([])
    expect(activityRows(events, 'documents', false)).toEqual([])
  })

  it('invoiceActivity_chipFilterPreservesServerOrder', () => {
    // reader.go:288-293 already ordered these; activityRows filters and slices and never sorts.
    // created_at is deliberately scrambled against array order, so a re-sort in either direction
    // reorders the approvals subset and this test sees it.
    const events: AuditEvent[] = [
      ev({ id: 'ev-1', event: 'invoice.created', created_at: '2026-08-20T10:00:00Z' }),
      ev({ id: 'ev-2', event: 'invoice.approval_armed', created_at: '2026-08-23T10:00:00Z' }),
      ev({ id: 'ev-3', event: 'submission.accepted', created_at: '2026-08-21T10:00:00Z' }),
      ev({ id: 'ev-4', event: 'invoice.approval_approved', created_at: '2026-08-25T10:00:00Z' }),
      ev({ id: 'ev-5', event: 'invoice.updated', created_at: '2026-08-22T10:00:00Z' }),
      ev({ id: 'ev-6', event: 'invoice.approval_cancelled', created_at: '2026-08-24T10:00:00Z' }),
    ]
    // Guard the scramble itself: descending would be [ev-4, ev-6, ev-2].
    expect([...events].sort((a, b) => b.created_at.localeCompare(a.created_at)).map((r) => r.id)).not.toEqual(
      events.map((r) => r.id),
    )
    const rows = activityRows(events, 'approvals', true)
    expect(rows.length).toBeGreaterThan(0)
    expect(rows.map((r) => r.id)).toEqual(['ev-2', 'ev-4', 'ev-6'])
    expect(rows.map((r) => r.event)).toEqual([
      'invoice.approval_armed',
      'invoice.approval_approved',
      'invoice.approval_cancelled',
    ])
  })

  it('invoiceActivity_doesNotMutateItsInput', () => {
    const events = evs(REACHABLE_EVENTS.slice(0, 8))
    const ids = events.map((e) => e.id)

    activityChips(events)
    const all = activityRows(events, 'all', true)
    activityRows(events, 'approvals', false)

    expect(events.map((e) => e.id)).toEqual(ids)
    // `return events` for the all/expanded case hands the caller the array it must not touch.
    expect(all).not.toBe(events)
    expect(all.map((r) => r.id)).toEqual(ids)
  })
})

describe('activityToggleCopy', () => {
  it('invoiceActivity_toggleReadsServerTotal', () => {
    expect(activityToggleCopy({ shown: 9, total: 9, fetched: 9, showAll: false })).toEqual({
      label: 'Show all 9 events',
      note: null,
    })
  })

  it('invoiceActivity_toggleNeverOverclaims', () => {
    // The note is the only place D-AC-9's two numbers may both appear. The label may name only
    // what is on screen.
    expect(ACTIVITY_COPY.auditLink).toBe('Open in Audit →')
    const wantNote = `The 100 most recent of 412 events are loaded. Chip counts and rows cover those 100 — use ${ACTIVITY_COPY.auditLink} for the whole log.`

    const expanded = activityToggleCopy({ shown: 100, total: 412, fetched: 100, showAll: true })
    expect(expanded.label).toBe('Show fewer')
    expect(expanded.note).toBe(wantNote)
    expect(expanded.note).toContain('100')
    expect(expanded.note).toContain('412')
    expect(expanded.note).toContain(ACTIVITY_COPY.auditLink)

    const collapsed = activityToggleCopy({ shown: 100, total: 412, fetched: 100, showAll: false })
    expect(collapsed.label).toBe('Show all 100 events')
    expect(collapsed.label).not.toContain('412')
    expect(collapsed.note).toBe(wantNote)
  })

  it('invoiceActivity_fetchLimitIsTheServersMax', () => {
    // A vitest cannot read internal/audit/handlers.go:39. AUDIT_PAGE_SIZES is the shipped
    // mirror of the same cap. Reads no stub, so it passes today.
    expect(AUDIT_PAGE_SIZES.length).toBeGreaterThan(0)
    expect(ACTIVITY_FETCH_LIMIT).toBe(Math.max(...AUDIT_PAGE_SIZES))
    expect(ACTIVITY_FETCH_LIMIT).toBe(100)
  })

  it('invoiceActivity_isExtractable', () => {
    // AUDIT-09 mounts this module under the invoice detail page. An import from the Audit
    // screen would drag the whole screen -- and its fetch -- along with it.
    const src = readFileSync(fileURLToPath(new URL('./invoiceActivity.ts', import.meta.url)), 'utf8')
    const imports = src.match(/^import .*$/gm) ?? []
    // Control needle: prove the scan reads a real import list before trusting its silence.
    expect(imports.length, 'the import scan found nothing -- the regex, not the file, is wrong').toBeGreaterThanOrEqual(2)
    const joined = imports.join('\n')
    expect(joined).toContain('./auditVocabulary')
    expect(joined).toContain('./audit')
    expect(joined).not.toContain('react')
    expect(joined).not.toContain('AuditView')
    expect(joined).not.toContain('authedFetch')
    expect(src).not.toContain('PlatformCtx')
  })
})
