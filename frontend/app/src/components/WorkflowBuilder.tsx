// Workflows — the policy builder: header, block palette, canvas, inspector, simulator.
//
// This component owns every transient bit of the edit session and NOTHING durable.
// Durable state is one `Policy` on ctx; each edit composes the next one with a pure
// reducer from lib/workflows.ts and hands the whole object to `ctx.savePolicy` — the
// single write funnel, so App.tsx never learns the node tree's shape.
//
// Two prototype behaviours are load-bearing and easy to lose:
//   * every edit DEMOTES a published policy back to draft. That lives in the lib's write
//     funnel (`touch` in lib/workflows.ts), not here, so it cannot be forgotten at a call
//     site; `save` is the only path that publishes, via `publishPolicy`.
//   * a condition may only sit in the root lane. Enforced by `canDrop` in the slot's
//     dragover (no preventDefault ⇒ the browser refuses the drop) and again in `place`.

import { useEffect, useState, type DragEvent } from 'react'

import { WorkflowCanvas } from './WorkflowCanvas'
import { WorkflowInspector } from './WorkflowInspector'
import { WorkflowSimulator } from './WorkflowSimulator'
import { NodeGlyph, NODE_TONE, PolicyStatusPill, WfSelect, wfBackGlyph, type WfPending, pendingNodeType } from './WorkflowParts'
import {
  appendNode,
  canDrop,
  clearSteps,
  deleteNode,
  findNode,
  insertNode,
  moveNode,
  newNode,
  parseLoc,
  publishPolicy,
  renamePolicy,
  rescopePolicy,
  updateNode,
  SIM_DEFAULT,
  WF_SCOPE_OPTIONS,
  type Loc,
  type NodePatch,
  type NodeType,
  type Policy,
  type SimContext,
} from '../lib/workflows'
import type { PlatformCtx } from '../types'

const PALETTE: { type: NodeType; name: string; desc: string }[] = [
  { type: 'approval', name: 'Approval', desc: 'Someone must sign off' },
  { type: 'condition', name: 'Condition', desc: 'Branch on amount or type' },
  { type: 'notify', name: 'Notify', desc: 'Send a heads-up' },
  { type: 'autoapprove', name: 'Auto-approve', desc: 'Clear without sign-off' },
]

const SCOPE_OPTIONS = WF_SCOPE_OPTIONS.map((s) => ({ value: s, label: s }))

/**
 * Width of the borderless title input. The prototype canvas-measures the string at
 * 600 24px and clamps it; this approximates the advance width per character instead —
 * the clamp bounds and the +6 trailing pixel are the parts that matter, since they are
 * what keep the caret visible and stop a long name pushing the status pill off-row.
 */
function nameWidth(name: string): number {
  return Math.min(460, Math.max(120, Math.ceil(name.length * 12.6) + 6))
}

export function WorkflowBuilder({ ctx, policy }: { ctx: PlatformCtx; policy: Policy }) {
  const [selId, setSelId] = useState<string | null>(null)
  const [drag, setDrag] = useState<WfPending | null>(null)
  const [armed, setArmed] = useState<WfPending | null>(null)
  const [dropHint, setDropHint] = useState<Loc | null>(null)
  const [saved, setSaved] = useState(false)
  const [sim, setSim] = useState<SimContext>(SIM_DEFAULT)

  // The Saved flash. 1700ms, and re-clicking Save restarts it rather than stacking a
  // second timer — the effect's cleanup cancels the one in flight.
  useEffect(() => {
    if (!saved) return
    const t = window.setTimeout(() => setSaved(false), 1700)
    return () => window.clearTimeout(t)
  }, [saved])

  // The reducers already demote a published policy to draft, so this just forwards.
  const applyEdit = (next: Policy) => ctx.savePolicy(next)

  function save() {
    ctx.savePolicy(publishPolicy(policy))
    setSaved(true)
  }

  function clear() {
    applyEdit(clearSteps(policy))
    setSelId(null)
    setArmed(null)
  }

  function selectNode(id: string) {
    setSelId((s) => (s === id ? null : id))
  }

  function removeNode(id: string) {
    applyEdit(deleteNode(policy, id))
    setSelId((s) => (s === id ? null : s))
    setArmed((a) => (a && a.kind === 'move' && a.id === id ? null : a))
  }

  function patchNode(id: string, patch: NodePatch) {
    applyEdit(updateNode(policy, id, patch))
  }

  /** Palette click appends at the root tail and selects — the drag-free way to add. */
  function append(type: NodeType) {
    const { policy: next, nodeId } = appendNode(policy, type)
    applyEdit(next)
    setSelId(nodeId)
  }

  /** Arms an existing step for click-placement; clicking the same step disarms it. */
  function armNode(id: string) {
    setArmed((a) => (a && a.kind === 'move' && a.id === id ? null : { kind: 'move', id }))
  }

  function startDrag(pending: WfPending, payload: string, effect: 'copy' | 'move', e: DragEvent) {
    try {
      e.dataTransfer.setData('text/plain', payload)
      e.dataTransfer.effectAllowed = effect
    } catch {
      /* dataTransfer unavailable — click-to-place still works */
    }
    setDrag(pending)
    setArmed(null)
  }

  function endDrag() {
    setDrag(null)
    setDropHint(null)
  }

  function place(pending: WfPending, loc: Loc) {
    const { laneKey, index } = parseLoc(loc)
    if (pending.kind === 'move') {
      applyEdit(moveNode(policy, pending.id, laneKey, index))
      return
    }
    if (!canDrop(pending.type, laneKey)) return
    const node = newNode(pending.type)
    applyEdit(insertNode(policy, laneKey, index, node))
    setSelId(node.id)
  }

  function onSlotOver(loc: Loc, e: DragEvent) {
    // Returning WITHOUT preventDefault is what makes the browser refuse an invalid
    // drop — a condition dragged over a branch lane simply never becomes a target.
    if (!drag) return
    if (!canDrop(pendingNodeType(policy, drag), parseLoc(loc).laneKey)) return
    e.preventDefault()
    setDropHint((h) => (h === loc ? h : loc))
  }

  function onSlotLeave(loc: Loc) {
    setDropHint((h) => (h === loc ? null : h))
  }

  function onSlotDrop(loc: Loc, e: DragEvent) {
    e.preventDefault()
    if (drag) place(drag, loc)
    endDrag()
  }

  function onSlotClick(loc: Loc) {
    if (!armed) return
    place(armed, loc)
    setArmed(null)
  }

  const selected = selId ? (findNode(policy, selId)?.node ?? null) : null
  const pending = drag ?? armed

  return (
    <>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 20, flexWrap: 'wrap', marginBottom: 18 }}>
        <div style={{ minWidth: 0 }}>
          <button
            type="button"
            onClick={ctx.closePolicy}
            className="pf-btn"
            style={{ display: 'inline-flex', alignItems: 'center', gap: 5, marginBottom: 12, padding: 0, border: 0, background: 'transparent', color: 'var(--fg-3)', fontSize: 12.5, fontFamily: 'var(--font-sans)', cursor: 'pointer' }}
          >
            <span style={{ display: 'inline-flex' }}>{wfBackGlyph}</span> All policies
          </button>

          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            {/* No pf-input: this is a borderless title, not a boxed field. The
                app-layer's `.asc-app input:focus` still paints the standard focus
                ring, which is the app's convention and deliberately amber. */}
            <input
              aria-label="Policy name"
              value={policy.name}
              onChange={(e) => applyEdit(renamePolicy(policy, e.target.value))}
              style={{ flex: '0 0 auto', width: nameWidth(policy.name), minWidth: 120, maxWidth: '100%', border: 0, borderBottom: '1.5px solid transparent', backgroundColor: 'transparent', color: 'var(--fg-1)', fontSize: 24, fontWeight: 600, letterSpacing: '-0.02em', padding: '2px 0' }}
            />
            <PolicyStatusPill status={policy.status} padding="3px 9px" />
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginTop: 9 }}>
            <span className="label">Applies</span>
            <WfSelect label="Applies" hideLabel value={policy.scope} options={SCOPE_OPTIONS} onChange={(v) => applyEdit(rescopePolicy(policy, v))} height={34} width={240} />
          </div>
        </div>

        <div style={{ display: 'flex', gap: 10, flex: 'none' }}>
          <button type="button" onClick={clear} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36, padding: '0 14px', fontSize: 13 }}>
            Clear steps
          </button>
          <button type="button" onClick={save} className="v2-btn pf-btn" style={{ height: 36, padding: '0 16px', fontSize: 13, background: 'var(--action)', color: 'var(--text-on-dark)' }}>
            {saved ? 'Saved' : 'Save & publish'}
          </button>
        </div>
      </div>

      <div style={{ marginBottom: 16, background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 16, padding: '12px 14px 14px' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 16, marginBottom: 10 }}>
          <div className="label">Building blocks</div>
          <div style={{ fontSize: 11, color: 'var(--fg-3)' }}>Drag a block into the flow, or click to append. Drag any step to reorder.</div>
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 10 }}>
          {PALETTE.map((b) => {
            const tone = NODE_TONE[b.type]
            return (
              <button
                key={b.type}
                type="button"
                draggable
                onDragStart={(e) => startDrag({ kind: 'palette', type: b.type }, b.type, 'copy', e)}
                onDragEnd={endDrag}
                onClick={() => append(b.type)}
                // pf-upcard, not pf-btn: pf-btn would force a pill radius on a tile,
                // and pf-upcard already carries exactly the hover this needs
                // (border-color -> var(--action)).
                className="pf-upcard"
                style={{ display: 'flex', alignItems: 'center', gap: 10, textAlign: 'left', padding: '10px 12px', border: '1px solid var(--line-2)', borderRadius: 9, background: 'var(--bg-1)', cursor: 'grab' }}
              >
                <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 7, background: tone.bg, color: tone.color, display: 'grid', placeItems: 'center' }}>
                  <NodeGlyph type={b.type} size={16} />
                </span>
                <span style={{ minWidth: 0 }}>
                  <span style={{ display: 'block', fontSize: 12.5, fontWeight: 600, color: 'var(--fg-1)' }}>{b.name}</span>
                  <span style={{ display: 'block', fontSize: 10.5, color: 'var(--fg-3)', lineHeight: 1.3, marginTop: 1 }}>{b.desc}</span>
                </span>
              </button>
            )
          })}
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(360px, 1fr) 320px', gap: 16, alignItems: 'start' }}>
        <WorkflowCanvas
          policy={policy}
          selId={selId}
          pending={pending}
          canClickPlace={armed !== null}
          armedNodeId={armed && armed.kind === 'move' ? armed.id : null}
          dropHint={dropHint}
          onSelect={selectNode}
          onDelete={removeNode}
          onArmNode={armNode}
          onNodeDragStart={(id, e) => startDrag({ kind: 'move', id }, id, 'move', e)}
          onDragEnd={endDrag}
          onSlotOver={onSlotOver}
          onSlotLeave={onSlotLeave}
          onSlotDrop={onSlotDrop}
          onSlotClick={onSlotClick}
        />

        <div style={{ position: 'sticky', top: 16, display: 'flex', flexDirection: 'column', gap: 16 }}>
          <WorkflowInspector node={selected} onPatch={patchNode} onRemove={removeNode} />
          <WorkflowSimulator policy={policy} sim={sim} onSim={setSim} />
        </div>
      </div>
    </>
  )
}
