import { useId } from 'react'
import type { ReactNode } from 'react'

import { GLYPHS, Icon } from '../../icons'

type FAQItemProps = {
  question: string
  open: boolean
  onToggle: () => void
  children?: ReactNode
}

export function FAQItem({ question, open, onToggle, children }: FAQItemProps) {
  const answerId = useId()
  return (
    <div className="ds-faq">
      <button type="button" className="ds-faq-btn" aria-expanded={open} aria-controls={answerId} onClick={onToggle}>
        <span>{question}</span>
        <Icon paths={open ? GLYPHS['chevron-up'] : GLYPHS['chevron-down']} size={18} strokeWidth={2} />
      </button>
      <div id={answerId} className="ds-faq-a" hidden={!open}>
        {children}
      </div>
    </div>
  )
}
