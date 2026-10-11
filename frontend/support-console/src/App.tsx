import { useCallback, useEffect, useRef, useState } from 'react'
import { Sidebar } from './components/Sidebar'
import { TopBar } from './components/TopBar'
import { Submissions } from './components/Submissions'
import { Rules } from './components/Rules'
import { Audit } from './components/Audit'
import { Tenants } from './components/Tenants'
import { Health } from './components/Health'
import { JobDrawer } from './components/JobDrawer'
import { RuleDrawer } from './components/RuleDrawer'
import { AuditDrawer } from './components/AuditDrawer'
import { KillConfirm } from './components/KillConfirm'
import { PublishModal } from './components/PublishModal'
import { Toast } from './components/Toast'
import { AUDIT_ENTRIES, SEED_JOBS } from './data'
import { gatewayBase, toApiError, useAsync } from '@invoice-os/api-client'
import { fetchRules, fetchVersions, switchRule, type RuleVersion, type RulesInForce } from './rulesApi'
import { StaffGate } from '@invoice-os/console-session'
import { SESSION_KEY, landingBase, signOut } from './auth'
import type { AuditFilter, DrawerState, Env, JobFilter, Screen, SubTab, ToastState, ToastTone } from './types'

// The whole console lives under `.asc-app` — that scope defines the design-system tokens
// (--action, --bg-*, --fg-*, …) and the utility classes (.v2-btn, .label, .mono, .eyebrow)
// every screen relies on. A full-height app shell: fixed sidebar + scrolling main column,
// with drawers/modals/toast layered on top.
export default function App() {
  return (
    <StaffGate storageKey={SESSION_KEY} target="support" gateway={gatewayBase()} landing={landingBase()}>
      <Console />
    </StaffGate>
  )
}

function Console() {
  // Mirrors the prototype's constructor state (Support Console.dc.html:708).
  const [screen, setScreen] = useState<Screen>('submissions')
  const [env, setEnv] = useState<Env>('sandbox')
  const [filter, setFilter] = useState<JobFilter>('all')
  const [subTab, setSubTab] = useState<SubTab>('jobs')
  const [drawer, setDrawer] = useState<DrawerState>(null)
  const [reqOpen, setReqOpen] = useState(true)
  const [resOpen, setResOpen] = useState(true)
  const [confirmKill, setConfirmKill] = useState<{ key: string; action: 'disable' | 'enable' } | null>(null)
  const [switching, setSwitching] = useState(false)
  const [publishOpen, setPublishOpen] = useState(false)
  const [selectedVersion, setSelectedVersion] = useState<number | null>(null)
  const [auditQuery, setAuditQuery] = useState('')
  const [auditFilter, setAuditFilter] = useState<AuditFilter>('all')
  const [tenantQuery, setTenantQuery] = useState('')
  const [tenantId, setTenantId] = useState('t1')
  const [jobs, setJobs] = useState(SEED_JOBS)
  const [toast, setToast] = useState<ToastState>(null)

  const toastTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const showToast = useCallback((msg: string, tag = '', tone: ToastTone = 'ok') => {
    setToast({ msg, tag, tone })
    if (toastTimer.current) clearTimeout(toastTimer.current)
    toastTimer.current = setTimeout(() => setToast(null), 3400)
  }, [])

  // Read when the Rules screen opens, never on console mount. selectedVersion null = the version in force.
  const onRules = screen === 'rules'
  const versionsRead = useAsync(fetchVersions, { immediate: onRules, deps: [onRules] })
  const rulesRead = useAsync(() => fetchRules(selectedVersion ?? undefined), { immediate: onRules, deps: [onRules, selectedVersion] })
  const lastList = useRef<RulesInForce | null>(null)
  if (rulesRead.data) lastList.current = rulesRead.data
  const list = rulesRead.data ?? (rulesRead.status === 'error' ? null : lastList.current)
  const rules = list?.rules ?? []
  const rulesStatus = rulesRead.status === 'error' ? (rulesRead.error?.status === 403 ? 'forbidden' : 'error') : list ? 'ready' : 'loading'
  const rulesBusy = switching || rulesRead.status === 'loading'
  const switchInFlight = useRef(false)
  const lastVersions = useRef<RuleVersion[]>([])
  if (versionsRead.data) lastVersions.current = versionsRead.data.versions
  const versions = lastVersions.current
  const inForceVersion = versions.find((v) => v.state === 'in_force')
  const listState = versions.find((v) => v.version === list?.version)?.state ?? (selectedVersion === null && list ? 'in_force' : null)
  const switchable = listState === 'in_force'
  const versionsNote = versionsRead.status === 'error' ? (versionsRead.error?.message ?? 'Versions unavailable') : 'Loading versions…'
  const expired = rulesRead.error?.status === 401 || versionsRead.error?.status === 401
  useEffect(() => {
    if (expired) void signOut()
  }, [expired])

  const dlCount = jobs.filter((j) => j.state === 'dead-letter').length

  const go = (s: Screen) => {
    setScreen(s)
    setDrawer(null)
  }

  // ---- job actions (proto:1114) ----
  // Cross-tenant mutations. Nothing reaches the audit log until accreditation (TopBar.tsx),
  // so the tag names that gate rather than asserting a write happened.
  const openJob = (id: string) => {
    setDrawer({ type: 'job', id })
    setReqOpen(true)
    setResOpen(true)
  }
  const reDriveOne = (id: string) => {
    setJobs((prev) => prev.map((j) => (j.id === id ? { ...j, state: 'queued', lastError: '—' } : j)))
    setDrawer(null)
    showToast('Re-drive queued · ' + id, 'AUDIT ON ACCREDITATION')
  }
  const reDriveAll = () => {
    const n = jobs.filter((j) => j.state === 'dead-letter').length
    setJobs((prev) => prev.map((j) => (j.state === 'dead-letter' ? { ...j, state: 'queued', lastError: '—' } : j)))
    showToast(`Re-drive queued · ${n} dead-letter ${n === 1 ? 'job' : 'jobs'}`, 'AUDIT ON ACCREDITATION')
  }
  const cancelJob = (id: string) => {
    setJobs((prev) => prev.map((j) => (j.id === id ? { ...j, state: 'failed', lastError: 'Cancelled by operator' } : j)))
    setDrawer(null)
    showToast('Cancelled · ' + id, 'AUDIT ON ACCREDITATION', 'red')
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
        void signOut()
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

  const selectVersion = (v: RuleVersion) => {
    setSelectedVersion(v.state === 'in_force' ? null : v.version)
    setDrawer(null)
  }

  // ---- resolve open drawer entities ----
  const drawerJob = drawer?.type === 'job' ? jobs.find((j) => j.id === drawer.id) : undefined
  const drawerRule = drawer?.type === 'rule' ? rules.find((r) => r.key === drawer.id) : undefined
  const drawerAudit = drawer?.type === 'audit' ? AUDIT_ENTRIES.find((a) => a.id === drawer.id) : undefined

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
      <Sidebar screen={screen} onNavigate={go} deadLetterCount={dlCount} />

      <main style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <TopBar screen={screen} env={env} onSetEnv={setEnv} />
        <div style={{ flex: 1, overflowY: 'auto' }}>
          {screen === 'submissions' && (
            <Submissions
              jobs={jobs}
              filter={filter}
              subTab={subTab}
              onFilterChange={setFilter}
              onSubTabChange={setSubTab}
              onOpenJob={openJob}
              onReDriveAll={reDriveAll}
              onReconcile={(id, appLabel) => showToast(`Reconciled ${id} → ${appLabel}`, 'AUDIT ON ACCREDITATION')}
              onRunSweep={() => showToast('Reconciliation sweep dispatched', 'RECONCILIATION')}
            />
          )}
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
              onPublish={() => setPublishOpen(true)}
            />
          )}
          {screen === 'audit' && (
            <Audit
              query={auditQuery}
              filter={auditFilter}
              onQueryChange={setAuditQuery}
              onFilterChange={setAuditFilter}
              onOpen={(id) => setDrawer({ type: 'audit', id })}
            />
          )}
          {screen === 'tenants' && (
            <Tenants
              query={tenantQuery}
              tenantId={tenantId}
              onQueryChange={setTenantQuery}
              onSelect={setTenantId}
              onViewJobs={() => go('submissions')}
              onViewAs={(name) => showToast(`Opened ${name} in read-only view-as`, 'AUDIT ON ACCREDITATION')}
            />
          )}
          {screen === 'health' && <Health deadLetterCount={dlCount} />}
        </div>
      </main>

      {drawerJob && (
        <JobDrawer
          job={drawerJob}
          env={env}
          reqOpen={reqOpen}
          resOpen={resOpen}
          onToggleReq={() => setReqOpen((v) => !v)}
          onToggleRes={() => setResOpen((v) => !v)}
          onClose={() => setDrawer(null)}
          onReDrive={() => reDriveOne(drawerJob.id)}
          onRePoll={() => showToast('Re-poll dispatched · ' + drawerJob.id, 'POLLING')}
          onCancel={() => cancelJob(drawerJob.id)}
        />
      )}

      {drawerRule && (
        <RuleDrawer
          rule={drawerRule}
          inForce={switchable}
          busy={rulesBusy}
          onKill={() => setConfirmKill({ key: drawerRule.key, action: 'disable' })}
          onClose={() => setDrawer(null)}
        />
      )}

      {drawerAudit && (
        <AuditDrawer
          entry={drawerAudit}
          env={env}
          onClose={() => setDrawer(null)}
          onCopy={() => showToast('Evidence JSON copied to clipboard', 'AUDIT')}
          onExport={() => showToast('Evidence bundle exported', 'AUDIT')}
        />
      )}

      {confirmKill && (
        <KillConfirm ruleKey={confirmKill.key} action={confirmKill.action} busy={rulesBusy} onClose={() => setConfirmKill(null)} onConfirm={doSwitch} />
      )}

      {publishOpen && (
        <PublishModal
          onClose={() => setPublishOpen(false)}
          onConfirm={() => {
            setPublishOpen(false)
            showToast('Published rule-set v9 · immutable', 'RULES')
          }}
        />
      )}

      {toast && <Toast toast={toast} />}
    </div>
  )
}
