// The Book-a-Demo lead-capture form. DemoModal mounts it inside the popup card.

import { useEffect, useRef, useState } from 'react'
import type { ChangeEvent, CSSProperties, FormEvent, ReactNode } from 'react'
import {
  validateDemoForm,
  firstNameOf,
  CONSENT_TEXT,
  TAXPAYER_SIZE_OPTIONS,
  ROLE_OPTIONS,
  VOLUME_OPTIONS,
  DEFAULT_FORM,
  DEMO_NAME_MAX,
  DEMO_EMAIL_MAX,
  DEMO_COMPANY_MAX,
  type DemoFormErrors,
  type DemoFormState,
  type DemoFieldKey,
  type DemoStep,
} from './demoForm'
import { resolveSubmitTarget, submitDemoLead, type DemoLead } from '../hubspot'
import { trackedHubSpotSubmit } from '../analytics'
import { DemoRateLimited, sendDemoRequest } from '../demoRequest'
import { IconTile } from './ds/IconTile'
import { MarketingConsent } from './MarketingConsent'

export function Glyph({ d, size = 16, sw = 1.7 }: { d: string | string[]; size?: number; sw?: number }) {
  const paths = Array.isArray(d) ? d : [d]
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={sw} strokeLinecap="round" strokeLinejoin="round">
      {paths.map((p, i) => (
        <path key={i} d={p} />
      ))}
    </svg>
  )
}

// Shared warning-triangle glyph (used for inline field errors and the error-state icon).
export const WARN_PATHS = [
  'M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z',
  'M12 9v4',
  'M12 17h.01',
]

// The form's half of DemoModal's single <style> element — interpolated there so the
// popup still renders exactly one <style>.
export const DEMO_FORM_CSS = `
  @keyframes dmSpin { to { transform: rotate(360deg); } }
  .dm-input, .dm-select { transition: border-color var(--dur-fast) var(--ease-out); }
  .dm-input:focus, .dm-select:focus { outline: 2px solid var(--ring); outline-offset: 2px; }
  .dm-err { border-color: var(--destructive) !important; }
  .dm-select { appearance: none; -webkit-appearance: none; }
  @media (max-width: 480px) { .dm-row { flex-direction: column !important; align-items: stretch !important; } }
`

const INPUT_STYLE: CSSProperties = { width: '100%', height: 42, background: 'var(--card)', border: '1px solid var(--input)', borderRadius: 'var(--radius)', padding: '0 13px', fontSize: 14, color: 'var(--ink)', fontFamily: 'var(--font-sans)' }
const SELECT_STYLE: CSSProperties = { ...INPUT_STYLE, padding: '0 32px 0 13px', cursor: 'pointer' }

// Shared with MarketingConsent.
export const CHECK_LABEL_STYLE: CSSProperties = { display: 'flex', alignItems: 'flex-start', gap: 12, fontSize: 13, lineHeight: 1.55, color: 'var(--foreground)', cursor: 'pointer' }
export const CHECK_INPUT_STYLE: CSSProperties = { flex: 'none', width: 18, height: 18, margin: '2px 0 0', accentColor: 'var(--primary)', cursor: 'pointer' }

export function DemoLeadForm({
  idPrefix,
  heading,
  onDone,
  submit,
}: {
  idPrefix: string
  heading?: ReactNode
  onDone?: () => void
  submit?: (lead: DemoLead) => Promise<void>
}) {
  const [form, setForm] = useState<DemoFormState>(DEFAULT_FORM)
  const [errors, setErrors] = useState<DemoFormErrors>({})
  const [demoStep, setDemoStep] = useState<DemoStep>('form')
  const [limited, setLimited] = useState(false)
  const submitTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const mounted = useRef(true)

  // Demo-mode stub: client-side theater, always resolves to success after ~1300ms
  // (matching the prototype's submitDemo). Its timer is stored on the shared
  // submitTimer ref so unmount cleanup still clears it. Deliberately ONE helper
  // with the delay written once — a closed HubSpot gate and a tripped honeypot
  // both route through it, and they must be timing-indistinguishable.
  const runStub = () =>
    new Promise<void>((resolve) => {
      submitTimer.current = setTimeout(resolve, 1300)
    })

  // Clear any pending submit timer on unmount. Escape/focus-restore stay on
  // DemoModal, which owns the overlay.
  useEffect(
    () => () => {
      if (submitTimer.current) clearTimeout(submitTimer.current)
      mounted.current = false
    },
    [],
  )

  // Keyed on demoStep, never a deferred setTimeout: a deferred focus used to land
  // after a submit and yank focus off the field at fault.
  useEffect(() => {
    if (demoStep === 'form') {
      document.getElementById(`${idPrefix}-name`)?.focus()
    } else if (demoStep === 'success') {
      ;(document.getElementById(`${idPrefix}-success-done`) ?? document.getElementById(`${idPrefix}-success`))?.focus()
    } else if (demoStep === 'error') {
      document.getElementById(`${idPrefix}-error-retry`)?.focus()
    }
  }, [demoStep, idPrefix])

  function setField(key: DemoFieldKey, value: string) {
    setForm((prev) => ({ ...prev, [key]: value }))
    if (key === 'name' || key === 'email' || key === 'company') {
      setErrors((prev) => ({ ...prev, [key]: undefined }))
    }
  }

  // setField's sibling: a computed-key spread carrying
  // `value: string` cannot also carry a checkbox.
  function setConsent(next: boolean) {
    setForm((prev) => ({ ...prev, consent: next }))
    setErrors((prev) => ({ ...prev, consent: undefined }))
  }

  function setMarketing(next: boolean) {
    setForm((prev) => ({ ...prev, marketing: next }))
  }

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (demoStep === 'submitting') return
    // Read the honeypot off the form SYNCHRONOUSLY, before any await: React clears
    // e.currentTarget once the handler's synchronous portion returns, so a
    // post-await read would always see null and the trap would silently never fire.
    // Reading the DOM (rather than mirroring it into state) is also what catches
    // the naive bots that assign input.value directly without dispatching an event.
    const trap = String(new FormData(e.currentTarget).get('website') ?? '').trim()
    const nextErrors = validateDemoForm(form)
    if (Object.keys(nextErrors).length) {
      setErrors(nextErrors)
      const firstKey = (['name', 'email', 'company', 'consent'] as const).find((k) => nextErrors[k])
      if (firstKey) document.getElementById(`${idPrefix}-${firstKey}`)?.focus()
      return
    }
    setErrors({})
    setLimited(false)
    setDemoStep('submitting')

    // Built field by field. The honeypot's value is
    // deliberately absent — it is not part of the form's data model.
    const lead: DemoLead = {
      name: form.name,
      email: form.email,
      company: form.company,
      role: form.role,
      size: form.size,
      volume: form.volume,
      consent: form.consent,
      marketing: form.marketing,
    }

    // All four branches share ONE success/error transition. A tripped honeypot is
    // dropped by running the very same stub a closed gate runs — no early return,
    // no distinguishable timing, and nothing written to the console on any path,
    // so a bot cannot tell a silent drop from a real submit. The error branch is
    // reachable by construction: any rejection — a HubSpot non-2xx, or an injected
    // failing `submit` in QA/tests — routes here.
    try {
      if (trap) await runStub()
      else if (submit) await submit(lead)
      else {
        const target = resolveSubmitTarget(window.location.hostname)
        // Wrapped here, not around the shared success transition below: this is the
        // only branch of the four that reaches HubSpot.
        if (target) {
          await trackedHubSpotSubmit(() => submitDemoLead(target, lead, CONSENT_TEXT))
          await sendDemoRequest(lead)
        } else {
          // The stub runs beside the gateway post so the stub's delay is never added to it.
          await Promise.all([sendDemoRequest(lead) ?? Promise.resolve(), runStub()])
        }
      }
      if (mounted.current) setDemoStep('success')
    } catch (e) {
      if (mounted.current) {
        setLimited(e instanceof DemoRateLimited)
        setDemoStep('error')
      }
    }
  }

  // The form panel does not exist yet at this point — the effect above focuses
  // `${idPrefix}-name` once React has put it back in the DOM.
  function retry() {
    setDemoStep('form')
  }

  const submitting = demoStep === 'submitting'
  const panelStyle: CSSProperties = { padding: '36px 24px 26px', textAlign: 'center', display: 'grid', gap: 12, justifyItems: 'center' }
  const panelH3: CSSProperties = { fontSize: 24, fontWeight: 700, letterSpacing: 'var(--tracking-h3)', margin: '4px 0 0', color: 'var(--ink)' }
  const panelP: CSSProperties = { lineHeight: 1.6, margin: '0 auto 8px', maxWidth: 360 }

  return (
    <>
      {(demoStep === 'form' || demoStep === 'submitting') && (
        <form noValidate onSubmit={handleSubmit} style={{ padding: '24px 24px 22px' }}>
          {heading}

          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            <div>
              <label htmlFor={`${idPrefix}-name`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                Full name <span style={{ color: 'var(--destructive)' }}>*</span>
              </label>
              <input
                id={`${idPrefix}-name`}
                type="text"
                className={'dm-input' + (errors.name ? ' dm-err' : '')}
                value={form.name}
                onChange={(e: ChangeEvent<HTMLInputElement>) => setField('name', e.target.value)}
                placeholder="Ada Okafor"
                autoComplete="name"
                maxLength={DEMO_NAME_MAX}
                aria-required="true"
                aria-invalid={Boolean(errors.name)}
                aria-describedby={errors.name ? `${idPrefix}-name-error` : undefined}
                disabled={submitting}
                style={INPUT_STYLE}
              />
              {errors.name && (
                <div id={`${idPrefix}-name-error`} role="alert" style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--destructive)' }}>
                  <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {errors.name}
                </div>
              )}
            </div>

            <div>
              <label htmlFor={`${idPrefix}-email`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                Work email <span style={{ color: 'var(--destructive)' }}>*</span>
              </label>
              <input
                id={`${idPrefix}-email`}
                type="email"
                className={'dm-input' + (errors.email ? ' dm-err' : '')}
                value={form.email}
                onChange={(e: ChangeEvent<HTMLInputElement>) => setField('email', e.target.value)}
                placeholder="you@company.com"
                autoComplete="email"
                maxLength={DEMO_EMAIL_MAX}
                aria-required="true"
                aria-invalid={Boolean(errors.email)}
                aria-describedby={errors.email ? `${idPrefix}-email-error` : undefined}
                disabled={submitting}
                style={INPUT_STYLE}
              />
              {errors.email && (
                <div id={`${idPrefix}-email-error`} role="alert" style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--destructive)' }}>
                  <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {errors.email}
                </div>
              )}
            </div>

            <div>
              <label htmlFor={`${idPrefix}-company`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                Company <span style={{ color: 'var(--destructive)' }}>*</span>
              </label>
              <input
                id={`${idPrefix}-company`}
                type="text"
                className={'dm-input' + (errors.company ? ' dm-err' : '')}
                value={form.company}
                onChange={(e: ChangeEvent<HTMLInputElement>) => setField('company', e.target.value)}
                placeholder="Okafor & Partners"
                autoComplete="organization"
                maxLength={DEMO_COMPANY_MAX}
                aria-required="true"
                aria-invalid={Boolean(errors.company)}
                aria-describedby={errors.company ? `${idPrefix}-company-error` : undefined}
                disabled={submitting}
                style={INPUT_STYLE}
              />
              {errors.company && (
                <div id={`${idPrefix}-company-error`} role="alert" style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--destructive)' }}>
                  <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {errors.company}
                </div>
              )}
            </div>

            <div>
              <label htmlFor={`${idPrefix}-role`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                Role <span style={{ color: 'var(--text-copy)' }}>(opt.)</span>
              </label>
              <div style={{ position: 'relative' }}>
                <select
                  id={`${idPrefix}-role`}
                  className="dm-select"
                  value={form.role}
                  onChange={(e: ChangeEvent<HTMLSelectElement>) => setField('role', e.target.value)}
                  disabled={submitting}
                  style={SELECT_STYLE}
                >
                  <option value="" disabled>Select…</option>
                  {ROLE_OPTIONS.map((opt) => (
                    <option key={opt} value={opt}>{opt}</option>
                  ))}
                </select>
                <span style={{ position: 'absolute', right: 12, top: '50%', transform: 'translateY(-50%)', pointerEvents: 'none', color: 'var(--muted-foreground)', display: 'inline-flex' }}>
                  <Glyph d="m6 9 6 6 6-6" size={14} sw={1.8} />
                </span>
              </div>
            </div>

            <div className="dm-row" style={{ display: 'flex', gap: 12, alignItems: 'flex-end' }}>
              <div style={{ flex: 1, minWidth: 0 }}>
                <label htmlFor={`${idPrefix}-size`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                  Taxpayer size <span style={{ color: 'var(--text-copy)' }}>(opt.)</span>
                </label>
                <div style={{ position: 'relative' }}>
                  <select
                    id={`${idPrefix}-size`}
                    className="dm-select"
                    value={form.size}
                    onChange={(e: ChangeEvent<HTMLSelectElement>) => setField('size', e.target.value)}
                    disabled={submitting}
                    style={SELECT_STYLE}
                  >
                    <option value="" disabled>Select…</option>
                    {TAXPAYER_SIZE_OPTIONS.map((opt) => (
                      <option key={opt} value={opt}>{opt}</option>
                    ))}
                  </select>
                  <span style={{ position: 'absolute', right: 12, top: '50%', transform: 'translateY(-50%)', pointerEvents: 'none', color: 'var(--muted-foreground)', display: 'inline-flex' }}>
                    <Glyph d="m6 9 6 6 6-6" size={14} sw={1.8} />
                  </span>
                </div>
              </div>
              <div style={{ flex: 1, minWidth: 0 }}>
                <label htmlFor={`${idPrefix}-volume`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                  Monthly invoices <span style={{ color: 'var(--text-copy)' }}>(opt.)</span>
                </label>
                <div style={{ position: 'relative' }}>
                  <select
                    id={`${idPrefix}-volume`}
                    className="dm-select"
                    value={form.volume}
                    onChange={(e: ChangeEvent<HTMLSelectElement>) => setField('volume', e.target.value)}
                    disabled={submitting}
                    style={SELECT_STYLE}
                  >
                    <option value="" disabled>Select…</option>
                    {VOLUME_OPTIONS.map((opt) => (
                      <option key={opt} value={opt}>{opt}</option>
                    ))}
                  </select>
                  <span style={{ position: 'absolute', right: 12, top: '50%', transform: 'translateY(-50%)', pointerEvents: 'none', color: 'var(--muted-foreground)', display: 'inline-flex' }}>
                    <Glyph d="m6 9 6 6 6-6" size={14} sw={1.8} />
                  </span>
                </div>
              </div>
            </div>

            <div>
              <label htmlFor={`${idPrefix}-consent`} style={CHECK_LABEL_STYLE}>
                <input
                  id={`${idPrefix}-consent`}
                  type="checkbox"
                  checked={form.consent}
                  onChange={(e: ChangeEvent<HTMLInputElement>) => setConsent(e.target.checked)}
                  aria-required="true"
                  aria-invalid={Boolean(errors.consent)}
                  aria-describedby={errors.consent ? `${idPrefix}-consent-error` : undefined}
                  disabled={submitting}
                  style={CHECK_INPUT_STYLE}
                />
                {/* The imported constant, never a retyped sentence: this is the one
                    mechanism that keeps the wording the visitor was SHOWN identical to
                    the wording submitted as legalConsentOptions.consent.text. */}
                {CONSENT_TEXT}
              </label>
              {errors.consent && (
                <div id={`${idPrefix}-consent-error`} role="alert" style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--destructive)' }}>
                  <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {errors.consent}
                </div>
              )}
            </div>

            <MarketingConsent id={`${idPrefix}-marketing`} checked={form.marketing} onChange={setMarketing} disabled={submitting} />
          </div>

          {/* Honeypot. Bots fill every input they find; humans never see this one, so
              any value here means "not a human" and the submission is dropped
              silently. Off-screen rather than display:none — bots skip display:none
              fields, which would defeat the trap entirely. tabIndex={-1} keeps it out
              of the Tab-trap, and is one half of that fix: an off-screen input still
              has a non-null offsetParent, so isFocusable's tabIndex clause is the
              other half. Uncontrolled and read straight off the form in handleSubmit;
              its value never reaches `lead`. */}
          {/* autoComplete="new-password" (not "off") plus the four vendor data-*
              attributes below are deliberate, not decoration: Chrome profile
              autofill and every major password manager ignore autocomplete="off"
              (Chromium issue 40223868), so a browser or password manager that
              autofills this field turns a REAL human's lead into a silent drop.
              "new-password" is safe here because password-manager "suggest a
              strong password" UI is gated on type="password", and this input
              stays type="text". Do not "tidy" these away. */}
          <div style={{ position: 'absolute', left: -9999, width: 1, height: 1, overflow: 'hidden' }}>
            <input
              type="text"
              name="website"
              tabIndex={-1}
              aria-hidden="true"
              autoComplete="new-password"
              data-lpignore="true"
              data-1p-ignore="true"
              data-bwignore="true"
              data-form-type="other"
            />
          </div>

          <button type="submit" disabled={submitting} className="ds-btn ds-btn--primary ds-btn--md" style={{ width: '100%', marginTop: 24 }}>
            {submitting ? (
              <>
                <span style={{ width: 15, height: 15, border: '2px solid color-mix(in srgb, var(--primary-foreground) 40%, transparent)', borderTopColor: 'var(--primary-foreground)', borderRadius: 'var(--radius-pill)', animation: 'dmSpin 0.7s linear infinite' }} />
                Booking…
              </>
            ) : (
              'Book my demo →'
            )}
          </button>
          <p className="t-caption" style={{ textAlign: 'center', margin: '14px 0 0' }}>No card required</p>
        </form>
      )}

      {demoStep === 'success' && (
        <div id={`${idPrefix}-success`} tabIndex={-1} style={panelStyle}>
          <IconTile name="check" tone="primary" size={48} iconSize={24} />
          <h3 style={panelH3}>You're booked</h3>
          <p className="t-body-sm" style={panelP}>
            Thanks, {firstNameOf(form.name)}. A compliance specialist will email {form.email} within one business day to lock your 20-minute slot.
          </p>
          {onDone && (
            <button id={`${idPrefix}-success-done`} onClick={onDone} className="ds-btn ds-btn--primary ds-btn--md" style={{ width: '100%' }}>Done</button>
          )}
        </div>
      )}

      {demoStep === 'error' && (
        <div style={panelStyle}>
          <span style={{ width: 48, height: 48, borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', color: 'var(--destructive)', display: 'inline-grid', placeItems: 'center' }}>
            <Glyph d={WARN_PATHS} size={24} sw={1.8} />
          </span>
          <h3 style={panelH3}>{limited ? 'Too many requests' : 'Something went wrong'}</h3>
          <p className="t-body-sm" style={panelP}>
            {limited
              ? 'Too many demo requests came from your network. Please try again later — your details are still here.'
              : "We couldn't book your demo just now. Please try again — your details are still here."}
          </p>
          <button id={`${idPrefix}-error-retry`} onClick={retry} className="ds-btn ds-btn--primary ds-btn--md" style={{ width: '100%' }}>Try again</button>
        </div>
      )}
    </>
  )
}
