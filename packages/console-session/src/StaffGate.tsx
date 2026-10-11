// The consoles render mock data: this gate makes the door right, and nothing server-side enforces it.
// ceiling: unset VITE_LANDING_URL renders with no gate; make that fail closed when a console reads real data.
import { useEffect, useRef, useState, type ReactNode } from 'react'

import { resolveConsoleBoot, type ConsoleBoot } from './boot'
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
  children: ReactNode
}): ReactNode {
  const [boot, setBoot] = useState<ConsoleBoot | null>(null)
  const started = useRef(false)

  useEffect(() => {
    // One latch per mount: StrictMode re-runs this effect and must not post twice.
    if (started.current) return
    started.current = true
    const search = window.location.search
    stripOneShotParams()
    void resolveConsoleBoot({ search, storageKey: p.storageKey, target: p.target, gateway: p.gateway, landing: p.landing }).then((b) => {
      if (b.kind === 'leave') {
        if (b.replace) window.location.replace(b.url)
        else window.location.href = b.url
      }
      setBoot(b)
    })
  }, [p.storageKey, p.target, p.gateway, p.landing])

  return boot?.kind === 'open' ? p.children : null
}
