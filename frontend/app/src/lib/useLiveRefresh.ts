// Live-refresh polling primitives (M5-09-07, [poll-overlay-not-rerun]). Two hooks, not
// one: useLiveRefresh's `active` argument is computed by the CALLER from
// shouldPollInvoice/shouldPollList (lib/invoices.ts, M5-09-03), which already needs
// document-visibility as an input -- so a visibilitychange listener living inside
// useLiveRefresh could never influence a value the caller already computed.
// useDocumentVisible is that listener, exported standalone; callers do
// `const visible = useDocumentVisible()` then feed it into the predicate that produces
// `active`.
//
// useLiveRefresh holds no data and makes no decisions -- it only calls `tick` on a fixed
// interval while `active`, and tears the interval down on deactivate/unmount (AC-5). It
// is NOT `useAsync.run()` under the hood: a poll tick must update an overlay
// (`setLive(...)`) at the call site, never dispatch useAsync's 'start' action, which
// would null `data` and flash <Loading/> every interval (THE LOAD-BEARING TRAP,
// packages/api-client/src/async-state.ts:47-48).
import { useEffect, useRef, useState } from 'react'

export function useDocumentVisible(): boolean {
  const [visible, setVisible] = useState(() => document.visibilityState === 'visible')

  useEffect(() => {
    const onVisibilityChange = () => setVisible(document.visibilityState === 'visible')
    document.addEventListener('visibilitychange', onVisibilityChange)
    return () => document.removeEventListener('visibilitychange', onVisibilityChange)
  }, [])

  return visible
}

// `tick` is read through a ref kept current every render (the sync effect below has no
// deps array), so the interval effect itself depends on nothing but `active`/`intervalMs`.
// A naive `useEffect(..., [tick, active, intervalMs])` would tear down and rebuild the
// interval every render -- `tick` is a fresh closure at both call sites -- which on the
// list would reset the 2s timer on every checkbox click and can starve polling
// indefinitely under interaction.
export function useLiveRefresh(tick: () => void, active: boolean, intervalMs: number): void {
  const saved = useRef(tick)
  useEffect(() => {
    saved.current = tick
  })

  useEffect(() => {
    if (!active) return
    const id = setInterval(() => saved.current(), intervalMs)
    return () => clearInterval(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, intervalMs])
}
