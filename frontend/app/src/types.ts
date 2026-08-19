// Domain types re-authored from the prototype's `this.state` / seed-data shapes
// (Platform.dc.html, class Component extends DCLogic).

// Type-only import: `portfolio.ts` imports `StatusStyle` from this file, so a runtime
// import here would form a cycle. `AuthedFetch`/`Entity` are only ever used as types below.
import type { AuthedFetch, Entity } from './lib/portfolio'
import type { ApiError, AsyncStatus } from '@invoice-os/api-client'
import type { ImportPreview } from './lib/importApi'
import type { ImportRun, PickedFile } from './lib/importRun'
// Type-only, mirroring the PickedFile edge above — lib/mappingGroups.ts type-imports
// `Mapping` from THIS file, so this is a benign type-only cycle (erased at compile,
// TS1484), same shape as the pre-existing PickedFile/Member edges.
import type { MappingGroup } from './lib/mappingGroups'
// Type-only, and it must stay that way: `lib/members.ts` VALUE-imports `./auth` and
// `./data`, both of which type-import this file, so `Member` closes the loop
// members -> auth/data -> types -> members. Benign only because every edge in it is
// erased at compile — this file has zero runtime exports. `tsc` is what enforces it
// (TS1484, from `verbatimModuleSyntax`), NOT the bundler: `vite build` alone erases a
// value import here and emits a byte-identical bundle.
import type { Member, MemberStatus } from './lib/members'
import type { CustomRule, Suggestion } from './lib/rules'
// Type-only for the reason the `Member` edge above spells out — `lib/roles.ts` type-imports
// `lib/members.ts`, which closes the same benign compile-erased loop.
import type { Role } from './lib/roles'
import type { Policy } from './lib/workflows'

export type SectorKey = 'logistics' | 'foods' | 'oilfield' | 'trading' | 'manufacturing' | 'textile'

export type SectorDef = {
  buyers: string[]
  items: string[]
  addr: string[]
  min: number
  max: number
}

export type Taxpayer = 'Large' | 'Medium' | 'Small'

export type InvoiceStatus = 'Transmitted' | 'Approved' | 'Pending' | 'Rejected' | 'Draft'

export type DocType = 'B2B' | 'B2G' | 'B2C'

export type LineItem = {
  desc: string
  qty: number
  price: number
}

export type Invoice = {
  number: string
  buyer: string
  buyerTin: string
  buyerAddress: string
  date: string
  items: LineItem[]
  status: InvoiceStatus
  wht: boolean
  docType?: DocType
}

// Everything the MOCK verdict engine (lib/validation.ts) reads. Satisfied by `Invoice`
// alone now — the mock dashboard's failing-count pass over its generated sample rows is
// the only surviving caller (lib/clients.ts's finishClient, and lib/charts.ts downstream
// of it). It is deliberately NOT satisfied by `Draft` any more: the real create flow gets
// its verdict from the server, never from this ([server-truth], [draft-is-not-validatable]).
export type Validatable = {
  buyerTin: string
  buyerAddress: string
  items: LineItem[]
  wht: boolean
}

// The manual create form's state — the shape draftToCreateRequest maps onto the
// POST /v1/invoices body, and nothing else. De-intersected from `Validatable` in
// INVCR-01-03: while `Draft = Validatable & {…}` the create flow was typed against the
// mock verdict engine, which is exactly the coupling [server-truth] exists to sever.
// `buyerAddress`/`wht`/`docType` left with it — `invoices` has no address, WHT or doc-type
// column and `createRequest` no such field, so each was a value the form collected and
// silently discarded. `Invoice` (the mock dashboard's row shape) keeps all three.
export type Draft = {
  number: string
  buyer: string
  buyerTin: string
  date: string
  currency: string
  items: LineItem[]
}

// Static per-company seed config (mirrors `this.CFG` entries). The prototype's raw
// CFG literal also carries `vd`/`vatd`/`fd`/`pd`/`validated`/`dist`/`docs`/`health`/
// `vat`/`vatNum`/`failing`/`pending`/`head` per company — every one of those is
// unconditionally recomputed (and so overwritten) by `buildClients()` and never read
// anywhere else in the render output, so they are omitted here (see src/data.tsx).
export type ClientCfg = {
  name: string
  short: string
  initials: string
  tin: string
  taxpayer: Taxpayer
  sector: SectorKey
  score: number | null
  vol: number
  failTarget?: number
  readiness: [number, number, number]
  readinessNote: string
  onboarding?: boolean
}

export type DonutSeg = {
  label: string
  color: string
  count: string
  pct: string
  dash: string
  offset: string
}

export type FailureRow = {
  label: string
  rule: string
  glyphId: 'cross'
  count: number
  bar: string
}

export type StatusStyle = { bg: string; border: string; text: string; label: string }

export type DashboardData = {
  failures: FailureRow[]
}

// Fully-built client: seed config + generated invoices + precomputed dashboard.
export type Client = ClientCfg & {
  // The real portfolio entity this Client is sourced from ([entity-picker] keystone) —
  // null only for the synthetic fallback (lib/clients.ts's emptyClient(), returned by
  // resolveActiveClient when a workspace — either persona — genuinely has zero entities,
  // task-304 AC-3), never backed by an actual business_entities row.
  entityId: string | null
  invoices: Invoice[]
  failing: number | '—'
  pending: number
  vatNum: number
  vatLabel: string
  count: number
  head: string
  dash: DashboardData | null
}

// `label`/`detail`/`fixLabel`/`patch` are no longer read by any production surface — the
// mock dashboard reads only `errors[].id` and `errors.length`. They die with that
// dashboard rather than being stripped here (§14 puts it out of scope). `patch` is typed
// over `Validatable`, not `Draft`: lib/validation.ts's own patches name buyerAddress/wht,
// neither of which is a `Draft` member any more.
export type ValidationIssue = {
  id: string
  label: string
  detail: string
  fixLabel: string
  patch: Partial<Validatable>
}

export type ValidationResult = {
  errors: ValidationIssue[]
  warnings: ValidationIssue[]
  passed: string[]
}

export type Mode = 'firm' | 'inhouse'

export type View = 'dashboard' | 'invoices' | 'validation' | 'rules' | 'workflows' | 'create' | 'detail' | 'clients' | 'customers' | 'reports' | 'settings' | 'approvals'

// 'review' was added by M4-08-04 under its former name (plan B1/DRIFT-1) — one subtask
// ahead of story §6's original assignment (M4-08-05), because wizardHeader's index-2
// branch does not compile against this union and lib/importFlow.ts's STAGE_OF is a total
// Record over it. INVCR-01-03 dropped the mock validate/approve tail's own two steps: the
// manual path is now ONE screen with one round trip, and the affirmation is the real
// invoice detail view rendering the server's row — there is deliberately no step between
// 'form' and that. INVCR-01-04 renamed the last member to 'review' ([three-stages]): the
// stage is the user's REVIEW SURFACE, not an import-report payload rendered on it — and
// INVCR-01-09 cashed that distinction by deleting CreateReport.tsx outright. 'review' now
// renders ReviewBatch.tsx off `reviewBatchIds` + two live GETs, which is also why the step
// is reachable by URL (`#review/<uuid>`) where the payload-backed one never could be.
export type CreateStep = 'upload' | 'mapping' | 'form' | 'review'

// A canonical invoice field the Map step places onto a spreadsheet column.
// `required` marks the fiscal identifier that recognition never guesses.
export type CanonField = { key: string; required?: boolean }

// canonical field key -> source column header, or null while unplaced
//
// Duplicate source headers are AMBIGUOUS BY DESIGN — the server takes the first
// match (resolveMapping, internal/importer/service.go). Do NOT re-key this by
// column index: the wire payload is Record<field, header>, so "the second column
// named VAT" is untransmittable. See task-177.
export type Mapping = Record<string, string | null>

// Declaration order (minus 'company') is the rendered tab order (SETTINGS_TABS maps it
// straight through), and 'members' is also the tab Settings opens on (App.tsx's initial
// settingsTab). 'company' (task-304, INVCR-01-19) is IN-HOUSE ONLY — SettingsView adds it
// to the tab strip conditionally on ctx.mode, it is never in the shared SETTINGS_TABS list
// (data.tsx). A firm workspace already has a dedicated multi-entity portfolio screen
// (ClientsView); in-house's is single-entity and lives in Settings instead ([entity-picker]).
export type SettingsTab = 'members' | 'roles' | 'connectors' | 'api' | 'signing' | 'company'

export type ConnectorId = 'sap' | 'quickbooks' | 'oracle' | 'sage' | 'odoo' | 'dynamics'

export type ConnectorsState = Record<ConnectorId, boolean>

// One row of a connector's ERP -> UBL field mapping: the ERP's native field name and
// the UBL 2.1 path it feeds (see data.tsx for each connector's defaults).
export type FieldMapRow = { erp: string; ubl: string }

// Mappings edited in the field-mapping modal, by connector. A connector absent here
// still renders its default mapping — only edited ones are held.
export type ConnectorMappings = Partial<Record<ConnectorId, FieldMapRow[]>>

// The signed-in caller shown in the sidebar footer. `tenantName`/`verified` come from
// the GET /v1/me round trip (M2-13): when verified, the tenant name was proven against
// the live backend; otherwise it falls back to the persona's static workspace label.
export type SignedInUser = {
  name: string
  initials: string
  tenantName: string | null
  verified: boolean
}

// The full app state + action bundle threaded through every section component, mirroring
// the prototype's single `renderVals()` bag of state/handlers (Platform.dc.html ~L1266+).
export type PlatformCtx = {
  authedFetch: AuthedFetch
  // The raw bearer token, for the ONE transport that cannot go through authedFetch:
  // GET /v1/documents/{id} streams octet-stream bytes and apiFetch always res.json()s.
  // Same `() => session.token` read-at-call-time closure makeImportAuth already exposes.
  getToken: () => string | null
  user: SignedInUser
  clients: Client[]
  active: Client
  // [entity-picker] step 1 of 3: the ONE live portfolio entity fetch, shared by the
  // workspace switcher (Sidebar) and ClientsView — previously each ran its own
  // independent listEntities() call. entities/entitiesState/entitiesError mirror the
  // AsyncState<Entity[]> shape they already rendered against (clientsViewState's
  // AsyncStatus ladder); refetchEntities is useAsync's `run`. CreateUpload was a third
  // consumer until [import-upload-unify] removed its entity <select>.
  entities: Entity[]
  // The `entities` member matching `active.entityId`, resolved ONCE in App.tsx beside
  // `active` — null for a workspace (either persona) with no entity resolved yet, for
  // the emptyClient() placeholder, and for the whole loading/error/no-gateway window.
  // Every filing gate
  // reads THIS, never `active.entityId`: `active` is rebuilt from `entities` by an effect,
  // so the id can be non-null while the entity itself is not yet in the list, and a gate
  // on the id would arm a button that swallows the click ([gate-on-the-resolved-entity]).
  // draftToCreateRequest also needs the real Entity, never a Client — Client.tin is lossy
  // (`e.tin ?? '—'`), so a TIN-less entity is unrepresentable through it.
  activeEntity: Entity | null
  entitiesState: AsyncStatus
  entitiesError: ApiError | null
  refetchEntities: () => void
  mode: Mode
  view: View
  draft: Draft
  createStep: CreateStep
  // The active group's mapping (BULK-01-04) — a DERIVED read of
  // `groups[groupIndex]?.mapping ?? null` in App.tsx, not its own state any more.
  // `assign`/`unmap` write back into `groups[groupIndex].mapping` instead of a
  // standalone setter. See `groups`/`groupIndex` below.
  mapping: Mapping | null
  armedField: string | null
  dragField: string | null
  selectedId: string | null
  // The header search box's committed term (BUG-01-05) -- set on submit, read by
  // InvoicesList as the `q` server-side filter. `''` means unfiltered.
  invoiceQuery: string
  switcherOpen: boolean
  sandbox: boolean
  settingsTab: SettingsTab
  connectors: ConnectorsState
  connectorMappings: ConnectorMappings

  // --- Manual create form · the one real round trip (INVCR-01-03) --------------
  // `filing` exists ALONGSIDE App.tsx's reqInFlight ref, not instead of it, and the pair
  // is not a redundancy to simplify away: a ref cannot re-render (so it can't disable the
  // button or spin), and state cannot beat a double-click (React batches, so both clicks
  // see the old value). The ref owns correctness, this owns the frame.
  filing: boolean
  // The server's own ApiError, rendered VERBATIM beside the primary — same treatment as
  // `importError` on the map step. No status->copy table: ApiError.message already carries
  // the gateway's {"error":…} text, and a second copy of it drifts.
  filingError: ApiError | null

  // --- Rules screen ---------------------------------------------------------
  // The ACTIVE client's custom rules, already resolved out of the per-client store
  // in App.tsx — components never see the store or the key, so a surface cannot
  // accidentally render one client's rules under another's name. The golden ruleset
  // is not here at all: it is inherited, identical for every tenant, and read
  // straight off lib/rules.ts by the view.
  customRules: CustomRule[]
  /** Key of the rule whose detail drawer is open, golden or custom. */
  openRuleKey: string | null

  // --- Workflows screen -----------------------------------------------------
  // The tenant's approval policies, fetched in App.tsx — the `members` triple below,
  // same shape and reason. Per TENANT: switching company does not change them, which is
  // why the nav item sits in the firm-wide sidebar group (Sidebar.tsx).
  //
  // Only the list and "which policy is open" live here. Builder-transient state — node
  // selection, the drag/drop hint, the scenario inputs, the saved flash, the in-flight write
  // lock — is local to WorkflowBuilder, and the list's two write-error slots to WorkflowsView:
  // the ClientsView precedent, each derived from one view and read nowhere else.
  policies: Policy[]
  policiesState: AsyncStatus
  policiesError: ApiError | null
  refetchPolicies: () => void
  /** Id of the policy open in the builder; null shows the policy list. */
  editingPolicyId: string | null

  // --- Settings › Members tab -----------------------------------------------
  // The tenant's live membership directory, fetched ONCE in App.tsx and shared by the
  // Members tab, the Roles tab and the Workflows builder — the entities/entitiesState/
  // entitiesError/refetchEntities shape above, for the same reason: three surfaces
  // reading one list cannot disagree about who is in this workspace.
  //
  // Everything transient inside the tab — search text, the role filter, which drawer
  // or menu is open — is local to MembersView, following the SettingsView precedent at
  // SettingsView.tsx:6-9 rather than the openRuleKey / editingPolicyId one: those are
  // screens reachable by nav, this is a tab panel.
  //
  // Both tabs branch on `membersState` and never on `members.length` — a failed fetch
  // resolves `data` to null, which an emptiness check would render as an empty tenant.
  members: Member[]
  membersState: AsyncStatus
  membersError: ApiError | null
  refetchMembers: () => void

  // --- Settings › Roles tab -------------------------------------------------
  // The tenant's approval seats, fetched ONCE in App.tsx and shared by the Roles tab and
  // the Workflows builder — the `members` triple above, same shape and reason. Per
  // TENANT, like `policies` above: one persona is one tenant, and a policy's steps point
  // at these seats, so the two must resolve for the same tenant or a step renders as a
  // deleted role.
  roles: Role[]
  rolesState: AsyncStatus
  rolesError: ApiError | null
  refetchRoles: () => void

  // --- Multi-invoice import path (M4-08-04) ---------------------------------
  // These live on ctx rather than in CreateUpload's local state because the two
  // halves of the flow are two components: the file is chosen in CreateUpload, which
  // UNMOUNTS when createStep leaves 'upload', while createImport fires from
  // CreateMapping. Local state would lose it in between. (`entityId` no longer has a
  // picker of its own since [import-upload-unify] — it mirrors `active` — but it is
  // read at createImport time, so it must survive that unmount too.)
  entityId: string | null
  // The run's ordered file selection (BULK-01-03, Core AC 1) — CreateUpload's
  // chosen-files list, per-file remove controls and per-file bad-extension notes all
  // render off this. Lives on ctx, not CreateUpload's local state, for the same reason
  // `entityId` does ([multi-invoice import path] above): CreateUpload UNMOUNTS when
  // createStep leaves 'upload'.
  pickedFiles: PickedFile[]
  // The refusal text from the most recent addPickedFiles call (lib/importRun's
  // capRefusal) — null when nothing was dropped. Same idiom as `importError`: state
  // lives on ctx, the component renders it verbatim.
  filesRefusal: string | null
  // Files sharing an identical column layout are mapped ONCE (BULK-01-04, Core AC 3,
  // decision [shared-mapping-shown]) — App.tsx's readAllColumns previews every picked
  // file, then lib/mappingGroups.ts's groupByLayout buckets them by columnSignature,
  // preserving first-appearance order. `mapping`/`preview` above/below are DERIVED reads
  // of `groups[groupIndex]`, not their own state. `MappingGroup` is pure client state
  // ([run-is-client-state]) — no table, no endpoint, no group id crosses the wire.
  groups: MappingGroup[]
  // Which group the mapping step currently shows. `continueMapping` advances this on a
  // complete mapping and starts the run only once it is the LAST group.
  groupIndex: number
  preview: ImportPreview | null
  // The sequential run's whole state (BULK-01-05, task-308) — one createImport in
  // flight at a time, one outcome per file, continuation through failures
  // ([partial-success-kept]). App.tsx's startRun() is the sole writer, via
  // lib/importRun.ts's runReducer; every view over it (runBatchIds/runFailures/
  // runFileRows/routeAfterRun) is a pure derivation of THIS value, never re-computed
  // ad hoc by a component. `status: 'idle'` both before a run starts and once
  // applyRoute has drained a finished run into `reviewBatchIds`/an opened invoice.
  // `'failed'` (BULK-01-05 QA correction, task-308) is a distinct landing applyRoute
  // sets on a `none` route (AC #9) instead of resetting to idle — `files`/`cursor`
  // survive so runFailures keeps returning them, and CreateMapping renders again
  // (runIsActive treats 'failed' like 'idle') until the user backs out via
  // restartImport/resetImport, or starts another run.
  run: ImportRun
  importError: ApiError | null
  // The batches the review step is showing (INVCR-01-09, widened BULK-01-05).
  // REPLACES the old singular `reviewBatchId: string | null` — a run's `review` route
  // (lib/importRun's routeAfterRun) carries every batch id created in the run, in run
  // order, and none may be dropped just because the review screen itself is not yet
  // widened to read more than the first (BULK-01-06). REPLACES the old
  // `report: ImportReport | null` before that, which was the POST's frozen 201 payload
  // held in memory: D4 made the review screen revisitable by URL (`#review/<uuid>`),
  // so it re-fetches from GET /v1/imports/{id} + the list endpoint's own totals
  // instead, and a stale in-memory report is exactly the frozen-counter source that
  // replaced. An id is all any consumer needs; nothing may resurrect the payload.
  reviewBatchIds: string[]
  // Set by openImportedInvoice (M4-08-05) when the user clicks through to a real
  // invoice. Mutually exclusive with `selectedId` by construction — both are members
  // of one DetailSelection atom in App.tsx (lib/importReport.ts), so neither can be left
  // stale when the other is written. Non-null makes InvoiceDetail render its honest
  // placeholder instead of resolving a mock invoice; M4-09 swaps that for a real fetch.
  importedInvoiceId: string | null

  nav: (id: View) => void
  setInvoiceQuery: (q: string) => void
  toggleSwitcher: () => void
  // [entity-picker] keystone: takes a real entity id, never an array index — the active
  // selection is never again "the mock array position the switcher happened to click".
  switchClient: (entityId: string) => void
  openCreate: () => void
  closeCreate: () => void
  updateDraft: <K extends keyof Draft>(field: K, value: Draft[K]) => void
  updateItem: (i: number, field: 'qty' | 'price', val: string) => void
  // Descriptions are strings and qty/price are numbers coerced off the input's text, so
  // this is a separate writer rather than a widened `field` union with a branch inside.
  updateItemDesc: (i: number, desc: string) => void
  addItem: () => void
  removeItem: (i: number) => void
  armField: (k: string) => void
  setDrag: (k: string) => void
  endDrag: () => void
  dropOn: (header: string) => void
  clickCol: (header: string) => void
  unmap: (header: string) => void
  continueMapping: () => void
  // Multi-file selection (BULK-01-03): addPickedFiles appends onto `pickedFiles` via
  // lib/importRun's addFiles (capped at MAX_RUN_FILES, never silently truncating —
  // `filesRefusal` carries the refusal text when it drops any). removePickedFile removes
  // one entry by id, preserving the order of the rest, and also clears `filesRefusal` —
  // a refusal names files that were NOT added, so removing one already-added file can
  // never be what that message is still talking about.
  addPickedFiles: (files: File[]) => void
  removePickedFile: (id: string) => void
  // Previews every picked file one at a time (BULK-01-04) and, on success, buckets them
  // into `groups` via lib/mappingGroups.ts's groupByLayout. A preview failure sets
  // `importError` naming the failing file and stays on 'upload' — never silently drops
  // the file, never carries it into the run. Renamed from the single-file `readColumns`.
  readAllColumns: () => void
  // Splits `fileId` out of whichever group currently holds it
  // (lib/mappingGroups.ts's splitOut) — a no-op on a single-file group. The split
  // group's mapping is a COPY of the shared group's mapping at split time, never a
  // fresh seed ([split-copies-the-mapping]).
  splitOutFile: (fileId: string) => void
  backToImport: () => void
  // The review surface's two ways back to the upload step (§7.4's "Import a corrected
  // file", §7.5's "Choose another file"): resetImport THEN backToImport, as one action so
  // the two call sites cannot drift on the order. Distinct from backToImport, whose other
  // caller is the Map step's back button — resetting there would wipe a file and preview
  // the user is going back precisely to keep.
  restartImport: () => void
  skipUpload: () => void
  // The manual path's ONLY action. Fire-and-forget: it never rejects and never returns a
  // verdict — outcomes arrive through `filing`/`filingError` and, on 201, through the
  // navigation to the real invoice detail. There is deliberately no companion
  // "approve"/"back to results" pair, because there is no step to go back from.
  fileDraft: () => void
  selectInvoice: (number: string) => void
  openImportedInvoice: (id: string) => void
  setSandbox: (v: boolean) => void
  setSettingsTab: (t: SettingsTab) => void
  toggleConnector: (id: ConnectorId) => void
  saveConnectorMapping: (id: ConnectorId, rows: FieldMapRow[]) => void
  // Rules screen. There is deliberately no editCustomRule/disableGoldenRule pair:
  // a tenant may adopt, switch off and remove its OWN rules, and may do nothing at
  // all to an inherited one.
  openRule: (key: string) => void
  closeRule: () => void
  addSuggestedRule: (s: Suggestion) => void
  toggleCustomRule: (key: string) => void
  removeCustomRule: (key: string) => void
  // Workflows screen. `savePolicy` is the ONE write funnel for a policy's contents: the
  // builder composes the next Policy with the pure reducers in lib/workflows.ts and hands
  // the whole object back, so App.tsx never needs to know the node tree's shape. It
  // resolves the SERVER's row, which re-mints every step id and may bump the version.
  //
  // Publishing is its own verb because saving no longer publishes: the server seals a
  // version, and sealing on every Save would silently override the policy in force.
  openPolicy: (id: string) => void
  closePolicy: () => void
  createPolicy: () => Promise<void>
  deletePolicy: (id: string) => Promise<void>
  savePolicy: (next: Policy) => Promise<Policy>
  publishPolicy: (id: string) => Promise<Policy>
  // Settings › Members. The ONE membership write the server backs. It resolves once the
  // server's own row has replaced the old one, and REJECTS with the gateway's ApiError
  // unreshaped — the caller renders that message at the control, so nothing here may
  // swallow it. Invite, remove and access-role writes have no endpoint; their controls
  // ship disabled with `MEMBER_UNBACKED`'s reason rather than calling a verb that lies.
  setMemberStatus: (id: string, status: Exclude<MemberStatus, 'invited'>) => Promise<void>
  // Settings › Roles. Rename (PATCH) and staffing (PUT /members) are separate server
  // writes, so they are separate verbs rather than one funnel over a whole Role — a
  // partial failure has to say which half failed. Same reject-unreshaped contract as
  // `setMemberStatus` above, restated once here rather than per verb.
  createRole: (title: string, desc: string, members: readonly string[]) => Promise<Role>
  renameRole: (key: string, title: string, desc: string) => Promise<Role>
  staffRole: (key: string, members: readonly string[]) => Promise<Role>
  deleteRole: (key: string) => Promise<void>
  signOut: () => void
  // Demo-only (DEMO-06). App supplies these ONLY under DEMO_MODE, so they are
  // `undefined` in every customer build and nothing outside src/demo/ may read them.
  becomePersona?: (member: Member, view: View) => Promise<void>
  returnToSeat?: (view: View) => void
  seatSubject?: string
}
