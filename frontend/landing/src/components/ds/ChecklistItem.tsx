import type { ReactNode } from 'react'

import { GLYPHS, Icon } from '../../icons'

export function ChecklistItem({ children }: { children?: ReactNode }) {
  return (
    <div className="ds-checklist-item">
      <span className="ds-checklist-icon" aria-hidden="true">
        <Icon paths={GLYPHS['circle-check']} size={17} strokeWidth={2} />
      </span>
      <span>{children}</span>
    </div>
  )
}
