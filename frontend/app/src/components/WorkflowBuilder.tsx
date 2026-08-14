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
// The second gate. `status === 'published'` is exactly "the top version is sealed"
// (policy_store.go:49-58), and the publish handler selects `WHERE policy_id = $1 AND NOT
// sealed` (policy_store.go:566-575), so publishing again earns a 409. The server's own
// sentence for it, plus the remedy the server does not state.
const PUBLISH_SEALED_REASON = 'This policy has no unpublished changes — edit and save a draft to publish again.'
const PUBLISH_BLOCKED_REASON_ID = 'publish-blocked-reason-text'
// Why the Applies select offers one option. An explanation, not a disabled reason (D-A):
// the control stores exactly what it offers, so there is nothing to paint shut.
const SCOPE_NOT_ROUTED = 'Per-scope routing is not yet available — every policy applies to all invoices.'

/**
 * A wrapper where a bare `disabled` cannot land: `WfSelect` carries no such prop
 * (WorkflowParts.tsx:216), and the canvas's drop and click-to-place handlers hang off divs.
 * `MemberDrawer.tsx:64-71` pre-authorises this trade rather than plumbing a prop through a
 * shared component.
 */
const FIELDSET_RESET = { border: 0, padding: 0, margin: 0, minInlineSize: 0 } as const

/**
 * The ghost variant's disabled paint (MemberDrawer.tsx:145's shape, kept local rather than
 * shared). Inline, so it outranks `.v2-btn-ghost:hover`, which would otherwise repaint a dead
 * control on hover.
 */
const DISABLED_GHOST = { background: 'transparent', borderColor: 'var(--line-1)', color: 'var(--fg-4)', cursor: 'not-allowed' } as const

/**
 * Why Publish cannot run, or null when it can. `dirty` is checked FIRST because saving is the
 * remedy in both states at once: `PUT .../draft` always answers an unsealed top version
 * (policy_store.go:464-468), so the save that clears `dirty` also clears the seal.
 */
function publishBlockedReason(server: Policy, dirty: boolean): string | null {
  if (dirty) return PUBLISH_BLOCKED_REASON
  return server.status === 'published' ? PUBLISH_SEALED_REASON : null
}

/**
 * What a publish would displace, off the last LANDED row — or null when nothing can be
 * published, because promising to replace a version the server would refuse to replace is the
 * false claim §5.3 exists to remove.
 *
 * `Policy.status` answers only "is the top version sealed", which is the whole of the first
 * question and none of the second: WHICH policy governs is `activeVersion`, tenant-wide. Three
 * branches after that, because `policyInForce` excludes self by design (policies.ts:193-195):
 * without the first one, the policy that IS in force renders 'nothing is in force'.
 */
function publishConsequence(server: Policy, policies: readonly Policy[]): string | null {
  if (server.status === 'published') return null
  if (server.activeVersion !== null) return `Publishing replaces v${server.activeVersion} of this policy, which is in force now. There is no undo.`
  const held = policyInForce(policies, server.id)
  return held ? `Publishing replaces «${held.name}», which is in force now. There is no undo.` : 'No policy is in force. Publishing puts this one in force.'
}

/**
 * The gateway's own sentence for a write it refused, verbatim. Inline rather than shared:
 * WorkflowsView.tsx:151-160, MembersTable.tsx:274-289, MemberDrawer.tsx:365-379 and
 * RoleModal.tsx:298-305 each carry their own copy, so a local one is the convention.
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
  // One flag for both verbs (RoleModal.tsx:73's shape). Both re-seed `working` from the answer,
  // so a keystroke typed inside the round trip is overwritten when it lands. It NAMES the verb
  // only so each control's pending label can — off a shared boolean, Publish reads 'Publishing…'
  // inside a save. Still one piece of state: `submitting` is derived, so either verb locks the
  // whole form ('ONE flag covers both verbs').
  const [pendingVerb, setPendingVerb] = useState<'save' | 'publish' | null>(null)
  const submitting = pendingVerb !== null

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
  // characters in between. The flash ends with the edit: 'Saved' next to 'Save your changes
  // first' would keep claiming a landed write for the rest of its 1700ms.
  function applyEdit(next: Policy) {
    setWorking(next)
    setSaved(false)
  }

  // Reference equality, not a structural compare — and NOT for the reason the plan gave: a deep
  // compare does not read dirty forever, because `save()` assigns one object to both states. It
  // errs the other way, reading clean on any edit that round-trips to identical content
  // (clearing an already-empty tree), and it rests on JSON key order besides. Reference
  // equality is false exactly when the two states are the same object. Pinned by 'an edit that
  // changes no content still counts as unsaved'.
  const dirty = working !== server
  // Both off `server`, the last landed row. `working.status` would answer identically — no
  // reducer touches `status` (workflows.ts:16) — but the seal is a server fact, and reading it
  // off the local tree would start being wrong the day a reducer does.
  const blockedReason = publishBlockedReason(server, dirty)
  const consequence = publishConsequence(server, ctx.policies)

  async function save() {
    if (submitting) return
    setPendingVerb('save')
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
    } finally {
      // `finally`, so a refusal re-opens the form over its error slot rather than stranding
      // the user in a dead one.
      setPendingVerb(null)
    }
  }

  // Re-seeds too, and that re-seed is what closes the control: the sealed `status` it lands is
  // what `publishBlockedReason` reads, so a second click is refused here rather than earning
  // the server's 409. Without it the pill also keeps reading DRAFT and the note flips to a
  // false 'No policy is in force'. Selection survives — publish SEALS, it rewrites no step, so
  // no id churns.
  async function publish() {
    if (submitting) return
    setPendingVerb('publish')
    setPublishError(null)
    try {
      const published = await ctx.publishPolicy(working.id)
      setWorking(published)
      setServer(published)
    } catch (err) {
      setPublishError(toApiError(err).message)
    } finally {
      setPendingVerb(null)
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
              disabled={submitting}
              onChange={(e) => applyEdit(renamePolicy(working, e.target.value))}
              style={{ flex: '0 0 auto', width: nameWidth(working.name), minWidth: 120, maxWidth: '100%', border: 0, borderBottom: '1.5px solid transparent', backgroundColor: 'transparent', color: 'var(--fg-1)', fontSize: 24, fontWeight: 600, letterSpacing: '-0.02em', padding: '2px 0' }}
            />
            <PolicyStatusPill status={working.status} padding="3px 9px" />
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginTop: 9 }}>
            <span className="label">Applies</span>
            {/* `display: flex` keeps blockifying `WfSelect`'s inline-block root, which it got for
                free as a direct flex item — a block container would give it a baseline and a
                line-box descender, nudging the select off centre in this row. */}
            <fieldset disabled={submitting} style={{ ...FIELDSET_RESET, display: 'flex' }}>
              <WfSelect label="Applies" hideLabel value={working.scope} options={SCOPE_OPTIONS} onChange={(v) => applyEdit(rescopePolicy(working, v))} height={34} width={240} />
            </fieldset>
          </div>
          {/* A block sibling of the row, not a child of it: the row has no `flexWrap`, so
              inside it this would sit beside the select. Carries its own marginTop — this
              column supplies no `gap`. Unlabelled, or it would collide with getByLabelText('Applies'). */}
          <div data-testid="scope-not-routed" style={{ marginTop: 6, fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5 }}>
            {SCOPE_NOT_ROUTED}
          </div>
        </div>

        {/* A column, not a row: each control's reason follows it down when the header wraps
            (WorkflowsView.tsx:86-99). */}
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 8, flex: 'none', maxWidth: 360 }}>
          <div style={{ display: 'flex', gap: 10 }}>
            <button type="button" onClick={clear} disabled={submitting} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36, padding: '0 14px', fontSize: 13, ...(submitting ? DISABLED_GHOST : null) }}>
              Clear steps
            </button>
            <button type="button" onClick={() => void save()} disabled={submitting} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36, padding: '0 16px', fontSize: 13 }}>
              {pendingVerb === 'save' ? 'Saving…' : saved ? 'Saved' : 'Save draft'}
            </button>
            {/* Disabled-with-a-reason, never hidden: the visible sibling below is the only layer
                a keyboard user and a text assertion can both reach. No `filter: 'none'` — that
                neutralises .v2-btn-primary's :hover, and this carries neither.
                The paint tracks BOTH causes and the reason only ONE: a dead button must not stay
                painted as the action (RoleModal.tsx:370-383), but a transient lock has no reason
                to state, and 'Save your changes first' is untrue mid-publish. */}
            <button
              type="button"
              onClick={() => void publish()}
              disabled={blockedReason !== null || submitting}
              title={blockedReason ?? undefined}
              aria-describedby={blockedReason ? PUBLISH_BLOCKED_REASON_ID : undefined}
              className="v2-btn pf-btn"
              style={{
                height: 36,
                padding: '0 16px',
                fontSize: 13,
                background: 'var(--action)',
                color: 'var(--text-on-dark)',
                ...(blockedReason !== null || submitting ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
              }}
            >
              {pendingVerb === 'publish' ? 'Publishing…' : 'Publish'}
            </button>
          </div>
          {blockedReason && (
            <div id={PUBLISH_BLOCKED_REASON_ID} data-testid="publish-blocked-reason" style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5, textAlign: 'right' }}>
              {blockedReason}
            </div>
          )}
          {/* Rendered whether or not Publish is clickable, so the consequence is read BEFORE
              the save that arms it — but not once the version is sealed, where the only honest
              answer is that there is nothing to publish, which the reason above already gives. */}
          {consequence && (
            <div data-testid="publish-consequence" style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5, textAlign: 'right' }}>
              {consequence}
            </div>
          )}
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
                disabled={submitting}
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
        {/* `pointerEvents` as well as `disabled`: the slot drop and click-to-place handlers hang
            off divs, which a disabled fieldset does not reach. */}
        <fieldset disabled={submitting} style={{ ...FIELDSET_RESET, ...(submitting ? { pointerEvents: 'none' as const } : null) }}>
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
        </fieldset>

        {/* The simulator stays OUTSIDE the fieldset: it writes only local `sim` state, so it
            loses nothing to a re-seed. */}
        <div style={{ position: 'sticky', top: 16, display: 'flex', flexDirection: 'column', gap: 16 }}>
          <fieldset disabled={submitting} style={{ ...FIELDSET_RESET, ...(submitting ? { pointerEvents: 'none' as const } : null) }}>
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
          </fieldset>
          <WorkflowSimulator policy={working} roles={ctx.roles} sim={sim} onSim={setSim} resolve={line(resolve)} />
        </div>
      </div>
    </>
  )
}
