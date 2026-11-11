import type { Refusal as RefusalValue } from '../messages'

export function Refusal({ refusal }: { refusal: RefusalValue }) {
  return (
    <p role="alert" className="check-refusal">
      {refusal.text}
    </p>
  )
}
