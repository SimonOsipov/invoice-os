import { useEffect, useRef } from 'react'
import { CHOOSE_ANOTHER } from '../copy'
import { GLYPHS, Icon } from '../ds'
import { offersAnotherFile, type Refusal as RefusalValue } from '../messages'

interface Props {
  refusal: RefusalValue
  onChooseAnother: () => void
}

export function Refusal({ refusal, onChooseAnother }: Props) {
  // The Send button unmounted with the sending view; focus moves to the alert.
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => ref.current?.focus(), [])
  return (
    <div ref={ref} tabIndex={-1} className="check-alert" role="alert">
      <Icon paths={GLYPHS['triangle-alert']} size={18} strokeWidth={2} />
      <div className="check-alert-body">
        <span>{refusal.text}</span>
        {offersAnotherFile(refusal) && (
          <button type="button" className="check-link" onClick={onChooseAnother}>
            {CHOOSE_ANOTHER}
          </button>
        )}
      </div>
    </div>
  )
}
