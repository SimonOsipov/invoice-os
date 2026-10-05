// Optional marketing opt-in; it never blocks submit.
import { CHECK_INPUT_STYLE, CHECK_LABEL_STYLE } from './DemoLeadForm'

export const MARKETING_CONSENT_TEXT = 'Allow marketing communications: ASComply Africa may email me product news and offers. I can unsubscribe at any time.'

export function MarketingConsent({ id, checked, onChange, disabled }: { id: string; checked: boolean; onChange: (next: boolean) => void; disabled: boolean }) {
  return (
    <label htmlFor={id} style={CHECK_LABEL_STYLE}>
      <input id={id} type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} disabled={disabled} style={CHECK_INPUT_STYLE} />
      {MARKETING_CONSENT_TEXT}
    </label>
  )
}
