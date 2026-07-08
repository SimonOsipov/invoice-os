// Domain types re-authored from the prototype's `this.state` / seed-data shapes
// (Platform.dc.html, class Component extends DCLogic).

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

// Shape shared by both `Invoice` and `Draft` — everything `validate()` reads.
export type Validatable = {
  buyerTin: string
  buyerAddress: string
  items: LineItem[]
  wht: boolean
}

export type Draft = Validatable & {
  number: string
  buyer: string
  date: string
  currency: string
  docType: DocType
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

export type ReadinessMetric = { label: string; pct: string; color: string }

export type KpiCard = {
  label: string
  value: string
  delta: string
  deltaColor: string
  stroke: string
  spark: string
}

export type ChartScore = {
  line: string
  area: string
  grid: string[]
  months: string[]
  now: number
  deltaLabel: string
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

export type ActivityRow = {
  who: string
  action: string
  target: string
  time: string
  dot: string
  line: string
}

export type StatusStyle = { bg: string; border: string; text: string; label: string }

export type DashboardData = {
  score: number
  ring: { circ: string; offset: string; color: string }
  readinessNote: string
  readinessMetrics: ReadinessMetric[]
  failing: number
  resolveLabel: string
  kpis: KpiCard[]
  chart: ChartScore
  donut: DonutSeg[]
  donutMeta: { r: number; total: string }
  failures: FailureRow[]
  hasFailures: boolean
  noFailures: boolean
  activity: ActivityRow[]
  pill: StatusStyle
}

// Fully-built client: seed config + generated invoices + precomputed dashboard.
export type Client = ClientCfg & {
  invoices: Invoice[]
  failing: number | '—'
  pending: number
  vatNum: number
  vatLabel: string
  count: number
  head: string
  dash: DashboardData | null
}

export type ValidationIssue = {
  id: string
  label: string
  detail: string
  fixLabel: string
  patch: Partial<Draft>
}

export type ValidationResult = {
  errors: ValidationIssue[]
  warnings: ValidationIssue[]
  passed: string[]
}

export type Mode = 'firm' | 'inhouse'

export type View = 'dashboard' | 'invoices' | 'create' | 'detail' | 'clients' | 'customers' | 'reports' | 'settings'

export type CreateStep = 'upload' | 'parsing' | 'form' | 'validating' | 'results'

export type SettingsTab = 'connectors' | 'api' | 'signing'

export type ConnectorId = 'sap' | 'quickbooks' | 'oracle' | 'sage' | 'odoo' | 'dynamics'

export type ConnectorsState = Record<ConnectorId, boolean>

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
  user: SignedInUser
  clients: Client[]
  active: Client
  mode: Mode
  view: View
  activeIdx: number
  draft: Draft
  createStep: CreateStep
  validation: ValidationResult | null
  uploadFile: string | null
  selectedId: string | null
  filter: string
  switcherOpen: boolean
  sandbox: boolean
  settingsTab: SettingsTab
  xmlOpen: boolean
  connectors: ConnectorsState
  valIdx: number
  parseIdx: number

  nav: (id: NavId) => void
  setFilter: (f: string) => void
  setMode: (m: Mode) => void
  toggleSwitcher: () => void
  switchClient: (i: number) => void
  openCreate: () => void
  closeCreate: () => void
  updateDraft: <K extends keyof Draft>(field: K, value: Draft[K]) => void
  updateItem: (i: number, field: 'qty' | 'price', val: string) => void
  runValidation: () => void
  applyFix: (patch: Partial<Draft>) => void
  backToEdit: () => void
  selectFile: (id: string) => void
  parseFile: () => void
  skipUpload: () => void
  approve: () => void
  selectInvoice: (number: string) => void
  toggleSandbox: () => void
  setSettingsTab: (t: SettingsTab) => void
  toggleConnector: (id: ConnectorId) => void
  openXml: () => void
  closeXml: () => void
  transmit: () => void
}
