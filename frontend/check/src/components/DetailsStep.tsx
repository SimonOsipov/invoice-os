import { useState } from 'react'
import { CONSENT_TEXT } from '../analytics'
import { BACK, EMAIL_LABEL, EMAIL_PLACEHOLDER, EMAIL_TITLE, SEND } from '../copy'
import { GLYPHS, Icon, IconTile } from '../ds'
import { EMAIL_RE } from '../flow'
import { CHECK_GLYPHS } from '../glyphs'
import { CONSENT_SENTENCE, EMAIL_SENTENCE, type Refusal as RefusalValue } from '../messages'
import { Refusal } from './Refusal'

interface Props {
  stepNo: string
  fileName: string
  fileMeta: string
  email: string
  ticked: boolean
  canSend: boolean
  refusal: RefusalValue | null
  onEmail: (v: string) => void
  onTick: (v: boolean) => void
  onBack: () => void
  onChooseAnother: () => void
  onSend: () => void
}

export function DetailsStep({ stepNo, fileName, fileMeta, email, ticked, canSend, refusal, onEmail, onTick, onBack, onChooseAnother, onSend }: Props) {
  // Touched state resets on remount (Back, a new file).
  const [emailTouched, setEmailTouched] = useState(false)
  const [tickTouched, setTickTouched] = useState(false)
  const emailErr = emailTouched && !EMAIL_RE.test(email.trim())
  const tickErr = tickTouched && !ticked
  return (
    <section className="check-card check-email">
      <form
        data-hs-do-not-collect="true"
        onSubmit={(e) => {
          e.preventDefault()
          if (canSend) onSend()
        }}
      >
        <div className="check-step-head">
          <span className="check-mono check-step-no">{stepNo}</span>
          <h2 className="check-step-title">{EMAIL_TITLE}</h2>
        </div>
        <div className="check-upload-file check-email-file">
          <IconTile name="file-text" size={36} />
          <div className="check-upload-meta">
            <span className="check-upload-name">{fileName}</span>
            <span className="check-upload-size">{fileMeta}</span>
          </div>
          <span className="check-file-ok" aria-hidden="true">
            <Icon paths={GLYPHS['circle-check']} size={18} strokeWidth={2} />
          </span>
        </div>
        <div className="check-email-field">
          <label htmlFor="check-email" className="check-label">
            {EMAIL_LABEL}
          </label>
          <input
            id="check-email"
            className={emailErr ? 'check-fld is-err' : 'check-fld'}
            type="email"
            required
            autoComplete="email"
            inputMode="email"
            placeholder={EMAIL_PLACEHOLDER}
            value={email}
            aria-invalid={emailErr ? true : undefined}
            aria-describedby={emailErr ? 'check-email-err' : undefined}
            onChange={(e) => onEmail(e.target.value)}
            onBlur={() => {
              if (email.trim() !== '') setEmailTouched(true)
            }}
          />
          {emailErr && (
            <p id="check-email-err" role="alert" className="check-err">
              <Icon paths={GLYPHS['triangle-alert']} size={16} strokeWidth={2} />
              <span>{EMAIL_SENTENCE}</span>
            </p>
          )}
        </div>
        <div className="check-consent-field">
          <label className="check-consent">
            <input
              type="checkbox"
              required
              checked={ticked}
              aria-describedby={tickErr ? 'check-consent-err' : undefined}
              onChange={(e) => {
                setTickTouched(true)
                onTick(e.target.checked)
              }}
            />
            <span>{CONSENT_TEXT}</span>
          </label>
          {tickErr && (
            <p id="check-consent-err" role="alert" className="check-err check-consent-err">
              <Icon paths={GLYPHS['triangle-alert']} size={16} strokeWidth={2} />
              <span>{CONSENT_SENTENCE}</span>
            </p>
          )}
        </div>
        {refusal && <Refusal refusal={refusal} onChooseAnother={onChooseAnother} />}
        <div className="check-actions">
          <button type="button" className="check-btn check-btn--outline" onClick={onBack}>
            {BACK}
          </button>
          <button type="submit" className="check-btn check-btn--next" disabled={!canSend}>
            {SEND}
            <Icon paths={CHECK_GLYPHS['arrow-right']} size={16} strokeWidth={2} />
          </button>
        </div>
      </form>
    </section>
  )
}
