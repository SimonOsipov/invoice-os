// Workflows — the flow canvas: trigger, the step rail, and the transmit terminal.
//
// Drop targets are SLOTS, not cards: one slot before every step plus a tail slot, each
// identified by `${laneKey}#${insertionIndex}` (lib/workflows.ts). `laneKey` is 'root'
// or '<conditionId>:then' / '<conditionId>:else', which is the whole reason the tree
// can only be two deep — a condition has no lane key of its own to nest into.
//
// A single `dropHint` string lives above this component, so exactly one slot can ever
// be lit. Slots also accept a CLICK once a step has been armed with its target button:
// that is the drag-free path, and the only one Playwright can drive (the import
// wizard's mapper takes the same arm-then-click approach).

import type { DragEvent, ReactNode } from 'react'

import {
  NodeGlyph,
  NODE_TONE,
  nodeSub,
  nodeTitle,
  pendingNodeType,
  ruleText,
  WfIconButton,
  wfCrossGlyph,
  wfSendGlyph,
  wfTargetGlyph,
  wfTriggerGlyph,
  type WfPending,
} from './WorkflowParts'
import { canDrop, parseLoc, type BranchNode, type ConditionNode, type Loc, type Policy, type WfNode } from '../lib/workflows'

export type CanvasProps = {
  policy: Policy
  selId: string | null
  /** The drag in flight, or the armed step — slots treat both identically. */
  pending: WfPending | null
  /** True when `pending` came from a click, so slots are clickable targets. */
  canClickPlace: boolean
  armedNodeId: string | null
  dropHint: Loc | null
  onSelect: (id: string) => void
  onDelete: (id: string) => void
  onArmNode: (id: string) => void
  onNodeDragStart: (id: string, e: DragEvent) => void
  onDragEnd: () => void
  onSlotOver: (loc: Loc, e: DragEvent) => void
  onSlotLeave: (loc: Loc) => void
  onSlotDrop: (loc: Loc, e: DragEvent) => void
  onSlotClick: (loc: Loc) => void
}

export function WorkflowCanvas(api: CanvasProps) {
  const { policy, pending } = api
  const nodes = policy.nodes
  const emptyIdle = nodes.length === 0 && pending === null

  return (
    <div
      style={{
        border: '1px solid var(--line-1)',
        borderRadius: 14,
        padding: '24px 22px 28px',
        // backgroundColor + backgroundImage as separate properties, never the
        // `background` shorthand — the shorthand resets the other half.
        backgroundColor: 'var(--bg-0)',
        backgroundImage: 'radial-gradient(var(--line-2) 1px, transparent 0)',
        backgroundSize: '18px 18px',
        backgroundPosition: '9px 9px',
      }}
    >
      <div style={{ width: '100%', maxWidth: 480, margin: '0 auto' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, background: 'var(--slate-800)', color: 'var(--text-on-dark)', borderRadius: 14, padding: '13px 16px', boxShadow: 'var(--shadow-soft)' }}>
          <span style={{ flex: 'none', width: 34, height: 34, borderRadius: 12, background: 'var(--on-dark-10)', display: 'grid', placeItems: 'center' }}>{wfTriggerGlyph}</span>
          <div style={{ minWidth: 0 }}>
            <div style={{ fontSize: 13.5, fontWeight: 600 }}>Invoice submitted</div>
            <div className="mono" style={{ fontSize: 10, color: 'var(--on-dark-70)', letterSpacing: '0.05em' }}>
              TRIGGER · {policy.scope.toUpperCase()}
            </div>
          </div>
        </div>

        {nodes.map((n, i) => (
          <div key={n.id}>
            <Slot api={api} loc={`root#${i}`} variant="root" />
            {n.type === 'condition' ? <ConditionBlock api={api} node={n} /> : <SimpleCard api={api} node={n} />}
          </div>
        ))}

        <Slot api={api} loc={`root#${nodes.length}`} variant="root" />

        {emptyIdle && (
          <div style={{ border: '1px dashed var(--line-2)', borderRadius: 14, padding: 22, textAlign: 'center', fontSize: 12.5, color: 'var(--fg-4)', marginBottom: 4 }}>
            No approval steps — invoices transmit immediately. Drag a block here to require sign-off.
          </div>
        )}

        <div style={{ display: 'flex', alignItems: 'center', gap: 12, background: 'var(--bg-2)', border: '1.5px solid var(--action)', borderRadius: 14, padding: '12px 16px', boxShadow: 'var(--shadow-card)' }}>
          <span style={{ flex: 'none', width: 34, height: 34, borderRadius: 12, background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center' }}>{wfSendGlyph}</span>
          <div style={{ minWidth: 0 }}>
            <div style={{ fontSize: 13.5, fontWeight: 600 }}>Transmit to FIRS / MBS</div>
            <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>
              SIGN · STAMP · SUBMIT
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

/**
 * One insertion point. Renders nothing at all when a drop here would be illegal, which
 * is also why `onDragOver` never gets a chance to preventDefault for such a target.
 * Root slots show a connector line at rest; branch slots have no resting form.
 */
function Slot({ api, loc, variant }: { api: CanvasProps; loc: Loc; variant: 'root' | 'branch' }) {
  const { laneKey } = parseLoc(loc)
  const hot = api.pending !== null
  const valid = canDrop(pendingNodeType(api.policy, api.pending), laneKey)
  const line = variant === 'root' && !hot
  const dropBox = hot && valid
  const active = dropBox && api.dropHint === loc
  const clickable = dropBox && api.canClickPlace

  if (!line && !dropBox) return null

  const borderColor = active ? 'var(--action)' : 'var(--line-2)'
  const bg = active ? 'var(--action-tint)' : 'transparent'
  const textColor = active ? 'var(--action)' : 'var(--fg-4)'

  return (
    <div
      data-wf-slot={loc}
      role={clickable ? 'button' : undefined}
      tabIndex={clickable ? 0 : undefined}
      aria-label={clickable ? `Place step at ${loc}` : undefined}
      onDragOver={(e) => api.onSlotOver(loc, e)}
      onDragLeave={() => api.onSlotLeave(loc)}
      onDrop={(e) => api.onSlotDrop(loc, e)}
      onClick={clickable ? () => api.onSlotClick(loc) : undefined}
      onKeyDown={
        clickable
          ? (e) => {
              if (e.key !== 'Enter' && e.key !== ' ') return
              e.preventDefault()
              api.onSlotClick(loc)
            }
          : undefined
      }
      style={{ display: 'flex', justifyContent: 'center', padding: variant === 'root' ? '4px 0' : '2px 0', cursor: clickable ? 'pointer' : undefined }}
    >
      {line && <span style={{ width: 2, height: 16, background: 'var(--line-2)' }} />}
      {dropBox &&
        (variant === 'root' ? (
          <div style={{ width: '100%', height: 32, display: 'grid', placeItems: 'center', border: `1.5px dashed ${borderColor}`, background: bg, borderRadius: 12, fontSize: 11, fontWeight: 600, color: textColor }}>
            {active ? 'Release to place' : 'Drop step here'}
          </div>
        ) : (
          <div style={{ width: '100%', height: 28, border: `1.5px dashed ${borderColor}`, background: bg, borderRadius: 7 }} />
        ))}
    </div>
  )
}

/** The two controls every step carries: arm-for-placement, and delete. */
function StepActions({ api, node, size }: { api: CanvasProps; node: WfNode; size: number }) {
  const label = node.type === 'condition' ? ruleText(node) : nodeTitle(node)
  return (
    <>
      <WfIconButton label={`Move ${label}`} glyph={wfTargetGlyph} size={size} pressed={api.armedNodeId === node.id} onClick={() => api.onArmNode(node.id)} />
      <WfIconButton label={`Delete ${label}`} glyph={wfCrossGlyph} size={size} tone="danger" onClick={() => api.onDelete(node.id)} />
    </>
  )
}

function cardShell(selected: boolean): { border: string; boxShadow: string } {
  return { border: `1.5px solid ${selected ? 'var(--action)' : 'var(--line-2)'}`, boxShadow: selected ? 'var(--shadow-soft)' : 'var(--shadow-card)' }
}

function SimpleCard({ api, node }: { api: CanvasProps; node: BranchNode }) {
  const tone = NODE_TONE[node.type]
  return (
    <div
      draggable
      onDragStart={(e) => api.onNodeDragStart(node.id, e)}
      onDragEnd={api.onDragEnd}
      onClick={() => api.onSelect(node.id)}
      style={{ position: 'relative', display: 'flex', alignItems: 'center', gap: 12, background: 'var(--bg-2)', borderRadius: 14, padding: '12px 14px', cursor: 'grab', ...cardShell(api.selId === node.id) }}
    >
      <span style={{ flex: 'none', width: 34, height: 34, borderRadius: 12, background: tone.bg, color: tone.color, display: 'grid', placeItems: 'center' }}>
        <NodeGlyph type={node.type} />
      </span>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontSize: 13.5, fontWeight: 600 }}>{nodeTitle(node)}</div>
        <div style={{ fontSize: 11.5, color: 'var(--fg-3)' }}>{nodeSub(node)}</div>
      </div>
      <Badge tone={tone.badgeColor} bg={tone.badgeBg} border={tone.badgeBorder}>
        {tone.badge}
      </Badge>
      <StepActions api={api} node={node} size={24} />
    </div>
  )
}

function Badge({ children, tone, bg, border }: { children: ReactNode; tone: string; bg: string; border: string }) {
  return (
    <span className="mono" style={{ flex: 'none', fontSize: 8.5, fontWeight: 600, color: tone, background: bg, border: `1px solid ${border}`, borderRadius: 8, padding: '2px 6px', letterSpacing: '0.05em' }}>
      {children}
    </span>
  )
}

function ConditionBlock({ api, node }: { api: CanvasProps; node: ConditionNode }) {
  const selected = api.selId === node.id
  const shell = cardShell(selected)
  return (
    <div style={{ background: 'var(--bg-2)', borderRadius: 16, overflow: 'hidden', ...shell }}>
      <div
        draggable
        onDragStart={(e) => api.onNodeDragStart(node.id, e)}
        onDragEnd={api.onDragEnd}
        onClick={() => api.onSelect(node.id)}
        style={{ display: 'flex', alignItems: 'center', gap: 11, padding: '11px 14px', background: selected ? 'var(--action-tint)' : 'var(--bg-2)', borderBottom: '1px solid var(--line-1)', cursor: 'grab' }}
      >
        <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 7, background: 'var(--slate-100)', color: 'var(--fg-1)', display: 'grid', placeItems: 'center' }}>
          <NodeGlyph type="condition" />
        </span>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="mono" style={{ fontSize: 9, fontWeight: 600, color: 'var(--fg-3)', letterSpacing: '0.06em' }}>
            CONDITION
          </div>
          <div style={{ fontSize: 13.5, fontWeight: 600 }}>{ruleText(node)}</div>
        </div>
        <StepActions api={api} node={node} size={24} />
      </div>

      {/* The 1px grid gap over a line-coloured parent IS the divider between lanes. */}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 1, background: 'var(--line-1)' }}>
        <Lane api={api} node={node} branch="then" />
        <Lane api={api} node={node} branch="else" />
      </div>
    </div>
  )
}

function Lane({ api, node, branch }: { api: CanvasProps; node: ConditionNode; branch: 'then' | 'else' }) {
  const items = branch === 'then' ? node.then : node.else
  const laneKey = `${node.id}:${branch}`
  const emptyIdle = items.length === 0 && api.pending === null

  return (
    <div style={{ background: 'var(--bg-1)', padding: '11px 11px 13px' }}>
      <div className="mono" style={{ fontSize: 9, fontWeight: 700, color: branch === 'then' ? 'var(--status-green-text)' : 'var(--fg-3)', letterSpacing: '0.06em', marginBottom: 8 }}>
        {branch === 'then' ? 'IF TRUE →' : 'OTHERWISE →'}
      </div>

      {items.map((c, j) => (
        <div key={c.id}>
          <Slot api={api} loc={`${laneKey}#${j}`} variant="branch" />
          <MiniCard api={api} node={c} />
        </div>
      ))}

      <Slot api={api} loc={`${laneKey}#${items.length}`} variant="branch" />

      {emptyIdle && (
        <div style={{ border: '1px dashed var(--line-2)', borderRadius: 7, padding: '12px 8px', textAlign: 'center', fontSize: 10.5, color: 'var(--fg-4)' }}>
          {branch === 'then' ? 'Drop steps here' : 'Continue (no extra step)'}
        </div>
      )}
    </div>
  )
}

function MiniCard({ api, node }: { api: CanvasProps; node: BranchNode }) {
  const tone = NODE_TONE[node.type]
  return (
    <div
      draggable
      onDragStart={(e) => api.onNodeDragStart(node.id, e)}
      onDragEnd={api.onDragEnd}
      onClick={() => api.onSelect(node.id)}
      style={{ display: 'flex', alignItems: 'center', gap: 9, background: 'var(--bg-2)', borderRadius: 9, padding: '9px 10px', cursor: 'grab', marginBottom: 6, ...cardShell(api.selId === node.id) }}
    >
      <span style={{ flex: 'none', width: 28, height: 28, borderRadius: 7, background: tone.bg, color: tone.color, display: 'grid', placeItems: 'center' }}>
        <NodeGlyph type={node.type} size={15} />
      </span>
      <div style={{ flex: 1, minWidth: 0, fontSize: 12, fontWeight: 600, lineHeight: 1.25 }}>{nodeTitle(node)}</div>
      <StepActions api={api} node={node} size={20} />
    </div>
  )
}
