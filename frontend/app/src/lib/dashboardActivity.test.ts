import { describe, expect, it } from 'vitest'
import type { AuditEvent } from './audit'
import { activityQuery, activityRow, relativeAgo } from './dashboardActivity'
import { fmtDate, fmtDateTime } from './format'

const NOW = Date.parse('2026-10-20T12:00:00Z')
const ago = (sec: number) => new Date(NOW - sec * 1000).toISOString()

function ev(over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 'a1',
    created_at: ago(300),
    event: 'invoice.created',
    actor: 'sub-1',
    actor_name: 'Ada Obi',
    actor_kind: 'person',
    entity_id: 'e1',
    company_name: 'Acme Ltd',
    company_scope: 'company',
    payload: null,
    ...over,
  }
}

describe('activityQuery', () => {
  it('in-house query has the limit and no company', () => {
    const q = activityQuery(true, 'e1')
    expect(q).toEqual({ limit: 5 })
    expect('company' in q!).toBe(false)
  })
  it('firm query carries the active entity', () => {
    expect(activityQuery(false, 'e1')).toEqual({ limit: 5, company: 'e1' })
  })
  it('firm with no client yields no query', () => {
    expect(activityQuery(false, null)).toBeNull()
  })
})

describe('activityRow', () => {
  it('row reads actor, label, company, time', () => {
    const r = activityRow(ev(), NOW)
    expect(r.who).toBe('Ada Obi')
    expect(r.label).toBe('Invoice created')
    expect(r.company).toBe('Acme Ltd')
    expect(r.when).toBe('5m ago')
  })
  it('unknown event code still reads a label', () => {
    expect(activityRow(ev({ event: 'foo.bar_baz' }), NOW).label).toBe('Bar baz')
  })
  it('empty actor name falls back to the subject', () => {
    expect(activityRow(ev({ actor_name: '', actor: 'sys-x' }), NOW).who).toBe('sys-x')
  })
  it('a company-scope row with no name shows a dash', () => {
    expect(activityRow(ev({ company_scope: 'company', company_name: null }), NOW).company).toBe('—')
  })
  it('workspace-scope row omits the company', () => {
    expect(activityRow(ev({ company_scope: 'workspace', company_name: null }), NOW).company).toBeNull()
  })
  it('a workspace-scope row never shows a company, even when a name is present', () => {
    expect(activityRow(ev({ company_scope: 'workspace', company_name: 'Acme Ltd' }), NOW).company).toBeNull()
  })
  it('an empty company name shows a dash', () => {
    expect(activityRow(ev({ company_scope: 'company', company_name: '' }), NOW).company).toBe('—')
  })
  it('row carries the id as key and the full time as title', () => {
    const e = ev({ id: 'row-9' })
    const r = activityRow(e, NOW)
    expect(r.key).toBe('row-9')
    expect(r.whenTitle).toBe(fmtDateTime(e.created_at))
    expect(r.whenTitle).not.toBe('—')
  })
  it('dot follows tone', () => {
    const dot = (event: string) => activityRow(ev({ event }), NOW).dot
    expect(dot('reconciliation.auto_fixed')).toBe('var(--status-green-text)')
    expect(dot('submission.rejected')).toBe('var(--status-red-text)')
    expect(dot('membership.suspended')).toBe('var(--status-amber-text)')
    expect(dot('invoice.created')).toBe('var(--line-3)')
  })
})

describe('relativeAgo', () => {
  it('relative-time buckets and edges', () => {
    const cases: [number, string][] = [
      [0, 'just now'],
      [59, 'just now'],
      [60, '1m ago'],
      [59 * 60 + 59, '59m ago'],
      [3600, '1h ago'],
      [23 * 3600 + 59 * 60, '23h ago'],
      [86400, '1d ago'],
      [6 * 86400 + 23 * 3600, '6d ago'],
    ]
    for (const [sec, want] of cases) expect(relativeAgo(ago(sec), NOW), `${sec}s`).toBe(want)
  })
  it('at 7 days the absolute date shows', () => {
    const iso = ago(7 * 86400)
    expect(relativeAgo(iso, NOW)).toBe(fmtDate(iso))
  })
  it('a future time reads just now', () => {
    expect(relativeAgo(ago(-300), NOW)).toBe('just now')
  })
  it('a bad timestamp reads a dash', () => {
    expect(relativeAgo('x', NOW)).toBe('—')
  })
})

describe('activityRow account events', () => {
  it('dashboardActivity_accountEventsRenderLabelAndTone', () => {
    const want: Array<[string, string, string]> = [
      ['account.signed_in', 'Signed in', 'var(--line-3)'],
      ['account.sign_in_failed', 'Sign-in failed — wrong password', 'var(--status-red-text)'],
      ['account.sign_in_blocked', 'Sign-in blocked — too many attempts', 'var(--status-red-text)'],
      ['account.signed_out', 'Signed out', 'var(--line-3)'],
      ['account.password_reset_requested', 'Password reset requested', 'var(--line-3)'],
      ['account.password_reset_completed', 'Password reset completed', 'var(--line-3)'],
    ]
    expect(want.length).toBe(6)
    for (const [event, label, dot] of want) {
      const r = activityRow(ev({ event, company_scope: 'workspace', company_name: null }), NOW)
      expect(r.label, event).toBe(label)
      expect(r.dot, event).toBe(dot)
      expect(r.company, event).toBeNull()
    }
  })
})
