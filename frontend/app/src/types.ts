// Domain types re-authored from the prototype's `this.state` / seed-data shapes
// (Platform.dc.html, class Component extends DCLogic).

// Type-only import: `portfolio.ts` imports `StatusStyle` from this file, so a runtime
// import here would form a cycle. `AuthedFetch`/`Entity` are only ever used as types below.
import type { AuthedFetch, Entity } from './lib/portfolio'
import type { ApiError, AsyncStatus } from '@invoice-os/api-client'
import type { ImportPreview, UploadPhase } from './lib/importApi'
// Type-only, and it must stay that way: `lib/members.ts` VALUE-imports `./auth` and
// `./data`, both of which type-import this file, so `Member` closes the loop
// members -> auth/data -> types -> members. Benign only because every edge in it is
// erased at compile — this file has zero runtime exports. `tsc` is what enforces it
// (TS1484, from `verbatimModuleSyntax`), NOT the bundler: `vite build` alone erases a
// value import here and emits a byte-identical bundle.
import type { Member } from './lib/members'
import type { CustomRule, Suggestion } from './lib/rules'
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

export type View = 'dashboard' | 'invoices' | 'validation' | 'rules' | 'workflows' | 'create' | 'detail' | 'clients' | 'customers' | 'reports' | 'settings'

// 'review' was added by M4-08-04 under its former name (plan B1/DRIFT-1) — one subtask
// ahead of story §6's original assignment (M4-08-05), because wizardHeader's index-2
// branch does not compile against this union and lib/importFlow.ts's STAGE_OF is a total
// Record over it. INVCR-01-03 dropped the mock validate/approve tail's own two steps: the
// manual path is now ONE screen with one round trip, and the affirmation is the real
// invoice detail view rendering the server's row — there is deliberately no step between
// 'form' and that. INVCR-01-04 renamed the last member to 'review' ([three-stages]): the
// stage is the user's REVIEW SURFACE, not an import-report payload rendered on it — and
// INVCR-01-09 cashed that distinction by deleting CreateReport.tsx outright. 'review' now
// renders ReviewBatch.tsx off `reviewBatchId` + two live GETs, which is also why the step
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
export type SettingsTab = 'members' | 'connectors' | 'api' | 'signing' | 'company'

export type ConnectorId = 'sap' | 'quickbooks' | 'oracle' | 'sage' | 'odoo' | 'dynamics'

export type ConnectorsState = Record<ConnectorId, boolean>

// One row of a connector's ERP -> UBL field mapping: the ERP's native field name and
// the UBL 2.1 path it feeds (see data.tsx for each connector's defaults).
export type FieldMapRow = { erp: string; ubl: string }

// Mappings edited in the field-mapping modal, by connector. A connector absent here
// still renders its default mapping — only edited ones are held.
export type ConnectorMappings = Partial<Record<ConnectorId, FieldMapRow[]>>

// Sidebar nav ids — a superset of `View`: 'approvals' is a synthetic in-house-mode nav
// item that `nav()` translates into `{ view: 'invoices', filter: 'Pending' }`.
export type NavId = View | 'approvals'

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
  mapping: Mapping | null
  armedField: string | null
  dragField: string | null
  selectedId: string | null
  filter: string
  switcherOpen: boolean
  sandbox: boolean
  settingsTab: SettingsTab
  xmlOpen: boolean
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
  // Approval policies for the CURRENT WORKSPACE, already resolved out of the
  // per-mode store in App.tsx. Per mode, not per client: the prototype keys this set
  // on firm/in-house and switching company in firm mode does not change it, which is
  // also why the nav item sits in the firm-wide sidebar group (lib/workflows.ts).
  //
  // Only the store and "which policy is open" live here. Everything transient inside
  // the builder — node selection, the drag/drop hint, the scenario inputs, the saved
  // flash — is local to WorkflowsView, following the ClientsView precedent: it is
  // derived from that one view and nothing else reads it.
  policies: Policy[]
  /** Id of the policy open in the builder; null shows the policy list. */
  editingPolicyId: string | null

  // --- Settings › Members tab -----------------------------------------------
  // The CURRENT WORKSPACE's people, already resolved out of the per-mode store in
  // App.tsx — per mode, not per client, the same reasoning as `policies` above.
  // Held on ctx rather than in MembersView because in-house Workflows resolves
  // approval positions to these people, so two surfaces read the one list.
  //
  // Everything transient inside the tab — search text, the role filter, which drawer
  // or menu is open, the invite modal — is local to MembersView, following the
  // SettingsView precedent at SettingsView.tsx:6-9 rather than the openRuleKey /
  // editingPolicyId one: those are screens reachable by nav, this is a tab panel.
  members: Member[]

  // --- Multi-invoice import path (M4-08-04) ---------------------------------
  // These live on ctx rather than in CreateUpload's local state because the two
  // halves of the flow are two components: the file is chosen in CreateUpload, which
  // UNMOUNTS when createStep leaves 'upload', while createImport fires from
  // CreateMapping. Local state would lose it in between. (`entityId` no longer has a
  // picker of its own since [import-upload-unify] — it mirrors `active` — but it is
  // read at createImport time, so it must survive that unmount too.)
  entityId: string | null
  importFile: File | null
  preview: ImportPreview | null
  uploadPhase: UploadPhase
  importError: ApiError | null
  // The batch the review step is showing (INVCR-01-09). REPLACES the old
  // `report: ImportReport | null`, which was the POST's frozen 201 payload held in
  // memory: D4 made the review screen revisitable by URL (`#review/<uuid>`), so it
  // re-fetches from GET /v1/imports/{id} + the list endpoint's own totals instead, and
  // a stale in-memory report is exactly the frozen-counter source that replaced. An id
  // is all any consumer needs; nothing may resurrect the payload.
  reviewBatchId: string | null
  // Set by openImportedInvoice (M4-08-05) when the user clicks through to a real
  // invoice. Mutually exclusive with `selectedId` by construction — both are members
  // of one DetailSelection atom in App.tsx (lib/importReport.ts), so neither can be left
  // stale when the other is written. Non-null makes InvoiceDetail render its honest
  // placeholder instead of resolving a mock invoice; M4-09 swaps that for a real fetch.
  importedInvoiceId: string | null

  nav: (id: NavId) => void
  setFilter: (f: string) => void
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
  selectImportFile: (f: File | null) => void
  readColumns: () => void
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
  openXml: () => void
  closeXml: () => void
  // Rules screen. There is deliberately no editCustomRule/disableGoldenRule pair:
  // a tenant may adopt, switch off and remove its OWN rules, and may do nothing at
  // all to an inherited one.
  openRule: (key: string) => void
  closeRule: () => void
  addSuggestedRule: (s: Suggestion) => void
  toggleCustomRule: (key: string) => void
  removeCustomRule: (key: string) => void
  // Workflows screen. `savePolicy` is the ONE write funnel: the builder composes the
  // next Policy with the pure reducers in lib/workflows.ts and hands the whole object
  // back, so App.tsx never needs to know the node tree's shape.
  openPolicy: (id: string) => void
  closePolicy: () => void
  createPolicy: () => void
  deletePolicy: (id: string) => void
  savePolicy: (next: Policy) => void
  // Settings › Members. Same one-funnel contract as `savePolicy`: the tab composes the
  // next Member(s) with the pure reducers in lib/members.ts and hands whole objects
  // back, so App.tsx never needs to know a member's shape. Deliberately no
  // suspend/reactivate/setRole pair — those are `saveMember` with a different row.
  saveMember: (next: Member) => void
  inviteMembers: (next: Member[]) => void
  dropMember: (id: string) => void
  signOut: () => void
}
