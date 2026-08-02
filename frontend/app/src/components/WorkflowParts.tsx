// Workflows — the presentation atoms the builder's four surfaces share.
//
// Own module (rather than living in WorkflowsView) for the same reason RulePills.tsx
// exists: the canvas, the inspector and the simulator all need the node tone/title
// vocabulary, and none of them should have to import a sibling screen to get it.
//
// Token note: the prototype's teal is `--accent`; in this repo `--accent` is the design
// system's AMBER and is deliberately not aliased (app-layer.css). Every teal below is
// `--action` / `--action-tint`. `--shadow-xs`/`--shadow-sm` map to `--shadow-card`/
// `--shadow-soft`, and the prototype's `--shadow-accent` has no repo equivalent, so the
// primary CTA carries no shadow — same as every other primary button in the app.

import type { ReactNode } from 'react'

import { DOC_TYPE_DEFS } from '../data'
import { chevDownGlyph } from '../glyphs'
import { Icon } from '../icons'
import { fmtPlain } from '../lib/format'
import {
  findNode,
  roleOf,
  ruleText,
  slaText,
  WF_ROLES,
  type BranchNode,
  type NodeType,
  type Policy,
  type PolicyStatus,
  type WfDocType,
} from '../lib/workflows'

/**
 * What the builder is currently about to place: either a fresh block from the palette
 * or an existing node being moved. One shape covers both the HTML5 drag and the
 * click-to-place arm, so every slot asks the same question of either.
 */
export type WfPending = { kind: 'palette'; type: NodeType } | { kind: 'move'; id: string }

/** The node type a pending action would land — `null` when a move's node has vanished. */
export function pendingNodeType(policy: Policy, pending: WfPending | null): NodeType | null {
  if (!pending) return null
  if (pending.kind === 'palette') return pending.type
  return findNode(policy, pending.id)?.node.type ?? null
}

// ---------------------------------------------------------------------------
// Glyphs — hand-authored path data, per the repo's no-Lucide rule (icons.tsx).
// ---------------------------------------------------------------------------

const NODE_PATHS: Record<NodeType, string[]> = {
  approval: ['M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'm16 11 2 2 4-4'],
  condition: ['M6 3v12', 'M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z', 'M6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z', 'M15 6a9 9 0 0 0-9 9'],
  notify: ['M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9', 'M10.3 21a1.94 1.94 0 0 0 3.4 0'],
  autoapprove: [
    'M4 14a1 1 0 0 1-.78-1.63l9.9-10.2a.5.5 0 0 1 .86.46l-1.92 6.02A1 1 0 0 0 13 10h7a1 1 0 0 1 .78 1.63l-9.9 10.2a.5.5 0 0 1-.86-.46l1.92-6.02A1 1 0 0 0 11 14z',
  ],
}

export function NodeGlyph({ type, size = 18 }: { type: NodeType; size?: number }) {
  return <Icon paths={NODE_PATHS[type]} size={size} />
}

/** The Workflows screen's own mark — the same git-branch used by the nav item. */
export const wfBranchGlyph = <Icon paths={NODE_PATHS.condition} size={18} />
export const wfPlusGlyph = <Icon paths={['M12 5v14M5 12h14']} size={15} strokeWidth={2} />
export const wfCrossGlyph = <Icon paths={['M18 6 6 18M6 6l12 12']} size={12} strokeWidth={2.2} />
export const wfBackGlyph = <Icon paths={['m15 18-6-6 6-6']} size={14} strokeWidth={2} />
export const wfTriggerGlyph = <Icon paths={['M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z', 'M14 2v6h6', 'M16 13H8M16 17H8']} size={18} />
export const wfSendGlyph = <Icon paths={['M22 2 11 13', 'M22 2l-7 20-4-9-9-4 20-7Z']} size={15} />
/** Arms a block or a step for click-to-place — the drag-free path through the builder. */
export const wfTargetGlyph = <Icon paths={['M12 3v3M12 18v3M3 12h3M18 12h3', 'M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z']} size={13} strokeWidth={2} />

// ---------------------------------------------------------------------------
// Tone + copy per node kind
// ---------------------------------------------------------------------------

export type NodeTone = { bg: string; color: string; badge: string; badgeBg: string; badgeBorder: string; badgeColor: string }

export const NODE_TONE: Record<NodeType, NodeTone> = {
  approval: { bg: 'var(--action-tint)', color: 'var(--action)', badge: 'APPROVAL', badgeBg: 'var(--action-tint)', badgeBorder: 'var(--action)', badgeColor: 'var(--action)' },
  condition: { bg: 'var(--slate-100)', color: 'var(--fg-1)', badge: 'CONDITION', badgeBg: 'var(--slate-100)', badgeBorder: 'var(--line-2)', badgeColor: 'var(--fg-2)' },
  notify: { bg: 'var(--status-amber-bg)', color: 'var(--status-amber-text)', badge: 'NOTIFY', badgeBg: 'var(--status-amber-bg)', badgeBorder: 'var(--status-amber-border)', badgeColor: 'var(--status-amber-text)' },
  autoapprove: { bg: 'var(--status-green-bg)', color: 'var(--status-green-text)', badge: 'AUTO', badgeBg: 'var(--status-green-bg)', badgeBorder: 'var(--status-green-border)', badgeColor: 'var(--status-green-text)' },
}

export function nodeTitle(n: BranchNode): string {
  if (n.type === 'approval') return `${roleOf(n.role).title} must approve`
  if (n.type === 'notify') return `Notify ${n.target}`
  return 'Auto-approve'
}

/**
 * A workflow role already resolved to a person AND already worded for the surface that asked
 * — the canvas and the inspector phrase the same resolution differently. The tone travels
 * with the copy because both come from one `roles.ts` `Resolved` the components never see:
 * `lib/roles.ts` is barred from the canvas and the inspector, so `WorkflowBuilder` renders
 * the string and hands it down. `amber` is true for "nobody holds this role", "the only
 * holder is suspended" and "the role is gone" alike.
 */
export type ResolvedLine = { line: string; amber: boolean }

/**
 * `approvalLine` REPLACES the abstract role line ("Finance") with a resolved person, and is
 * passed only in in-house mode. Called with one argument the output is byte-identical to
 * before, which is what keeps firm-mode cards unchanged.
 */
export function nodeSub(n: BranchNode, approvalLine?: string): string {
  if (n.type === 'approval') {
    // roleOf's unknown-key fallback carries an empty line; joining unconditionally
    // would render a leading " · " on a card whose role went missing.
    return [approvalLine ?? roleOf(n.role).line, slaText(n.sla)].filter(Boolean).join(' · ')
  }
  if (n.type === 'notify') return `Watcher · ${n.channel}`
  return 'Clears without manual sign-off'
}

/** The condition head's headline — the same sentence the inspector previews. */
export { ruleText }

// The simulator's rail is 320px wide at 12.5px, so its rows drop the " must approve"
// suffix the canvas cards carry and lean on the mono sub-line for the rest.
export function simTitle(n: BranchNode): string {
  return n.type === 'approval' ? roleOf(n.role).title : nodeTitle(n)
}

export function simSub(n: BranchNode): string {
  if (n.type === 'approval') return roleOf(n.role).line.toUpperCase()
  if (n.type === 'notify') return n.channel.toUpperCase()
  return 'NO SIGN-OFF NEEDED'
}

// ---------------------------------------------------------------------------
// Option lists
// ---------------------------------------------------------------------------

export type WfOption = { value: string; label: string }

export const ROLE_OPTIONS: WfOption[] = WF_ROLES.map((r) => ({ value: r.key, label: `${r.title} · ${r.line}` }))

export const SLA_OPTIONS: WfOption[] = [
  { value: '0', label: 'No deadline' },
  { value: '24', label: 'Within 24 hours' },
  { value: '48', label: 'Within 48 hours' },
  { value: '72', label: 'Within 72 hours' },
]

export const FIELD_OPTIONS: WfOption[] = [
  { value: 'amount', label: 'Invoice amount' },
  { value: 'docType', label: 'Document type' },
  { value: 'newCustomer', label: 'Customer' },
]

export const OP_OPTIONS: WfOption[] = [
  { value: '>', label: 'greater than' },
  { value: '>=', label: 'at least' },
  { value: '<', label: 'less than' },
  { value: '<=', label: 'at most' },
]

/** The one doc-type list in the app — the same triple the create flow renders. */
export const DOC_OPTIONS: WfOption[] = DOC_TYPE_DEFS.map(([code, kind]) => ({ value: code, label: `${code} · ${kind}` }))

export const CUST_OPTIONS: WfOption[] = [
  { value: 'true', label: 'New / unverified' },
  { value: 'false', label: 'Existing' },
]

/**
 * The FIRM notify list — a module constant with one consumer, and the object firm mode is
 * handed by identity rather than by value (MEMB-01 §15.2). Editing it in place would change
 * firm mode, which is exactly what the in-house fork must not do: in-house builds its own
 * list from the member roster and passes it as the same prop.
 */
export const TARGET_OPTIONS: WfOption[] = ['Tax Team', 'Finance Team', 'Audit Committee', 'Internal Audit', 'Preparer'].map((t) => ({ value: t, label: t }))

/** Self-labelling options — the shape the two roster-derived lists (notify, delegate) take. */
export function toOptions(values: string[]): WfOption[] {
  return values.map((v) => ({ value: v, label: v }))
}

export const CHANNEL_OPTIONS: WfOption[] = ['Email', 'In-app', 'SMS'].map((c) => ({ value: c, label: c }))

export const AMOUNT_PRESETS: { label: string; value: number }[] = [
  { label: '₦100M', value: 100_000_000 },
  { label: '₦500M', value: 500_000_000 },
  { label: '₦1B', value: 1_000_000_000 },
]

export function isDocType(v: unknown): v is WfDocType {
  return v === 'B2B' || v === 'B2G' || v === 'B2C'
}

// ---------------------------------------------------------------------------
// Controls
// ---------------------------------------------------------------------------

export const POLICY_TONE: Record<PolicyStatus, { bg: string; border: string; text: string; label: string }> = {
  published: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'PUBLISHED' },
  draft: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'DRAFT' },
}

export function PolicyStatusPill({ status, padding = '2px 8px' }: { status: PolicyStatus; padding?: string }) {
  const tone = POLICY_TONE[status]
  return (
    <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 5, background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 999, padding }}>
      <span style={{ width: 5, height: 5, borderRadius: 99, background: tone.text }} />
      <span className="mono" style={{ fontSize: 8.5, fontWeight: 600, color: tone.text, letterSpacing: '0.05em' }}>
        {tone.label}
      </span>
    </span>
  )
}

/**
 * A labelled select with its own chevron. The app-layer strips native select chrome
 * (`appearance: none`) and forbids drawing a chevron into the control's background —
 * the `.asc-app` rule outranks any component background — so the caller draws one
 * beside it. This is the first styled <select> in the app; every other screen dodged it.
 */
export function WfSelect({ label, value, options, onChange, height = 38, marginBottom = 0, hideLabel = false, width }: {
  label: string
  value: string
  options: WfOption[]
  onChange: (v: string) => void
  height?: number
  marginBottom?: number
  /** For the scope row, where a sibling `.label` already names the control on screen. */
  hideLabel?: boolean
  width?: number | string
}) {
  return (
    <label style={{ display: hideLabel ? 'inline-block' : 'block', width, marginBottom }} aria-label={hideLabel ? label : undefined}>
      {!hideLabel && (
        <span className="label" style={{ display: 'block', marginBottom: 6 }}>
          {label}
        </span>
      )}
      <span style={{ position: 'relative', display: 'block' }}>
        <select
          className="pf-select"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          style={{ width: '100%', height, padding: '0 32px 0 12px', border: '1px solid var(--line-2)', backgroundColor: 'var(--bg-1)', color: 'var(--fg-1)', fontSize: 13, cursor: 'pointer', boxSizing: 'border-box' }}
        >
          {options.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <span aria-hidden="true" style={{ position: 'absolute', right: 9, top: '50%', transform: 'translateY(-50%)', display: 'inline-flex', color: 'var(--fg-3)', pointerEvents: 'none' }}>
          {chevDownGlyph}
        </span>
      </span>
    </label>
  )
}

/** ₦-prefixed mono amount field. The ₦ is a sibling span, never a background image. */
export function WfAmountInput({ value, onChange, ariaLabel, marginBottom = 0 }: {
  value: number
  onChange: (v: number) => void
  ariaLabel: string
  marginBottom?: number
}) {
  return (
    <div style={{ position: 'relative', marginBottom }}>
      <span aria-hidden="true" style={{ position: 'absolute', left: 11, top: '50%', transform: 'translateY(-50%)', fontSize: 13, color: 'var(--fg-3)', pointerEvents: 'none' }}>
        ₦
      </span>
      <input
        type="text"
        inputMode="numeric"
        className="pf-input"
        aria-label={ariaLabel}
        value={fmtPlain(value)}
        onChange={(e) => onChange(Number(e.target.value.replace(/[^0-9]/g, '')) || 0)}
        style={{ width: '100%', height: 38, padding: '0 10px 0 24px', border: '1px solid var(--line-2)', backgroundColor: 'var(--bg-1)', color: 'var(--fg-1)', fontFamily: 'var(--font-mono)', fontSize: 13, boxSizing: 'border-box' }}
      />
    </div>
  )
}

/** 34×18 switch. Same pf-toggle/pf-knob transitions the Rules screen uses. */
export function WfToggle({ on, onToggle, label }: { on: boolean; onToggle: () => void; label: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={label}
      onClick={onToggle}
      className="pf-toggle"
      style={{ flex: 'none', position: 'relative', display: 'inline-block', width: 34, height: 18, padding: 0, border: 0, borderRadius: 99, cursor: 'pointer', background: on ? 'var(--action)' : 'var(--line-3)' }}
    >
      <span className="pf-knob" style={{ position: 'absolute', top: 2, left: 2, width: 14, height: 14, borderRadius: 99, background: 'var(--bg-2)', transform: on ? 'translateX(16px)' : 'translateX(0)', boxShadow: 'var(--shadow-card)' }} />
    </button>
  )
}

/** The small square action buttons that ride inside a card: delete, and arm-to-place. */
export function WfIconButton({ label, glyph, onClick, size = 24, tone = 'plain', pressed }: {
  label: string
  glyph: ReactNode
  onClick: () => void
  size?: number
  tone?: 'plain' | 'danger'
  pressed?: boolean
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      aria-pressed={pressed}
      onClick={(e) => {
        // Every one of these sits inside a card that selects on click.
        e.stopPropagation()
        onClick()
      }}
      className="pf-btn"
      style={{
        flex: 'none',
        display: 'grid',
        placeItems: 'center',
        width: size,
        height: size,
        border: 0,
        padding: 0,
        cursor: 'pointer',
        background: pressed ? 'var(--action-tint)' : 'transparent',
        color: pressed ? 'var(--action)' : tone === 'danger' ? 'var(--fg-4)' : 'var(--fg-3)',
      }}
    >
      {glyph}
    </button>
  )
}
