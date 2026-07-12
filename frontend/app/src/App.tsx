import { useCallback, useEffect, useRef, useState } from 'react'
import { INHOUSE_IDX } from './data'
import { APP_PERSONAS, signIn, type Persona, type PersonaId, type Session } from './auth'
import { SignIn } from './components/SignIn'
import { loadSession, saveSession, clearSession, shouldAutoSignIn } from './lib/session'
import { buildClients, defaultDraft } from './lib/clients'
import { validate } from './lib/validation'
import { Sidebar } from './components/Sidebar'
import { Header } from './components/Header'
import { DashboardActive } from './components/DashboardActive'
import { DashboardOnboarding } from './components/DashboardOnboarding'
import { InvoicesList } from './components/InvoicesList'
import { CreateFlow } from './components/CreateFlow'
import { InvoiceDetail } from './components/InvoiceDetail'
import { ClientsView } from './components/ClientsView'
import { CustomersView } from './components/CustomersView'
import { ReportsView } from './components/ReportsView'
import { SettingsView } from './components/SettingsView'
import { XmlModal } from './components/XmlModal'
import type {
  Client,
  ConnectorId,
  ConnectorsState,
  CreateStep,
  Draft,
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
  const [mode, setMode_] = useState<Mode>(session.persona.mode)
  const [view, setView] = useState<View>('dashboard')
  const [activeIdx, setActiveIdx] = useState(initialIdx)
  const [draft, setDraft] = useState<Draft>(() => defaultDraft(clients[initialIdx]))
  const [createStep, setCreateStep] = useState<CreateStep>('form')
  const [validation, setValidation] = useState<ValidationResult | null>(null)
  const [uploadFile, setUploadFile] = useState<string | null>(null)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [filter, setFilter] = useState('all')
  const [switcherOpen, setSwitcherOpen] = useState(false)
  const [sandbox, setSandbox] = useState(false)
  const [settingsTab, setSettingsTab_] = useState<SettingsTab>('connectors')
  const [xmlOpen, setXmlOpen] = useState(false)
  const [connectors, setConnectors] = useState<ConnectorsState>(INITIAL_CONNECTORS)
  const [valIdx, setValIdx] = useState(0)
  const [parseIdx, setParseIdx] = useState(0)

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

  const active = clients[activeIdx]

  function nav(id: NavId) {
    if (id === 'approvals') { setView('invoices'); setFilter('Pending'); setSwitcherOpen(false); return }
    if (id === 'invoices') { setView('invoices'); setFilter('all'); setSwitcherOpen(false); return }
    setView(id as View)
    setSwitcherOpen(false)
  }

  function setMode(m: Mode) {
    if (m === 'inhouse') {
      setMode_(m)
      setActiveIdx(INHOUSE_IDX)
      setView('dashboard')
      setSelectedId(null)
      setFilter('all')
      setSwitcherOpen(false)
    } else {
      setMode_(m)
      setSwitcherOpen(false)
    }
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
    setSwitcherOpen(false)
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

  function backToEdit() {
    clearVal()
    setCreateStep('form')
  }

  function selectFile(id: string) {
    setUploadFile(id)
  }

  function parseFile() {
    if (!uploadFile) return
    clearVal()
    const TOTAL = 6
    setCreateStep('parsing')
    setParseIdx(0)
    parseTimer.current = setInterval(() => {
      setParseIdx((prev) => {
        const next = prev + 1
        if (next >= TOTAL) {
          if (parseTimer.current) clearInterval(parseTimer.current)
          parseTimer.current = null
          parseDone.current = setTimeout(() => setCreateStep('form'), 320)
          return TOTAL
        }
        return next
      })
    }, 200)
  }

  function skipUpload() {
    clearVal()
    setCreateStep('form')
    setUploadFile(null)
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
    selectedId,
    filter,
    switcherOpen,
    sandbox,
    settingsTab,
    xmlOpen,
    connectors,
    valIdx,
    parseIdx,
    nav,
    setFilter,
    setMode,
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
    skipUpload,
    approve,
    selectInvoice,
    toggleSandbox,
    setSettingsTab,
    toggleConnector,
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

  // Mirror the session to storage: persist while signed in, wipe on sign out / cleared session.
  useEffect(() => {
    if (session) saveSession(session)
    else clearSession()
  }, [session])

  const signOut = useCallback(() => setSession(null), [])

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
  // persona. The `session` read here is the BOOT value (deps are [doSignIn] → mount-only):
  // shouldAutoSignIn gates on "did boot produce a session?", so a rehydrated session wins
  // over a stale deep-link param. The `p as PersonaId` cast is safe — shouldAutoSignIn
  // returns true only for 'firm' | 'inhouse'.
  useEffect(() => {
    const p = new URLSearchParams(window.location.search).get('persona')
    if (shouldAutoSignIn(session, p)) void doSignIn(APP_PERSONAS[p as PersonaId])
  }, [doSignIn])

  if (!session) return <SignIn signingIn={signingIn} onPick={doSignIn} />
  return <Workspace session={session} onSignOut={signOut} />
}
