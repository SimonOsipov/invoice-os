import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { APP_PERSONAS, landingBase, signIn, type Persona, type PersonaId, type Session } from './auth'
import { SignIn, SignInLoading } from './components/SignIn'
import { resolveBootSession, saveSession, clearSession, shouldAutoSignIn } from './lib/session'
import { ApiError, gatewayBase, toApiError, useAsync } from '@invoice-os/api-client'
import { makeAuthedFetch } from './lib/authedFetch'
import { buildClients, defaultDraft, resolveActiveClient } from './lib/clients'
import { clientsViewState, listEntities, shouldFetchEntities, type Entity } from './lib/portfolio'
import { fileDraftGate, fileDraftInvoice } from './lib/invoiceDraft'
import { createInvoice, listInvoices } from './lib/invoices'
import { parseReviewHash, reviewHash, reviewQuery } from './lib/reviewBatch'
import { canSubmitMapping, toImportMapping } from './lib/mapping'
import {
  addFiles,
  attachDocumentIds,
  canReadColumnsAll,
  markRunFailed,
  markRunRouted,
  removeFile,
  routeAfterRun,
  runReducer,
  type ImportRun,
  type PickedFile,
  type RunFile,
  type RunRoute,
} from './lib/importRun'
import { canSubmitAllMappings, groupByLayout, groupOfFile, splitOut, type MappingGroup } from './lib/mappingGroups'
import { clearSelection, selectImported, selectMock, type DetailSelection } from './lib/importReport'
import { createImport, makeImportAuth, previewImport, type ImportPreview } from './lib/importApi'
import {
  listMembers,
  membersViewState,
  replaceMember,
  setMembershipStatus,
  toMember,
  type Member,
  type MemberStatus,
  type MembershipWire,
} from './lib/members'
import {
  addSuggested,
  customRulesFor,
  customRulesKey,
  removeCustom,
  toggleCustom,
  type CustomRule,
  type CustomRuleStore,
  type Suggestion,
} from './lib/rules'
import {
  addRole,
  createStaffedRole,
  deleteWorkflowRole,
  listWorkflowRoles,
  removeRole,
  replaceRole,
  rolePatch,
  setRoleMembers,
  updateWorkflowRole,
  type Role,
} from './lib/roles'
import {
  createApprovalPolicy,
  deleteApprovalPolicy,
  listApprovalPolicies,
  publishApprovalPolicy,
  putApprovalPolicyDraft,
} from './lib/policies'
import { removePolicy, replacePolicy, type Policy } from './lib/workflows'
import { flaskGlyph, shieldGlyph15 } from './glyphs'
import { Sidebar } from './components/Sidebar'
import { Header } from './components/Header'
import { DashboardActive } from './components/DashboardActive'
import { DashboardOnboarding } from './components/DashboardOnboarding'
import { InvoicesList } from './components/InvoicesList'
import { CreateFlow } from './components/CreateFlow'
import { InvoiceDetail } from './components/InvoiceDetail'
import { ClientsView } from './components/ClientsView'
import { ValidationView } from './components/ValidationView'
import { RulesView } from './components/RulesView'
import { WorkflowsView } from './components/WorkflowsView'
import { CustomersView } from './components/CustomersView'
import { ReportsView } from './components/ReportsView'
import { SettingsView } from './components/SettingsView'
import { ApprovalsView } from './components/ApprovalsView'
import type {
  Client,
  ConnectorId,
  ConnectorMappings,
  ConnectorsState,
  CreateStep,
  Draft,
  FieldMapRow,
  Mapping,
  Mode,
  PlatformCtx,
  SettingsTab,
  SignedInUser,
  View,
} from './types'

const INITIAL_CONNECTORS: ConnectorsState = { sap: true, quickbooks: true, oracle: false, sage: false, odoo: false, dynamics: false }

// Environment banner under the header, one per state — the environment is always
// stated, never conveyed by absence. `live` cannot render while the LIVE segment is
// disabled (Header.tsx); it is kept as the copy that ships when filing switches on.
export const ENV_BANNER = {
  sandbox: {
    bg: 'var(--status-amber-bg)',
    border: 'var(--status-amber-border)',
    text: 'var(--status-amber-text)',
    icon: flaskGlyph,
    msg: 'Sandbox environment — invoices are validated and stamped against simulated clearance, not filed with NRS. Live filing switches on at accreditation.',
    tag: 'TEST DATA · SIMULATED',
  },
  live: {
    bg: 'var(--action-tint)',
    border: 'var(--teal-200)',
    text: 'var(--action-soft)',
    icon: shieldGlyph15,
    msg: 'Live environment — filing to NRS switches on at accreditation.',
    tag: 'PENDING ACCREDITATION',
  },
} as const

// Exported so a test can assert the shipped default without mounting Workspace, which
// needs a session and a live entities fetch.
export const SANDBOX_DEFAULT = true

// This app shell is ported from the prototype's `class Component extends DCLogic`
// (Platform.dc.html ~L980-1263): `this.state` becomes typed `useState` hooks below,
// and every handler in the "actions" section is ported 1:1 as a plain function.
// Rendered only once signed in (see App): the persona picks the initial workspace mode.
function Workspace({ session, onSignOut }: { session: Session; onSignOut: () => void }) {
  // Workspace type is a property of the authenticated identity, not a user-flippable
  // view: the firm persona gets the firm workspace, the in-house persona the in-house
  // workspace, and there is no in-app switch between them (that would require signing
  // in as the other persona). Under GoTrue (M8) this keys off the token's role/tenant.
  const mode: Mode = session.persona.mode

  const authedFetch = useMemo(() => makeAuthedFetch(session, onSignOut), [session, onSignOut])
  // Same (session, onSignOut) pair, one construction site — the multipart XHR transport
  // cannot drift from the fetch path on auth or the 401 sign-out (importApi.ts D3).
  const importAuth = useMemo(() => makeImportAuth(session, onSignOut), [session, onSignOut])

  // [entity-picker] step 1 of 3: ONE fetch of the tenant's live portfolio entities,
  // shared by the switcher below and ClientsView (via ctx.entities/entitiesState/
  // entitiesError/refetchEntities) — previously each ran its own independent
  // listEntities() call, and the switcher didn't fetch at all (it read the static
  // buildClients() mock roster), so the surfaces could each show a different company
  // list. Same base/shouldFetchEntities/clientsViewState idiom as ClientsView.tsx used
  // individually before (no-gateway build stays at zero network). CreateUpload was a
  // third consumer until [import-upload-unify] deleted its entity <select> — it now
  // mirrors `active` and reads none of these.
  const base = gatewayBase()
  const entitiesAsync = useAsync<Entity[]>(
    () => (base ? listEntities(authedFetch, base).then((r) => r.entities) : Promise.reject(new Error('no gateway configured'))),
    { immediate: shouldFetchEntities(base) },
  )
  const entitiesState = clientsViewState(base, entitiesAsync)
  // Memoized (not a bare `?? []`): a fresh [] literal on every render — while `data`
  // stays null throughout the loading/idle/error/empty window — would give the sync
  // effect below a "changed" dependency on every render and loop forever.
  const entitiesList = useMemo(() => entitiesAsync.data ?? [], [entitiesAsync.data])

  // The switcher roster. Rebuilt whenever the live entity list changes (first load, or a
  // refetch after Add/Edit client on the Clients page) — a full rebuild, not a merge, and
  // since INVCR-01-03 this effect is its ONLY writer: the mock approve() that used to
  // prepend a locally-built invoice to a client's list is gone, so no creation path writes
  // active.invoices any more. Those generated SAMPLE rows never rendered anywhere live
  // anyway — InvoiceDetail's mock branch is fully retired (M5-09-04); they only ever fed
  // CustomersView/ReportsView, themselves still-mock surfaces this plan's next
  // step migrates off active.invoices.
  const [clients, setClients] = useState<Client[]>([])
  useEffect(() => {
    setClients(buildClients(entitiesList))
  }, [entitiesList])

  // [entity-picker] keystone: the active selection is a real entity id, never an index
  // into a mock array. null until the user (or the fallback below) picks one.
  const [activeEntityId, setActiveEntityId] = useState<string | null>(null)

  // task-304 (INVCR-01-19) deleted the old `if (mode === 'inhouse') return
  // inhouseClient(...)` special case here: it never even consulted `clients` (built from
  // the LIVE entitiesList), so seeding in-house a real business_entities row (AC-1,
  // db/seed.dev.sql) alone fixed nothing — this memo still had to stop hardcoding the
  // answer. resolveActiveClient (lib/clients.ts) is now the ONE resolution path for both
  // workspace modes ([in-house-degenerate-case], AC-2) — every one of the ~15 places
  // reading ctx.active needs SOMETHING defined, never `undefined`, which is exactly what
  // that function is total over: an explicit switcher pick, the server's own first row
  // with nothing chosen, or the shared emptyClient() placeholder with genuinely nothing
  // to resolve (AC-3's bootstrap window, either persona).
  const active: Client = useMemo(
    () => resolveActiveClient(clients, activeEntityId),
    [clients, activeEntityId],
  )

  // The REAL portfolio entity behind `active`, resolved once here rather than re-`find`ing
  // it at each consumer ([gate-on-the-resolved-entity]). Two things depend on it being the
  // resolved object and not `active.entityId`:
  //
  //  - the filing gate. `active` is rebuilt from `entitiesList` by an effect, so between a
  //    refetch landing and that rebuild the id can name an entity not (yet) in the list.
  //    Gating on the id there arms the button and the click does nothing — the exact
  //    silent-no-op shape [inhouse-can-start] exists to forbid.
  //  - draftToCreateRequest, which takes `Entity` and never `Client`: buildClientForEntity
  //    does `tin: e.tin ?? '—'`, so a TIN-less entity is unrepresentable through Client and
  //    would cross the wire as the literal em-dash.
  //
  // null for a workspace (either persona) with no entity resolved yet, for the
  // emptyClient() placeholder, and for the whole loading/error/no-gateway window — all
  // three are the same honest "nothing to file against", so they need no separate copy.
  const activeEntity = useMemo(
    () => entitiesList.find((e) => e.id === active.entityId) ?? null,
    [entitiesList, active.entityId],
  )

  // --- `#review/<uuid>` deep link (INVCR-01-09, AC-1 / D4) — the READ half ---------
  //
  // ONE lazy parse of `window.location.hash`, here in Workspace rather than App: App
  // renders <Workspace> only once `session != null`, so a read up there would run before
  // the session exists on the deep-link hand-off path. The three initializers below are
  // DERIVED from this single value — there is deliberately no boot effect that navigates,
  // because StrictMode double-invokes effects and a navigating one would fire twice.
  //
  // The hash survives the landing hand-off: App's `?persona=` strip (below) rebuilds the
  // URL as `pathname + window.location.hash`, preserving it.
  //
  // There is NO `hashchange` listener, by decision. D4 asks that the screen survive a
  // RELOAD, which it does. In-SPA history navigation does not exist for any surface in
  // this app (replaceState everywhere, no router), and a listener for this one screen
  // would make the app half-routed with a divergence the mirror effect below cannot
  // reconcile (hash hand-deleted while the review screen is still mounted). Recorded
  // limitation: pasting a review hash into an already-open tab's address bar does not
  // navigate until reload.
  const [bootBatchIds] = useState<string[]>(() => parseReviewHash(window.location.hash) ?? [])
  const [view, setView] = useState<View>(bootBatchIds.length > 0 ? 'create' : 'dashboard')
  const [draft, setDraft] = useState<Draft>(() => defaultDraft(active))
  const [createStep, setCreateStep] = useState<CreateStep>(bootBatchIds.length > 0 ? 'review' : 'form')
  // Widened from a single `reviewBatchId` (BULK-01-05, task-308): a run's `review`
  // route (lib/importRun's routeAfterRun) carries every batch id created in the run.
  // `reviewHash`/`parseReviewHash` are widened in place (BULK-01-06), so every id now
  // round-trips through the URL, not just the first.
  const [reviewBatchIds, setReviewBatchIds] = useState<string[]>(bootBatchIds)
  // Files sharing an identical column layout are mapped ONCE (BULK-01-04, Core AC 3,
  // decision [shared-mapping-shown]) — readAllColumns (below) previews every picked file
  // and lib/mappingGroups.ts's groupByLayout buckets them, preserving first-appearance
  // order. `groupIndex` is which group the mapping step currently shows; `mapping`/
  // `preview` below are DERIVED reads of the active group, not their own state any more
  // — assign/unmap (below) write back into groups[groupIndex].mapping instead.
  const [groups, setGroups] = useState<MappingGroup[]>([])
  const [groupIndex, setGroupIndex] = useState(0)
  const [armedField, setArmedField] = useState<string | null>(null)
  const [dragField, setDragField] = useState<string | null>(null)
  // ONE atom for what the detail view renders, never two loose fields
  // ([detail-target-exclusive], debate F6). Written ONLY through the three total
  // constructors below, so every write sets both members and "forgot to clear the
  // counterpart" is a type error rather than a discipline. Two independent fields would
  // mean one click-through hijacks the detail view for the rest of the session: every
  // later InvoicesList click would set selectedId while a stale importedInvoiceId kept
  // the placeholder on screen. Do NOT reintroduce a `setSelectedId`, and do NOT write
  // this state with an inline object literal — go through a constructor.
  const [detailSel, setDetailSel] = useState<DetailSelection>(clearSelection())
  // Header search box's committed term (BUG-01-05) -- InvoicesList reads this as `q`.
  const [invoiceQuery, setInvoiceQuery] = useState('')
  const [switcherOpen, setSwitcherOpen] = useState(false)
  // Every deployment is a sandbox today, so this is a client-side constant, not a
  // posture value fetched from the server.
  const [sandbox, setSandbox] = useState(SANDBOX_DEFAULT)
  // Settings opens on Members. This literal is the ONLY thing that decides which tab
  // opens — SETTINGS_TABS' array order decides only which renders first — so the two
  // have to be changed together.
  const [settingsTab, setSettingsTab_] = useState<SettingsTab>('members')
  const [connectors, setConnectors] = useState<ConnectorsState>(INITIAL_CONNECTORS)
  // Field-mapping edits live at the workspace, not inside SettingsView, so a saved
  // mapping survives navigating away from Settings and back.
  const [connectorMappings, setConnectorMappings] = useState<ConnectorMappings>({})
  // Custom validation rules, PER CLIENT (lib/rules.ts). Held here rather than in
  // RulesView so a client's set survives navigating away and back, and so switching
  // company genuinely swaps the set instead of carrying one client's rules over to
  // the next. A client absent from the store has never been edited and reads the
  // seed set; only edited clients get an entry.
  const [customRuleStore, setCustomRuleStore] = useState<CustomRuleStore>({})
  const [openRuleKey, setOpenRuleKey] = useState<string | null>(null)
  const rulesKey = customRulesKey(active.entityId)
  const customRules = customRulesFor(customRuleStore, rulesKey)
  // The tenant's approval policies — the `membersAsync` idiom below, verbatim except for
  // the mirror's guard. Per TENANT, so switching company does not swap the set.
  const policiesAsync = useAsync<Policy[]>(
    () => (base ? listApprovalPolicies(authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null },
  )
  const policiesState = membersViewState(base, policiesAsync.status)
  // Guarded on the STATUS, unlike the two mirrors below: `start` nulls `data` and three
  // write verbs refetch from a populated screen, so an ungated write blanks the list for
  // that round trip. Truthiness alone cannot be the gate — `success` nulls `data` on the
  // empty branch too, so a tenant that deleted its last policy would keep a ghost forever.
  const [policies, setPolicies] = useState<Policy[]>([])
  useEffect(() => {
    if (policiesAsync.status === 'ready' || policiesAsync.status === 'empty') setPolicies(policiesAsync.data ?? [])
  }, [policiesAsync.status, policiesAsync.data])
  const [editingPolicyId, setEditingPolicyId] = useState<string | null>(null)
  // The tenant's membership directory — ONE fetch, shared by the Members tab, the Roles
  // tab and the Workflows builder. The `entitiesAsync` idiom above: no mode key, because
  // one persona is one tenant and the server answers for that tenant alone.
  const membersAsync = useAsync<Member[]>(
    () =>
      base
        ? listMembers(authedFetch, base).then((ws) => ws.map((w) => toMember(w, session.persona.subject)))
        : Promise.reject(new Error('no gateway configured')),
    { immediate: base != null },
  )
  const membersState = membersViewState(base, membersAsync.status)
  // A local mirror rebuilt from the async data, the `entitiesList → setClients` shape:
  // `asyncReducer`'s `start` nulls `data`, so refetching after a status write would blank
  // the whole roster for the round trip. The write patches this instead; any later refetch
  // overwrites it wholesale, so the fetch stays authoritative.
  const [members, setMembers] = useState<Member[]>([])
  useEffect(() => {
    setMembers(membersAsync.data ?? [])
  }, [membersAsync.data])
  // The approval seats a policy's steps point at — the `membersAsync` idiom immediately
  // above, verbatim: ONE fetch, shared by the Roles tab and the Workflows builder.
  const rolesAsync = useAsync<Role[]>(
    () => (base ? listWorkflowRoles(authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: base != null },
  )
  const rolesState = membersViewState(base, rolesAsync.status)
  const [roles, setRoles] = useState<Role[]>([])
  useEffect(() => {
    setRoles(rolesAsync.data ?? [])
  }, [rolesAsync.data])
  // Multi-invoice import path (M4-08-04). `entityId` is a REAL portfolio entity id.
  // [entity-picker] step 3 of 3: DEFAULTS to `active.entityId` (resetImport, below) —
  // the user already answered "which company" via the switcher, so the import wizard
  // does not ask again. [import-upload-unify] made that the ONLY source: CreateUpload's
  // own <select> is gone, so this is now a pure mirror of `active` and the two can no
  // longer disagree (that dropdown also listed archived entities the switcher hides,
  // so it could file a LIVE import under a company the switcher would never offer).
  // Still never carried over from a PREVIOUS run in the same session — resetImport
  // reseeds from `active` every time, not from whatever this state last held.
  const [entityId, setEntityId] = useState<string | null>(null)
  // The run's ordered file selection (BULK-01-03, Core AC 1). `filesRefusal` holds the
  // most recent addPickedFiles call's refusal text (lib/importRun's capRefusal), or
  // null.
  const [pickedFiles, setPickedFiles] = useState<PickedFile[]>([])
  const [filesRefusal, setFilesRefusal] = useState<string | null>(null)
  // The active group's preview/mapping (BULK-01-04) — DERIVED reads, never their own
  // state. Both are null whenever `groups` is empty or `groupIndex` is out of range
  // (before readAllColumns has ever resolved, or after a resetImport).
  const activeGroup = groups[groupIndex] ?? null
  const preview = activeGroup?.preview ?? null
  const mapping = activeGroup?.mapping ?? null
  // The sequential run's whole state (BULK-01-05, task-308) — REPLACES the old
  // single-file `uploadPhase` state. `startRun` (below) is the sole writer, via
  // lib/importRun's runReducer; `run` is tracked in a local variable inside that
  // function's own async loop (a `setState` call does not resolve synchronously) and
  // mirrored here purely for rendering — same discipline readAllColumns' local
  // `previewed` array already uses. `'idle'` both before a run starts and once
  // applyRoute has drained a finished run into `reviewBatchIds`/an opened invoice;
  // `'failed'` (not `'finished'` — markRunFailed flips it) survives ONLY across a
  // `none` route (AC #9).
  const [run, setRun] = useState<ImportRun>({ files: [], cursor: 0, status: 'idle' })
  const [importError, setImportError] = useState<ApiError | null>(null)

  // The manual form's one round trip (INVCR-01-03). `filing` renders the disabled/spinner
  // frame; correctness against a double-click belongs to reqInFlight below, not to this.
  const [filing, setFiling] = useState(false)
  const [filingError, setFilingError] = useState<ApiError | null>(null)

  // Re-entrancy guard for the THREE server round trips this workspace fires. A ref, not
  // state: React batches state updates, so a fast double-click can fire the handler twice
  // before a `disabled` prop re-renders — and for startImport that would create the SAME
  // import twice, i.e. duplicate invoices, while for fileDraft it would persist TWO
  // separately-submittable invoices (createRequest carries no idempotency key; that field
  // is batch-submit-only). readColumns (upload step), startImport (mapping step) and
  // fileDraft (form step) live on different steps and can never overlap, so one flag covers
  // all three. A ref also cannot get stuck the way a component-local flag would: the
  // wizard components never observe the rejection that would clear it, since errors come
  // back through ctx.importError / ctx.filingError.
  const reqInFlight = useRef(false)

  // resetImport() snapshots active.entityId at openCreate so a company switch cannot
  // silently retarget an import already in flight. But that snapshot can be taken
  // BEFORE the entities fetch resolves — click "New invoice" fast enough (cold fleet,
  // slow link) and `active` is still emptyClient(), so the snapshot is null. Reading
  // columns no longer depends on it (the preview endpoint takes the file alone), but
  // FILING does: a stale-null snapshot would carry a firm OR in-house user all the way
  // to the Map step and then refuse the commit, even though their entities had long
  // since landed.
  //
  // Before [import-upload-unify] the entity <select> was the escape hatch; there is no
  // longer one, so re-seed on exactly that null -> resolved transition, below. This
  // comment used to describe that re-seed without the effect existing at all — the gap
  // is what a deployed topology run caught ([inhouse-can-file] LIVE, dev-env.yml run
  // 30667013148): GET .../portfolio/v1/entities started at (test-relative) ~45214ms and
  // did not resolve until ~45567ms, while "New invoice" was clicked at ~45325ms — squarely
  // inside that window, on this run's real network timing. The click's resetImport()
  // snapshot therefore captured null, `active.entityId` resolved to a real id ~240ms
  // later while CreateFlow was still mid readAllColumns (still on 'upload'), and with no
  // re-seed the wizard's own `entityId` stayed null for the rest of the session even
  // though the live header already showed the resolved company — CreateMapping's
  // `canFile = entityId !== null` then refused a workspace that plainly had an entity.
  //
  // Confined to the upload step: past it the columns are already read against the
  // snapshot, and moving the target then is the retarget resetImport exists to prevent.
  // Gated on `entityId === null` (not a bare active.entityId mirror) so it can only ever
  // fill a stale-null snapshot in — it never overwrites an already-resolved snapshot if
  // `active` itself changes mid-upload-step.
  useEffect(() => {
    if (createStep === 'upload' && entityId === null && active.entityId !== null) {
      setEntityId(active.entityId)
    }
  }, [createStep, entityId, active.entityId])
  // --- `#review/<uuid>` deep link (INVCR-01-09, AC-1 / D4) — the WRITE half ---------
  //
  // ONE writer, mirroring state to the URL, rather than a `location.hash = …` at every
  // exit from review. Every one of Finish, `← Invoices`, `Choose another file`, `Enter
  // one invoice instead`, sidebar nav, switchClient and openCreate would otherwise each
  // have to remember to clear the hash — and `closeCreate` in particular does not, so a
  // reload after Finish would bounce straight back into the review screen. Mirroring the
  // state makes "clear it" structural instead of remembered; it is the same idiom as
  // App's session mirror below.
  //
  // `replaceState`, never `location.hash = …`: assigning adds a history entry the back
  // button bounces off, and assigning `''` leaves a bare `#` in the URL. The rebuilt URL
  // keeps `search` — App's `?persona=` strip is the one writer that removes it, and it
  // preserves the hash in turn, so the two effects compose in either order.
  //
  // At boot this rewrites the identical URL (the three initializers above already agree
  // with the hash it parses), so the first pass is a no-op rather than a navigation.
  useEffect(() => {
    // BULK-01-06 widens the hash to carry every id in the run.
    const h = reviewHash(view, createStep, reviewBatchIds)
    window.history.replaceState(null, '', window.location.pathname + window.location.search + (h ?? ''))
    // `reviewBatchIds.join(',')`, never the array reference itself: a fresh array every
    // render would otherwise re-run this effect on every render forever.
  }, [view, createStep, reviewBatchIds.join(',')])

  function nav(id: View) {
    setView(id)
    setSwitcherOpen(false)
  }

  function toggleSwitcher() {
    setSwitcherOpen((o) => !o)
  }

  function switchClient(id: string) {
    setActiveEntityId(id)
    setView('dashboard')
    setDetailSel(clearSelection())
    setSwitcherOpen(false)
    setDraft(defaultDraft(clients.find((c) => c.entityId === id) ?? active))
    setCreateStep('form')
    // A batch belongs to ONE entity. Leaving this set would keep the review screen's
    // deep-link id pointing at the company just left — and the mirror effect above would
    // keep writing its hash into the URL from the incoming company's dashboard.
    setReviewBatchIds([])
    // A failed filing's message named the company just left. `filing` is deliberately NOT
    // cleared: a request already in flight is still in flight, and it will land on the
    // invoice it was fired for — under the PREVIOUS company. Leaving the button disabled
    // until it settles is the honest frame; cancelling it is not something this flow does.
    setFilingError(null)
    // Custom rules are per client, so the incoming client has a different set — a
    // drawer left open would keep describing a rule from the company just left.
    setOpenRuleKey(null)
    // Policies are per tenant, so the SET is unchanged — but the switch lands on the
    // dashboard, and leaving this set means the next visit to Workflows reopens the
    // builder mid-edit instead of the policy list the user asked for.
    setEditingPolicyId(null)
  }

  function openCreate() {
    setView('create')
    setCreateStep('upload')
    setDraft(defaultDraft(active))
    setFilingError(null)
    setSwitcherOpen(false)
    resetImport()
  }

  // Every piece of import state is per-run: a second import must not inherit the first
  // one's preview, progress, error or report. `entityId` defaults to `active.entityId`
  // ([entity-picker] step 3 of 3) rather than blank — the user already picked this
  // company via the switcher. Since [import-upload-unify] there is no in-wizard way to
  // diverge from it, but the reseed still matters: every open takes the CURRENT
  // `active`, so switching company between two runs cannot leave the second run filing
  // under the first run's entity.
  function resetImport() {
    setEntityId(active.entityId)
    setPickedFiles([])
    setFilesRefusal(null)
    setGroups([])
    setGroupIndex(0)
    setRun({ files: [], cursor: 0, status: 'idle' })
    setImportError(null)
    // Per-run, sitting exactly where `setReport(null)` sat: a second import must not
    // inherit the first one's batch, and `Import a corrected file` routes back through
    // here specifically so a deep-link arrival — which carries no picked files, preview
    // or mapping at all — cannot open the upload step on another run's state.
    setReviewBatchIds([])
  }

  function closeCreate() {
    setView('invoices')
  }

  function updateDraft<K extends keyof Draft>(field: K, value: Draft[K]) {
    setDraft((d) => ({ ...d, [field]: value }))
  }

  function updateItem(i: number, field: 'qty' | 'price', val: string) {
    setDraft((d) => ({
      ...d,
      items: d.items.map((it, idx) => (idx === i ? { ...it, [field]: val === '' ? 0 : Number(val) } : it)),
    }))
  }

  // Separate from updateItem: a description is a string kept verbatim, while qty/price are
  // numbers coerced off the input's text. One writer switching on the field would have to
  // branch on the value's type, which is how a description ends up as 0.
  function updateItemDesc(i: number, desc: string) {
    setDraft((d) => ({
      ...d,
      items: d.items.map((it, idx) => (idx === i ? { ...it, desc } : it)),
    }))
  }

  // A blank line, not a copy of the last one: qty 1 is the only value that cannot silently
  // multiply a price the operator has not entered yet.
  function addItem() {
    setDraft((d) => ({ ...d, items: [...d.items, { desc: '', qty: 1, price: 0 }] }))
  }

  // The form disables its remove control at one remaining line, so this never empties the
  // list in practice; the filter is still written to be total rather than assuming that.
  function removeItem(i: number) {
    setDraft((d) => (d.items.length <= 1 ? d : { ...d, items: d.items.filter((_it, idx) => idx !== i) }))
  }

  // Appends onto the run's file selection (BULK-01-03) via lib/importRun's addFiles —
  // capped at MAX_RUN_FILES, never silently truncating. `filesRefusal` carries the
  // refusal text CreateUpload renders verbatim whenever the cap drops any incoming file;
  // a rejected-extension file still lands in `pickedFiles` and the per-file note
  // explains why (that gate is canReadColumnsAll, not selection).
  function addPickedFiles(files: File[]) {
    const result = addFiles(pickedFiles, files)
    setPickedFiles(result.files)
    setFilesRefusal(result.refusal)
  }

  // Removes one entry by id, preserving the order of the rest (lib/importRun's
  // removeFile). An unknown id is a no-op. Also clears `filesRefusal`: that message
  // names files that were NOT added past the five-file cap, and removing an
  // already-added file can never be what it is still talking about — left uncleared, it
  // used to linger on screen after the very removal that (partially) makes room for more
  // (BULK-01-03 QA gap, dfc3a19).
  function removePickedFile(id: string) {
    setPickedFiles((cur) => removeFile(cur, id))
    setFilesRefusal(null)
  }

  // Previews every picked file ONE AT A TIME (BULK-01-04, Core AC 3), then buckets them
  // into `groups` via lib/mappingGroups.ts's groupByLayout. Renamed from the single-file
  // `readColumns` this replaces.
  //
  // reqInFlight is taken ONCE here, not once per file: the old single-shot readColumns
  // took the guard and released it in one `.then/.finally` pair around ONE request, and
  // looping by calling that function N times would deadlock on this very ref (each call
  // early-returns on `if (reqInFlight.current) return`). This is one async function that
  // takes the guard once at the top and releases it once in a `finally` around the whole
  // per-file loop.
  function readAllColumns() {
    const base = gatewayBase()
    // base == null is the no-gateway build: zero network, and the button is disabled
    // too — this is the second of the two guards, not the only one. No entityId clause:
    // preview neither sends nor needs one (see canReadColumns, wrapped here by
    // canReadColumnsAll — the same predicate CreateUpload's own gate reads).
    if (base == null || !canReadColumnsAll(pickedFiles)) return
    if (reqInFlight.current) return
    reqInFlight.current = true
    setImportError(null)

    // Snapshotted once, same discipline as the old readColumns closing over a fixed
    // `importFile`: this run previews exactly the files picked at click time.
    const files = pickedFiles

    void (async () => {
      try {
        const previewed: { fileId: string; preview: ImportPreview }[] = []
        for (const pf of files) {
          let result: ImportPreview
          try {
            result = await previewImport(importAuth, base, pf.file)
          } catch (err) {
            // AC6: names the failing file, stays on 'upload' — never silently drops it,
            // never carries it into the run. The already-previewed files ahead of it in
            // the loop are discarded too (no partial groups), so a retry click re-reads
            // every file fresh, matching the old single-file all-or-nothing behaviour.
            const wrapped = toApiError(err)
            setImportError(new ApiError(wrapped.kind, `${pf.file.name}: ${wrapped.message}`, wrapped.status, wrapped.body))
            return
          }
          previewed.push({ fileId: pf.id, preview: result })
        }
        // Functional form, not the `files` snapshot: a file removed while the previews
        // were in flight must stay removed, not be resurrected by a stale array.
        setPickedFiles((cur) => attachDocumentIds(cur, previewed))
        setGroups(groupByLayout(previewed))
        setGroupIndex(0)
        setCreateStep('mapping')
        // A fresh mapping cycle must not carry a PREVIOUS run's leftovers onto it.
        // Reachable now in a way it never was before task-308's QA correction: a
        // `none`-routed run lands on `run.status: 'failed'` rather than resetting (AC
        // #9), which is what makes ctx.backToImport's "← Back to import" button
        // usable again from that landing — and from there the operator can change the
        // file selection entirely and re-preview. Without this, CreateMapping's
        // failures footer (runFailures(run)) would keep naming files from the run
        // that already finished, superimposed on a completely different group/mapping
        // this preview just built. `startRun` itself never needs this: it always
        // dispatches its own 'start' action against a fresh `{status:'idle'}` seed
        // (below), overwriting whatever `run` currently holds regardless.
        setRun({ files: [], cursor: 0, status: 'idle' })
      } finally {
        reqInFlight.current = false
      }
    })()
  }

  // Where a finished RUN lands (INVCR-01-09, BULK-01-05, Core AC 8). `review` and
  // `rejected` batches are handled IDENTICALLY on purpose: both are the review step,
  // and which SURFACE it renders there is decided by reviewShellState(batch) off the
  // batch GET alone — never by this route's `kind`.
  //
  // `review` moves `run` to idle via lib/importRun.ts's markRunRouted (BULK-01-07
  // wiring correction) rather than the literal `{files:[],cursor:0,status:'idle'}`
  // reset it used before — `files`/`cursor` now survive untouched, same discipline as
  // markRunFailed below, because ReviewBatch's `filesStrip(batches, ctx.run)` still
  // needs to read a run-only failure off THIS run: a file whose upload request itself
  // failed before any batch ever existed carries no batchId, so nothing in `batches`
  // represents it either (Core AC 5). The literal reset used to wipe that the instant
  // the run finished, so that failure could never reach the review screen at all.
  // `status` still ends at 'idle' — the same value the old reset already produced —
  // so runIsActive(run) still goes false and CreateFlow's body-swap gate still lets
  // 'review' render in place of ImportProgress.
  //
  // `single` still resets `run` to a literal empty idle state, deliberately NOT given
  // the same treatment: openImportedInvoice sets `view` to 'detail', unmounting the
  // whole create flow (ImportProgress, ReviewBatch, everything that reads `run`) —
  // and routeAfterRun only ever returns 'single' for a one-file run whose one file
  // IMPORTED (BULK-05-8's run-size gate), so there is no failure this route could
  // ever be dropping.
  //
  // `none` (every file in the run failed at the request level) does NOT reset `run`
  // the same way — lib/importRun.ts's markRunFailed (BULK-01-05 QA correction,
  // task-308) flips ONLY `status`, to 'failed'. `files`/`cursor` survive untouched, so
  // runFailures(run) still returns every failure. runIsActive(run) reads 'failed' the
  // same as 'idle' (false), so CreateFlow renders CreateMapping again instead of the
  // dead-end ImportProgress card (no buttons at all) — the operator lands back on an
  // INTERACTIVE mapping step, per-file failures visible there (AC #9), free to fix
  // whatever the error named and start another run from the still-intact selection and
  // mappings, or back out entirely via restartImport/resetImport.
  function applyRoute(route: RunRoute) {
    if (route.kind === 'single') {
      openImportedInvoice(route.invoiceId)
      setRun({ files: [], cursor: 0, status: 'idle' })
      return
    }
    if (route.kind === 'review') {
      setReviewBatchIds(route.batchIds)
      setCreateStep('review')
      setRun(markRunRouted)
      return
    }
    setRun(markRunFailed)
  }

  // The sequential run (BULK-01-05, task-308): one createImport in flight at a time,
  // each file its own outcome, continuation through failures ([partial-success-kept]).
  // Replaces the single-file startImport.
  //
  // `[sequential-not-parallel]`: each file is AWAITED before the next one starts —
  // NEVER Promise.all, never fire-and-forget. internal/importer/service.go's
  // ExistingNumbers duplicate precheck is a synchronous, against-stored DB read per
  // file, committed before Import() returns — only a sequentially-awaited next file's
  // precheck actually sees the previous file's committed rows; running them
  // concurrently would race straight past it into a raw 23505.
  //
  // `entityId` is read ONCE here and passed to EVERY file's createImport call
  // ([entity-picker] step 3 of 3, extended to a run — Core AC 6: a run never spans
  // entities and never asks again). There is no per-file entity anywhere in this
  // function (AC #7).
  //
  // reqInFlight is taken ONCE for the whole run and released ONCE in a `finally` after
  // the loop (and the routing it decides) exits — same shape as readAllColumns above,
  // extended from "one request" to "the whole sequential run" (AC #8).
  function startRun() {
    const base = gatewayBase()
    if (base == null || !entityId || !canSubmitAllMappings(groups)) return
    if (reqInFlight.current) return
    reqInFlight.current = true
    setImportError(null)

    // Snapshotted once, same discipline as readAllColumns' `files` snapshot: this run
    // sends exactly the files and group mappings confirmed at click time.
    const filesSnapshot = pickedFiles
    const groupsSnapshot = groups
    const runFiles: RunFile[] = filesSnapshot.map((pf) => ({
      id: pf.id,
      name: pf.file.name,
      groupId: groupOfFile(groupsSnapshot, pf.id)?.id ?? '',
      outcome: { kind: 'pending' },
    }))

    // Tracked in a local variable, not read back off React state mid-loop: `setRun`
    // below is for RENDERING only. The loop's own control flow (which file is next,
    // whether the run just finished) reads THIS variable — same discipline
    // readAllColumns' local `previewed` array uses, for the same reason (a `setState`
    // call does not resolve synchronously).
    let localRun: ImportRun = runReducer({ files: [], cursor: 0, status: 'idle' }, { type: 'start', files: runFiles })
    setRun(localRun)

    void (async () => {
      try {
        for (const rf of runFiles) {
          const documentId = filesSnapshot.find((pf) => pf.id === rf.id)?.documentId
          const group = groupOfFile(groupsSnapshot, rf.id)
          if (!documentId || !group) {
            // Should never happen: every picked file is bucketed into a group AND given
            // its stored document's id by readAllColumns before the Map step (and this
            // function) is reachable at all. Recorded as a failed outcome rather than
            // throwing, so the loop keeps going ([partial-success-kept]) instead of
            // leaving the run stuck 'running' forever with reqInFlight never released.
            localRun = runReducer(localRun, {
              type: 'settled',
              outcome: {
                kind: 'failed',
                message: documentId
                  ? 'no mapping found for this file'
                  : 'this file was not uploaded — read its columns again',
              },
            })
            setRun(localRun)
            continue
          }
          try {
            // Each file is sent with its OWN group's toImportMapping(mapping) (AC #7)
            // — never a second, per-file entity field.
            const report = await createImport(
              importAuth,
              base,
              { documentId, entityId, mapping: toImportMapping(group.mapping) },
              (phase) => {
                localRun = runReducer(localRun, { type: 'phase', phase })
                setRun(localRun)
              },
            )
            localRun = runReducer(localRun, {
              type: 'settled',
              outcome: { kind: 'imported', batchId: report.id, report },
            })
          } catch (err) {
            // A REQUEST-level failure (network/http/malformed) — never ends the run
            // early; the loop moves on to the next file regardless
            // ([partial-success-kept]).
            localRun = runReducer(localRun, {
              type: 'settled',
              outcome: { kind: 'failed', message: toApiError(err).message },
            })
          }
          setRun(localRun)
        }

        // Core AC 8's single-invoice shortcut, extended to a run: fired ONLY when the
        // run holds exactly one file that came back with exactly one ready invoice —
        // any other shape discards the answer in routeAfterRun anyway, so asking is
        // pure cost.
        let resolvedInvoiceId: string | null = null
        const onlyFile = localRun.files.length === 1 ? localRun.files[0] : null
        if (onlyFile && onlyFile.outcome.kind === 'imported' && onlyFile.outcome.report.status === 'completed' && onlyFile.outcome.report.ready_invoices === 1) {
          const soleReport = onlyFile.outcome.report
          resolvedInvoiceId = await listInvoices(authedFetch, base, reviewQuery(soleReport.id, 'all', { limit: 1 })).then(
            (r) => r.invoices[0]?.id ?? null,
            // DEGRADES to null, never setImportError. The import SUCCEEDED and the
            // rows are in the ledger; an error banner here would say "failed" about
            // data that landed. routeAfterRun turns a null id into the review
            // surface, which is the honest fallback — the batch is real either way.
            () => null,
          )
        }
        applyRoute(routeAfterRun(localRun, resolvedInvoiceId))
      } finally {
        reqInFlight.current = false
      }
    })()
  }

  function armField(k: string) {
    setArmedField((a) => (a === k ? null : k))
  }

  function setDrag(k: string) {
    setDragField(k)
  }

  function endDrag() {
    setDragField(null)
  }

  // A field lives on exactly one column: assigning clears whatever else held
  // this column, so duplicate mappings are structurally impossible.
  //
  // Writes into groups[groupIndex].mapping (BULK-01-04) rather than a standalone
  // setMapping — `mapping` is now a DERIVED read of the active group, so the only place
  // a placement can live is back on that group. Every other group's mapping is
  // untouched, matching CreateMapping.tsx's own guard ([layout-signature-is-ordered]):
  // this screen renders ONE group at a time.
  function assign(field: string, header: string) {
    setGroups((gs) =>
      gs.map((g, i) => {
        if (i !== groupIndex) return g
        const next: Mapping = { ...g.mapping }
        Object.keys(next).forEach((k) => {
          if (next[k] === header) next[k] = null
        })
        next[field] = header
        return { ...g, mapping: next }
      }),
    )
    setArmedField(null)
    setDragField(null)
  }

  function dropOn(header: string) {
    if (dragField) assign(dragField, header)
    else setDragField(null)
  }

  function clickCol(header: string) {
    if (armedField) assign(armedField, header)
  }

  function unmap(header: string) {
    setGroups((gs) =>
      gs.map((g, i) => {
        if (i !== groupIndex) return g
        const next: Mapping = { ...g.mapping }
        Object.keys(next).forEach((k) => {
          if (next[k] === header) next[k] = null
        })
        return { ...g, mapping: next }
      }),
    )
  }

  // Splits `fileId` out of whichever group currently holds it
  // (lib/mappingGroups.ts's splitOut) — a no-op on a single-file group. The split
  // group's mapping is a COPY of the shared group's mapping at split time, never a
  // fresh initMappingFromHeaders ([split-copies-the-mapping]): the operator splits to
  // change one field, and discarding their existing placements would be a punishment,
  // not a clarification. The split group is appended at the end of `groups`, so
  // `groupIndex` (and therefore the screen the operator is currently on) is unaffected.
  function splitOutFile(fileId: string) {
    setGroups((gs) => splitOut(gs, fileId))
  }

  // The continue control used to swallow the click outright when invoice_number was
  // unplaced: the button rendered `cursor: not-allowed` but was NOT disabled (only the
  // no-entity case is), so a firm user clicked an armed-looking primary and nothing at
  // all happened. It now ANSWERS the click by arming the one field it is waiting for —
  // every unplaced column lights up as a drop target (CreateMapping's `dropHot` is
  // driven by armedField alone, so no styling change was needed) and the footer note
  // switches to the armed instruction.
  //
  // `setArmedField`, deliberately NOT `armField` — armField is a TOGGLE
  // (`a === k ? null : k`, just above), so routing this through it would DIS-ARM on the
  // second click and reproduce the exact do-nothing click this branch exists to delete.
  // A set is idempotent; clicking continue five times leaves the chip armed five times.
  //
  // BULK-01-04: gates on the ACTIVE group's mapping (canSubmitMapping, the same
  // invoice_number-only structural check `mapping`/`preview` above already delegate to —
  // no second gate is introduced here either). On an incomplete mapping the answer is
  // unchanged. On a complete one, this now advances groupIndex to the next group instead
  // of always starting the run — the run itself starts only once the LAST group's
  // mapping is confirmed complete (startRun(), BULK-01-05, sends every group in
  // sequence).
  function continueMapping() {
    if (!canSubmitMapping(mapping)) {
      setArmedField('invoice_number')
      return
    }
    if (groupIndex < groups.length - 1) {
      setGroupIndex((i) => i + 1)
      return
    }
    startRun()
  }

  function backToImport() {
    setCreateStep('upload')
  }

  // The review surface's TWO ways back to the upload step ("Choose another file" on the
  // rejected-file card, "Import a corrected file" on the unreadable-rows tab). ONE action
  // rather than asking the component to call resetImport-then-backToImport in the right
  // order at two call sites — and deliberately NOT folded into backToImport itself, whose
  // other caller is the Map step's back button, where wiping the chosen file and its
  // preview would be a regression.
  //
  // The reset is what makes this safe from a DEEP LINK: an arrival by URL carries no
  // picked files, no preview and no mapping at all, so the upload step must not open
  // on whatever a previous run in this session happened to leave behind.
  function restartImport() {
    resetImport()
    setCreateStep('upload')
  }

  function skipUpload() {
    setCreateStep('form')
  }

  // The manual path's one round trip. Replaces the mock approve(), which wrote the draft
  // into local `clients` state, showed a success screen and persisted NOTHING — reachable
  // in LIVE via "Skip — enter manually", so a production user could complete it and lose
  // the invoice silently.
  //
  // Everything ordering-sensitive lives in fileDraftInvoice (lib/invoiceDraft.ts), which is
  // node-testable under the no-jsdom constraint; this is the wiring only. `onCreated` is
  // passed as a BARE function reference so there is no local closure here that could do
  // anything other than navigate, and no branch in which it fires early.
  //
  // The guard is the second of two — CreateForm renders the same fileDraftGate result as a
  // disabled button with a named reason, so a blocked click never reaches here. `base ==
  // null` is the no-gateway build (zero network), and the `activeEntity == null` clause is
  // redundant with the gate but required for the narrowing draftToCreateRequest's
  // `Pick<Entity, …>` parameter needs.
  function fileDraft() {
    const base = gatewayBase()
    if (base == null || activeEntity == null || !fileDraftGate(draft, activeEntity).canFile) return
    void fileDraftInvoice(draft, activeEntity, {
      create: (input) => createInvoice(authedFetch, base, input),
      inFlight: reqInFlight,
      onPending: setFiling,
      onError: setFilingError,
      onCreated: openImportedInvoice,
    })
  }

  function selectInvoice(number: string) {
    setView('detail')
    setDetailSel(selectMock(number))
  }

  // Click-through from a rule-violation row in the import report (Core AC4), from a row in
  // the invoices list, and — since INVCR-01-03 — the landing point of a successful manual
  // filing. `id` is always a real invoice UUID, never a mock invoice number, so InvoiceDetail
  // resolves it against the server instead of against active.invoices. Reused verbatim by
  // the create flow rather than given a variant: "the real detail screen showing the
  // server's own row" IS the whole affirmation that a filing succeeded, and a second route
  // into it is a second thing that can be wrong.
  function openImportedInvoice(id: string) {
    setView('detail')
    setDetailSel(selectImported(id))
  }

  function setSettingsTab(t: SettingsTab) {
    setSettingsTab_(t)
  }

  function toggleConnector(id: ConnectorId) {
    setConnectors((c) => ({ ...c, [id]: !c[id] }))
  }

  function saveConnectorMapping(id: ConnectorId, rows: FieldMapRow[]) {
    setConnectorMappings((m) => ({ ...m, [id]: rows }))
  }

  function openRule(key: string) {
    setOpenRuleKey(key)
  }

  function closeRule() {
    setOpenRuleKey(null)
  }

  // All three custom-rule writers go through the same shape: resolve THIS client's
  // list (seed set if untouched), run the pure reducer, store it back under this
  // client's key. `rulesKey` is captured per render off `active`, so a write can
  // never land on the company the switcher just left.
  function updateCustomRules(fn: (rules: CustomRule[]) => CustomRule[]) {
    setCustomRuleStore((store) => ({ ...store, [rulesKey]: fn(customRulesFor(store, rulesKey)) }))
  }

  function addSuggestedRule(s: Suggestion) {
    updateCustomRules((rules) => addSuggested(rules, s))
  }

  function toggleCustomRule(key: string) {
    updateCustomRules((rules) => toggleCustom(rules, key))
  }

  function removeCustomRule(key: string) {
    updateCustomRules((rules) => removeCustom(rules, key))
    // The drawer is showing the rule that just stopped existing.
    setOpenRuleKey((k) => (k === key ? null : k))
  }

  function openPolicy(id: string) {
    setEditingPolicyId(id)
  }

  function closePolicy() {
    setEditingPolicyId(null)
  }

  // The four policy writes, the `setMemberStatus` shape below: call the gateway first,
  // then patch the mirror off the SERVER's own row, so a rejection reaches the caller
  // with nothing here to roll back.
  //
  // The two verbs that change the list's LENGTH also refetch: WorkflowsView's ladder reads
  // `policiesState`, which only a fetch writes, so a mirror patch alone leaves the screen
  // making the previous fetch's claim. Pinned by policiesWiring.test.ts, 'the two verbs
  // that change the list length refetch the status the ladder reads'.

  // Creating opens the builder in the same step: a blank row appended to the list with
  // nothing else happening reads as a click that did nothing. Appended, not prepended —
  // the server orders by created_at then id.
  async function createPolicy(): Promise<void> {
    const created = await createApprovalPolicy(authedFetch, base!, 'Untitled policy')
    setPolicies((list) => [...list, created])
    setEditingPolicyId(created.id)
    policiesAsync.run()
  }

  // The DELETE response is inert, so the mirror drops the id rather than patching a row.
  async function deletePolicy(id: string): Promise<void> {
    await deleteApprovalPolicy(authedFetch, base!, id)
    setPolicies((list) => removePolicy(list, id))
    // The builder is editing the policy that just stopped existing.
    setEditingPolicyId((cur) => (cur === id ? null : cur))
    policiesAsync.run()
  }

  // The ONE write funnel for a policy's contents: the builder composes the next Policy
  // with the pure reducers and hands the whole object back, so nothing here needs to
  // know the node tree's shape. The row that lands is the server's — it re-mints every
  // step id and may bump the version, so the composed one is already stale.
  async function savePolicy(next: Policy): Promise<Policy> {
    const saved = await putApprovalPolicyDraft(authedFetch, base!, next.id, next)
    setPolicies((list) => replacePolicy(list, saved))
    return saved
  }

  async function publishPolicy(id: string): Promise<Policy> {
    const published = await publishApprovalPolicy(authedFetch, base!, id)
    // Refetch, not a one-row patch: the active slot is TENANT-wide, so publishing this
    // policy deactivates whichever other one held it — a change this response cannot report.
    policiesAsync.run()
    return published
  }

  // The one membership write the server backs. The row that lands is the SERVER's own,
  // never a client-composed one — so a status the server declined to set can never appear
  // on screen.
  //
  // No try/catch: the rejection has to reach MembersView, which renders the gateway's own
  // reason at the control. Because nothing writes before the await, a failed call leaves
  // the previous status rendered and there is no optimistic state to roll back.
  async function setMemberStatus(id: string, status: Exclude<MemberStatus, 'invited'>) {
    const wire: MembershipWire = await setMembershipStatus(authedFetch, base!, id, status)
    setMembers((list) => replaceMember(list, toMember(wire, session.persona.subject)))
  }

  // Settings › Roles' four writes, the `setMemberStatus` shape repeated: each calls the
  // gateway first and patches the mirror off the SERVER's own returned row, so a rejection
  // reaches the caller with nothing here to roll back.
  async function createRole(title: string, desc: string, members: readonly string[]): Promise<Role> {
    const created = await createStaffedRole(authedFetch, base!, title, desc, members)
    setRoles((list) => addRole(list, created))
    return created
  }

  // Diffs against the mirror's own copy of the role, not a caller-supplied one — `rolePatch`
  // is what lets an unchanged field skip the wire entirely.
  async function renameRole(key: string, title: string, desc: string): Promise<Role> {
    const current = roles.find((r) => r.key === key)
    if (!current) throw new Error('Role no longer exists')
    const patch = rolePatch(current, title, desc)
    if (Object.keys(patch).length === 0) return current
    const updated = await updateWorkflowRole(authedFetch, base!, key, patch)
    setRoles((list) => replaceRole(list, updated))
    return updated
  }

  async function staffRole(key: string, members: readonly string[]): Promise<Role> {
    const updated = await setRoleMembers(authedFetch, base!, key, members)
    setRoles((list) => replaceRole(list, updated))
    return updated
  }

  async function deleteRole(key: string): Promise<void> {
    await deleteWorkflowRole(authedFetch, base!, key)
    setRoles((list) => removeRole(list, key))
  }

  const user: SignedInUser = {
    name: session.persona.name,
    initials: session.persona.initials,
    tenantName: session.me?.tenant.name ?? null,
    verified: session.verified,
  }

  const ctx: PlatformCtx = {
    authedFetch,
    // Reuses importAuth's accessor rather than a second closure, so the two byte-level
    // transports can never drift on which session they read.
    getToken: importAuth.getToken,
    user,
    clients,
    active,
    entities: entitiesList,
    activeEntity,
    entitiesState,
    entitiesError: entitiesAsync.error,
    refetchEntities: entitiesAsync.run,
    mode,
    view,
    draft,
    createStep,
    mapping,
    armedField,
    dragField,
    selectedId: detailSel.selectedId,
    invoiceQuery,
    switcherOpen,
    sandbox,
    settingsTab,
    connectors,
    connectorMappings,
    filing,
    filingError,
    customRules,
    openRuleKey,
    policies,
    policiesState,
    policiesError: policiesAsync.error,
    refetchPolicies: policiesAsync.run,
    editingPolicyId,
    members,
    membersState,
    membersError: membersAsync.error,
    refetchMembers: membersAsync.run,
    roles,
    rolesState,
    rolesError: rolesAsync.error,
    refetchRoles: rolesAsync.run,
    entityId,
    pickedFiles,
    filesRefusal,
    groups,
    groupIndex,
    preview,
    run,
    importError,
    reviewBatchIds,
    importedInvoiceId: detailSel.importedInvoiceId,
    nav,
    setInvoiceQuery,
    toggleSwitcher,
    switchClient,
    openCreate,
    closeCreate,
    updateDraft,
    updateItem,
    updateItemDesc,
    addItem,
    removeItem,
    armField,
    setDrag,
    endDrag,
    dropOn,
    clickCol,
    unmap,
    continueMapping,
    addPickedFiles,
    removePickedFile,
    readAllColumns,
    splitOutFile,
    backToImport,
    restartImport,
    skipUpload,
    fileDraft,
    selectInvoice,
    openImportedInvoice,
    setSandbox,
    setSettingsTab,
    toggleConnector,
    saveConnectorMapping,
    openRule,
    closeRule,
    addSuggestedRule,
    toggleCustomRule,
    removeCustomRule,
    openPolicy,
    closePolicy,
    createPolicy,
    deletePolicy,
    savePolicy,
    publishPolicy,
    setMemberStatus,
    createRole,
    renameRole,
    staffRole,
    deleteRole,
    signOut: onSignOut,
  }

  return (
    <div
      className="asc-app pf-shell"
      style={{ height: '100vh', display: 'flex', background: 'var(--bg-1)', fontFamily: 'var(--font-sans)', color: 'var(--fg-1)', overflow: 'hidden' }}
    >
      <Sidebar ctx={ctx} />
      <main className="pf-main" style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <Header ctx={ctx} />
        {(() => {
          const b = ENV_BANNER[sandbox ? 'sandbox' : 'live']
          return (
            <div data-testid="env-banner" style={{ flex: 'none', background: b.bg, borderBottom: `1px solid ${b.border}`, padding: '7px 24px', display: 'flex', alignItems: 'center', gap: 9 }}>
              <span style={{ color: b.text, flex: 'none', display: 'inline-flex' }}>{b.icon}</span>
              <span style={{ fontSize: 12.5, color: b.text, fontWeight: 500 }}>{b.msg}</span>
              <span className="mono" style={{ marginLeft: 'auto', fontSize: 10, color: b.text, opacity: 0.85, letterSpacing: '0.05em' }}>
                {b.tag}
              </span>
            </div>
          )
        })()}
        <div className="pf-scroll" style={{ flex: 1, overflowY: 'auto' }}>
          {view === 'dashboard' && (active.onboarding ? <DashboardOnboarding ctx={ctx} /> : <DashboardActive ctx={ctx} />)}
          {view === 'invoices' && <InvoicesList ctx={ctx} />}
          {view === 'create' && <CreateFlow ctx={ctx} />}
          {view === 'detail' && <InvoiceDetail ctx={ctx} />}
          {view === 'clients' && <ClientsView ctx={ctx} />}
          {view === 'validation' && <ValidationView ctx={ctx} />}
          {view === 'rules' && <RulesView ctx={ctx} />}
          {view === 'workflows' && <WorkflowsView ctx={ctx} />}
          {view === 'customers' && <CustomersView ctx={ctx} />}
          {view === 'reports' && <ReportsView ctx={ctx} />}
          {view === 'settings' && <SettingsView ctx={ctx} />}
          {view === 'approvals' && <ApprovalsView ctx={ctx} />}
        </div>
      </main>
    </div>
  )
}

// App gates the workspace behind the mock sign-in (M2-13). Picking a persona runs the
// real round trip (mint → GET /v1/me) when a gateway is configured; on failure it enters
// with the persona's static identity, marked unverified, so the showcase never hard-fails.
export default function App() {
  // Persona to auto-sign-in from a landing deep-link (?persona=), resolved ONCE at boot:
  // non-null when the param names a persona this app can open. A non-null value means an
  // auto-sign-in is in flight, so the render gate shows a loading splash instead of the
  // "Choose an account" picker — the landing → app hand-off never flashes that redundant
  // card before the mint → /me round trip resolves. Declared BEFORE `session` because the
  // session initializer below reads it.
  const [autoPersona] = useState<PersonaId | null>(() => {
    const p = new URLSearchParams(window.location.search).get('persona')
    return shouldAutoSignIn(p) ? (p as PersonaId) : null
  })
  // Lazy initializer: synchronously rehydrate a persisted session at boot (no network,
  // no SignIn flash) so a reload / new tab returns straight to the workspace. A stored
  // token already past its `exp` resolves to NO session — entering the workspace on one
  // only buys a dashboard that 401s a moment later.
  //
  // A deep-link hand-off boots with NO session even when one is stored: the user just chose
  // a profile on the landing page and that choice wins (see shouldAutoSignIn). Rehydrating
  // here would render the PREVIOUS persona's workspace for the duration of the mint → /me
  // round trip — and re-persist it via the mirror effect below — before swapping identity
  // under the user. Starting empty shows the loading splash for the persona actually being
  // signed in, and the same mirror effect clears the superseded session on that first pass.
  const [session, setSession] = useState<Session | null>(() => (autoPersona ? null : resolveBootSession()))
  const [signingIn, setSigningIn] = useState<PersonaId | null>(null)

  // Mirror the session to storage: persist while signed in, wipe on sign out / cleared session.
  useEffect(() => {
    if (session) saveSession(session)
    else clearSession()
  }, [session])

  // Sign out returns the user to the marketing landing page (the real sign-in front
  // door). Nulling React state alone would only swap in the app's own minimal
  // persona-picker, so wipe the persisted session and navigate away. Also the 401 handler
  // (makeAuthedFetch → onSignOut): an invalidated session belongs back at the front door,
  // not the in-app picker. The `?persona=` deep-link is no longer this function's problem —
  // it is stripped from the URL when consumed at boot, so no history entry behind this
  // navigation can auto-sign the same persona back in.
  const signOut = useCallback(() => {
    // Drop the in-memory session, not just the persisted copy. clearSession() only wipes
    // localStorage, so without this the invalidated session stayed in React state and
    // Workspace kept rendering — which is exactly how a 401'd reload left the user parked
    // on a dead dashboard behind an "unauthorized / HTTP 401" card instead of signed out.
    // The old comment below claimed this fallback already happened; it did not.
    setSession(null)
    clearSession()
    // landingBase() is null when VITE_LANDING_URL isn't configured (e.g. the default
    // standalone showcase build) — never navigate to `null` (stringifies to "null").
    // With it unset we now land on the app's own persona-picker, which is a front door;
    // the workspace behind an expired token is not.
    const dest = landingBase()
    if (dest) window.location.href = dest
  }, [])

  const doSignIn = useCallback(async (persona: Persona) => {
    setSigningIn(persona.id)
    try {
      setSession(await signIn(persona))
    } catch (err) {
      // A configured gateway that is unreachable: degrade to an unverified session so the
      // app still opens. console.warn (not error) keeps the Playwright smoke's no-error gate green.
      console.warn('[app] sign-in round trip failed; entering with unverified identity:', err)
      setSession({ persona, token: null, me: null, verified: false })
    } finally {
      setSigningIn(null)
    }
  }, [])

  // task-21 hand-off: the landing routes here as ?persona=firm|inhouse; auto-sign-in that
  // persona. autoPersona already encodes the shouldAutoSignIn guard (the param names a
  // persona this app can open), resolved once at boot, so this fires at most once on mount.
  useEffect(() => {
    if (autoPersona) void doSignIn(APP_PERSONAS[autoPersona])
  }, [autoPersona, doSignIn])

  // Drop the consumed ?persona= from the URL. The param is a one-shot hand-off, and leaving
  // it behind made it a credential-free sign-in link: after Sign out, Back to the
  // `?persona=firm` history entry walked straight into the workspace again with no OTP —
  // which reads as "logging out doesn't work". Stripping it also removes the stale-leftover
  // case that used to justify letting a stored session beat the param, so a plain reload now
  // resolves through the stored session instead of re-minting.
  //
  // replaceState, not a navigation: it must not add a history entry the back button can
  // bounce off. Reads the URL directly rather than depending on render state — this is the
  // only writer, and it runs once. Same treatment as ops-console/src/App.tsx.
  useEffect(() => {
    if (!autoPersona) return
    if (new URLSearchParams(window.location.search).has('persona')) {
      window.history.replaceState(null, '', window.location.pathname + window.location.hash)
    }
  }, [autoPersona])

  // The single front door. Any sessionless visit — never signed in, signed out, session
  // expired while the tab was closed, or token invalidated by a 401 — goes to the landing
  // page rather than being offered a second place to sign in here.
  //
  // Suppressed when a ?persona= deep link is present: that hand-off is ABOUT to mint a
  // fresh token, so bouncing to landing would break landing → app. Also skipped when no
  // landing URL is configured (the standalone showcase build), which keeps its own picker.
  useEffect(() => {
    if (session || autoPersona) return
    const dest = landingBase()
    if (dest) window.location.href = dest
  }, [session, autoPersona])

  if (!session) {
    // A deep-link auto-sign-in is in flight: show a loading splash, NOT the persona
    // picker, so the landing → app hand-off doesn't flash "Choose an account" before the
    // mint → /me round trip resolves.
    if (autoPersona) return <SignInLoading persona={APP_PERSONAS[autoPersona]} />
    // No session and no deep link. The landing page is the product's single sign-in front
    // door, so go there rather than offer a SECOND place to sign in — the effect above has
    // already started that navigation; render nothing rather than flash a picker the user
    // is about to be moved off.
    if (landingBase()) return null
    // No landing configured (the standalone showcase build). There is nowhere to send
    // anyone, so the in-app picker stays as the fallback — without it this build would be
    // a dead end. It is the ONLY path that still renders SignIn.
    return <SignIn signingIn={signingIn} onPick={doSignIn} />
  }
  return <Workspace session={session} onSignOut={signOut} />
}
