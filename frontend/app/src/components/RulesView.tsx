// Rules — the tenant-facing view of the validation engine.
//
// Mirrors the internal Support Console's "Rules admin" (support-console/src/components/
// Rules.tsx) with one structural difference that drives every decision below: the
// golden ruleset is PUBLISHED BY ASComply and read-only here, and each client stacks
// its own custom rules on top of it. So the versions rail states what is inherited
// rather than offering a draft to publish, the learned-rules inbox becomes tenant-
// scoped "Suggested for you", and the On column is a lock — not a toggle — on every
// inherited row.

import type { ReactNode } from 'react'

import { GOLDEN_RULES, GOLDEN_SET, GOLDEN_VERSIONS, openSuggestions, SUGGESTED_RULES, type CustomRule, type Rule } from '../lib/rules'
import { lockGlyph, sparkGlyph } from '../glyphs'
import { RuleDrawer } from './RuleDrawer'
import { SeverityPill, TypePill } from './RulePills'
import type { PlatformCtx } from '../types'

// Key · Type · Target field · Severity · Source · Message · On
const RULE_COLS = 'minmax(160px,1.1fr) 132px minmax(130px,1fr) 76px 82px minmax(170px,1.4fr) 74px'
const TABLE_MIN_WIDTH = 940

const VERSION_TONE = {
  active: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)' },
  superseded: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' },
} as const

// One labelled band inside the single table — the groups are headers, not two
// tables. No border-top: whatever precedes a group header (the column row, or the
// last rule of the previous group) already draws a bottom hairline, and stacking
// both would read as a 2px rule nowhere else in the app has.
function GroupHeader({ label, accent }: { label: string; accent?: boolean }) {
  return (
    <div
      className="label"
      style={{
        padding: '9px 16px',
        background: accent ? 'var(--action-tint)' : 'var(--bg-1)',
        borderBottom: '1px solid var(--line-1)',
        color: accent ? 'var(--action)' : 'var(--fg-3)',
        minWidth: TABLE_MIN_WIDTH,
      }}
    >
      {label}
    </div>
  )
}

/** The shared row body. `on` is whatever belongs in the On column — lock or toggle. */
function RuleRow({ rule, sourceLabel, sourceAccent, on, onOpen }: {
  rule: Rule
  sourceLabel: string
  sourceAccent: boolean
  on: ReactNode
  onOpen: () => void
}) {
  return (
    <div
      className="pf-row"
      onClick={onOpen}
      style={{ display: 'grid', gridTemplateColumns: RULE_COLS, padding: '12px 16px', borderBottom: '1px solid var(--line-1)', alignItems: 'center', minWidth: TABLE_MIN_WIDTH }}
    >
      <span className="mono" style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg-1)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>
        {rule.key}
      </span>
      <TypePill type={rule.type} />
      <span className="mono" style={{ fontSize: 11.5, color: 'var(--fg-2)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>
        {rule.field}
      </span>
      <span>
        <SeverityPill severity={rule.severity} />
      </span>
      <span className="mono" style={{ fontSize: 10.5, fontWeight: 600, color: sourceAccent ? 'var(--action)' : 'var(--fg-3)' }}>
        {sourceLabel}
      </span>
      <span style={{ fontSize: 12, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 12 }}>{rule.message}</span>
      <span style={{ justifySelf: 'end' }}>{on}</span>
    </div>
  )
}

export function RulesView({ ctx }: { ctx: PlatformCtx }) {
  const { active, mode, customRules, openRuleKey } = ctx
  const isFirm = mode === 'firm'
  const scope = active.short
  const total = GOLDEN_RULES.length + customRules.length
  const suggestions = openSuggestions(SUGGESTED_RULES, customRules)

  const subtitle = isFirm
    ? `ASComply's golden ruleset plus the custom checks you run for ${scope}`
    : `ASComply's golden ruleset plus the custom checks ${scope} runs internally`

  // A key can name a golden rule OR a custom one, never both — rules.test.ts pins
  // that the two sets are key-disjoint, so this lookup order carries no ambiguity.
  const openGolden = openRuleKey ? GOLDEN_RULES.find((r) => r.key === openRuleKey) : undefined
  const openCustom: CustomRule | undefined = openRuleKey ? customRules.find((r) => r.key === openRuleKey) : undefined

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 24, flexWrap: 'wrap', marginBottom: 22 }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 10 }}>
            VALIDATION ENGINE
          </div>
          <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Rules</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{subtitle}</p>
        </div>
        <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
          {total} RULES
        </span>
      </div>

      <div className="pf-rules-grid" style={{ display: 'grid', gridTemplateColumns: '244px minmax(0,1fr)', gap: 18, alignItems: 'start' }}>
        {/* ---- rail: the inherited ruleset, then what this client could add ---- */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', overflow: 'hidden' }}>
            <div className="label" style={{ padding: '12px 14px 8px' }}>
              Golden ruleset · {GOLDEN_SET.id}
            </div>
            {GOLDEN_VERSIONS.map((v) => {
              const tone = VERSION_TONE[v.kind]
              return (
                <div
                  key={v.version}
                  style={{ padding: '11px 14px', borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 10, background: v.kind === 'active' ? 'var(--action-tint)' : 'var(--bg-2)' }}
                >
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span className="mono" style={{ display: 'block', fontSize: 13, fontWeight: 700, color: 'var(--fg-1)' }}>
                      {v.version}
                    </span>
                    <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)', marginTop: 1 }}>
                      {v.meta}
                    </span>
                  </span>
                  <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 999, padding: '2px 8px' }}>
                    <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: tone.text, letterSpacing: '0.04em' }}>
                      {v.tag}
                    </span>
                  </span>
                </div>
              )
            })}
            <div style={{ padding: '11px 14px', borderTop: '1px solid var(--line-1)', fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
              Published and maintained by ASComply. New versions arrive automatically — you never edit these.
            </div>
          </div>

          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', overflow: 'hidden' }}>
            <div style={{ padding: '12px 14px', display: 'flex', alignItems: 'center', gap: 8, borderBottom: '1px solid var(--line-1)' }}>
              <span style={{ color: 'var(--action)', display: 'inline-flex' }}>{sparkGlyph}</span>
              <span style={{ fontSize: 13, fontWeight: 600 }}>Suggested for you</span>
              <span className="mono" style={{ marginLeft: 'auto', fontSize: 10, fontWeight: 700, background: 'var(--action-tint)', color: 'var(--action)', borderRadius: 99, padding: '1px 7px' }}>
                {suggestions.length}
              </span>
            </div>
            {suggestions.length === 0 ? (
              <div style={{ padding: '14px', fontSize: 12, lineHeight: 1.5, color: 'var(--fg-3)' }}>
                Nothing to suggest right now — every rule we inferred from your rejections is already in your custom list.
              </div>
            ) : (
              suggestions.map((s) => (
                <div key={s.key} style={{ padding: '11px 14px', borderBottom: '1px solid var(--line-1)' }}>
                  <div className="mono" style={{ fontSize: 11.5, fontWeight: 600, color: 'var(--fg-1)', marginBottom: 2, wordBreak: 'break-all' }}>
                    {s.key}
                  </div>
                  <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', lineHeight: 1.4, marginBottom: 8 }}>
                    {s.derivation}
                  </div>
                  <button
                    type="button"
                    onClick={() => ctx.addSuggestedRule(s)}
                    className="pf-btn"
                    style={{ width: '100%', border: '1px solid var(--line-2)', background: 'var(--bg-2)', cursor: 'pointer', height: 28, borderRadius: 'var(--radius-sm)', fontFamily: 'var(--font-sans)', fontSize: 11.5, fontWeight: 600, color: 'var(--action)' }}
                  >
                    Add as custom rule
                  </button>
                </div>
              ))
            )}
          </div>
        </div>

        {/* ---- the one table, in two labelled groups ---- */}
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflowX: 'auto', background: 'var(--bg-2)' }}>
          <div style={{ padding: '13px 16px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, minWidth: TABLE_MIN_WIDTH }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <span style={{ fontSize: 14, fontWeight: 500, fontFamily: 'var(--font-display)' }}>Rules in force</span>
              <span className="mono" style={{ fontSize: 10, fontWeight: 700, background: 'var(--action-tint)', color: 'var(--action)', border: '1px solid var(--teal-200)', borderRadius: 99, padding: '1px 8px' }}>
                GOLDEN {GOLDEN_SET.version} + {customRules.length} CUSTOM
              </span>
            </div>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              {total} RULES
            </span>
          </div>

          <div style={{ display: 'grid', gridTemplateColumns: RULE_COLS, padding: '9px 16px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)', minWidth: TABLE_MIN_WIDTH }}>
            <span className="label">Key</span>
            <span className="label">Type</span>
            <span className="label">Target field</span>
            <span className="label">Severity</span>
            <span className="label">Source</span>
            <span className="label">Message</span>
            <span className="label" style={{ textAlign: 'right' }}>
              On
            </span>
          </div>

          <GroupHeader label={`INHERITED · GOLDEN RULESET ${GOLDEN_SET.id} ${GOLDEN_SET.version}`} />
          {GOLDEN_RULES.map((r) => (
            <RuleRow
              key={r.key}
              rule={r}
              sourceLabel="GOLDEN"
              sourceAccent={false}
              onOpen={() => ctx.openRule(r.key)}
              on={
                // A lock, never a disabled toggle: a greyed-out switch says "you
                // could flip this if you had permission", and no tenant ever can.
                <span className="mono" style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 9, fontWeight: 700, color: 'var(--fg-4)', letterSpacing: '0.04em' }}>
                  <span style={{ display: 'inline-flex' }}>{lockGlyph}</span>
                  LOCKED
                </span>
              }
            />
          ))}

          <GroupHeader label={`CUSTOM · ${scope}`} accent />
          {customRules.length === 0 ? (
            <div style={{ padding: '20px 16px', borderBottom: '1px solid var(--line-1)', fontSize: 13, lineHeight: 1.6, color: 'var(--fg-3)', minWidth: TABLE_MIN_WIDTH }}>
              No custom rules yet — the golden ruleset alone is running. Add one from the suggestions on the left.
            </div>
          ) : (
            customRules.map((r) => (
              <RuleRow
                key={r.key}
                rule={r}
                sourceLabel="CUSTOM"
                sourceAccent
                onOpen={() => ctx.openRule(r.key)}
                on={
                  // Inside a clickable row, so the toggle's own click must not also
                  // open the drawer.
                  <span style={{ display: 'inline-flex' }} onClick={(e) => e.stopPropagation()}>
                    <button
                      type="button"
                      role="switch"
                      aria-checked={r.enabled}
                      aria-label={`${r.enabled ? 'Disable' : 'Enable'} ${r.key}`}
                      onClick={() => ctx.toggleCustomRule(r.key)}
                      className="pf-toggle"
                      style={{ display: 'inline-flex', width: 34, height: 20, borderRadius: 99, background: r.enabled ? 'var(--action)' : 'var(--line-3)', padding: 2, border: 0, cursor: 'pointer' }}
                    >
                      <span className="pf-knob" style={{ width: 16, height: 16, borderRadius: 99, background: 'var(--bg-2)', transform: r.enabled ? 'translateX(14px)' : 'translateX(0)', boxShadow: 'var(--shadow-soft)' }} />
                    </button>
                  </span>
                }
              />
            ))
          )}

          <div style={{ padding: '11px 16px', background: 'var(--bg-1)', fontSize: 11.5, color: 'var(--fg-3)', minWidth: TABLE_MIN_WIDTH }}>
            Custom rules evaluate after the golden ruleset. A golden rule can never be disabled or edited.
          </div>
        </div>
      </div>

      {openGolden && <RuleDrawer rule={openGolden} scope={scope} onClose={ctx.closeRule} />}
      {openCustom && (
        <RuleDrawer
          rule={openCustom}
          scope={scope}
          onClose={ctx.closeRule}
          onRemove={() => ctx.removeCustomRule(openCustom.key)}
        />
      )}
    </div>
  )
}
