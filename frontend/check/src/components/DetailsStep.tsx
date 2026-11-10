import { CONSENT_TEXT } from '../analytics'
import { SENDING_TEXT } from '../copy'
import type { Refusal as RefusalValue } from '../messages'
import { Refusal } from './Refusal'

interface Props {
  fileName: string
  email: string
  ticked: boolean
  sending: boolean
  canSend: boolean
  refusal: RefusalValue | null
  onEmail: (v: string) => void
  onTick: (v: boolean) => void
  onBack: () => void
  onSend: () => void
}

export function DetailsStep({ fileName, email, ticked, sending, canSend, refusal, onEmail, onTick, onBack, onSend }: Props) {
  return (
    <section className="check-card">
      <h2>Where should we send the results?</h2>
      <p className="check-file">{fileName}</p>
      <form
        data-hs-do-not-collect="true"
        onSubmit={(e) => {
          e.preventDefault()
          if (canSend && !sending) onSend()
        }}
      >
        <label htmlFor="check-email" className="check-label">
          Email address
        </label>
        <input id="check-email" type="email" required autoComplete="email" value={email} onChange={(e) => onEmail(e.target.value)} />
        <label className="check-consent">
          <input type="checkbox" required checked={ticked} onChange={(e) => onTick(e.target.checked)} />
          <span>{CONSENT_TEXT}</span>
        </label>
        <div className="check-actions">
          <button type="button" className="check-btn check-btn--outline" disabled={sending} onClick={onBack}>
            Back
          </button>
          <button type="submit" className="check-btn" disabled={!canSend || sending}>
            Send
          </button>
        </div>
        {sending && <p role="status">{SENDING_TEXT}</p>}
        {refusal && <Refusal refusal={refusal} />}
      </form>
    </section>
  )
}
