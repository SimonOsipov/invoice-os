import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

import { AUDIT_EVENTS, auditEventView, type AuditDomain } from './auditVocabulary'

const REPO_ROOT = resolve(__dirname, '../../../..')

// The 39 identifiers this app claims to label, measured against the Go tree. Five families
// are built from a variable rather than a literal, so a grep for quoted strings undercounts:
// tenancy/store.go, portfolio/store.go, validation/store.go, document/document.go and
// submission/verdict_audit.go ("submission."+outcome).
//
// The three extraction events are the opposite case: cmd/submission/main.go spells each one
// literally, so the source scan below can demand a label for all three.
const EXPECTED: Record<AuditDomain, string[]> = {
  invoices: [
    'invoice.created',
    'invoice.updated',
    'invoice.validated',
    'invoice.transitioned',
    'invoice.kept_as_is',
    'invoice.unkept_as_is',
    'invoice.resolved_outside',
    'invoice.unresolved_outside',
  ],
  approvals: [
    'invoice.approval_armed',
    'invoice.approval_approved',
    'invoice.approval_rejected',
    'invoice.approval_cancelled',
  ],
  policies: [
    'approval_policy.created',
    'approval_policy.updated',
    'approval_policy.published',
    'approval_policy.deleted',
  ],
  roles: ['workflow_role.created', 'workflow_role.updated', 'workflow_role.deleted', 'workflow_role.staffed'],
  companies: [
    'portfolio.entity.created',
    'portfolio.entity.updated',
    'portfolio.entity.onboarded',
    'portfolio.entity.offboarded',
  ],
  documents: [
    'document.created',
    'document.read',
    'document.reused',
    'extraction.succeeded',
    'extraction.failed',
    'extraction.field_corrected',
  ],
  memberships: ['membership.suspended', 'membership.reactivated'],
  validation: ['validation.rule.enabled', 'validation.rule.disabled'],
  submissions: ['submission.accepted', 'submission.rejected', 'submission.failed'],
  reconciliation: ['reconciliation.drift_detected', 'reconciliation.auto_fixed'],
}

const ALL = Object.values(EXPECTED).flat()

describe('audit vocabulary', () => {
  it('auditVocabulary_hasAllThirtyNineTypes', () => {
    const shipped = Object.keys(AUDIT_EVENTS)
    // An empty collection satisfies every assertion inside a loop, so pin the size first.
    expect(shipped.length).toBeGreaterThan(0)
    expect(shipped.length).toBe(39)
    expect(ALL.length).toBe(39)
    expect(new Set(shipped)).toEqual(new Set(ALL))
  })

  it('auditVocabulary_domainsAreExactlyTen', () => {
    const domains = new Set(Object.values(AUDIT_EVENTS).map((e) => e.domain))
    expect(domains.size).toBe(10)
    for (const [domain, ids] of Object.entries(EXPECTED)) {
      for (const id of ids) {
        // Named before it is dereferenced: a missing id would otherwise crash the loop with a
        // TypeError rather than say which identifier is absent.
        expect(AUDIT_EVENTS[id], `${id} must carry a label`).toBeDefined()
        expect(AUDIT_EVENTS[id].domain).toBe(domain)
      }
    }
  })

  it('auditVocabulary_everyEmittedEventHasALabel', () => {
    // grep -o over the Go tree for audit.Record( call sites. A scan that stops matching
    // returns zero misses, which reads exactly like a complete vocabulary -- so this
    // asserts a control needle and a population floor before trusting the miss list.
    const out = execFileSync(
      'grep',
      ['-rn', '--include=*.go', 'audit.Record(', 'internal', 'cmd'],
      { cwd: REPO_ROOT, encoding: 'utf8' },
    )
    const callSites = out.split('\n').filter(Boolean)
    expect(callSites.length).toBeGreaterThanOrEqual(50)

    // Production only. A _test.go fixture spelling an event name would otherwise satisfy this
    // guard on behalf of a writer that does not exist: internal/platform/db's scan needles and
    // cmd/submission's adapter needles both spell real event names inside string constants.
    const prodLines = callSites.filter((line) => !/^[^:]*_test\.go:/.test(line))
    expect(prodLines.length, 'population floor: production audit.Record call sites').toBeGreaterThanOrEqual(35)
    const prod = prodLines.join('\n')

    // Control needle: submission.failed is built from a variable and only exists because
    // AUDIT-03 merged. If the scan cannot see this file at all, the whole result is void.
    const verdict = execFileSync('grep', ['-c', 'submission.', 'internal/submission/verdict_audit.go'], {
      cwd: REPO_ROOT,
      encoding: 'utf8',
    })
    expect(Number(verdict.trim())).toBeGreaterThan(0)
    expect(AUDIT_EVENTS['submission.failed']).toBeDefined()

    // Every literal identifier the tree emits must carry a label. The character class excludes
    // the newline: grep emits one line per call site, and a class that spans them lets a
    // multi-line call swallow its own event and match the NEXT site's instead -- which is what
    // it did to extraction.succeeded, silently, while reporting no misses.
    const literals = [...prod.matchAll(/audit\.Record\([^)\n]*?"([a-z_]+(?:\.[a-z_]+)+)"/g)].map((m) => m[1])
    const found = new Set(literals)
    expect(found.size, 'population floor: distinct literal events the matcher reads').toBeGreaterThanOrEqual(25)
    const missing = [...found].filter((id) => !(id in AUDIT_EVENTS))
    expect(missing).toEqual([])

    // The other direction: a label with no writer is drift too. cmd/** sits outside the
    // `frontend` CI path filter, so this is the assertion that makes the Go adapter and these
    // three labels one push rather than two.
    for (const id of ['extraction.succeeded', 'extraction.failed', 'extraction.field_corrected']) {
      expect(literals, `${id} must be emitted by a Go writer`).toContain(id)
    }
  })

  it('auditVocabulary_colourOnlyForOutcome', () => {
    for (const [id, entry] of Object.entries(AUDIT_EVENTS)) {
      if (entry.outcome === undefined) {
        expect(auditEventView(id).tone, `${id} must not carry a status tone`).toBeNull()
      }
    }
    // And at least one outcome-bearing event does take a tone, so the assertion above is
    // not vacuously satisfied by a function that returns null for everything.
    expect(auditEventView('submission.failed').tone).not.toBeNull()
  })

  it('auditVocabulary_unknownTypeIsNeverRawPrimary', () => {
    const view = auditEventView('something.invented_here')
    expect(view.label).not.toBe('something.invented_here')
    expect(view.label.length).toBeGreaterThan(0)
    expect(view.tone).toBeNull()
  })
})
