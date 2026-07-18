import { useCallback, useRef, useState } from 'react'
import { Sidebar } from './components/Sidebar'
import { TopBar } from './components/TopBar'
import { Overview } from './components/Overview'
import { Submissions } from './components/Submissions'
import { Evidence } from './components/Evidence'
import { ApiWebhooks } from './components/ApiWebhooks'
import { Billing } from './components/Billing'
import { Status } from './components/Status'
import { JobDrawer } from './components/JobDrawer'
import { Toast } from './components/Toast'
import { SEED_SUBMISSIONS } from './data'
import type { DrawerState, Env, JobFilter, Range, Screen, ToastState, ToastTone } from './types'

// The whole console lives under `.if-v2` — that scope defines the design-system
// tokens (--accent, --bg-*, --fg-*, …) and the utility classes (.v2-btn, .label,
// .mono) that every screen relies on. It's a full-height app shell: a fixed
// sidebar + a scrolling main column, with drawers/modals/toast layered on top.
export default function App() {
  // Mirrors the prototype's constructor state (Developer Console.dc.html:744).
  // `evQuery` (M4-20-05) and `reveal`/`confirmRotate` (M4-20-06) land with the
  // screens that read them — `noUnusedLocals` rejects state that nothing
  // consumes yet. `subQuery` arrives here with its two consumers on Submissions
  // (the search input and the empty state).
  const [screen, setScreen] = useState<Screen>('overview')
  const [env, setEnv] = useState<Env>('live')
  const [range, setRange] = useState<Range>('30d')
  const [filter, setFilter] = useState<JobFilter>('all')
  const [subQuery, setSubQuery] = useState('')
  const [drawer, setDrawer] = useState<DrawerState>(null)
  const [reqOpen, setReqOpen] = useState(true)
  const [resOpen, setResOpen] = useState(true)
  const [toast, setToast] = useState<ToastState>(null)
  const [jobs, setJobs] = useState(SEED_SUBMISSIONS)

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
    showToast('Re-drive queued · ' + id, 'ACCEPTED')
  }
  const reDriveAll = () => {
    const ids = jobs.filter((j) => j.state === 'dead-letter').map((j) => j.id)
    setJobs((prev) => prev.map((j) => (j.state === 'dead-letter' ? { ...j, state: 'queued', lastError: '—' } : j)))
    showToast('Re-drove ' + ids.length + ' dead-letter submissions', 'QUEUED')
  }
  const cancelJob = (id: string) => {
    setJobs((prev) => prev.map((j) => (j.id === id ? { ...j, state: 'failed', lastError: 'Cancelled by user' } : j)))
    setDrawer(null)
    showToast('Cancelled · ' + id, '', 'red')
  }

  // ---- resolve open drawer entities ----
  const drawerJob = drawer?.type === 'job' ? jobs.find((j) => j.id === drawer.id) : undefined

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
          {screen === 'overview' && <Overview range={range} onRangeChange={setRange} />}
          {screen === 'submissions' && (
            <Submissions
              jobs={jobs}
              filter={filter}
              query={subQuery}
              onFilterChange={setFilter}
              onQueryChange={setSubQuery}
              onOpenJob={openJob}
              onReDriveAll={reDriveAll}
            />
          )}
          {screen === 'evidence' && <Evidence />}
          {screen === 'api' && <ApiWebhooks />}
          {screen === 'billing' && <Billing />}
          {screen === 'status' && <Status />}
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

      {toast && <Toast toast={toast} />}
    </div>
  )
}
