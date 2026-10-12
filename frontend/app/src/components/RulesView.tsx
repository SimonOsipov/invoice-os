// Rules: the golden ruleset (GET /v1/rules, read-only, locked) and the tenant's custom rules
// (/v1/custom-rules). Golden rows carry a lock, never a switch (F-285).

import { useEffect, useRef, useState, type ReactNode } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { lockGlyph, plusGlyph } from '../glyphs'
import { companySetupAccess } from '../lib/members'
import {
  ADMIN_ONLY_REASON,
  deleteCustomRule,
  getGoldenRules,
  listCustomRules,
  ruleInputOf,
  ruleKeyLabel,
  rulesInScope,
  scopeLabel,
  updateCustomRule,
  type CustomRule,
  type GoldenRule,
  type GoldenRules,
  type GoldenVersion,
} from '../lib/rulesApi'
import { RuleDrawer } from './RuleDrawer'
import { SeverityPill, TypePill } from './RulePills'
import type { PlatformCtx } from '../types'

// Key · Type · Target field · Severity · Source · Message · On
//
// These minimums are load-bearing, not taste. The table sits beside a fixed 244px
// rail inside a 252px-sidebar shell, which leaves it ~854px at a 1440px viewport —
// so a min-width above that pushes the LAST column off-screen behind a horizontal
// scrollbar. That column is the lock/toggle: the one cell on the row that says
// whether a rule is inherited or yours to switch off. It has to be visible without
// scrolling at a normal laptop width. Sum of the minimums below is 744 + 32px of
// row padding = 776, which is what TABLE_MIN_WIDTH states.
const RULE_COLS = 'minmax(150px,1.1fr) 118px minmax(120px,1fr) 72px 74px minmax(140px,1.3fr) 70px'
const TABLE_MIN_WIDTH = 790

const VERSION_TONE: Record<GoldenVersion['state'], { bg: string; border: string; text: string; tag: string }> = {
  in_force: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', tag: 'IN USE' },
  scheduled: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', tag: 'SCHEDULED' },
  superseded: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', tag: 'SUPERSEDED' },
}

function versionMeta(v: GoldenVersion): string {
  return `${v.state === 'scheduled' ? 'starts' : 'since'} ${v.effective_from} · ${v.rule_count} rules`
}

const DISABLED_STYLE = { opacity: 0.45, cursor: 'not-allowed', filter: 'none' } as const

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

/** The shared row body. `on` is whatever belongs in the On column: lock or switch. */
function RuleRow({ rule, sourceLabel, sourceAccent, on, onOpen }: {
  rule: GoldenRule | CustomRule
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
      <span className="mono" title={rule.key} style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg-1)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>
        {ruleKeyLabel(rule.key)}
      </span>
      <TypePill type={rule.type} />
      <span className="mono" style={{ fontSize: 11.5, color: 'var(--fg-2)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', paddingRight: 10 }}>
        {rule.target || 'whole invoice'}
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

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

export function RulesView({ ctx }: { ctx: PlatformCtx }) {
  const { active, mode, openRuleKey, authedFetch } = ctx
  const base = gatewayBase()
  const isFirm = mode === 'firm'
  const scope = active.short

  const golden = useAsync<GoldenRules>(
    () => (base ? getGoldenRules(authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null },
  )
  const customList = useAsync<CustomRule[]>(
    () => (base ? listCustomRules(authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null, isEmpty: () => false },
  )
  // Mirror of the last good list, so a toggle replaces one row and a refetch does not blank the table.
  const [custom, setCustom] = useState<CustomRule[] | null>(null)
  // A response older than the last write is stale: writes bump the counter, a fetch records it at start.
  const writes = useRef(0)
  const fetchedAt = useRef(0)
  useEffect(() => {
    if (customList.data && fetchedAt.current === writes.current) setCustom(customList.data)
  }, [customList.data])
  function refetchCustom() {
    fetchedAt.current = writes.current
    customList.run()
  }

  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set())
  const [rowErrors, setRowErrors] = useState<Readonly<Record<string, string>>>({})
  const [removing, setRemoving] = useState(false)
  const [removeError, setRemoveError] = useState<string | null>(null)

  useEffect(() => setRemoveError(null), [openRuleKey])

  const access = companySetupAccess(ctx.membersState, ctx.members)
  const canWrite = access === 'add'
  const reason = access === 'wait' ? ADMIN_ONLY_REASON : null

  const subtitle = isFirm
    ? `ASComply's golden ruleset plus the custom checks you run for ${scope}`
    : `ASComply's golden ruleset plus the custom checks ${scope} runs internally`

  // A failed refetch with rules already shown stays inline; only a first load fails the screen.
  const failed = golden.status === 'error' ? golden : customList.status === 'error' && custom == null ? customList : null
  const refreshError = customList.status === 'error' && custom != null ? customList.error : null
  const ready = golden.data != null && custom != null
  const inScope = rulesInScope(custom ?? [], ctx.activeEntity?.id ?? null)

  function retry() {
    if (golden.status === 'error') golden.run()
    if (customList.status === 'error') refetchCustom()
  }

  async function toggle(r: CustomRule) {
    if (!base || busy.has(r.id)) return
    setBusy((b) => new Set(b).add(r.id))
    setRowErrors((m) => {
      const next = { ...m }
      delete next[r.id]
      return next
    })
    try {
      const updated = await updateCustomRule(authedFetch, base, r.id, { ...ruleInputOf(r), enabled: !r.enabled })
      writes.current++
      setCustom((list) => (list ? list.map((x) => (x.id === updated.id ? updated : x)) : list))
    } catch (e) {
      setRowErrors((m) => ({ ...m, [r.id]: errorText(e) }))
    } finally {
      setBusy((b) => {
        const next = new Set(b)
        next.delete(r.id)
        return next
      })
    }
  }

  async function remove(r: CustomRule) {
    if (!base || removing) return
    setRemoving(true)
    setRemoveError(null)
    try {
      await deleteCustomRule(authedFetch, base, r.id)
      writes.current++
      setCustom((list) => (list ? list.filter((x) => x.id !== r.id) : list))
      ctx.closeRule()
      refetchCustom()
    } catch (e) {
      setRemoveError(errorText(e))
    } finally {
      setRemoving(false)
    }
  }

  const g = golden.data
  const total = (g?.rules.length ?? 0) + inScope.length
  // A key names a golden rule or a custom one, never both (`custom:` prefix).
  const openGolden = openRuleKey ? g?.rules.find((r) => r.key === openRuleKey) : undefined
  const openCustom = openRuleKey ? inScope.find((r) => r.key === openRuleKey) : undefined
  const customBanner = (r: CustomRule) =>
    `Custom rule · ${r.entity_id === null ? 'FIRM-WIDE' : (ctx.activeEntity?.name ?? scope)} · runs after the golden ruleset`

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 20, flexWrap: 'wrap', marginBottom: 22 }}>
        <div style={{ minWidth: 0 }}>
          <div className="eyebrow" style={{ marginBottom: 7 }}>
            VALIDATION ENGINE
          </div>
          <h1 style={{ fontSize: 26, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Rules</h1>
          <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>{subtitle}</p>
        </div>
        {ready && (
          <span className="mono" style={{ flex: 'none', fontSize: 11, color: 'var(--fg-3)' }}>
            {total} RULES
          </span>
        )}
      </div>

      {base == null && <EmptyState title="Rules are not available" message="Rules load from the server." />}
      {base != null && failed?.error && <ErrorState error={failed.error} onRetry={retry} />}
      {base != null && !failed && !ready && <Loading label="Loading rules…" />}

      {base != null && !failed && g != null && custom != null && (
        <div className="pf-rules-grid" style={{ display: 'grid', gridTemplateColumns: '244px minmax(0,1fr)', gap: 18, alignItems: 'start' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', overflow: 'hidden' }}>
              <div className="label" style={{ padding: '12px 14px 8px' }}>
                Golden ruleset
              </div>
              {g.versions.map((v) => {
                const tone = VERSION_TONE[v.state]
                return (
                  // Meta gets the card's full width below the tag: beside a SUPERSEDED pill it breaks mid-phrase.
                  <div
                    key={v.rule_set_version_id}
                    style={{ padding: '11px 14px', borderTop: '1px solid var(--line-1)', background: v.state === 'in_force' ? 'var(--action-tint)' : 'var(--bg-2)' }}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                      <span className="mono" style={{ fontSize: 13, fontWeight: 700, color: 'var(--fg-1)' }}>
                        v{v.version}
                      </span>
                      <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 'var(--radius-sm)', padding: '2px 8px' }}>
                        <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: tone.text, letterSpacing: '0.04em' }}>
                          {tone.tag}
                        </span>
                      </span>
                    </div>
                    <span className="mono" style={{ display: 'block', fontSize: 10, color: 'var(--fg-3)', marginTop: 3 }}>
                      {versionMeta(v)}
                    </span>
                  </div>
                )
              })}
              <div style={{ padding: '11px 14px', borderTop: '1px solid var(--line-1)', fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
                Published and maintained by ASComply. New versions arrive automatically — you never edit these.
              </div>
            </div>
          </div>

          <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflowX: 'auto', background: 'var(--bg-2)' }}>
            <div style={{ padding: '13px 16px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, minWidth: TABLE_MIN_WIDTH }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                <span className="card-title">Rules in force</span>
                <span className="mono" style={{ fontSize: 10, fontWeight: 700, background: 'var(--action-tint)', color: 'var(--action)', border: '1px solid var(--teal-200)', borderRadius: 'var(--radius-sm)', padding: '1px 8px' }}>
                  GOLDEN v{g.version} + {inScope.length} CUSTOM
                </span>
              </div>
              <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                {/* ENGI-15-07 gives New rule its editor. */}
                <button
                  type="button"
                  disabled={!canWrite}
                  title={reason ?? undefined}
                  aria-describedby={reason ? 'rules-admin-reason' : undefined}
                  data-testid="rules-new"
                  className="v2-btn v2-btn-primary pf-btn"
                  style={{ flex: 'none', height: 30, gap: 7, ...(canWrite ? null : DISABLED_STYLE) }}
                >
                  <span style={{ display: 'inline-flex' }}>{plusGlyph}</span> New rule
                </button>
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                  {total} RULES
                </span>
              </div>
            </div>

            {refreshError && (
              <div
                data-testid="rules-refresh-error"
                style={{ padding: '8px 16px', borderBottom: '1px solid var(--line-1)', fontSize: 12, color: 'var(--status-red-text)', background: 'var(--status-red-bg)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, minWidth: TABLE_MIN_WIDTH }}
              >
                <span>{refreshError.message}</span>
                <button type="button" onClick={refetchCustom} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 26 }}>
                  Retry
                </button>
              </div>
            )}

            {reason && (
              <div
                id="rules-admin-reason"
                data-testid="rules-admin-reason"
                style={{ padding: '8px 16px', borderBottom: '1px solid var(--line-1)', fontSize: 11.5, color: 'var(--fg-3)', textAlign: 'right', minWidth: TABLE_MIN_WIDTH }}
              >
                {reason}
              </div>
            )}

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

            <GroupHeader label={`INHERITED · GOLDEN RULESET v${g.version}`} />
            {g.rules.map((r) => (
              <RuleRow
                key={r.key}
                rule={r}
                sourceLabel="GOLDEN"
                sourceAccent={false}
                onOpen={() => ctx.openRule(r.key)}
                on={
                  // A lock, never a disabled switch: no tenant can ever flip a golden rule.
                  <span className="mono" style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 9, fontWeight: 700, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
                    <span style={{ display: 'inline-flex' }}>{lockGlyph}</span>
                    LOCKED
                  </span>
                }
              />
            ))}

            <GroupHeader label={`CUSTOM · ${scope}`} accent />
            {inScope.length === 0 ? (
              <div style={{ padding: '18px 16px', borderBottom: '1px solid var(--line-1)', fontSize: 13, lineHeight: 1.55, color: 'var(--fg-3)', minWidth: TABLE_MIN_WIDTH }}>
                No custom rules yet — the golden ruleset alone is running.
              </div>
            ) : (
              inScope.map((r) => {
                const off = !canWrite || busy.has(r.id)
                return (
                  <div key={r.id}>
                    <RuleRow
                      rule={r}
                      sourceLabel={scopeLabel(r, mode)}
                      sourceAccent
                      onOpen={() => ctx.openRule(r.key)}
                      on={
                        // The switch's own click must not also open the drawer.
                        <span style={{ display: 'inline-flex' }} onClick={(e) => e.stopPropagation()}>
                          <button
                            type="button"
                            role="switch"
                            aria-checked={r.enabled}
                            aria-label={`${r.enabled ? 'Disable' : 'Enable'} rule "${r.message}"`}
                            disabled={off}
                            title={reason ?? undefined}
                            onClick={() => void toggle(r)}
                            className="pf-toggle"
                            style={{ display: 'inline-flex', width: 34, height: 20, borderRadius: 99, background: r.enabled ? 'var(--action)' : 'var(--line-3)', padding: 2, border: 0, cursor: 'pointer', ...(off ? DISABLED_STYLE : null) }}
                          >
                            <span className="pf-knob" style={{ width: 16, height: 16, borderRadius: '50%', background: 'var(--bg-2)', transform: r.enabled ? 'translateX(14px)' : 'translateX(0)' }} />
                          </button>
                        </span>
                      }
                    />
                    {rowErrors[r.id] && (
                      <div
                        data-testid="rule-row-error"
                        style={{ padding: '6px 16px', borderBottom: '1px solid var(--line-1)', fontSize: 12, color: 'var(--status-red-text)', background: 'var(--status-red-bg)', minWidth: TABLE_MIN_WIDTH }}
                      >
                        {rowErrors[r.id]}
                      </div>
                    )}
                  </div>
                )
              })
            )}

            <div style={{ padding: '12px 16px', lineHeight: 1.5, fontSize: 11.5, color: 'var(--fg-3)', minWidth: TABLE_MIN_WIDTH }}>
              Custom rules evaluate after the golden ruleset. A golden rule can never be disabled or edited.
            </div>
          </div>
        </div>
      )}

      {openGolden && g && (
        <RuleDrawer rule={openGolden} banner={`Managed by ASComply · golden ruleset v${g.version} · read-only`} onClose={ctx.closeRule} />
      )}
      {openCustom && (
        <RuleDrawer
          rule={openCustom}
          banner={customBanner(openCustom)}
          onClose={ctx.closeRule}
          actions={{
            disabled: !canWrite,
            reason,
            removing,
            error: removeError,
            onRemove: () => void remove(openCustom),
          }}
        />
      )}
    </div>
  )
}
