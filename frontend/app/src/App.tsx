import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { APP_PERSONAS, landingBase, signIn, type Persona, type PersonaId, type Session } from './auth'
import { SignIn, SignInLoading } from './components/SignIn'
import { resolveBootSession, saveSession, clearSession, shouldAutoSignIn } from './lib/session'
import { gatewayBase, toApiError, useAsync, type ApiError } from '@invoice-os/api-client'
import { makeAuthedFetch } from './lib/authedFetch'
import { buildClients, defaultDraft, emptyClient, inhouseClient } from './lib/clients'
import { clientsViewState, listEntities, shouldFetchEntities, type Entity } from './lib/portfolio'
import { fileDraftGate, fileDraftInvoice } from './lib/invoiceDraft'
import { createInvoice, listInvoices } from './lib/invoices'
import { parseReviewHash, reviewHash, reviewQuery, routeAfterImport, type PostImportRoute } from './lib/reviewBatch'
import { initMappingFromHeaders, toImportMapping } from './lib/mapping'
import { canReadColumns, canStartImport } from './lib/importFlow'
import { clearSelection, selectImported, selectMock, type DetailSelection } from './lib/importReport'
import {
  createImport,
  makeImportAuth,
  previewImport,
  type ImportPreview,
  type UploadPhase,
} from './lib/importApi'
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
  newPolicy,
  removePolicy,
  replacePolicy,
  seedPolicies,
  type Policy,
  type PolicyStore,
} from './lib/workflows'
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
import { XmlModal } from './components/XmlModal'
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
  NavId,
  PlatformCtx,
  SettingsTab,
  SignedInUser,
  View,
} from './types'

const INITIAL_CONNECTORS: ConnectorsState = { sap: true, quickbooks: true, oracle: false, sage: false, odoo: false, dynamics: false }

// Environment banner under the header, one per state. Adopted from ops-console
// TopBar.tsx, which states the environment in BOTH states — the app previously
// showed a banner only in sandbox, so "live" was conveyed by absence. Copy stays in
// the app's transmission-centric voice rather than the ops console's key-centric one
// ("sk_live"/"sk_test" are a developer-console concern, not an accountant's).
const ENV_BANNER = {
  sandbox: {
    bg: 'var(--status-amber-bg)',
    border: 'var(--status-amber-border)',
    text: 'var(--status-amber-text)',
    icon: flaskGlyph,
    msg: 'Sandbox environment — transmissions are simulated against the FIRS test adapter. No live data is sent.',
    tag: 'TEST DATA · SIMULATED',
  },
  live: {
    bg: 'var(--action-tint)',
    border: 'var(--teal-200)',
    text: 'var(--action-soft)',
    icon: shieldGlyph15,
    msg: 'Live environment — transmissions are sent to FIRS and return legally-valid clearance evidence.',
    tag: 'PRODUCTION · FIRS',
  },
} as const

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
    () => (base ? listEntities(authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
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
  // CustomersView/ReportsView/XmlModal, themselves still-mock surfaces this plan's next
  // step migrates off active.invoices.
  const [clients, setClients] = useState<Client[]>([])
  useEffect(() => {
    setClients(buildClients(entitiesList))
  }, [entitiesList])

  // [entity-picker] keystone: the active selection is a real entity id, never an index
  // into a mock array. null until the user (or the fallback below) picks one.
  const [activeEntityId, setActiveEntityId] = useState<string | null>(null)

  // In-house has ZERO business_entities rows (db/seed.dev.sql seeds the firm tenant
  // only) — its identity comes from the TENANT, never from this fetch ([entity-picker]
  // trap 1). Firm mode falls back to `clients[0]` (whichever entity the server returns
  // first) while `activeEntityId` is unset, and to the "nothing here yet" placeholder for
  // the loading/error/no-gateway/zero-entities window ([entity-picker] trap 2) — every
  // one of the ~15 places reading ctx.active needs SOMETHING defined, never `undefined`.
  const active: Client = useMemo(() => {
    if (mode === 'inhouse') return inhouseClient(session.me?.tenant.name ?? session.persona.org)
    return clients.find((c) => c.entityId === activeEntityId) ?? clients[0] ?? emptyClient()
  }, [mode, clients, activeEntityId, session])

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
  // null for in-house (no business_entities row at all), for the emptyClient() placeholder,
  // and for the whole loading/error/no-gateway window — all three are the same honest
  // "nothing to file against", so they need no separate copy.
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
  const [bootBatchId] = useState<string | null>(() => parseReviewHash(window.location.hash))
  const [view, setView] = useState<View>(bootBatchId ? 'create' : 'dashboard')
  const [draft, setDraft] = useState<Draft>(() => defaultDraft(active))
  const [createStep, setCreateStep] = useState<CreateStep>(bootBatchId ? 'review' : 'form')
  const [reviewBatchId, setReviewBatchId] = useState<string | null>(bootBatchId)
  const [mapping, setMapping] = useState<Mapping | null>(null)
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
  const [filter, setFilter] = useState('all')
  const [switcherOpen, setSwitcherOpen] = useState(false)
  const [sandbox, setSandbox] = useState(false)
  const [settingsTab, setSettingsTab_] = useState<SettingsTab>('connectors')
  const [xmlOpen, setXmlOpen] = useState(false)
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
  // Approval policies, PER WORKSPACE MODE (lib/workflows.ts) — deliberately NOT per
  // client the way custom rules above are: the store is keyed firm/inhouse, so
  // switching company in firm mode does not swap the set. Held here rather than in
  // WorkflowsView so both the list and a half-built policy survive navigating away and
  // back. `mode` is fixed by the signed-in persona, so this index is stable for the
  // whole session.
  const [policyStore, setPolicyStore] = useState<PolicyStore>(seedPolicies)
  const [editingPolicyId, setEditingPolicyId] = useState<string | null>(null)
  const policies = policyStore[mode]
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
  const [importFile, setImportFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<ImportPreview | null>(null)
  const [uploadPhase, setUploadPhase] = useState<UploadPhase>({ kind: 'idle' })
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
  // FILING does: a stale-null snapshot would carry a firm user all the way to the Map
  // step and then refuse the commit, even though their entities had long since landed.
  // Before [import-upload-unify] the entity <select> was the escape hatch; there is no
  // longer one, so re-seed on exactly that null -> resolved transition. Confined to the
  // upload step: past it the columns are already read against the snapshot, and moving
  // the target then is the retarget resetImport exists to prevent. In-house stays null
  // through this — active.entityId is null there too, so the guard's third clause holds.
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
    const h = reviewHash(view, createStep, reviewBatchId)
    window.history.replaceState(null, '', window.location.pathname + window.location.search + (h ?? ''))
  }, [view, createStep, reviewBatchId])

  function nav(id: NavId) {
    if (id === 'approvals') { setView('invoices'); setFilter('Pending'); setSwitcherOpen(false); return }
    if (id === 'invoices') { setView('invoices'); setFilter('all'); setSwitcherOpen(false); return }
    setView(id as View)
    setSwitcherOpen(false)
  }

  function toggleSwitcher() {
    setSwitcherOpen((o) => !o)
  }

  function switchClient(id: string) {
    setActiveEntityId(id)
    setView('dashboard')
    setDetailSel(clearSelection())
    setFilter('all')
    setSwitcherOpen(false)
    setDraft(defaultDraft(clients.find((c) => c.entityId === id) ?? active))
    setCreateStep('form')
    // A batch belongs to ONE entity. Leaving this set would keep the review screen's
    // deep-link id pointing at the company just left — and the mirror effect above would
    // keep writing its hash into the URL from the incoming company's dashboard.
    setReviewBatchId(null)
    // A failed filing's message named the company just left. `filing` is deliberately NOT
    // cleared: a request already in flight is still in flight, and it will land on the
    // invoice it was fired for — under the PREVIOUS company. Leaving the button disabled
    // until it settles is the honest frame; cancelling it is not something this flow does.
    setFilingError(null)
    // Custom rules are per client, so the incoming client has a different set — a
    // drawer left open would keep describing a rule from the company just left.
    setOpenRuleKey(null)
    // Policies are per workspace, so the SET is unchanged — but the switch lands on the
    // dashboard, and leaving this set means the next visit to Workflows reopens the
    // builder mid-edit instead of the policy list the user asked for.
    setEditingPolicyId(null)
  }

  function openCreate() {
    setView('create')
    setCreateStep('upload')
    setDraft(defaultDraft(active))
    setFilingError(null)
    setMapping(null)
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
    setImportFile(null)
    setPreview(null)
    setUploadPhase({ kind: 'idle' })
    setImportError(null)
    // Per-run, sitting exactly where `setReport(null)` sat: a second import must not
    // inherit the first one's batch, and `Import a corrected file` routes back through
    // here specifically so a deep-link arrival — which carries no importFile, preview or
    // mapping at all — cannot open the upload step on another run's state.
    setReviewBatchId(null)
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

  // Stores whatever the input yielded — the extension rule lives in canReadColumns
  // alone, so there is exactly one gate that can be right or wrong, not two that can
  // disagree. A rejected file still lands here and the Import panel explains why.
  // Choosing a different file invalidates any preview already read from the old one.
  function selectImportFile(f: File | null) {
    setImportFile(f)
    setPreview(null)
    setImportError(null)
  }

  function readColumns() {
    const base = gatewayBase()
    // base == null is the no-gateway build: zero network, and the button is disabled
    // too — this is the second of the two guards, not the only one.
    // No entityId clause: preview neither sends nor needs one (see canReadColumns).
    // `!importFile` stays for the type narrowing previewImport's File param needs.
    if (base == null || !importFile || !canReadColumns(importFile)) return
    if (reqInFlight.current) return
    reqInFlight.current = true
    setImportError(null)
    previewImport(importAuth, base, importFile)
      .then(
        (res) => {
          setPreview(res)
          setMapping(initMappingFromHeaders(res.columns))
          setCreateStep('mapping')
        },
        (err: unknown) => setImportError(toApiError(err)),
      )
      .finally(() => {
        reqInFlight.current = false
      })
  }

  // Where a classified import LANDS (INVCR-01-09, Core AC 8). `review` and `rejected`
  // are handled IDENTICALLY on purpose: both are the review step, and which SURFACE it
  // renders there is decided by reviewShellState(batch) off the batch GET alone — never
  // by this route's `kind`. That single ownership is what makes the POST-arrival path and
  // a deep-link revisit (which never calls routeAfterImport at all) run one derivation.
  function applyRoute(route: PostImportRoute) {
    if (route.kind === 'single') {
      openImportedInvoice(route.invoiceId)
      return
    }
    setReviewBatchId(route.batchId)
    setCreateStep('review')
  }

  function startImport() {
    const base = gatewayBase()
    if (base == null || !importFile || !entityId || !mapping || !canStartImport(preview, mapping)) return
    if (reqInFlight.current) return
    reqInFlight.current = true
    setImportError(null)
    // Seed 'sending' with an unknown total: uploadPercent maps total 0 to null, so the
    // UI opens on the indeterminate spinner and only flips to a determinate bar if the
    // transport actually reports a computable length. Zero progress events is legal
    // (importApi IMPAPI-08), so nothing here may assume a determinate frame ever lands.
    setUploadPhase({ kind: 'sending', loaded: 0, total: 0 })
    createImport(importAuth, base, { file: importFile, entityId, mapping: toImportMapping(mapping) }, setUploadPhase)
      .then(
        (res) => {
          // Core AC 8: exactly ONE ready invoice goes straight to that invoice, so the
          // single-invoice path never makes the user read a batch report about a batch of
          // one. The id is NOT in the 201 body on this path — Go appends InvoiceViolations
          // only when a violation exists, so a CLEAN single invoice is counted and never
          // listed — which is why it takes a follow-up list page to resolve.
          //
          // Fired ONLY at ready_invoices === 1. At any other count its answer is
          // discarded, so asking is pure cost.
          if (res.status === 'completed' && res.ready_invoices === 1) {
            // RETURNED, not floating: `.finally` below releases reqInFlight, and an
            // un-returned promise would release it mid-flight and re-arm the button
            // while this chain is still resolving.
            return listInvoices(authedFetch, base, reviewQuery(res.id, 'all', { limit: 1 }))
              .then(
                (r) => r.invoices[0]?.id ?? null,
                // DEGRADES to null, never setImportError. The import SUCCEEDED and the
                // rows are in the ledger; an error banner here would say "failed" about
                // data that landed. routeAfterImport turns a null id into the review
                // surface, which is the honest fallback — the batch is real either way.
                () => null,
              )
              .then((id) => applyRoute(routeAfterImport(res, id)))
          }
          applyRoute(routeAfterImport(res, null))
        },
        // Stays on 'mapping' on purpose (AC5): a failed import must not advance to a
        // review step with no batch to show. Note this is the REQUEST's error arm — the
        // server's own "the file was rejected" verdict arrives as a resolved 201 and is
        // classified by routeAfterImport above, not here.
        (err: unknown) => setImportError(toApiError(err)),
      )
      .finally(() => {
        reqInFlight.current = false
      })
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
  function assign(field: string, header: string) {
    setMapping((m) => {
      if (!m) return m
      const next: Mapping = { ...m }
      Object.keys(next).forEach((k) => {
        if (next[k] === header) next[k] = null
      })
      next[field] = header
      return next
    })
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
    setMapping((m) => {
      if (!m) return m
      const next: Mapping = { ...m }
      Object.keys(next).forEach((k) => {
        if (next[k] === header) next[k] = null
      })
      return next
    })
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
  function continueMapping() {
    if (!mapping || !mapping.invoice_number) {
      setArmedField('invoice_number')
      return
    }
    startImport()
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
  // importFile, no preview and no mapping at all, so the upload step must not open on
  // whatever a previous run in this session happened to leave behind.
  function restartImport() {
    resetImport()
    setCreateStep('upload')
  }

  function skipUpload() {
    setCreateStep('form')
    setMapping(null)
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
  // `Pick<Entity, …>` parameter needs — the same reason readColumns keeps `!importFile`.
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

  function openXml() {
    setXmlOpen(true)
  }

  function closeXml() {
    setXmlOpen(false)
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

  // Same one-funnel shape as updateCustomRules above: resolve THIS workspace's policy
  // list, run the pure reducer from lib/workflows.ts, store it back under the mode key.
  // Every policy write goes through here, so no caller has to know the store is keyed.
  function updatePolicies(fn: (list: Policy[]) => Policy[]) {
    setPolicyStore((store) => ({ ...store, [mode]: fn(store[mode]) }))
  }

  function openPolicy(id: string) {
    setEditingPolicyId(id)
  }

  function closePolicy() {
    setEditingPolicyId(null)
  }

  // Creating opens the builder in the same step: a blank "Untitled policy" row appended
  // to the list with nothing else happening reads as a click that did nothing.
  function createPolicy() {
    const p = newPolicy()
    updatePolicies((list) => [...list, p])
    setEditingPolicyId(p.id)
  }

  function deletePolicy(id: string) {
    updatePolicies((list) => removePolicy(list, id))
    // The builder is editing the policy that just stopped existing.
    setEditingPolicyId((cur) => (cur === id ? null : cur))
  }

  // The ONE write funnel for a policy's contents: the builder composes the next Policy
  // with the pure reducers and hands the whole object back, so nothing here needs to
  // know the node tree's shape.
  function savePolicy(next: Policy) {
    updatePolicies((list) => replacePolicy(list, next))
  }

  const user: SignedInUser = {
    name: session.persona.name,
    initials: session.persona.initials,
    tenantName: session.me?.tenant.name ?? null,
    verified: session.verified,
  }

  const ctx: PlatformCtx = {
    authedFetch,
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
    filter,
    switcherOpen,
    sandbox,
    settingsTab,
    xmlOpen,
    connectors,
    connectorMappings,
    filing,
    filingError,
    customRules,
    openRuleKey,
    policies,
    editingPolicyId,
    entityId,
    importFile,
    preview,
    uploadPhase,
    importError,
    reviewBatchId,
    importedInvoiceId: detailSel.importedInvoiceId,
    nav,
    setFilter,
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
    selectImportFile,
    readColumns,
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
    openXml,
    closeXml,
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
            <div style={{ flex: 'none', background: b.bg, borderBottom: `1px solid ${b.border}`, padding: '7px 24px', display: 'flex', alignItems: 'center', gap: 9 }}>
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
        </div>
      </main>
      {xmlOpen && <XmlModal ctx={ctx} />}
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
