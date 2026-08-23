// Shared popover shell for the five audit filter triggers (AUDIT-07). Anatomy: MoreMenu
// (MemberParts.tsx:534-618) -- wrapper ref covers trigger + panel, useDismiss(open,
// onDismiss, wrapRef), trigger toggles explicitly on click so its own button can close an
// open panel. Two departures: a labelled trigger carrying chevDownGlyph (not icon-only),
// and arbitrary children instead of MenuAction[].
//
// STUB for AUDIT-07-02 Test-Spec (Mode A) -- renders nothing. Real anatomy lands with the
// executor pass that turns FilterPopover.test.tsx green.

import type { ReactNode } from 'react'

export interface FilterPopoverProps {
  /** Prefixes every data-testid this component renders. */
  testId: string
  label: string
  summary?: string
  open: boolean
  onOpen: () => void
  onClose: () => void
  children: ReactNode
}

export function FilterPopover(_props: FilterPopoverProps): JSX.Element | null {
  return null
}
