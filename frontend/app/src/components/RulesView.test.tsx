// @vitest-environment jsdom
// A real (hand-off) session's Rules screen shows its own empty lines, not the demo rules and suggestions.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { GOLDEN_RULES, SEED_CUSTOM_RULES, SUGGESTED_RULES, type CustomRule } from '../lib/rules'
import type { PlatformCtx } from '../types'
import { RulesView } from './RulesView'

function rulesCtx(handoff: boolean): PlatformCtx {
  const ctx = {
    mode: 'firm',
    handoff,
    active: { short: 'Adaeze Ventures', initials: 'AV', tin: '', entityId: null },
    customRules: handoff ? [] : SEED_CUSTOM_RULES,
    openRuleKey: null,
    openRule: vi.fn(),
    closeRule: vi.fn(),
    addSuggestedRule: vi.fn(),
    toggleCustomRule: vi.fn(),
    removeCustomRule: vi.fn(),
  }
  return ctx as unknown as PlatformCtx
}

afterEach(cleanup)

describe('RulesView', () => {
  it('hand-off: Rules shows no custom rule and no suggestion', () => {
    render(<RulesView ctx={rulesCtx(true)} />)
    expect(screen.getByText('No suggestions to show.')).toBeTruthy()
    expect(screen.getByText('No custom rules yet — the golden ruleset alone is running.')).toBeTruthy()
    expect(SUGGESTED_RULES.length).toBeGreaterThan(0)
    SUGGESTED_RULES.forEach((s) => expect(screen.queryByText(s.key)).toBeNull())
    expect(screen.queryByText(/Derived from/)).toBeNull()
    expect(screen.queryAllByText('CUSTOM')).toHaveLength(0)
    expect(screen.queryAllByText('Add as custom rule')).toHaveLength(0)
  })

  // Control: green before and after; the total is arithmetic over ctx.customRules.
  it('hand-off: the total counts the golden rules only', () => {
    render(<RulesView ctx={rulesCtx(true)} />)
    expect(screen.getAllByText(`${GOLDEN_RULES.length} RULES`).length).toBeGreaterThan(0)
    expect(screen.getByText(/\+ 0 CUSTOM/)).toBeTruthy()
    SEED_CUSTOM_RULES.forEach((r) => expect(screen.queryByText(r.key)).toBeNull())
  })

  // Control: green before and after.
  it('persona: Rules keeps the seeded rules and suggestions', () => {
    render(<RulesView ctx={rulesCtx(false)} />)
    expect(screen.getAllByText('CUSTOM')).toHaveLength(5)
    expect(SEED_CUSTOM_RULES).toHaveLength(5)
    expect(screen.getByText('Derived from 9 rejections on your invoices')).toBeTruthy()
    expect(screen.getAllByText('Add as custom rule')).toHaveLength(3)
    expect(screen.getAllByText(`${GOLDEN_RULES.length + 5} RULES`).length).toBeGreaterThan(0)
    expect(screen.queryByText('No suggestions to show.')).toBeNull()
  })
})

describe('RulesView, adversarial', () => {
  const stored: CustomRule[] = [{ ...SEED_CUSTOM_RULES[0], key: 'mine.only' }]

  function ctxWith(handoff: boolean, customRules: CustomRule[]): PlatformCtx {
    return { ...(rulesCtx(handoff) as unknown as Record<string, unknown>), customRules } as unknown as PlatformCtx
  }

  it("hand-off: a stored rule renders, the empty row goes, and the total counts it", () => {
    render(<RulesView ctx={ctxWith(true, stored)} />)
    expect(screen.getByText('mine.only')).toBeTruthy()
    expect(screen.getAllByText('CUSTOM')).toHaveLength(1)
    expect(screen.queryByText('No custom rules yet — the golden ruleset alone is running.')).toBeNull()
    expect(screen.getAllByText(`${GOLDEN_RULES.length + 1} RULES`).length).toBeGreaterThan(0)
    expect(screen.getByText('No suggestions to show.')).toBeTruthy()
  })

  // Control: persona copy for the empty custom list and for exhausted suggestions is unchanged.
  it('persona: an empty custom list keeps the full empty line', () => {
    render(<RulesView ctx={ctxWith(false, [])} />)
    expect(screen.getByText('No custom rules yet — the golden ruleset alone is running. Add one from the suggestions on the left.')).toBeTruthy()
    expect(screen.queryByText('No custom rules yet — the golden ruleset alone is running.')).toBeNull()
    expect(screen.getAllByText('Add as custom rule')).toHaveLength(3)
  })

  it('persona: every suggestion adopted keeps the "Nothing to suggest" line', () => {
    const adopted = SUGGESTED_RULES.map((s) => ({ ...SEED_CUSTOM_RULES[0], key: s.key }))
    render(<RulesView ctx={ctxWith(false, adopted)} />)
    expect(screen.getByText(/^Nothing to suggest right now/)).toBeTruthy()
    expect(screen.queryByText('No suggestions to show.')).toBeNull()
    expect(screen.getAllByText('CUSTOM')).toHaveLength(SUGGESTED_RULES.length)
  })
})
