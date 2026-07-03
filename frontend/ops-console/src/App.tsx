import { useCallback, useRef, useState } from 'react'
import { Sidebar } from './components/Sidebar'
import { TopBar } from './components/TopBar'
import { Submissions } from './components/Submissions'
import { Rules } from './components/Rules'
import { Audit } from './components/Audit'
import { Tenants } from './components/Tenants'
import { Health } from './components/Health'
import { JobDrawer } from './components/JobDrawer'
import { AuditDrawer } from './components/AuditDrawer'
import { RuleDrawer } from './components/RuleDrawer'
import { KillConfirm } from './components/KillConfirm'
import { PublishModal } from './components/PublishModal'
import { Toast } from './components/Toast'
import { AUDIT_ENTRIES, SEED_JOBS, SEED_RULES } from './data'
import type { DrawerState, Env, JobFilter, Screen, SubTab, ToastState, ToastTone } from './types'

// The whole console lives under `.if-v2` — that scope defines the design-system
// tokens (--accent, --bg-*, --fg-*, …) and the utility classes (.v2-btn, .label,
// .mono) that every screen relies on. It's a full-height app shell: a fixed
// sidebar + a scrolling main column, with drawers/modals/toast layered on top.
export default function App() {
  const [screen, setScreen] = useState<Screen>('submissions')
  const [env, setEnv] = useState<Env>('sandbox')
  const [filter, setFilter] = useState<JobFilter>('all')
  const [subTab, setSubTab] = useState<SubTab>('jobs')
  const [drawer, setDrawer] = useState<DrawerState>(null)
  const [reqOpen, setReqOpen] = useState(true)
  const [resOpen, setResOpen] = useState(true)
  const [confirmKill, setConfirmKill] = useState<string | null>(null)
  const [publishOpen, setPublishOpen] = useState(false)
  const [toast, setToast] = useState<ToastState>(null)
  const [testRan, setTestRan] = useState(false)
  const [auditQuery, setAuditQuery] = useState('')
  const [auditFilter, setAuditFilter] = useState('all')
  const [tenantQuery, setTenantQuery] = useState('')
  const [tenantId, setTenantId] = useState('t1')
  const [jobs, setJobs] = useState(SEED_JOBS)
  const [rules, setRules] = useState(SEED_RULES)

  const toastTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const showToast = useCallback((msg: string, tag = '', tone: ToastTone = 'ok') => {
    setToast({ msg, tag, tone })
    if (toastTimer.current) clearTimeout(toastTimer.current)
    toastTimer.current = setTimeout(() => setToast(null), 3400)
  }, [])

  const dlCount = jobs.filter((j) => j.state === 'dead-letter').length

  const go = (s: Screen) => {
    setScreen(s)
    setDrawer(null)
  }

  // ---- job actions ----
  const openJob = (id: string) => {
    setDrawer({ type: 'job', id })
    setReqOpen(true)
    setResOpen(true)
  }
  const reDriveOne = (id: string) => {
    setJobs((prev) => prev.map((j) => (j.id === id ? { ...j, state: 'queued', lastError: '—' } : j)))
    setDrawer(null)
    showToast('Re-drive queued · ' + id, 'AUDIT LOGGED')
  }
  const reDriveAll = () => {
    const ids = jobs.filter((j) => j.state === 'dead-letter').map((j) => j.id)
    setJobs((prev) => prev.map((j) => (j.state === 'dead-letter' ? { ...j, state: 'queued', lastError: '—' } : j)))
    showToast('Re-drove ' + ids.length + ' dead-letter jobs', 'AUDIT LOGGED')
  }
  const cancelJob = (id: string) => {
    setJobs((prev) => prev.map((j) => (j.id === id ? { ...j, state: 'failed', lastError: 'Cancelled by operator' } : j)))
    setDrawer(null)
    showToast('Cancelled · ' + id, 'AUDIT LOGGED', 'red')
  }

  // ---- rule actions ----
  const openRule = (key: string) => {
    setDrawer({ type: 'rule', id: key })
    setTestRan(false)
  }
  const toggleRule = (key: string) => {
    const r = rules.find((x) => x.key === key)
    if (!r) return
    if (r.enabled) {
      setConfirmKill(key)
    } else {
      setRules((prev) => prev.map((x) => (x.key === key ? { ...x, enabled: true } : x)))
      showToast('Re-enabled ' + key, 'RULES')
    }
  }
  const doKill = () => {
    const key = confirmKill
    if (!key) return
    setRules((prev) => prev.map((x) => (x.key === key ? { ...x, enabled: false } : x)))
    setConfirmKill(null)
    showToast('Kill-switch · ' + key + ' disabled', 'AUDIT LOGGED', 'red')
  }
  const confirmPublish = () => {
    setPublishOpen(false)
    showToast('Published rule-set v9 · immutable', 'RULES')
  }

  // ---- open audit ----
  const openAudit = (id: string) => setDrawer({ type: 'audit', id })

  // ---- resolve open drawer entities ----
  const drawerJob = drawer?.type === 'job' ? jobs.find((j) => j.id === drawer.id) : undefined
  const drawerAudit = drawer?.type === 'audit' ? AUDIT_ENTRIES.find((a) => a.id === drawer.id) : undefined
  const drawerRule = drawer?.type === 'rule' ? rules.find((r) => r.key === drawer.id) : undefined

  return (
    <div
      className="if-v2"
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
              onFilterChange={setFilter}
              subTab={subTab}
              onSubTabChange={setSubTab}
              onOpenJob={openJob}
              onReDriveAll={reDriveAll}
              onReconcileFix={(id, appLabel) => showToast('Reconciled ' + id + ' → ' + appLabel.toLowerCase(), 'AUDIT LOGGED')}
            />
          )}
          {screen === 'rules' && (
            <Rules
              rules={rules}
              onOpenRule={openRule}
              onToggleRule={toggleRule}
              onOpenPublish={() => setPublishOpen(true)}
              onPromoteLearned={(key) => showToast('Promoted ' + key + ' to draft v9', 'RULES')}
            />
          )}
          {screen === 'audit' && (
            <Audit
              auditQuery={auditQuery}
              onAuditQueryChange={setAuditQuery}
              auditFilter={auditFilter}
              onAuditFilterChange={setAuditFilter}
              onOpenAudit={openAudit}
            />
          )}
          {screen === 'tenants' && (
            <Tenants
              tenantQuery={tenantQuery}
              onTenantQueryChange={setTenantQuery}
              tenantId={tenantId}
              onSelectTenant={setTenantId}
              onViewJobs={() => go('submissions')}
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
      {drawerAudit && (
        <AuditDrawer
          entry={drawerAudit}
          env={env}
          onClose={() => setDrawer(null)}
          onCopy={() => showToast('Evidence JSON copied to clipboard', 'AUDIT')}
          onExport={() => showToast('Evidence bundle exported (signed)', 'AUDIT')}
        />
      )}
      {drawerRule && (
        <RuleDrawer
          rule={drawerRule}
          testRan={testRan}
          onRunTest={() => setTestRan(true)}
          onClose={() => setDrawer(null)}
          onKill={() => toggleRule(drawerRule.key)}
        />
      )}

      {confirmKill && <KillConfirm ruleKey={confirmKill} env={env} onCancel={() => setConfirmKill(null)} onConfirm={doKill} />}
      {publishOpen && <PublishModal onClose={() => setPublishOpen(false)} onConfirm={confirmPublish} />}

      {toast && <Toast toast={toast} />}
    </div>
  )
}
