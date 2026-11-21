// The consoles render mock data: this gate makes the door right, and nothing server-side enforces it.
// ceiling: unset VITE_LANDING_URL renders with no gate; make that fail closed when a console reads real data.
import { useEffect, useRef, useState, type ReactNode } from 'react'

import { resolveConsoleBoot, type ConsoleBoot } from './boot'
import { clearSignedOut, isIapHost, switchGoogleAccount } from './iap'
import type { ConsoleTarget } from './state'

// Removes the one-shot params so a reload or Back never replays them.
function stripOneShotParams(): void {
  const u = new URL(window.location.href)
  if (!u.searchParams.has('handoff') && !u.searchParams.has('auth')) return
  u.searchParams.delete('handoff')
  u.searchParams.delete('auth')
  window.history.replaceState(null, '', u.pathname + u.search + u.hash)
}

export function StaffGate(p: {
  storageKey: string
  target: ConsoleTarget
  gateway: string | null
  landing: string | null
  app?: string | null
  iap?: boolean
  children: ReactNode
}): ReactNode {
  const [boot, setBoot] = useState<ConsoleBoot | null>(null)
  const iap = p.iap ?? isIapHost(window.location.hostname)
  const started = useRef(false)

  useEffect(() => {
    // One latch per mount: StrictMode re-runs this effect and must not post twice.
    if (started.current) return
    started.current = true
    const search = window.location.search
    stripOneShotParams()
    void resolveConsoleBoot({
      search,
      storageKey: p.storageKey,
      target: p.target,
      gateway: p.gateway,
      landing: p.landing,
      app: p.app,
      iap,
    }).then((b) => {
      if (b.kind === 'leave') {
        if (b.replace) window.location.replace(b.url)
        else window.location.href = b.url
      }
      setBoot(b)
    })
  }, [p.storageKey, p.target, p.gateway, p.landing, p.app, iap])

  if (boot?.kind === 'open') return p.children
  if (!iap || boot?.kind === 'leave') return null
  const reload = () => window.location.reload()
  if (boot === null) {
    return (
      <main className="asc-app">
        <p role="status">Opening the console…</p>
      </main>
    )
  }
  if (boot.kind === 'failed') {
    return (
      <main className="asc-app">
        <p role="alert">Could not open the console.</p>
        <button type="button" className="v2-btn v2-btn-primary" onClick={reload}>
          Try again
        </button>
      </main>
    )
  }
  if (boot.kind === 'no-staff') {
    return (
      <main className="asc-app">
        <h1>No staff account for this Google account</h1>
        <p>{boot.email} is not an ASComply staff account. Ask an ASComply admin to give this email staff access.</p>
        <a
          href="/_gcp_iap/clear_login_cookie"
          onClick={(e) => {
            e.preventDefault()
            void switchGoogleAccount({
              storageKey: p.storageKey,
              gateway: p.gateway,
            })
          }}
        >
          Use a different Google account
        </a>
      </main>
    )
  }
  return (
    <main className="asc-app">
      <p>You are signed out of the console.</p>
      <button
        type="button"
        className="v2-btn v2-btn-primary"
        onClick={() => {
          clearSignedOut(p.storageKey)
          reload()
        }}
      >
        Open the console
      </button>
    </main>
  )
}
