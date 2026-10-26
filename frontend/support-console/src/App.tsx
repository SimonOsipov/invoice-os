import { useCallback, useEffect, useRef, useState } from 'react'
import { Sidebar } from './components/Sidebar'
import { TopBar } from './components/TopBar'
import { Rules } from './components/Rules'
import { Tenants } from './components/Tenants'
import { Submissions } from './components/Submissions'
import { Audit, type AuditView } from './components/Audit'
import { AuditEntryDrawer, StaffChangeDrawer } from './components/AuditDrawer'
import { EvidenceDrawer, type EvidenceInputs } from './components/EvidenceDrawer'
import { Health } from './components/Health'
import { AiUsage, type AiView } from './components/AiUsage'
import { Blog } from './components/Blog'
import { RuleDrawer } from './components/RuleDrawer'
import { CodeListDrawer } from './components/CodeListDrawer'
import { KillConfirm } from './components/KillConfirm'
import { JobDrawer } from './components/JobDrawer'
import { JobActionConfirm, type JobAction, REASON_NOTICE } from './components/JobActionConfirm'
import { DeletionConfirm, PURGE_REASON_NOTICE } from './components/DeletionConfirm'
import { PublishModal } from './components/PublishModal'
import { DraftTestModal } from './components/DraftTestModal'
import { Toast } from './components/Toast'
import { type ApiError, gatewayBase, toApiError, useAsync } from '@invoice-os/api-client'
import {
  addDraftRule,
  diffRules,
  editDraftRule,
  fetchCodeListSummary,
  fetchCodeListSyncs,
  fetchRules,
  fetchVersions,
  openDraft,
  publishDraft,
  publishWarnings,
  removeDraftRule,
  ruleBody,
  switchRule,
  testDraft,
  type DiffRow,
  type DraftTestResult,
  type RuleForm,
  type RuleVersion,
  type RulesInForce,
} from './rulesApi'
import { cancelJob, fetchJob, fetchJobs, redriveJob, type JobDetail, type JobState, type StaffJob } from './jobsApi'
import { defaultBundlePeriod, downloadBundle, fetchAuditEvents, fetchBundlePreview, fetchStaffChanges, fetchStaffEntities, rangeInverted, staffChangeLink, type AuditFilters, type StaffAuditPage, type StaffAuditRow, type StaffChangeLink } from './auditApi'
import { fetchAiCalls, fetchAiTotals, type AiCallsPage, type AiFilters, type AiTotalsPage } from './aiUsageApi'
import { fetchStaffHealth, type StaffHealth } from './healthApi'
import { DEFAULT_RANGE, fetchStaffMetrics, HEALTH_REFRESH_MS, type RangeKey, type StaffMetrics } from './metricsApi'
import { cancelTenantDeletion, deleteMemberAccount, detailStatus, fetchStaffTenant, fetchStaffTenants, memberName, previewMemberDeletion, purgeTenant, startTenantDeletion, type StaffMember } from './tenantsApi'
import { StaffGate } from '@invoice-os/console-session'
import { SESSION_KEY, appBase, expireSession, landingBase } from './auth'
import { keptFor } from './blog'
import { currentSub } from './session'
import type { BlogFilters, DrawerState, Env, Rule, Screen, ToastState, ToastTone } from './types'

// The whole console lives under `.asc-app` — that scope defines the design-system tokens
// (--action, --bg-*, --fg-*, …) and the utility classes (.v2-btn, .label, .mono, .eyebrow)
// every screen relies on. A full-height app shell: fixed sidebar + scrolling main column,
// with drawers/modals/toast layered on top.
export default function App() {
  if (landingBase() === null) return null
  return (
    <StaffGate storageKey={SESSION_KEY} target="support" gateway={gatewayBase()} landing={landingBase()} app={appBase()}>
      <Console />
    </StaffGate>
  )
}

// Opens on Blog when the signed-in member has a draft kept from a lost session.
function initialScreen(): Screen {
  if (typeof localStorage === 'undefined') return 'rules'
  return keptFor(localStorage, currentSub()) ? 'blog' : 'rules'
}

function Console() {
  const [screen, setScreen] = useState<Screen>(initialScreen)
  const [blogFilters, setBlogFilters] = useState<BlogFilters>({ tab: 'posts', q: '', status: 'all', cat: 'all' })
  const [env, setEnv] = useState<Env>('sandbox')
  const [drawer, setDrawer] = useState<DrawerState>(null)
  const [confirmKill, setConfirmKill] = useState<{ key: string; action: 'disable' | 'enable' } | null>(null)
  const [switching, setSwitching] = useState(false)
  const [publish, setPublish] = useState<{ rows: DiffRow[] | null; rules?: { draft: Rule[]; inForce: Rule[] }; error: string | null } | null>(null)
  const [testOpen, setTestOpen] = useState(false)
  const [testResult, setTestResult] = useState<DraftTestResult | null>(null)
  const [draftBusy, setDraftBusy] = useState(false)
  const [selectedVersion, setSelectedVersion] = useState<number | null>(null)
  const [toast, setToast] = useState<ToastState>(null)
  const [tenantQuery, setTenantQuery] = useState('')
  const [tenantId, setTenantId] = useState<string | null>(null)
  const [jobState, setJobState] = useState<JobState | undefined>(undefined)
  const [jobTenant, setJobTenant] = useState('')
  const [confirmJob, setConfirmJob] = useState<JobAction | null>(null)
  const [jobBusy, setJobBusy] = useState(false)
  const [deletion, setDeletion] = useState<{ kind: 'start' | 'cancel' | 'purge' } | { kind: 'member'; member: StaffMember } | null>(null)
  const [deletionBusy, setDeletionBusy] = useState(false)
  const [acctOpens, setAcctOpens] = useState(0)
  const [auditView, setAuditView] = useState<AuditView>('tenant')
  const [auditFilters, setAuditFilters] = useState<AuditFilters>({})
  const [auditMore, setAuditMore] = useState<{ key: number; events: StaffAuditRow[]; page: StaffAuditPage['page'] } | null>(null)
  const [auditMoreKey, setAuditMoreKey] = useState<number | null>(null)
  const [auditFocus, setAuditFocus] = useState<StaffChangeLink | null>(null)
  const [aiView, setAiView] = useState<AiView>('calls')
  const [aiFilters, setAiFilters] = useState<AiFilters>({})
  const [aiMore, setAiMore] = useState<{ key: number; calls: AiCallsPage['calls']; page: AiCallsPage['page'] } | null>(null)
  const [aiMoreKey, setAiMoreKey] = useState<number | null>(null)
  const [totalsMore, setTotalsMore] = useState<{ key: number; totals: AiTotalsPage['totals']; page: AiTotalsPage['page'] } | null>(null)
  const [totalsMoreKey, setTotalsMoreKey] = useState<number | null>(null)
  const focusSeenLoading = useRef(false)
  const [evidence, setEvidence] = useState<EvidenceInputs>({ tenant: '', entity: '', from: '', to: '' })
  const [bundleBusy, setBundleBusy] = useState(false)
  const bundleInFlight = useRef(false)

  const toastTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const showToast = useCallback((msg: string, tag = '', tone: ToastTone = 'ok') => {
    setToast({ msg, tag, tone })
    if (toastTimer.current) clearTimeout(toastTimer.current)
    toastTimer.current = setTimeout(() => setToast(null), 3400)
  }, [])

  // selectedVersion null = the version in force.
  const onRules = screen === 'rules'
  const versionsRead = useAsync(fetchVersions, { immediate: onRules, deps: [onRules] })
  const rulesRead = useAsync(() => fetchRules(selectedVersion ?? undefined), { immediate: onRules, deps: [onRules, selectedVersion] })
  const codeListsRead = useAsync(fetchCodeListSummary, { immediate: onRules, deps: [onRules], isEmpty: () => false })
  const codeList = drawer?.type === 'code-list' ? drawer.id : null
  const syncsRead = useAsync(() => fetchCodeListSyncs(codeList ?? ''), { immediate: codeList !== null, deps: [codeList], isEmpty: () => false })
  const lastList = useRef<RulesInForce | null>(null)
  if (rulesRead.data) lastList.current = rulesRead.data
  const list = rulesRead.data ?? (rulesRead.status === 'error' ? null : lastList.current)
  const rules = list?.rules ?? []
  const rulesStatus = rulesRead.status === 'error' ? (rulesRead.error?.status === 403 ? 'forbidden' : 'error') : list ? 'ready' : 'loading'
  const rulesBusy = switching || rulesRead.status === 'loading'
  const switchInFlight = useRef(false)
  const draftInFlight = useRef(false)
  const publishRead = useRef(0)
  const lastVersions = useRef<RuleVersion[]>([])
  const lastToday = useRef('')
  if (versionsRead.data) lastVersions.current = versionsRead.data.versions
  const versions = lastVersions.current
  const today = versionsRead.data?.today ?? lastToday.current
  lastToday.current = today
  const inForceVersion = versions.find((v) => v.state === 'in_force')
  const draftVersion = versions.find((v) => v.state === 'draft')
  const listState = versions.find((v) => v.version === list?.version)?.state ?? (selectedVersion === null && list ? 'in_force' : null)
  const switchable = listState === 'in_force'
  const versionsNote = versionsRead.status === 'error' ? (versionsRead.error?.message ?? 'Versions unavailable') : 'Loading versions…'
  const readNote = (r: { status: string; error: { status: number | null; message: string } | null }): string =>
    r.status !== 'error' ? 'Loading…' : r.error?.status === 403 ? 'Your account is not ASComply staff.' : (r.error?.message ?? 'Unavailable')
  const onTenants = screen === 'tenants'
  const tenantsRead = useAsync(fetchStaffTenants, { immediate: onTenants, deps: [onTenants] })
  const failedTenants = useRef(new WeakMap<ApiError, string>())
  const tenantRead = useAsync(async () => {
    const id = tenantId ?? ''
    try { return await fetchStaffTenant(id) } catch (e) { const error = toApiError(e); failedTenants.current.set(error, id); throw error }
  }, { immediate: onTenants && tenantId !== null, deps: [onTenants, tenantId], isEmpty: () => false })
  const deletionMember = deletion?.kind === 'member' ? deletion.member.user_id : null
  const acctKey = `${tenantId}/${deletionMember}/${acctOpens}`
  // Keyed like previewRead, plus the open count: an answer for another member or an earlier open never shows.
  const acctRead = useAsync(async () => ({ key: acctKey, res: await previewMemberDeletion(tenantId ?? '', deletionMember ?? '') }), { immediate: deletionMember !== null && tenantId !== null, deps: [acctKey], isEmpty: () => false })
  const acctPreview = acctRead.data?.key === acctKey ? acctRead.data.res : null
  const tenantsStatus = tenantsRead.status === 'error' ? (tenantsRead.error?.status === 403 ? 'forbidden' : 'error') : tenantsRead.status === 'ready' || tenantsRead.status === 'empty' ? tenantsRead.status : 'loading'
  const onHealth = screen === 'health'
  // Reads carry the visit they started in, so a stale answer from an earlier visit never shows.
  const visit = useRef(0)
  if (!onHealth) visit.current++
  const healthRead = useAsync(async () => { const v = visit.current; return { v, value: await fetchStaffHealth() } }, { immediate: onHealth, deps: [onHealth], isEmpty: () => false })
  const lastHealth = useRef<StaffHealth | null>(null)
  const freshHealth = healthRead.data?.v === visit.current ? healthRead.data.value : null
  if (!onHealth) lastHealth.current = null
  else if (freshHealth) lastHealth.current = freshHealth
  const keepOn = (r: { status: string; error: ApiError | null }) => r.status === 'loading' || (r.status === 'error' && r.error?.status !== 401 && r.error?.status !== 403)
  const health = freshHealth ?? (keepOn(healthRead) ? lastHealth.current : null)
  const healthFailed = useRef(false)
  if (freshHealth) healthFailed.current = false
  else if (healthRead.status === 'error' && health) healthFailed.current = true
  const healthStale = healthFailed.current && health !== null
  const healthStatus = health ? 'ready' : healthRead.status === 'error' ? (healthRead.error?.status === 403 ? 'forbidden' : 'error') : 'loading'
  const [healthRange, setHealthRange] = useState<RangeKey>(DEFAULT_RANGE)
  const [healthService, setHealthService] = useState('')
  const [healthZoom, setHealthZoom] = useState<[number, number] | null>(null)
  const [healthIsolated, setHealthIsolated] = useState<string | null>(null)
  useEffect(() => { if (!onHealth) setHealthZoom(null) }, [onHealth])
  const metricsKey = `${healthRange}|${healthService}`
  const metricsRead = useAsync(async () => { const v = visit.current; return { v, key: metricsKey, value: await fetchStaffMetrics(healthRange, healthService) } }, { immediate: onHealth, deps: [onHealth, healthRange, healthService], isEmpty: () => false })
  const lastMetrics = useRef<{ key: string; value: StaffMetrics } | null>(null)
  const freshMetrics = metricsRead.data?.v === visit.current && metricsRead.data.key === metricsKey ? metricsRead.data.value : null
  if (!onHealth) lastMetrics.current = null
  else if (freshMetrics) lastMetrics.current = { key: metricsKey, value: freshMetrics }
  const keptMetrics = lastMetrics.current?.key === metricsKey && keepOn(metricsRead) ? lastMetrics.current.value : null
  const metrics = freshMetrics ?? keptMetrics
  const metricsFailed = useRef(false)
  if (freshMetrics) metricsFailed.current = false
  else if (metricsRead.status === 'error' && metrics) metricsFailed.current = true
  const metricsStale = metricsFailed.current && metrics !== null
  const metricsStatus = metrics ? 'ready' : metricsRead.status === 'error' ? (metricsRead.error?.status === 403 ? 'forbidden' : 'error') : 'loading'
  const refreshHealth = useRef(() => {})
  refreshHealth.current = () => { healthRead.run(); metricsRead.run() }
  const denied = [healthRead, metricsRead].some((r) => r.status === 'error' && (r.error?.status === 401 || r.error?.status === 403))
  useEffect(() => {
    if (!onHealth || denied) return
    // A background tab must not wake the scale-to-zero fleet: skip hidden ticks, read once on return.
    let skipped = false
    const id = setInterval(() => {
      if (document.hidden) skipped = true
      else refreshHealth.current()
    }, HEALTH_REFRESH_MS)
    const onVisible = () => {
      if (!document.hidden && skipped) { skipped = false; refreshHealth.current() }
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => { clearInterval(id); document.removeEventListener('visibilitychange', onVisible) }
  }, [onHealth, denied])
  const onSubmissions = screen === 'submissions'
  const jobsRead = useAsync(() => fetchJobs(jobState, jobTenant || undefined), { immediate: onSubmissions, deps: [onSubmissions, jobState, jobTenant], isEmpty: () => false })
  const jobTenantsRead = useAsync(fetchStaffTenants, { immediate: onSubmissions, deps: [onSubmissions] })
  const jobsKey = `${jobState}|${jobTenant}`
  const lastJobs = useRef<{ key: string; jobs: StaffJob[] } | null>(null)
  if (jobsRead.data) lastJobs.current = { key: jobsKey, jobs: jobsRead.data }
  const keptJobs = jobsRead.status === 'loading' && lastJobs.current?.key === jobsKey ? lastJobs.current.jobs : null
  const jobsStatus = jobsRead.status === 'error' ? (jobsRead.error?.status === 403 ? 'forbidden' : 'error') : jobsRead.status === 'ready' || keptJobs ? 'ready' : 'loading'
  const onAudit = screen === 'audit' && auditView === 'tenant'
  const { tenant: aTenant, event: aEvent, actor: aActor, from: aFrom, to: aTo } = auditFilters
  const auditRead = useAsync(() => fetchAuditEvents(auditFilters), {
    immediate: onAudit && !rangeInverted(auditFilters),
    deps: [onAudit, aTenant, aEvent, aActor, aFrom, aTo],
    isEmpty: (p) => p.events.length === 0,
  })
  const auditTenantsRead = useAsync(fetchStaffTenants, { immediate: screen === 'audit', deps: [screen === 'audit'] })
  const auditKey = JSON.stringify([onAudit, aTenant, aEvent, aActor, aFrom, aTo])
  // One token per first-page read or filter change; a "Load more" answer from an older token is dropped.
  const auditGen = useRef({ n: 0, key: auditKey, data: auditRead.data })
  if (auditGen.current.key !== auditKey || auditGen.current.data !== auditRead.data) auditGen.current = { n: auditGen.current.n + 1, key: auditKey, data: auditRead.data }
  const auditToken = auditGen.current.n
  useEffect(() => setAuditMore(null), [auditToken])
  const moreHere = auditMore?.key === auditToken ? auditMore : null
  const auditRows = [...(auditRead.data?.events ?? []), ...(moreHere?.events ?? [])]
  const auditPage = moreHere?.page ?? auditRead.data?.page
  const auditStatus = auditRead.status === 'error' ? (auditRead.error?.status === 403 ? 'forbidden' : 'error') : auditRead.status === 'ready' || auditRead.status === 'empty' ? auditRead.status : 'loading'
  const onAiCalls = screen === 'ai' && aiView !== 'totals'
  const aiFailed = aiView === 'failures'
  const { tenant: iTenant, purpose: iPurpose, outcome: iOutcome, from: iFrom, to: iTo } = aiFilters
  const aiRead = useAsync(() => fetchAiCalls(aiFilters, aiFailed), {
    immediate: onAiCalls && !rangeInverted(aiFilters),
    deps: [onAiCalls, aiFailed, iTenant, iPurpose, iOutcome, iFrom, iTo],
    isEmpty: (p) => p.calls.length === 0,
  })
  const aiTenantsRead = useAsync(fetchStaffTenants, { immediate: screen === 'ai', deps: [screen === 'ai'] })
  const aiKey = JSON.stringify([onAiCalls, aiFailed, iTenant, iPurpose, iOutcome, iFrom, iTo])
  // Same token scheme as the audit paging above.
  const aiGen = useRef({ n: 0, key: aiKey, data: aiRead.data })
  if (aiGen.current.key !== aiKey || aiGen.current.data !== aiRead.data) aiGen.current = { n: aiGen.current.n + 1, key: aiKey, data: aiRead.data }
  const aiToken = aiGen.current.n
  useEffect(() => setAiMore(null), [aiToken])
  const aiMoreHere = aiMore?.key === aiToken ? aiMore : null
  const aiCalls = [...(aiRead.data?.calls ?? []), ...(aiMoreHere?.calls ?? [])]
  const aiPage = aiMoreHere?.page ?? aiRead.data?.page
  const aiStatus = aiRead.status === 'error' ? (aiRead.error?.status === 403 ? 'forbidden' : 'error') : aiRead.status === 'ready' || aiRead.status === 'empty' ? aiRead.status : 'loading'
  const patchAiFilters = (patch: AiFilters) =>
    setAiFilters((f) => Object.fromEntries(Object.entries({ ...f, ...patch }).filter(([, v]) => v)) as AiFilters)
  const loadMoreAi = async () => {
    const cursor = aiPage?.has_more ? aiPage.next_cursor : null
    const sent = aiToken
    if (!cursor || aiMoreKey === sent) return
    setAiMoreKey(sent)
    try {
      const page = await fetchAiCalls(aiFilters, aiFailed, cursor)
      if (sent !== aiGen.current.n) return
      setAiMore((m) => ({ key: sent, calls: [...(m?.key === sent ? m.calls : []), ...page.calls], page: page.page }))
    } catch (e) {
      if (sent !== aiGen.current.n) return
      const err = toApiError(e)
      if (err.status === 401) void expireSession()
      else showToast(err.message, '', 'red')
    } finally {
      setAiMoreKey((k) => (k === sent ? null : k))
    }
  }
  const onAiTotals = screen === 'ai' && aiView === 'totals'
  const totalsRead = useAsync(() => fetchAiTotals(aiFilters), {
    immediate: onAiTotals && !rangeInverted(aiFilters),
    deps: [onAiTotals, iTenant, iPurpose, iOutcome, iFrom, iTo],
    isEmpty: (p) => p.totals.length === 0,
  })
  const totalsKey = JSON.stringify([onAiTotals, iTenant, iPurpose, iOutcome, iFrom, iTo])
  const totalsGen = useRef({ n: 0, key: totalsKey, data: totalsRead.data })
  if (totalsGen.current.key !== totalsKey || totalsGen.current.data !== totalsRead.data) totalsGen.current = { n: totalsGen.current.n + 1, key: totalsKey, data: totalsRead.data }
  const totalsToken = totalsGen.current.n
  useEffect(() => setTotalsMore(null), [totalsToken])
  const totalsMoreHere = totalsMore?.key === totalsToken ? totalsMore : null
  const aiTotals = [...(totalsRead.data?.totals ?? []), ...(totalsMoreHere?.totals ?? [])]
  const totalsPage = totalsMoreHere?.page ?? totalsRead.data?.page
  const totalsStatus = totalsRead.status === 'error' ? (totalsRead.error?.status === 403 ? 'forbidden' : 'error') : totalsRead.status === 'ready' || totalsRead.status === 'empty' ? totalsRead.status : 'loading'
  const loadMoreTotals = async () => {
    const cursor = totalsPage?.has_more ? totalsPage.next_cursor : null
    const sent = totalsToken
    if (!cursor || totalsMoreKey === sent) return
    setTotalsMoreKey(sent)
    try {
      const page = await fetchAiTotals(aiFilters, cursor)
      if (sent !== totalsGen.current.n) return
      setTotalsMore((m) => ({ key: sent, totals: [...(m?.key === sent ? m.totals : []), ...page.totals], page: page.page }))
    } catch (e) {
      if (sent !== totalsGen.current.n) return
      const err = toApiError(e)
      if (err.status === 401) void expireSession()
      else showToast(err.message, '', 'red')
    } finally {
      setTotalsMoreKey((k) => (k === sent ? null : k))
    }
  }
  const openAudit = drawer?.type === 'audit' ? auditRows.find((r) => r.id === drawer.id) : undefined
  const onStaffChanges = screen === 'audit' && auditView === 'staff'
  const changesRead = useAsync(fetchStaffChanges, { immediate: onStaffChanges, deps: [onStaffChanges] })
  const changesStatus = changesRead.status === 'error' ? (changesRead.error?.status === 403 ? 'forbidden' : 'error') : changesRead.status === 'ready' || changesRead.status === 'empty' ? changesRead.status : 'loading'
  const openChange = drawer?.type === 'staff-change' ? changesRead.data?.find((c) => c.id === drawer.id) : undefined
  const openTenantLog = (link: StaffChangeLink) => {
    setAuditFilters({ tenant: link.tenant, event: link.event, from: link.day, to: link.day })
    setAuditView('tenant')
    setDrawer(null)
    focusSeenLoading.current = false
    setAuditFocus(link)
  }
  // Opens the entry that matches the link once the re-read lands; the stale page of the render before the read starts is skipped.
  useEffect(() => {
    if (!auditFocus) return
    if (auditStatus === 'loading') focusSeenLoading.current = true
    if (!focusSeenLoading.current || auditStatus === 'loading') return
    setAuditFocus(null)
    // ceiling: matches on the first page only; past 50 same-event rows that day the filtered list shows without a drawer
    const hit = auditRows.find((r) => r.tenant_id === auditFocus.tenant && r.event === auditFocus.event && r.created_at === auditFocus.focusAt)
    if (hit) setDrawer({ type: 'audit', id: hit.id })
  }, [auditFocus, auditStatus, auditRows])
  const patchAuditFilters = (patch: AuditFilters) =>
    setAuditFilters((f) => Object.fromEntries(Object.entries({ ...f, ...patch }).filter(([, v]) => v)) as AuditFilters)
  const loadMoreAudit = async () => {
    const cursor = auditPage?.has_more ? auditPage.next_cursor : null
    const sent = auditToken
    if (!cursor || auditMoreKey === sent) return
    setAuditMoreKey(sent)
    try {
      const page = await fetchAuditEvents(auditFilters, cursor)
      if (sent !== auditGen.current.n) return
      setAuditMore((m) => ({ key: sent, events: [...(m?.key === sent ? m.events : []), ...page.events], page: page.page }))
    } catch (e) {
      if (sent !== auditGen.current.n) return
      const err = toApiError(e)
      if (err.status === 401) void expireSession()
      else showToast(err.message, '', 'red')
    } finally {
      setAuditMoreKey((k) => (k === sent ? null : k))
    }
  }
  const onEvidence = drawer?.type === 'evidence'
  const entitiesRead = useAsync(() => fetchStaffEntities(evidence.tenant), { immediate: onEvidence && !!evidence.tenant, deps: [onEvidence, evidence.tenant], isEmpty: () => false })
  const previewKey = JSON.stringify(evidence)
  const previewArmed = onEvidence && !!evidence.tenant && !!evidence.entity && !!evidence.from && !!evidence.to && evidence.from <= evidence.to
  // Each answer carries the key it was read for, so a preview never outlives the inputs that produced it.
  const previewRead = useAsync(async () => ({ key: previewKey, res: await fetchBundlePreview(evidence.tenant, { entityId: evidence.entity, from: evidence.from, to: evidence.to }) }), { immediate: previewArmed, deps: [onEvidence, previewKey] })
  const shownPreview = previewRead.data?.key === previewKey ? previewRead.data.res : null
  const openEvidence = () => {
    setEvidence({ tenant: auditFilters.tenant ?? '', entity: '', ...defaultBundlePeriod() })
    setDrawer({ type: 'evidence' })
  }
  const doDownload = async () => {
    if (!shownPreview || bundleInFlight.current) return
    bundleInFlight.current = true
    setBundleBusy(true)
    try {
      const { blob, filename } = await downloadBundle(evidence.tenant, { entityId: evidence.entity, from: evidence.from, to: evidence.to }, shownPreview.filename)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      a.click()
      URL.revokeObjectURL(url)
      showToast('Bundle downloaded')
    } catch (e) {
      const err = toApiError(e)
      if (err.status === 401) void expireSession()
      else showToast(err.message, '', 'red')
    } finally {
      bundleInFlight.current = false
      setBundleBusy(false)
    }
  }
  const openJob = drawer?.type === 'job' ? drawer.id : null
  const jobRead = useAsync(() => fetchJob(openJob ?? ''), { immediate: openJob !== null, deps: [openJob], isEmpty: () => false })
  const lastJob = useRef<JobDetail | null>(null)
  const freshJob = jobRead.data?.job.id === openJob ? jobRead.data : null
  if (freshJob) lastJob.current = freshJob
  const jobDetail = freshJob ?? (jobRead.status === 'loading' && lastJob.current?.job.id === openJob ? lastJob.current : null)
  const jobNote = jobRead.status === 'error' ? { text: jobRead.error?.status === 403 ? 'Your account is not ASComply staff.' : (jobRead.error?.message ?? 'Unavailable'), onRetry: jobRead.run } : { text: 'Loading job…' }
  const jobInFlight = useRef(false)
  const expired = [rulesRead, versionsRead, codeListsRead, syncsRead, tenantsRead, tenantRead, jobsRead, jobTenantsRead, jobRead, acctRead, healthRead, metricsRead, auditRead, auditTenantsRead, aiRead, totalsRead, aiTenantsRead, changesRead, entitiesRead, previewRead].some((r) => r.error?.status === 401)
  useEffect(() => {
    if (expired) void expireSession()
  }, [expired])

  const go = (s: Screen) => {
    setScreen(s)
    setDrawer(null)
    setConfirmJob(null)
    setDeletion(null)
  }

  // ---- job actions: one request at a time; a 404 or 409 re-reads what the server now holds ----
  const doJobAction = async (reason: string) => {
    if (!confirmJob || !openJob || jobInFlight.current) return
    const action = confirmJob
    jobInFlight.current = true
    setJobBusy(true)
    try {
      const acted = await (action === 'redrive' ? redriveJob : cancelJob)(openJob, reason)
      lastJob.current = acted
      setConfirmJob(null)
      showToast(action === 'redrive' ? 'Job re-driven' : 'Job cancelled', 'AUDITED')
      jobsRead.run()
      jobRead.run()
    } catch (e) {
      const err = toApiError(e)
      if (err.status === 401) {
        void expireSession()
        return
      }
      showToast(err.message, '', 'red')
      if (err.status === 404 || err.status === 409) {
        setConfirmJob(null)
        jobsRead.run()
        jobRead.run()
      }
    } finally {
      jobInFlight.current = false
      setJobBusy(false)
    }
  }

  // ---- deletion actions: one request at a time; a 404 or 409 shows the server sentence and re-reads ----
  const deletionInFlight = useRef(false)
  const doDeletion = async (reason: string, typedName: string) => {
    const t = tenantRead.data?.tenant
    if (!deletion || !t || deletionInFlight.current) return
    const id = t.id
    deletionInFlight.current = true
    setDeletionBusy(true)
    try {
      let done: string
      if (deletion.kind === 'start') {
        await startTenantDeletion(id, reason)
        done = 'Deletion scheduled'
      } else if (deletion.kind === 'cancel') {
        await cancelTenantDeletion(id, reason)
        done = 'Deletion cancelled'
      } else if (deletion.kind === 'purge') {
        await purgeTenant(id, typedName, reason)
        done = 'Purge started'
      } else if ('member' in deletion) {
        if (!acctPreview || acctPreview.outcome === 'delete_workspace') return
        const heir = acctPreview.outcome === 'hand_on' && acctPreview.heir_user_id ? { heir_user_id: acctPreview.heir_user_id } : {}
        await deleteMemberAccount(id, deletion.member.user_id, { outcome: acctPreview.outcome, ...heir, reason })
        done = 'Account deleted'
      } else return
      setDeletion(null)
      showToast(done, 'AUDITED')
      tenantsRead.run()
      tenantRead.run()
    } catch (e) {
      const err = toApiError(e)
      if (err.status === 401) {
        void expireSession()
        return
      }
      showToast(err.message, '', 'red')
      if (err.status === 404 || err.status === 409) {
        setDeletion(null)
        tenantsRead.run()
        tenantRead.run()
      }
    } finally {
      deletionInFlight.current = false
      setDeletionBusy(false)
    }
  }
  const deletionModal = () => {
    const t = tenantRead.data?.tenant
    if (!deletion || !t) return null
    const common = { busy: deletionBusy, onClose: () => setDeletion(null), onConfirm: (reason: string, typed: string) => void doDeletion(reason, typed) }
    if (deletion.kind === 'start') {
      return <DeletionConfirm {...common} title={`Start deleting ${t.name}?`} body="The workspace locks at once and is deleted in 30 days. Every active admin gets an email with a cancel link." confirmLabel="Start deletion" notice={REASON_NOTICE} />
    }
    if (deletion.kind === 'cancel') {
      return <DeletionConfirm {...common} title={`Cancel the deletion of ${t.name}?`} body="The workspace unlocks at once. Every active admin gets an email." confirmLabel="Cancel deletion" notice={REASON_NOTICE} />
    }
    if (deletion.kind === 'purge') {
      return (
        <DeletionConfirm {...common} title={`Purge ${t.name} now?`} body="Every member's account, every file and every invoice is deleted now. There is no export and no undo." confirmLabel="Purge now" nameToMatch={t.name} notice={PURGE_REASON_NOTICE} />
      )
    }
    if (!('member' in deletion)) return null
    const who = memberName(deletion.member)
    const o = acctPreview?.outcome
    const last = o === 'delete_workspace'
    const body = !acctPreview
      ? acctRead.status === 'error'
        ? (acctRead.error?.message ?? 'Unavailable')
        : 'Reading…'
      : last
        ? `${who} is the last active member. Start a workspace deletion instead.`
        : `Their account goes at once. Their name stays in the history of ${t.name}, marked (deleted).${o === 'hand_on' ? ` They are the last admin. Admin moves to ${acctPreview.heir_name}, who joined first.` : ''}`
    return (
      <DeletionConfirm {...common} title={`Delete ${who}'s account?`} body={body} confirmLabel={last ? undefined : 'Delete account'} ready={acctPreview !== null} notice={REASON_NOTICE} />
    )
  }

  // ---- rule actions ----
  const toggleRule = (key: string) => {
    const rule = rules.find((r) => r.key === key)
    if (rule && !rulesBusy && switchable) setConfirmKill({ key, action: rule.enabled ? 'disable' : 'enable' })
  }
  const doSwitch = async (reason: string) => {
    if (!confirmKill || rulesBusy || !switchable || switchInFlight.current) return
    const { key, action } = confirmKill
    switchInFlight.current = true
    setSwitching(true)
    try {
      await switchRule(key, action === 'enable', reason)
      setConfirmKill(null)
      if (action === 'disable') showToast('Kill-switch · ' + key + ' disabled', 'AUDITED', 'red')
      else showToast('Re-enabled ' + key, 'AUDITED')
      rulesRead.run()
    } catch (e) {
      const err = toApiError(e)
      if (err.status === 401) {
        void expireSession()
        return
      }
      showToast(err.message, '', 'red')
      if (err.status === 404 || err.status === 409) {
        setConfirmKill(null)
        rulesRead.run()
      }
    } finally {
      switchInFlight.current = false
      setSwitching(false)
    }
  }

  // ---- draft actions: one request at a time; a 404 or 409 re-reads what the server now holds ----
  const reread = () => {
    versionsRead.run()
    rulesRead.run()
  }
  const draftCall = async <T,>(call: () => Promise<T>): Promise<T | undefined> => {
    if (draftInFlight.current) return undefined
    draftInFlight.current = true
    setDraftBusy(true)
    try {
      return await call()
    } catch (e) {
      const err = toApiError(e)
      if (err.status === 401) void expireSession()
      else showToast(err.message, '', 'red')
      if (err.status === 404 || err.status === 409) reread()
      return undefined
    } finally {
      draftInFlight.current = false
      setDraftBusy(false)
    }
  }
  const newDraft = async () => {
    const opened = await draftCall(openDraft)
    if (!opened) return
    setSelectedVersion(opened.version)
    versionsRead.run()
    showToast(`Opened draft v${opened.version} from v${opened.from_version}`, 'AUDITED')
  }
  const saveDraftRule = async (form: RuleForm, isNew: boolean) => {
    const body = ruleBody(form)
    const saved = await draftCall(() => (isNew ? addDraftRule({ key: form.key, ...body }) : editDraftRule(form.key, body)))
    if (!saved) return
    setDrawer(null)
    showToast(`${isNew ? 'Added' : 'Saved'} ${form.key} in draft v${saved.rule_set_version}`, 'AUDITED')
    reread()
  }
  const removeFromDraft = async (key: string) => {
    const removed = await draftCall(() => removeDraftRule(key))
    if (!removed) return
    setDrawer(null)
    showToast(`Removed ${key} from draft v${removed.rule_set_version}`, 'AUDITED', 'red')
    reread()
  }
  const runDraftTest = async (invoice: Record<string, unknown>) => {
    setTestResult(null)
    const res = await draftCall(() => testDraft(invoice))
    if (res) setTestResult(res)
  }
  // The diff is the draft against the version in force, whatever version is selected.
  const openPublish = () => {
    if (!draftVersion) return
    const id = ++publishRead.current
    setPublish({ rows: null, error: null })
    Promise.all([fetchRules(draftVersion.version), fetchRules()]).then(
      ([d, f]) => id === publishRead.current && setPublish({ rows: diffRules(d.rules, f.rules), rules: { draft: d.rules, inForce: f.rules }, error: null }),
      (e) => {
        if (id !== publishRead.current) return
        const err = toApiError(e)
        if (err.status === 401) void expireSession()
        else setPublish({ rows: null, error: err.message })
      },
    )
  }
  const closePublish = () => {
    publishRead.current++
    setPublish(null)
  }
  const confirmPublish = async (date: string) => {
    const done = await draftCall(() => publishDraft(date))
    if (!done) return
    closePublish()
    showToast(`Published v${done.version} · starts ${done.effective_from}`, 'AUDITED')
    versionsRead.run()
    if (selectedVersion === null) rulesRead.run()
    else setSelectedVersion(null)
  }

  const selectVersion = (v: RuleVersion) => {
    setSelectedVersion(v.state === 'in_force' ? null : v.version)
    setDrawer(null)
  }

  // ---- resolve open drawer entities ----
  const drawerRule = drawer?.type === 'rule' ? rules.find((r) => r.key === drawer.id) : undefined
  const onDraft = listState === 'draft'
  const draftEdit = (key: string | null) =>
    onDraft
      ? {
          draftKeys: rules.map((r) => r.key),
          busy: draftBusy,
          onSave: (form: RuleForm) => void saveDraftRule(form, key === null),
          onRemove: () => void (key !== null && removeFromDraft(key)),
        }
      : undefined

  return (
    <div
      className="asc-app"
      style={{
        height: '100vh',
        display: 'flex',
        background: 'var(--bg-1)',
        fontFamily: 'var(--font-sans)',
        color: 'var(--fg-1)',
        overflow: 'hidden',
      }}
    >
      <Sidebar screen={screen} onNavigate={go} />

      <main style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <TopBar screen={screen} env={env} onSetEnv={setEnv} />
        <div style={{ flex: 1, overflowY: 'auto' }}>
          {screen === 'rules' && (
            <Rules
              rules={rules}
              status={rulesStatus}
              errorText={rulesRead.error?.message}
              version={list?.version ?? null}
              state={listState}
              versions={versions}
              versionsNote={versionsNote}
              selected={selectedVersion ?? inForceVersion?.version ?? null}
              onSelectVersion={selectVersion}
              busy={rulesBusy}
              onRetry={rulesRead.run}
              onOpenRule={(key) => setDrawer({ type: 'rule', id: key })}
              onToggleRule={toggleRule}
              onPublish={openPublish}
              draftVersion={draftVersion?.version ?? null}
              draftBusy={draftBusy}
              onNewDraft={newDraft}
              codeLists={codeListsRead.data}
              codeListsNote={readNote(codeListsRead)}
              onOpenCodeList={(list) => setDrawer({ type: 'code-list', id: list })}
              onAddRule={() => setDrawer({ type: 'new-rule' })}
              onTestDraft={() => {
                setTestResult(null)
                setTestOpen(true)
              }}
            />
          )}
          {screen === 'submissions' && (
            <Submissions
              status={jobsStatus}
              errorText={jobsRead.error?.message}
              jobs={jobsRead.data ?? keptJobs ?? []}
              state={jobState}
              onState={setJobState}
              tenant={jobTenant}
              onTenant={setJobTenant}
              tenants={jobTenantsRead.data ?? []}
              onRetry={jobsRead.run}
              onOpen={(id) => setDrawer({ type: 'job', id })}
              tenantsError={jobTenantsRead.status === 'error' && jobTenantsRead.error?.status !== 401 ? { text: jobTenantsRead.error?.message ?? 'Unavailable', onRetry: jobTenantsRead.run } : undefined}
            />
          )}
          {screen === 'audit' && (
            <Audit
              view={auditView}
              onView={setAuditView}
              status={auditStatus}
              errorText={auditRead.error?.message}
              rows={auditRows}
              hasMore={auditPage?.has_more ?? false}
              moreBusy={auditMoreKey === auditToken}
              onMore={() => void loadMoreAudit()}
              filters={auditFilters}
              onFilters={patchAuditFilters}
              tenants={auditTenantsRead.data ?? []}
              tenantsError={auditTenantsRead.status === 'error' && auditTenantsRead.error?.status !== 401 ? { text: auditTenantsRead.error?.message ?? 'Unavailable', onRetry: auditTenantsRead.run } : undefined}
              onRetry={auditRead.run}
              onOpen={(id) => setDrawer({ type: 'audit', id })}
              changes={changesRead.data ?? []}
              changesStatus={changesStatus}
              changesErrorText={changesRead.error?.message}
              onRetryChanges={changesRead.run}
              onOpenChange={(id) => setDrawer({ type: 'staff-change', id })}
              onEvidence={openEvidence}
            />
          )}
          {screen === 'tenants' && (
            <Tenants
              status={tenantsStatus}
              errorText={tenantsRead.error?.message}
              tenants={tenantsRead.data ?? []}
              query={tenantQuery}
              onQueryChange={setTenantQuery}
              selectedId={tenantId}
              onSelect={setTenantId}
              onRetry={tenantsRead.run}
              detailStatus={detailStatus(tenantRead, tenantId, (tenantRead.error && failedTenants.current.get(tenantRead.error)) || null)}
              detail={tenantRead.data}
              detailErrorText={tenantRead.error?.message}
              onRetryDetail={tenantRead.run}
              onViewJobs={(id) => {
                setJobTenant(id)
                setJobState(undefined)
                go('submissions')
              }}
              onDeletion={(kind) => setDeletion({ kind })}
              onDeleteAccount={(member) => { setAcctOpens((n) => n + 1); setDeletion({ kind: 'member', member }) }}
              actionBusy={deletionBusy}
            />
          )}
          {screen === 'ai' && (
            <AiUsage
              view={aiView}
              onView={setAiView}
              filters={aiFilters}
              onFilters={patchAiFilters}
              tenants={aiTenantsRead.data ?? []}
              tenantsError={aiTenantsRead.status === 'error' && aiTenantsRead.error?.status !== 401 ? { text: aiTenantsRead.error?.message ?? 'Unavailable', onRetry: aiTenantsRead.run } : undefined}
              status={aiStatus}
              errorText={aiRead.error?.message}
              calls={aiCalls}
              hasMore={aiPage?.has_more ?? false}
              moreBusy={aiMoreKey === aiToken}
              onMore={() => void loadMoreAi()}
              onRetry={aiRead.run}
              totalsStatus={totalsStatus}
              totalsErrorText={totalsRead.error?.message}
              totals={aiTotals}
              totalsHasMore={totalsPage?.has_more ?? false}
              totalsMoreBusy={totalsMoreKey === totalsToken}
              onMoreTotals={() => void loadMoreTotals()}
              onRetryTotals={totalsRead.run}
            />
          )}
          {screen === 'blog' && <Blog filters={blogFilters} onFilters={setBlogFilters} onToast={showToast} />}
          {screen === 'health' && (
            <Health
              status={healthStatus}
              errorText={healthRead.error?.message}
              data={health}
              healthStale={healthStale}
              metricsStale={metricsStale}
              onRetry={healthRead.run}
              metricsStatus={metricsStatus}
              metricsErrorText={metricsRead.error?.message}
              metrics={metrics}
              onRetryMetrics={metricsRead.run}
              range={healthRange}
              onRange={(r) => { setHealthRange(r); setHealthZoom(null) }}
              service={healthService}
              onService={(v) => { setHealthService(v); setHealthZoom(null); setHealthIsolated(null) }}
              zoom={healthZoom}
              onZoom={setHealthZoom}
              isolated={healthIsolated}
              onIsolate={setHealthIsolated}
            />
          )}
        </div>
      </main>

      {codeList !== null && <CodeListDrawer list={codeList} syncs={syncsRead.data} note={readNote(syncsRead)} onClose={() => setDrawer(null)} />}

      {(drawerRule || (drawer?.type === 'new-rule' && onDraft)) && (
        <RuleDrawer
          key={drawerRule?.key ?? 'new'}
          rule={drawerRule ?? null}
          inForce={switchable}
          busy={rulesBusy}
          draft={draftEdit(drawerRule?.key ?? null)}
          onKill={() => drawerRule && setConfirmKill({ key: drawerRule.key, action: 'disable' })}
          onClose={() => setDrawer(null)}
        />
      )}

      {openJob !== null && (
        <JobDrawer
          detail={jobDetail}
          note={jobNote}
          busy={jobBusy}
          onRedrive={() => setConfirmJob('redrive')}
          onCancel={() => setConfirmJob('cancel')}
          onClose={() => {
            setDrawer(null)
            setConfirmJob(null)
          }}
        />
      )}

      {openAudit && <AuditEntryDrawer entry={openAudit} onClose={() => setDrawer(null)} />}

      {onEvidence && (
        <EvidenceDrawer
          inputs={evidence}
          onInputs={(patch) => setEvidence((v) => ({ ...v, ...patch }))}
          tenants={auditTenantsRead.data ?? []}
          tenantsError={auditTenantsRead.status === 'error' && auditTenantsRead.error?.status !== 401 ? { text: auditTenantsRead.error?.message ?? 'Unavailable', onRetry: auditTenantsRead.run } : undefined}
          entities={evidence.tenant && entitiesRead.status === 'ready' ? entitiesRead.data : null}
          entitiesError={evidence.tenant && entitiesRead.status === 'error' && entitiesRead.error?.status !== 401 ? { text: entitiesRead.error?.message ?? 'Unavailable', onRetry: entitiesRead.run } : undefined}
          preview={shownPreview}
          previewLoading={previewRead.status === 'loading'}
          previewError={previewRead.status === 'error' && previewRead.error?.status !== 401 ? { text: previewRead.error?.message ?? 'Unavailable', onRetry: previewRead.run } : undefined}
          busy={bundleBusy}
          onDownload={() => void doDownload()}
          onClose={() => setDrawer(null)}
        />
      )}

      {openChange && (
        <StaffChangeDrawer
          change={openChange}
          onClose={() => setDrawer(null)}
          onOpenTenantLog={() => {
            const link = staffChangeLink(openChange)
            if (link) openTenantLog(link)
          }}
        />
      )}

      {confirmJob && jobDetail && (
        <JobActionConfirm invoiceNumber={jobDetail.job.invoice_number} action={confirmJob} busy={jobBusy} onClose={() => setConfirmJob(null)} onConfirm={(reason) => void doJobAction(reason)} />
      )}

      {deletionModal()}

      {confirmKill && (
        <KillConfirm ruleKey={confirmKill.key} action={confirmKill.action} busy={rulesBusy} onClose={() => setConfirmKill(null)} onConfirm={doSwitch} />
      )}

      {publish && draftVersion && (
        <PublishModal
          draftVersion={draftVersion.version}
          inForceVersion={inForceVersion?.version ?? null}
          rows={publish.rows}
          warnings={publishWarnings(versions, publish.rules?.draft, publish.rules?.inForce)}
          today={today}
          busy={draftBusy}
          error={publish.error}
          onClose={closePublish}
          onConfirm={(date) => void confirmPublish(date)}
        />
      )}

      {testOpen && <DraftTestModal result={testResult} busy={draftBusy} onRun={(invoice) => void runDraftTest(invoice)} onClose={() => setTestOpen(false)} />}

      {toast && <Toast toast={toast} />}
    </div>
  )
}
