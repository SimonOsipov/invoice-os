import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { INHOUSE_IDX, PARSE_LABELS } from './data'
import { APP_PERSONAS, landingBase, signIn, type Persona, type PersonaId, type Session } from './auth'
import { SignIn, SignInLoading } from './components/SignIn'
import { resolveBootSession, saveSession, clearSession, shouldAutoSignIn } from './lib/session'
import { gatewayBase, toApiError, type ApiError } from '@invoice-os/api-client'
import { makeAuthedFetch } from './lib/authedFetch'
import { buildClients, defaultDraft } from './lib/clients'
import { validate } from './lib/validation'
import { initMappingFromHeaders, toImportMapping } from './lib/mapping'
import { canReadColumns, canStartImport } from './lib/importFlow'
import { clearSelection, selectImported, selectMock, type DetailSelection } from './lib/importReport'
import {
  createImport,
  makeImportAuth,
  previewImport,
  type ImportPreview,
  type ImportReport,
  type UploadPhase,
} from './lib/importApi'
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
  ValidationResult,
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
  const initialIdx = session.persona.mode === 'inhouse' ? INHOUSE_IDX : 1
  const [clients, setClients] = useState<Client[]>(() => buildClients())
  // Workspace type is a property of the authenticated identity, not a user-flippable
  // view: the firm persona gets the firm workspace, the in-house persona the in-house
  // workspace, and there is no in-app switch between them (that would require signing
  // in as the other persona). Under GoTrue (M8) this keys off the token's role/tenant.
  const mode: Mode = session.persona.mode
  const [view, setView] = useState<View>('dashboard')
  const [activeIdx, setActiveIdx] = useState(initialIdx)
  const [draft, setDraft] = useState<Draft>(() => defaultDraft(clients[initialIdx]))
  const [createStep, setCreateStep] = useState<CreateStep>('form')
  const [validation, setValidation] = useState<ValidationResult | null>(null)
  const [uploadFile, setUploadFile] = useState<string | null>(null)
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
  const [valIdx, setValIdx] = useState(0)
  const [parseIdx, setParseIdx] = useState(0)
  // Multi-invoice import path (M4-08-04). `entityId` is a REAL portfolio entity id
  // chosen by the user — never derived from `active`, which comes from buildClients()
  // and carries no server id at all, so guessing from it would file invoices under the
  // wrong supplier TIN ([entity-picker]).
  const [entityId, setEntityId] = useState<string | null>(null)
  const [importFile, setImportFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<ImportPreview | null>(null)
  const [uploadPhase, setUploadPhase] = useState<UploadPhase>({ kind: 'idle' })
  const [importError, setImportError] = useState<ApiError | null>(null)
  const [report, setReport] = useState<ImportReport | null>(null)

  // Re-entrancy guard for the two import round trips. A ref, not state: React batches
  // state updates, so a fast double-click can fire the handler twice before a `disabled`
  // prop re-renders — and for startImport that would create the SAME import twice, i.e.
  // duplicate invoices. readColumns and startImport live on different steps and can
  // never overlap, so one flag covers both. A ref also cannot get stuck the way a
  // component-local flag would: CreateUpload never observes the rejection that would
  // clear it, since errors come back through ctx.importError.
  const reqInFlight = useRef(false)

  const valTimer = useRef<ReturnType<typeof setInterval> | null>(null)
  const valDone = useRef<ReturnType<typeof setTimeout> | null>(null)
  const parseTimer = useRef<ReturnType<typeof setInterval> | null>(null)
  const parseDone = useRef<ReturnType<typeof setTimeout> | null>(null)

  const clearVal = () => {
    if (valTimer.current) { clearInterval(valTimer.current); valTimer.current = null }
    if (valDone.current) { clearTimeout(valDone.current); valDone.current = null }
    if (parseTimer.current) { clearInterval(parseTimer.current); parseTimer.current = null }
    if (parseDone.current) { clearTimeout(parseDone.current); parseDone.current = null }
  }

  useEffect(() => clearVal, [])

  const authedFetch = useMemo(() => makeAuthedFetch(session, onSignOut), [session, onSignOut])
  // Same (session, onSignOut) pair, one construction site — the multipart XHR transport
  // cannot drift from the fetch path on auth or the 401 sign-out (importApi.ts D3).
  const importAuth = useMemo(() => makeImportAuth(session, onSignOut), [session, onSignOut])

  const active = clients[activeIdx]

  function nav(id: NavId) {
    if (id === 'approvals') { setView('invoices'); setFilter('Pending'); setSwitcherOpen(false); return }
    if (id === 'invoices') { setView('invoices'); setFilter('all'); setSwitcherOpen(false); return }
    setView(id as View)
    setSwitcherOpen(false)
  }

  function toggleSwitcher() {
    setSwitcherOpen((o) => !o)
  }

  function switchClient(i: number) {
    setActiveIdx(i)
    setView('dashboard')
    setDetailSel(clearSelection())
    setFilter('all')
    setSwitcherOpen(false)
    setDraft(defaultDraft(clients[i]))
    setCreateStep('form')
    setValidation(null)
  }

  function openCreate() {
    clearVal()
    setView('create')
    setCreateStep('upload')
    setDraft(defaultDraft(clients[activeIdx]))
    setValidation(null)
    setUploadFile(null)
    setMapping(null)
    setSwitcherOpen(false)
    resetImport()
  }

  // Every piece of import state is per-run: a second import must not inherit the first
  // one's preview, progress, error or report. `entityId` is deliberately included —
  // re-picking the entity is one click, and silently carrying the previous choice into
  // a fresh run is the [entity-picker] hazard in a slower form.
  function resetImport() {
    setEntityId(null)
    setImportFile(null)
    setPreview(null)
    setUploadPhase({ kind: 'idle' })
    setImportError(null)
    setReport(null)
  }

  function closeCreate() {
    clearVal()
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

  function runValidation() {
    clearVal()
    const TOTAL = 16
    const draftAtRun = draft
    setCreateStep('validating')
    setValIdx(0)
    valTimer.current = setInterval(() => {
      setValIdx((prev) => {
        const next = prev + 1
        if (next >= TOTAL) {
          if (valTimer.current) clearInterval(valTimer.current)
          valTimer.current = null
          valDone.current = setTimeout(() => {
            setCreateStep('results')
            setValidation(validate(draftAtRun))
          }, 300)
          return TOTAL
        }
        return next
      })
    }, 95)
  }

  function applyFix(patch: Partial<Draft>) {
    const nd = { ...draft, ...patch }
    setDraft(nd)
    setValidation(validate(nd))
  }

  // The results screen is reachable only from the single-document path now (the
  // multi-invoice path ends at the server's report, not at a locally-built draft), so
  // leaving it always lands back on the form.
  function backToEdit() {
    clearVal()
    setCreateStep('form')
  }

  function selectFile(id: string) {
    setUploadFile(id)
  }

  function selectEntity(id: string | null) {
    setEntityId(id)
    setImportError(null)
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
    if (base == null || !importFile || !canReadColumns(entityId, importFile)) return
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

  function startImport() {
    const base = gatewayBase()
    if (base == null || !importFile || !entityId || !mapping || !canStartImport(preview, mapping)) return
    if (reqInFlight.current) return
    reqInFlight.current = true
    setImportError(null)
    setReport(null)
    // Seed 'sending' with an unknown total: uploadPercent maps total 0 to null, so the
    // UI opens on the indeterminate spinner and only flips to a determinate bar if the
    // transport actually reports a computable length. Zero progress events is legal
    // (importApi IMPAPI-08), so nothing here may assume a determinate frame ever lands.
    setUploadPhase({ kind: 'sending', loaded: 0, total: 0 })
    createImport(importAuth, base, { file: importFile, entityId, mapping: toImportMapping(mapping) }, setUploadPhase)
      .then(
        (res) => {
          setReport(res)
          setCreateStep('report')
        },
        // Stays on 'mapping' on purpose (AC5): a failed import must not advance to a
        // report step that has no report to show.
        (err: unknown) => setImportError(toApiError(err)),
      )
      .finally(() => {
        reqInFlight.current = false
      })
  }

  function parseFile() {
    if (!uploadFile) return
    clearVal()
    const TOTAL = PARSE_LABELS.length
    setCreateStep('parsing')
    setParseIdx(0)
    parseTimer.current = setInterval(() => {
      setParseIdx((prev) => {
        const next = prev + 1
        if (next >= TOTAL) {
          if (parseTimer.current) clearInterval(parseTimer.current)
          parseTimer.current = null
          // Sample documents have no columns to map, so they go straight to the
          // single-invoice form. Spreadsheets take the server-backed import path
          // (Import -> Map -> Report), which never routes through here.
          parseDone.current = setTimeout(() => setCreateStep('form'), 320)
          return TOTAL
        }
        return next
      })
    }, 200)
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

  function continueMapping() {
    if (!mapping || !mapping.invoice_number) return
    startImport()
  }

  function backToImport() {
    clearVal()
    setCreateStep('upload')
  }

  function skipUpload() {
    clearVal()
    setCreateStep('form')
    setUploadFile(null)
    setMapping(null)
  }

  function approve() {
    if (!validation || validation.errors.length > 0 || validation.warnings.length > 0) return
    const d = draft
    const inv = { number: d.number, buyer: d.buyer, buyerTin: d.buyerTin, buyerAddress: d.buyerAddress, date: d.date, items: d.items, status: 'Approved' as const, wht: d.wht, docType: d.docType || 'B2B' }
    setClients((cs) => cs.map((c, idx) => (idx === activeIdx ? { ...c, invoices: [inv, ...c.invoices] } : c)))
    setView('detail')
    setDetailSel(selectMock(inv.number))
  }

  function selectInvoice(number: string) {
    setView('detail')
    setDetailSel(selectMock(number))
  }

  // Click-through from a rule-violation row in the import report (Core AC4). `id` is a
  // real invoice UUID (invoice_violations[].invoice_id), NOT a mock invoice number — so
  // InvoiceDetail renders its placeholder rather than resolving it against active.invoices.
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
    mode,
    view,
    activeIdx,
    draft,
    createStep,
    validation,
    uploadFile,
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
    valIdx,
    parseIdx,
    entityId,
    importFile,
    preview,
    uploadPhase,
    importError,
    report,
    importedInvoiceId: detailSel.importedInvoiceId,
    nav,
    setFilter,
    toggleSwitcher,
    switchClient,
    openCreate,
    closeCreate,
    updateDraft,
    updateItem,
    runValidation,
    applyFix,
    backToEdit,
    selectFile,
    parseFile,
    armField,
    setDrag,
    endDrag,
    dropOn,
    clickCol,
    unmap,
    continueMapping,
    selectEntity,
    selectImportFile,
    readColumns,
    backToImport,
    skipUpload,
    approve,
    selectInvoice,
    openImportedInvoice,
    setSandbox,
    setSettingsTab,
    toggleConnector,
    saveConnectorMapping,
    openXml,
    closeXml,
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
