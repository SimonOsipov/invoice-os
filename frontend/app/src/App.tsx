import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { FILE_DATA, INHOUSE_IDX, PARSE_LABELS } from './data'
import { APP_PERSONAS, landingBase, signIn, type Persona, type PersonaId, type Session } from './auth'
import { SignIn, SignInLoading } from './components/SignIn'
import { loadSession, saveSession, clearSession, shouldAutoSignIn } from './lib/session'
import { gatewayBase, toApiError, type ApiError } from '@invoice-os/api-client'
import { makeAuthedFetch } from './lib/authedFetch'
import { buildClients, defaultDraft } from './lib/clients'
import { validate } from './lib/validation'
// groupInvoices is NOT imported here any more — backToEdit/continueMapping stopped
// calling it. It still lives in lib/mapping.ts and CreateReview.tsx still calls it;
// M4-08-06 owns removing both.
import { initMapping, initMappingFromHeaders, toImportMapping } from './lib/mapping'
import { canReadColumns, canStartImport } from './lib/importFlow'
import {
  createImport,
  makeImportAuth,
  previewImport,
  type ImportPreview,
  type ImportReport,
  type UploadPhase,
} from './lib/importApi'
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
  const [selectedId, setSelectedId] = useState<string | null>(null)
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
    setSelectedId(null)
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
  // leaving it always lands back on the form. The old groupInvoices(...) > 1 -> 'review'
  // branch was the browser-side grouping verdict this story removes.
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
    const fileId = uploadFile
    setCreateStep('parsing')
    setParseIdx(0)
    parseTimer.current = setInterval(() => {
      setParseIdx((prev) => {
        const next = prev + 1
        if (next >= TOTAL) {
          if (parseTimer.current) clearInterval(parseTimer.current)
          parseTimer.current = null
          // Tabular files hand off to Map; PDF/JPG have no columns to map, so
          // they go straight to the single-invoice form as before.
          parseDone.current = setTimeout(() => {
            if (FILE_DATA[fileId]) {
              setMapping(initMapping(fileId))
              setCreateStep('mapping')
            } else {
              setCreateStep('form')
            }
          }, 320)
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

  function backToMapping() {
    clearVal()
    setCreateStep('mapping')
  }

  function createDrafts() {
    runValidation()
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
    setSelectedId(inv.number)
  }

  function selectInvoice(number: string) {
    setView('detail')
    setSelectedId(number)
  }

  function toggleSandbox() {
    setSandbox((s) => !s)
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

  function transmit() {
    const idx = activeIdx
    const sel = selectedId
    setClients((cs) =>
      cs.map((c, i) => (i === idx ? { ...c, invoices: c.invoices.map((inv) => (inv.number === sel ? { ...inv, status: 'Transmitted' as const } : inv)) } : c)),
    )
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
    selectedId,
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
    startImport,
    backToImport,
    backToMapping,
    createDrafts,
    skipUpload,
    approve,
    selectInvoice,
    toggleSandbox,
    setSettingsTab,
    toggleConnector,
    saveConnectorMapping,
    openXml,
    closeXml,
    transmit,
    signOut: onSignOut,
  }

  return (
    <div
      className="if-v2 pf-shell"
      style={{ height: '100vh', display: 'flex', background: 'var(--bg-1)', fontFamily: 'var(--font-sans)', color: 'var(--fg-1)', overflow: 'hidden' }}
    >
      <Sidebar ctx={ctx} />
      <main className="pf-main" style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <Header ctx={ctx} />
        {sandbox && (
          <div style={{ flex: 'none', background: 'var(--status-amber-bg)', borderBottom: '1px solid var(--status-amber-border)', padding: '7px 24px', display: 'flex', alignItems: 'center', gap: 9 }}>
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.7} strokeLinecap="round" strokeLinejoin="round" style={{ color: 'var(--status-amber-text)', flex: 'none' }}>
              <path d="M9 3h6M10 3v6.5L5.5 17a2 2 0 0 0 1.8 3h9.4a2 2 0 0 0 1.8-3L14 9.5V3" />
              <path d="M7.5 14h9" />
            </svg>
            <span style={{ fontSize: 12.5, color: 'var(--status-amber-text)', fontWeight: 500 }}>
              Sandbox environment — transmissions are simulated against the FIRS test adapter. No live data is sent.
            </span>
          </div>
        )}
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
  // Lazy initializer: synchronously rehydrate a persisted session at boot (no network,
  // no SignIn flash) so a reload / new tab returns straight to the workspace.
  const [session, setSession] = useState<Session | null>(() => loadSession())
  const [signingIn, setSigningIn] = useState<PersonaId | null>(null)
  // Persona to auto-sign-in from a landing deep-link (?persona=), resolved ONCE at boot
  // from the same guard the mount effect below uses: non-null only when boot produced NO
  // session AND the param names a known persona. A non-null value means an auto-sign-in
  // is in flight, so the render gate shows a loading splash instead of the "Choose an
  // account" picker — the landing → app hand-off never flashes that redundant card
  // before the mint → /me round trip resolves.
  const [autoPersona] = useState<PersonaId | null>(() => {
    const p = new URLSearchParams(window.location.search).get('persona')
    return shouldAutoSignIn(session, p) ? (p as PersonaId) : null
  })

  // Mirror the session to storage: persist while signed in, wipe on sign out / cleared session.
  useEffect(() => {
    if (session) saveSession(session)
    else clearSession()
  }, [session])

  // Sign out returns the user to the marketing landing page (the real sign-in front
  // door). Nulling React state alone would only swap in the app's own minimal
  // persona-picker AND leave any `?persona=` deep-link in the URL — so the next reload
  // would auto-sign the same persona straight back in (see the mount effect below),
  // defeating the logout. Wiping the persisted session then navigating away drops the
  // param and lands on landing. Also the 401 handler (makeAuthedFetch → onSignOut):
  // an invalidated session belongs back at the front door, not the in-app picker.
  const signOut = useCallback(() => {
    clearSession()
    // landingBase() is null when VITE_LANDING_URL isn't configured (e.g. the default
    // standalone showcase build) — never navigate to `null` (stringifies to "null").
    // The already-cleared session still falls back to the app's own persona-picker.
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
  // persona. autoPersona already encodes the shouldAutoSignIn guard (boot had no session
  // AND a known persona param), resolved once at boot, so a rehydrated session wins over a
  // stale deep-link param and this fires at most once on mount.
  useEffect(() => {
    if (autoPersona) void doSignIn(APP_PERSONAS[autoPersona])
  }, [autoPersona, doSignIn])

  if (!session) {
    // A deep-link auto-sign-in is in flight: show a loading splash, NOT the persona
    // picker, so the landing → app hand-off doesn't flash "Choose an account" before the
    // mint → /me round trip resolves. The picker only renders for a direct visit with no
    // (valid) ?persona= deep link.
    return autoPersona ? <SignInLoading persona={APP_PERSONAS[autoPersona]} /> : <SignIn signingIn={signingIn} onPick={doSignIn} />
  }
  return <Workspace session={session} onSignOut={signOut} />
}
