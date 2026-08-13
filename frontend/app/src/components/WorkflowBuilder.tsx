// Workflows — the policy builder: header, block palette, canvas, inspector, simulator.
//
// The edit session is LOCAL. `working` is the tree on screen, `server` the last one the
// gateway answered with; each edit composes the next `working` with a pure reducer from
// lib/workflows.ts and touches no network. `Save draft` and `Publish` are the only writes,
// both through a ctx verb, so App.tsx never learns the node tree's shape.
//
// One prototype behaviour is load-bearing and easy to lose: a condition may only sit in
// the root lane. Enforced by `canDrop` in the slot's dragover (no preventDefault ⇒ the
// browser refuses the drop) and again in `place`.

import { useEffect, useState, type DragEvent, type ReactNode } from 'react'

import { ErrorState, Loading, toApiError } from '@invoice-os/api-client'
import { WorkflowCanvas } from './WorkflowCanvas'
import { WorkflowInspector } from './WorkflowInspector'
import { WorkflowSimulator } from './WorkflowSimulator'
import { NodeGlyph, NODE_TONE, PolicyStatusPill, roleOptions, TARGET_OPTIONS, toOptions, WfSelect, wfBackGlyph, type WfPending, pendingNodeType } from './WorkflowParts'
import { delegateCandidates, inhouseNotifyTargets } from '../lib/members'
import { policyInForce } from '../lib/policies'
import { inspectorResolve, resolve } from '../lib/roles'
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
  renamePolicy,
  rescopePolicy,
  updateNode,
  SIM_DEFAULT,
  WF_SCOPE_OPTIONS,
  type Loc,
  type NodePatch,
  type NodeType,
  type Policy,
  type RoleKey,
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

// Publish seals the SAVED draft, so an unsaved tree is not publishable. `aria-describedby`
// target, a module const rather than useId() — one builder renders at a time (the
// InvoiceDetail.tsx:145-148 rationale).
const PUBLISH_BLOCKED_REASON = 'Save your changes first — Publish seals the last saved draft.'
const PUBLISH_BLOCKED_REASON_ID = 'publish-blocked-reason-text'

/**
 * What a publish would displace, off the last LANDED row. Three branches, because
 * `policyInForce` excludes self by design (policies.ts:192-195): without the first one, the
 * policy that IS in force renders 'nothing is in force'. Never off `Policy.status`, which
 * only says the top version is sealed.
 */
function publishConsequence(server: Policy, policies: readonly Policy[]): string {
  if (server.activeVersion !== null) return `Publishing replaces v${server.activeVersion} of this policy, which is in force now. There is no undo.`
  const held = policyInForce(policies, server.id)
  return held ? `Publishing replaces «${held.name}», which is in force now. There is no undo.` : 'No policy is in force. Publishing puts this one in force.'
}

/**
 * The gateway's own sentence for a write it refused, verbatim. Inline rather than shared:
 * WorkflowsView.tsx:151-160, MembersTable.tsx:274-289, MemberDrawer.tsx:365-379 and
 * RoleModal.tsx:292-299 each carry their own copy, so a local one is the convention.
 */
function WriteError({ testId, children }: { testId: string; children: ReactNode }) {
  return (
    <div
      data-testid={testId}
      style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12.5, lineHeight: 1.5, color: 'var(--status-red-text)' }}
    >
      {children}
    </div>
  )
}

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
  // The tree on screen, and the last one the gateway answered with. Seeded from the prop and
  // never re-synced: WorkflowsView keys this component on the policy id, so opening a different
  // policy remounts it. No effect can do that job — the prop's identity churns on every
  // unrelated refetch (App re-derives Policy objects through toPolicy).
  const [working, setWorking] = useState(policy)
  const [server, setServer] = useState(policy)
  const [selId, setSelId] = useState<string | null>(null)
  const [drag, setDrag] = useState<WfPending | null>(null)
  const [armed, setArmed] = useState<WfPending | null>(null)
  const [dropHint, setDropHint] = useState<Loc | null>(null)
  const [saved, setSaved] = useState(false)
  const [sim, setSim] = useState<SimContext>(SIM_DEFAULT)
  // Two slots, never one: a publish failure must not blank a save's reason
  // (WorkflowsView.tsx:71-73's rationale).
  const [saveError, setSaveError] = useState<string | null>(null)
  const [publishError, setPublishError] = useState<string | null>(null)

  // The Saved flash. 1700ms, and re-clicking Save restarts it rather than stacking a
  // second timer — the effect's cleanup cancels the one in flight.
  useEffect(() => {
    if (!saved) return
    const t = window.setTimeout(() => setSaved(false), 1700)
    return () => window.clearTimeout(t)
  }, [saved])

  // After every hook above, so hook order never changes: an unlanded roles fetch must not
  // read as "every role was deleted" (roleOf's fallback, lib/roles.ts:63). Lives here, not
  // WorkflowsView — that forwards ctx whole and reads no role data ([D-BUILDER-GUARD]).
  if (ctx.rolesState === 'loading' || ctx.rolesState === 'idle') return <Loading />
  if (ctx.rolesState === 'error') return ctx.rolesError ? <ErrorState error={ctx.rolesError} onRetry={ctx.refetchRoles} /> : null

  // Local only. Every edit reaches the server through Save draft, never through a keystroke —
  // a per-keystroke PUT composes each request from the last LANDED name and drops the
  // characters in between.
  const applyEdit = setWorking

  // Reference equality, not a structural compare: the server re-mints every step id on each
  // PUT draft (policies.ts:18), so a deep compare of a saved tree against its pre-save self
  // differs on ids alone and reads dirty forever.
  const dirty = working !== server

  async function save() {
    setSaveError(null)
    try {
      // ONE object into BOTH states. Cloning either side (`setServer({ ...saved })`) leaves
      // `dirty` permanently true and Publish permanently dead.
      const landed = await ctx.savePolicy(working)
      setWorking(landed)
      setServer(landed)
      setSaved(true)
      // Every step id was re-minted, so a held selection points at a node that no longer exists.
      setSelId(null)
      setArmed(null)
      setDropHint(null)
    } catch (err) {
      setSaveError(toApiError(err).message)
    }
  }

  // Re-seeds too, or the pill keeps reading DRAFT, `dirty` stays false so a second click
  // re-publishes, and the consequence note flips to a false 'No policy is in force'. Selection
  // survives: publish SEALS, it does not rewrite steps, so no id churns.
  async function publish() {
    setPublishError(null)
    try {
      const published = await ctx.publishPolicy(working.id)
      setWorking(published)
      setServer(published)
    } catch (err) {
      setPublishError(toApiError(err).message)
    }
  }

  function clear() {
    applyEdit(clearSteps(working))
    setSelId(null)
    setArmed(null)
  }

  function selectNode(id: string) {
    setSelId((s) => (s === id ? null : id))
  }

  function removeNode(id: string) {
    applyEdit(deleteNode(working, id))
    setSelId((s) => (s === id ? null : s))
    setArmed((a) => (a && a.kind === 'move' && a.id === id ? null : a))
  }

  function patchNode(id: string, patch: NodePatch) {
    applyEdit(updateNode(working, id, patch))
  }

  /** Palette click appends at the root tail and selects — the drag-free way to add. */
  function append(type: NodeType) {
    const { policy: next, nodeId } = appendNode(working, type)
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
      applyEdit(moveNode(working, pending.id, laneKey, index))
      return
    }
    if (!canDrop(pending.type, laneKey)) return
    const node = newNode(pending.type)
    applyEdit(insertNode(working, laneKey, index, node))
    setSelId(node.id)
  }

  function onSlotOver(loc: Loc, e: DragEvent) {
    // Returning WITHOUT preventDefault is what makes the browser refuse an invalid
    // drop — a condition dragged over a branch lane simply never becomes a target.
    if (!drag) return
    if (!canDrop(pendingNodeType(working, drag), parseLoc(loc).laneKey)) return
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

  const selected = selId ? (findNode(working, selId)?.node ?? null) : null
  const pending = drag ?? armed

  // --- The resolution seam --------------------------------------------------
  //
  // This is the ONLY component in the Workflows surface that imports lib/members.ts. The three
  // panels below receive an already-rendered `Resolved`, so they never learn a member list
  // exists and never learn which mode they are in.
  //
  // BOTH modes resolve — firm workspaces staff real roles too, so the `inhouse ?` gates that
  // used to blank `resolve`/`delegates` are gone. `notifyOptions` keeps its fork, and its firm
  // arm is the TARGET_OPTIONS object itself — the same reference the inspector used to import,
  // not a copy. Do not "symmetrise" that into `toOptions(TARGET_OPTIONS.map(...))`.
  const inhouse = ctx.mode === 'inhouse'

  // Two closures over one resolution: the tone travels with the copy (`warn`) but the wording
  // does not — the canvas says "Musa Danjuma +1", the inspector "Currently: Musa Danjuma".
  const line = (fmt: typeof resolve) => (key: RoleKey) => fmt(ctx.roles, ctx.members, key)

  // Per-node by design: a value stored on the SELECTED node stays selectable, so moving the
  // node off it drops it from the list. `''` covers "nothing selected" and "not a notify
  // node" alike — inhouseNotifyTargets' append guard is falsy-safe.
  const notifyOptions = inhouse
    ? toOptions(inhouseNotifyTargets(selected?.type === 'notify' ? selected.target : ''))
    : TARGET_OPTIONS

  // Same per-node reason as `notifyOptions`: the deleted key the list carries is the SELECTED
  // step's, so moving off that step drops it again.
  const roleChoices = roleOptions(ctx.roles, selected?.type === 'approval' ? selected.role : '')

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
              value={working.name}
              onChange={(e) => applyEdit(renamePolicy(working, e.target.value))}
              style={{ flex: '0 0 auto', width: nameWidth(working.name), minWidth: 120, maxWidth: '100%', border: 0, borderBottom: '1.5px solid transparent', backgroundColor: 'transparent', color: 'var(--fg-1)', fontSize: 24, fontWeight: 600, letterSpacing: '-0.02em', padding: '2px 0' }}
            />
            <PolicyStatusPill status={working.status} padding="3px 9px" />
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginTop: 9 }}>
            <span className="label">Applies</span>
            <WfSelect label="Applies" hideLabel value={working.scope} options={SCOPE_OPTIONS} onChange={(v) => applyEdit(rescopePolicy(working, v))} height={34} width={240} />
          </div>
        </div>

        {/* A column, not a row: each control's reason follows it down when the header wraps
            (WorkflowsView.tsx:86-99). */}
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 8, flex: 'none', maxWidth: 360 }}>
          <div style={{ display: 'flex', gap: 10 }}>
            <button type="button" onClick={clear} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36, padding: '0 14px', fontSize: 13 }}>
              Clear steps
            </button>
            <button type="button" onClick={() => void save()} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36, padding: '0 16px', fontSize: 13 }}>
              {saved ? 'Saved' : 'Save draft'}
            </button>
            {/* Disabled-with-a-reason, never hidden: the visible sibling below is the only layer
                a keyboard user and a text assertion can both reach. No `filter: 'none'` — that
                neutralises .v2-btn-primary's :hover, and this carries neither. */}
            <button
              type="button"
              onClick={() => void publish()}
              disabled={dirty}
              title={dirty ? PUBLISH_BLOCKED_REASON : undefined}
              aria-describedby={dirty ? PUBLISH_BLOCKED_REASON_ID : undefined}
              className="v2-btn pf-btn"
              style={{
                height: 36,
                padding: '0 16px',
                fontSize: 13,
                background: 'var(--action)',
                color: 'var(--text-on-dark)',
                ...(dirty ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
              }}
            >
              Publish
            </button>
          </div>
          {dirty && (
            <div id={PUBLISH_BLOCKED_REASON_ID} data-testid="publish-blocked-reason" style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5, textAlign: 'right' }}>
              {PUBLISH_BLOCKED_REASON}
            </div>
          )}
          {/* Unconditional: it names what Publish would displace, not whether it is clickable.
              Behind the gate it would first appear one click from an act with no undo. */}
          <div data-testid="publish-consequence" style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5, textAlign: 'right' }}>
            {publishConsequence(server, ctx.policies)}
          </div>
          {saveError && <WriteError testId="policy-save-error">{saveError}</WriteError>}
          {publishError && <WriteError testId="policy-publish-error">{publishError}</WriteError>}
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
          policy={working}
          roles={ctx.roles}
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
          resolve={line(resolve)}
        />

        <div style={{ position: 'sticky', top: 16, display: 'flex', flexDirection: 'column', gap: 16 }}>
          <WorkflowInspector
            node={selected}
            onPatch={patchNode}
            onRemove={removeNode}
            resolve={line(inspectorResolve)}
            delegates={delegateCandidates(ctx.members)}
            notifyOptions={notifyOptions}
            roleOptions={roleChoices}
            onManageRoles={() => {
              ctx.setSettingsTab('roles')
              ctx.nav('settings')
            }}
          />
          <WorkflowSimulator policy={working} roles={ctx.roles} sim={sim} onSim={setSim} resolve={line(resolve)} />
        </div>
      </div>
    </>
  )
}
