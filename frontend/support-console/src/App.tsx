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
import { AUDIT_ENTRIES, SEED_JOBS, SEED_RULES } from './data'
import { landingBase } from './auth'
import { resolveSupportBootSession } from './session'
import type { AuditFilter, DrawerState, Env, JobFilter, Screen, SubTab, ToastState, ToastTone } from './types'

// The whole console lives under `.asc-app` — that scope defines the design-system tokens
// (--action, --bg-*, --fg-*, …) and the utility classes (.v2-btn, .label, .mono, .eyebrow)
// every screen relies on. A full-height app shell: fixed sidebar + scrolling main column,
// with drawers/modals/toast layered on top.
export default function App() {
  // Sign-in gate. The landing page is the single front door: it hands off here as
  // ?persona=support, which this persists once so the following reload needs no param.
  // Resolved ONCE at boot — a later re-render must not re-read the URL, since the effect
  // below strips the param from it.
  const [session] = useState(() => resolveSupportBootSession(window.location.search))

  useEffect(() => {
    if (session) {
      // Drop the consumed ?persona= so a copied URL isn't a sign-in link and a refresh
      // goes through the stored session instead. replaceState, not a navigation: it must
      // not add a history entry the back button can bounce off.
      if (new URLSearchParams(window.location.search).has('persona')) {
        window.history.replaceState(null, '', window.location.pathname + window.location.hash)
      }
      return
    }
    // Not signed in -> the front door. landingBase() is null on the standalone showcase
    // build (no VITE_LANDING_URL); there is nowhere to send anyone, so the console renders
    // rather than becoming a dead end — see the gate below.
    const dest = landingBase()
    if (dest) window.location.href = dest
  }, [session])

  // Redirecting: render nothing rather than a frame of console chrome the visitor was
  // never signed in to see. Only reached when a landing URL exists — without one the
  // showcase build falls through and renders normally.
  if (!session && landingBase()) {
    return null
  }

  return <Console />
}

// The console proper. Split out of the gate above so that `App`'s early return can never
// skip these hooks — a conditional return placed above them would violate the Rules of
// Hooks the moment the gate's outcome changed.
function Console() {
  // Mirrors the prototype's constructor state (Support Console.dc.html:708).
  const [screen, setScreen] = useState<Screen>('submissions')
  const [env, setEnv] = useState<Env>('sandbox')
  const [filter, setFilter] = useState<JobFilter>('all')
  const [subTab, setSubTab] = useState<SubTab>('jobs')
  const [drawer, setDrawer] = useState<DrawerState>(null)
  const [reqOpen, setReqOpen] = useState(true)
  const [resOpen, setResOpen] = useState(true)
  const [confirmKill, setConfirmKill] = useState<string | null>(null)
  const [publishOpen, setPublishOpen] = useState(false)
  const [testRan, setTestRan] = useState(false)
  const [auditQuery, setAuditQuery] = useState('')
  const [auditFilter, setAuditFilter] = useState<AuditFilter>('all')
  const [tenantQuery, setTenantQuery] = useState('')
  const [tenantId, setTenantId] = useState('t1')
  const [jobs, setJobs] = useState(SEED_JOBS)
  const [rules, setRules] = useState(SEED_RULES)
  const [toast, setToast] = useState<ToastState>(null)

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

  // ---- job actions (proto:1114) ----
  // Cross-tenant mutations. Nothing reaches the audit log until accreditation (TopBar.tsx,
  // KillConfirm.tsx), so the tag names that gate rather than asserting a write happened.
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
    showToast(`Re-drove ${n} dead-letter ${n === 1 ? 'job' : 'jobs'}`, 'AUDIT ON ACCREDITATION')
  }
  const cancelJob = (id: string) => {
    setJobs((prev) => prev.map((j) => (j.id === id ? { ...j, state: 'failed', lastError: 'Cancelled by operator' } : j)))
    setDrawer(null)
    showToast('Cancelled · ' + id, 'AUDIT ON ACCREDITATION', 'red')
  }

  // ---- rule actions (proto:1099) ----
  // Enabling is immediate; disabling routes through the confirm modal, because turning a
  // rule off stops validating every tenant's invoices against it.
  const toggleRule = (key: string) => {
    const rule = rules.find((r) => r.key === key)
    if (!rule) return
    if (rule.enabled) {
      setConfirmKill(key)
      return
    }
    setRules((prev) => prev.map((r) => (r.key === key ? { ...r, enabled: true } : r)))
    showToast('Re-enabled ' + key, 'RULES')
  }
  const doKill = () => {
    if (!confirmKill) return
    setRules((prev) => prev.map((r) => (r.key === confirmKill ? { ...r, enabled: false } : r)))
    showToast('Kill-switch · ' + confirmKill + ' disabled', 'AUDIT ON ACCREDITATION', 'red')
    setConfirmKill(null)
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
              onOpenRule={(key) => {
                setDrawer({ type: 'rule', id: key })
                setTestRan(false)
              }}
              onToggleRule={toggleRule}
              onPublish={() => setPublishOpen(true)}
              onPromote={(key) => showToast('Promoted ' + key + ' to draft v9', 'RULES')}
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
          testRan={testRan}
          onRunTest={() => setTestRan(true)}
          onKill={() => setConfirmKill(drawerRule.key)}
          onClose={() => setDrawer(null)}
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

      {confirmKill && <KillConfirm ruleKey={confirmKill} env={env} onClose={() => setConfirmKill(null)} onConfirm={doKill} />}

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
