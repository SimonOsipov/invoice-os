// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.

import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, describe, expect, it } from 'vitest'

import type { AuditEvent } from '../lib/audit'

import { AuditRow } from './AuditRow'
import { AuditTable } from './AuditTable'

afterEach(cleanup)

function ev(over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: '11111111-1111-1111-1111-111111111111',
    created_at: '2026-08-20T09:15:00Z',
    event: 'submission.accepted',
    actor: 'system',
    actor_name: 'System',
    actor_kind: 'system',
    entity_id: 'a0000000-0000-0000-0000-000000000001',
    company_name: 'Honeywell Group',
    company_scope: 'company',
    payload: {},
    ...over,
  }
}

// The parent owns `expandedId` (ReviewInvoicesTab.tsx's idiom). Single-open is a property
// of that ownership, not of the row -- so the test drives it through a parent, which is
// also the shape AUDIT-09 will mount.
function TwoRows() {
  const [openId, setOpenId] = useState<string | null>(null)
  const a = ev({ id: 'row-a', payload: { a_key: 'A' } })
  const b = ev({ id: 'row-b', payload: { b_key: 'B' } })
  return (
    <>
      {[a, b].map((e) => (
        <AuditRow key={e.id} event={e} expanded={openId === e.id} onToggle={() => setOpenId(openId === e.id ? null : e.id)} />
      ))}
    </>
  )
}

describe('AuditRow', () => {
  it('auditRow_singleOpenAtATime', () => {
    render(<TwoRows />)
    const [rowA, rowB] = screen.getAllByTestId('audit-row')
    fireEvent.click(rowA)
    expect(screen.getByText('A')).toBeTruthy()
    fireEvent.click(rowB)
    // Opening B closed A: the parent holds one id, so no second panel can survive.
    expect(screen.getByText('B')).toBeTruthy()
    expect(screen.queryByText('A')).toBeNull()
    expect(screen.getAllByTestId('audit-expansion')).toHaveLength(1)
    expect(rowA.getAttribute('aria-expanded')).toBe('false')
    expect(rowB.getAttribute('aria-expanded')).toBe('true')
  })

  it('auditRow_expansionRendersOnlyPayloadKeys', () => {
    // The design mock draws six fields for an accepted transmission. The payload the Go
    // writer actually stores has five. Rendering the payload's OWN keys is the contract --
    // a fixed field list would print empty rows and read as missing data.
    const payload = { irn: 'NG-001', csid: 'CSID-9', status_code: '200', attempt: 2 }
    render(<AuditRow event={ev({ payload })} expanded onToggle={() => {}} />)
    const fields = screen.getAllByTestId('audit-payload-field')
    expect(fields).toHaveLength(4)
    expect(screen.getByText('NG-001')).toBeTruthy()
    expect(screen.queryByText(/duration/i)).toBeNull()
  })

  it('auditRow_invoiceAffordanceReadsBothKeys', () => {
    // The invoice key is inconsistent by design: `id` from internal/invoice/* and
    // approval/engine.go, `invoice_id` from approval/decision.go and
    // submission/verdict_audit.go. Reading only one silently drops half the log.
    const cases: Array<{ name: string; payload: Record<string, unknown>; want: string }> = [
      { name: 'id', payload: { id: 'inv-1' }, want: 'inv-1' },
      { name: 'invoice_id', payload: { invoice_id: 'inv-2' }, want: 'inv-2' },
    ]
    for (const c of cases) {
      const seen: string[] = []
      render(<AuditRow event={ev({ payload: c.payload })} expanded onToggle={() => {}} onFilterToInvoice={(id) => seen.push(id)} />)
      const link = screen.getByTestId('audit-invoice-affordance')
      fireEvent.click(link)
      expect(seen, `payload key ${c.name} must yield the affordance`).toEqual([c.want])
      cleanup()
    }
  })

  it('auditRow_isExtractable', () => {
    // AUDIT-09 mounts this row scoped to one invoice. An import from the Audit screen
    // would drag the whole screen -- and its fetch -- along with it.
    const src = readFileSync(join(__dirname, 'AuditRow.tsx'), 'utf8')
    const imports = src.match(/^import .*$/gm) ?? []
    // Control needle: prove the scan reads a real import list before trusting its silence.
    expect(imports.length, 'the import scan found nothing -- the regex, not the file, is wrong').toBeGreaterThanOrEqual(3)
    expect(imports.join('\n')).toContain('../lib/audit')
    expect(imports.join('\n')).not.toContain('AuditView')
    expect(src).not.toContain('PlatformCtx')
  })
})

describe('AuditRow evidence affordance', () => {
  it('auditRow_evidenceAffordanceIsInertNotFaked', () => {
    // AUDIT-08 owns the drawer. The story permits the button in a disabled state and
    // forbids faking the drawer, so the button carries a VISIBLE reason: a title= on a
    // disabled button never fires in Chromium.
    render(<AuditRow event={ev({ event: 'submission.accepted', payload: { id: 'inv-1', irn: 'NG-1' } })} expanded onToggle={() => {}} />)
    const btn = screen.getByTestId('audit-evidence-affordance')
    expect(btn).toHaveProperty('disabled', true)
    expect(screen.getByTestId('audit-evidence-blocked-reason').textContent).toBeTruthy()
  })

  it('auditRow_evidenceAffordanceOnlyWhereEvidenceExists', () => {
    // A policy edit has no transmission behind it; offering the link would claim a record
    // that does not exist.
    render(<AuditRow event={ev({ event: 'approval_policy.updated', payload: { id: 'pol-1' } })} expanded onToggle={() => {}} />)
    expect(screen.queryByTestId('audit-evidence-affordance')).toBeNull()
  })
})

describe('AuditRow degrades by scrolling, not by collapsing', () => {
  it('auditRow_takesNoClassThatCollapsesTheGrid', () => {
    // platform.css forces `grid-template-columns: minmax(0,1fr) !important` on
    // .pf-list-row and .pf-list-head at <=480px. On a min-width table that produces a
    // single 868px column, not a narrow table -- the opposite of the Core AC, which says
    // this table degrades by SCROLLING. MembersTable.tsx avoids both classes for exactly
    // this reason and takes .pf-row alone, for the hover highlight.
    render(<AuditRow event={ev()} expanded={false} onToggle={() => {}} />)
    const row = screen.getByTestId('audit-row')
    expect(row.className).toContain('pf-row')
    expect(row.className, 'pf-list-row collapses this grid at <=480px').not.toContain('pf-list-row')

    // Control needle: the scan can see a class it is looking for, so its silence above is
    // evidence rather than an empty string.
    expect(row.className.length).toBeGreaterThan(0)

    // The header half of the same rule. Rendered, not scanned: AuditTable.tsx NAMES
    // pf-list-head in the comment explaining why it does not use it, so a source scan
    // would fail on the explanation.
    cleanup()
    render(
      <AuditTable>
        <AuditRow event={ev()} expanded={false} onToggle={() => {}} />
      </AuditTable>,
    )
    expect(screen.getByTestId('audit-table-head').className).not.toContain('pf-list-head')
  })
})

// Raw style attribute, not .style.*: jsdom's CSSStyleDeclaration can drop var() shorthands.
function sv(el: Element, prop: string): string | null {
  const style = el.getAttribute('style') ?? ''
  const match = style.match(new RegExp(`(?:^|;\\s*)${prop}:\\s*([^;]+)`))
  return match ? match[1].trim() : null
}

type Variant = 'audit' | 'activity'
const VARIANTS: Variant[] = ['audit', 'activity']

function mount(variant: Variant | undefined, event: AuditEvent, expanded = false) {
  render(
    <AuditTable variant={variant}>
      <AuditRow event={event} expanded={expanded} onToggle={() => {}} variant={variant} />
    </AuditTable>,
  )
}

describe('AuditTable card (D-4)', () => {
  it('auditTable_cardLivesOnTheScrollerAndOnlyForTheAuditVariant', () => {
    mount(undefined, ev())
    const table = screen.getByTestId('audit-table')
    const scroller = table.parentElement as HTMLElement
    // Default variant is 'audit': the scroller wears the card, the inner table wears none.
    expect(sv(scroller, 'border')).toBe('1px solid var(--line-1)')
    expect(sv(scroller, 'border-radius')).toBe('var(--radius-md)')
    expect(sv(scroller, 'background')).toBe('var(--bg-2)')
    expect(sv(scroller, 'overflow-x')).toBe('auto')
    for (const prop of ['border', 'border-radius', 'background', 'overflow']) {
      expect(sv(table, prop), `audit-table must not carry ${prop}`).toBeNull()
    }
    cleanup()

    mount('activity', ev())
    const activityScroller = screen.getByTestId('audit-table').parentElement as HTMLElement
    expect(sv(activityScroller, 'overflow-x')).toBe('auto')
    for (const prop of ['border', 'border-radius', 'background']) {
      expect(sv(activityScroller, prop), `activity scroller must not carry ${prop}`).toBeNull()
    }
  })

  it('auditTable_headPaddingFollowsTheVariant', () => {
    const want: Record<Variant, string> = { audit: '10px 18px', activity: '9px 18px' }
    for (const v of VARIANTS) {
      mount(v, ev())
      expect(sv(screen.getByTestId('audit-table-head'), 'padding'), v).toBe(want[v])
      cleanup()
    }
    mount(undefined, ev())
    expect(sv(screen.getByTestId('audit-table-head'), 'padding')).toBe(want.audit)
  })
})

describe('AuditRow density per variant (D-4)', () => {
  it('auditRow_rowCellsFollowTheVariant', () => {
    const want = {
      audit: { pad: '11px 18px', whatSize: '13px', companySize: '12.5px', companyColor: 'var(--fg-1)', whenColor: 'var(--fg-2)' },
      activity: { pad: '10px 18px', whatSize: '12.5px', companySize: '12px', companyColor: 'var(--fg-2)', whenColor: 'var(--fg-3)' },
    }
    // An absent variant is the audit density.
    for (const v of [...VARIANTS, undefined]) {
      mount(v, ev())
      const w = want[v ?? 'audit']
      const row = screen.getByTestId('audit-row')
      expect(sv(row, 'padding'), `${v} row padding`).toBe(w.pad)
      expect(sv(screen.getByTestId('audit-what'), 'font-size'), `${v} what`).toBe(w.whatSize)
      expect(sv(screen.getByTestId('audit-what'), 'font-weight')).toBe('500')
      const company = screen.getByTestId('audit-company')
      expect(sv(company, 'font-size'), `${v} company size`).toBe(w.companySize)
      expect(sv(company, 'color'), `${v} company colour`).toBe(w.companyColor)
      const when = Array.from(row.querySelectorAll('.mono')).find((e) => e.getAttribute('style')?.includes('font-size: 11px')) as HTMLElement
      expect(when, 'the when cell').toBeTruthy()
      expect(sv(when, 'font-size'), `${v} when size`).toBe('11px')
      expect(sv(when, 'color'), `${v} when colour`).toBe(w.whenColor)
      // chevron rail is the last cell
      expect(sv(row.lastElementChild as HTMLElement, 'color'), `${v} chevron`).toBe('var(--fg-3)')
      // Audit turns the glyph inside the cell; activity turns the cell.
      const cell = row.lastElementChild as HTMLElement
      const inner = cell.firstElementChild as HTMLElement | null
      expect([sv(cell, 'transform'), inner ? sv(inner, 'transform') : null], `${v} chevron rotation`).toEqual(v === 'activity' ? ['rotate(-90deg)', null] : [null, 'rotate(-90deg)'])
      cleanup()
    }
  })

  it('auditRow_workspaceAndUnattributedCompanyStayFg3InBothVariants', () => {
    for (const v of VARIANTS) {
      for (const scope of ['workspace', 'unattributed'] as const) {
        mount(v, ev({ company_scope: scope, company_name: null }))
        expect(sv(screen.getByTestId('audit-company'), 'color'), `${v}/${scope}`).toBe('var(--fg-3)')
        cleanup()
      }
    }
  })

  it('auditRow_actorCellReceivesTheRowVariant', () => {
    mount('activity', ev({ actor_kind: 'person', actor_name: 'Chinedu Okafor', actor: 'c-1' }))
    const name = screen.getByTestId('audit-row').querySelector('[data-testid="actor-initials"]')!.parentElement!.nextElementSibling as HTMLElement
    expect(sv(name, 'font-size')).toBe('12.5px')
    expect(sv(name, 'font-weight')).toBe('600')
    cleanup()
    mount('audit', ev({ actor_kind: 'person', actor_name: 'Chinedu Okafor', actor: 'c-1' }))
    const auditName = screen.getByTestId('audit-row').querySelector('[data-testid="actor-initials"]')!.parentElement!.nextElementSibling as HTMLElement
    expect(sv(auditName, 'font-size')).toBe('13px')
    expect(sv(auditName, 'font-weight')).toBe('500')
  })
})

describe('AuditRow expansion per variant (D-4, D-14)', () => {
  const payload = { irn: 'NG-001', csid: 'CSID-9', status_code: '200', attempt: 2 }

  it('auditRow_expansionSurfaceFollowsTheVariant', () => {
    const want = {
      audit: { pad: '16px 18px 15px 53px', gap: '12px 28px' },
      activity: { pad: '14px 18px 14px 52px', gap: '11px 26px' },
    }
    for (const v of VARIANTS) {
      mount(v, ev({ payload }), true)
      const exp = screen.getByTestId('audit-expansion')
      expect(sv(exp, 'background'), v).toBe('var(--bg-1)')
      expect(sv(exp, 'border-top'), `${v} expansion borderTop`).toBe('1px solid var(--line-1)')
      expect(sv(exp, 'padding'), v).toBe(want[v].pad)
      const grid = screen.getAllByTestId('audit-payload-field')[0].parentElement as HTMLElement
      expect(sv(grid, 'grid-template-columns'), `${v} columns`).toBe('repeat(3,minmax(0,1fr))')
      expect(sv(grid, 'gap'), `${v} gap`).toBe(want[v].gap)
      const fields = screen.getAllByTestId('audit-payload-field')
      expect(fields.length).toBeGreaterThan(0)
      for (const f of fields) {
        const [key, value] = Array.from(f.children) as HTMLElement[]
        expect(key.className, 'keys wear the cascade .label').toBe('label')
        expect(sv(key, 'margin-bottom')).toBe('3px')
        expect(value.className).toBe('mono')
        expect(sv(value, 'font-size')).toBe('11.5px')
        expect(sv(value, 'color')).toBe('var(--fg-1)')
      }
      cleanup()
    }
  })

  it('auditRow_rowDropsItsOwnRuleWhileOpenSoTheDividerIsOnePixel', () => {
    // The expansion draws borderTop; a row rule beside it would stack to a 2px divider.
    mount('audit', ev({ payload }), false)
    expect(sv(screen.getByTestId('audit-row'), 'border-bottom')).toBe('1px solid var(--line-1)')
    cleanup()
    mount('audit', ev({ payload }), true)
    const rowRule = sv(screen.getByTestId('audit-row'), 'border-bottom')
    expect(rowRule === null || /^0(px)?$|^none/.test(rowRule), `open row rule was ${rowRule}`).toBe(true)
    expect(sv(screen.getByTestId('audit-expansion'), 'border-bottom')).toBe('1px solid var(--line-1)')
  })

  it('auditRow_footerIdsAreFg3UnderTheirOwnRuleEvenWithAnEmptyPayload', () => {
    for (const p of [payload, {}]) {
      mount('audit', ev({ payload: p }), true)
      const ident = screen.getByTestId('audit-event-identifier')
      const id = screen.getByTestId('audit-event-id')
      const footer = ident.parentElement as HTMLElement
      expect(footer.contains(id)).toBe(true)
      expect(sv(footer, 'border-top')).toBe('1px solid var(--line-1)')
      expect(sv(footer, 'padding-top')).toBe('11px')
      expect(sv(footer, 'margin-top')).toBe('14px')
      expect(sv(footer, 'gap')).toBe('14px')
      expect(sv(footer, 'font-size')).toBe('10px')
      expect(sv(footer, 'color')).toBe('var(--fg-3)')
      expect(footer.className).toBe('mono')
      cleanup()
    }
    mount('audit', ev({ payload: {} }), true)
    expect(screen.getByText('This event carries no detail.')).toBeTruthy()
    expect(screen.queryAllByTestId('audit-payload-field')).toHaveLength(0)
  })

  it('auditRow_disabledEvidenceButtonKeepsItsPaintAndTheD3Recipe', () => {
    mount('audit', ev({ event: 'submission.accepted', payload: { id: 'inv-1' } }), true)
    const btn = screen.getByTestId('audit-evidence-affordance')
    expect(btn).toHaveProperty('disabled', true)
    expect(sv(btn, 'opacity')).toBe('0.45')
    expect(sv(btn, 'cursor')).toBe('not-allowed')
    expect(sv(btn, 'filter')).toBe('none')
    expect(sv(btn, 'color')).not.toBe('var(--fg-4)')
    expect(sv(btn, 'background')).toBe('transparent')
    expect(btn.className).toBe('v2-btn v2-btn-ghost pf-btn')
    expect(sv(btn, 'height')).toBe('30px')
    expect(sv(btn, 'font-size')).toBe('12px')
    cleanup()
    mount('activity', ev({ event: 'submission.accepted', payload: { id: 'inv-1' } }), true)
    expect(sv(screen.getByTestId('audit-evidence-affordance'), 'font-size')).toBe('12.5px')
    const footer = screen.getByTestId('audit-event-identifier').parentElement as HTMLElement
    expect(sv(footer, 'margin-top')).toBe('12px')
    expect(sv(footer, 'padding-top')).toBe('10px')
    expect(sv(footer, 'gap')).toBe('14px')
  })

  it('auditRow_evidenceReasonSpacingFollowsTheVariant', () => {
    const e = ev({ event: 'submission.accepted', payload: { id: 'inv-1' } })
    mount('audit', e, true)
    const wrap = screen.getByTestId('audit-evidence-affordance').parentElement as HTMLElement
    expect(sv(wrap, 'display')).toBe('inline-flex')
    expect(sv(wrap, 'gap')).toBe('10px')
    expect(sv(screen.getByTestId('audit-evidence-blocked-reason'), 'margin-top')).toBe('0px')
    expect(sv(wrap.parentElement as HTMLElement, 'gap')).toBe('14px')
    expect(sv(wrap.parentElement as HTMLElement, 'margin-top')).toBe('14px')
    cleanup()
    mount('activity', e, true)
    expect(sv(screen.getByTestId('audit-evidence-blocked-reason'), 'margin-top')).toBe('5px')
    expect(sv(screen.getByTestId('audit-evidence-affordance').parentElement?.parentElement as HTMLElement, 'margin-top')).toBe('12px')
    expect(screen.getByTestId('audit-evidence-affordance').getAttribute('aria-describedby')).toBe(screen.getByTestId('audit-evidence-blocked-reason').id)
  })

  it('auditRow_linkAndEvidenceShareOneActionRowAndALinkOnlyRowStillRenders', () => {
    const open = () => {}
    // Submission row with an invoice: link and disabled evidence sit in the same flex row.
    render(<AuditRow event={ev({ payload: { id: 'inv-1' } })} expanded onToggle={() => {}} onFilterToInvoice={open} />)
    const row = screen.getByTestId('audit-invoice-affordance').parentElement as HTMLElement
    expect(row.contains(screen.getByTestId('audit-evidence-affordance'))).toBe(true)
    cleanup()
    // Invoices-domain row: no evidence button, the link still gets the row.
    render(<AuditRow event={ev({ event: 'invoice.updated', payload: { id: 'inv-1' } })} expanded onToggle={() => {}} onFilterToInvoice={open} />)
    expect(screen.queryByTestId('audit-evidence-affordance')).toBeNull()
    const linkRow = screen.getByTestId('audit-invoice-affordance').parentElement as HTMLElement
    expect(sv(linkRow, 'display')).toBe('flex')
    expect(sv(linkRow, 'margin-top')).toBe('14px')
  })

  it('auditRow_invoiceAffordanceIsActionSemibold', () => {
    render(<AuditRow event={ev({ payload: { id: 'inv-1' } })} expanded onToggle={() => {}} onFilterToInvoice={() => {}} />)
    const link = screen.getByTestId('audit-invoice-affordance')
    expect(sv(link, 'color')).toBe('var(--action)')
    expect(sv(link, 'font-weight')).toBe('600')
  })

describe('AuditRow button faces', () => {
  it('auditRow_invoiceLinkUsesTheAppFace', () => {
    render(<AuditRow event={ev({ payload: { invoice_id: 'i-1', invoice_number: 'INV-1' } })} expanded onToggle={() => {}} onFilterToInvoice={() => {}} />)
    expect(sv(screen.getByTestId('audit-invoice-affordance'), 'font-family')).toBe('var(--font-sans)')
  })
})
})
