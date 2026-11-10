import { landingHref } from '../links'
import type { Refusal as RefusalValue } from '../messages'

export function Refusal({ refusal }: { refusal: RefusalValue }) {
  return (
    <p role="alert" className="check-refusal">
      {refusal.text}
      {refusal.signUp && (
        <>
          {' '}
          <a href={landingHref('/?register')}>Sign up</a>
        </>
      )}
    </p>
  )
}
