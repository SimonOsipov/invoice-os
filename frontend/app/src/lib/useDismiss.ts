// Escape / outside-click dismissal for transient surfaces.
//
// This is the app's FIRST outside-click handler and its FIRST Escape handler. Before it,
// `frontend/app/src` contained exactly one `document.addEventListener` — `visibilitychange`
// in lib/useLiveRefresh.ts:23 — and no keydown listener at all. The Sidebar company
// switcher (Sidebar.tsx:143-190) looks like a precedent and is not: `switcherOpen` is ctx
// state, toggled by the trigger and cleared imperatively by nav() / switchClient()
// (App.tsx:287-299). It does not dismiss on an outside click, and MEMB-01-04 AC#7 requires
// both an outside click and Escape, which that shape cannot give.
//
// Two house postures disagreed and one was picked rather than blended (Surface Conflicts):
// `landing/src` closes its modals on Escape through a window `keydown` listener with
// cleanup (SignInModal.tsx:32-40, DemoModal.tsx:73-85), while
// `ops-console/src/components/RotateConfirm.tsx:12` records a deliberate no-Escape
// decision for a confirm step. AC#7 requires Escape, so the landing idiom wins.
//
// Shared rather than written per surface on purpose. Three dismissible surfaces land in
// three consecutive subtasks — the row menu (MEMB-01-04), the invite modal (MEMB-01-06)
// and the member drawer (MEMB-01-07) — and three hand-rolled listeners would be three
// places to forget the cleanup. A modal or drawer that closes on its own scrim passes no
// `outsideRef` and gets Escape alone.

import { useEffect, type RefObject } from 'react'

/**
 * Calls `onDismiss` on Escape while `open`, and — only when `outsideRef` is supplied — on
 * a `mousedown` outside that element. Registers nothing while `open` is false.
 *
 * `outsideRef` must wrap the TRIGGER as well as the surface. With the ref on the popover
 * alone, clicking the trigger of an open menu dismisses it on mousedown and the trigger's
 * own click re-opens it, so the menu can never be closed by its own button.
 *
 * `mousedown`, not `click`: `click` fires on release, so a drag that starts outside and
 * ends inside the surface would leave it open.
 *
 * `onDismiss` is a dependency, so callers pass a stable callback (`useCallback`) unless
 * they want the listeners re-registered on every render.
 */
export function useDismiss(open: boolean, onDismiss: () => void, outsideRef?: RefObject<HTMLElement | null>): void {
  useEffect(() => {
    if (!open) return
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') onDismiss()
    }
    function onMouseDown(e: MouseEvent) {
      const el = outsideRef?.current
      if (el && !el.contains(e.target as Node)) onDismiss()
    }
    window.addEventListener('keydown', onKeyDown)
    if (outsideRef) document.addEventListener('mousedown', onMouseDown)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
      if (outsideRef) document.removeEventListener('mousedown', onMouseDown)
    }
  }, [open, onDismiss, outsideRef])
}
