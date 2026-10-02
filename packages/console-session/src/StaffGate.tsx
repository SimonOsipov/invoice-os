// The consoles call no backend and render mock data: this gate makes the door right, and nothing server-side enforces it.
// ceiling: unset VITE_LANDING_URL renders with no gate; make that fail closed when a console reads real data.
import type { ReactNode } from 'react'

import type { ConsoleTarget } from './state'

export function StaffGate(_p: {
  storageKey: string
  target: ConsoleTarget
  gateway: string | null
  landing: string | null
  children: ReactNode
}): ReactNode {
  return null
}
